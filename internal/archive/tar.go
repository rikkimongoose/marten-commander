package archive

import (
	archivetar "archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	TARFormat   Format = "tar"
	TARGZFormat Format = "tar.gz"
)

const tarProbeCompressedLimit int64 = 256 << 10

// TARDriver indexes headers once and opens regular files through ReaderAt
// section readers. MaxEntries bounds the in-memory metadata index.
type TARDriver struct{ MaxEntries uint64 }

func (TARDriver) Format() Format { return TARFormat }

func (TARDriver) Probe(ctx context.Context, input Input) (Match, error) {
	if err := contextError(ctx); err != nil {
		return NoMatch, err
	}
	if input == nil || input.Size() < 0 {
		return NoMatch, fmt.Errorf("%w: invalid TAR input", ErrRead)
	}
	prefix, err := readPrefix(input, 1024, input.Size())
	if err != nil {
		return NoMatch, err
	}
	if !validTARPrefix(prefix) && !hasTARSignature(prefix) {
		return NoMatch, nil
	}
	return DefiniteMatch, nil
}

func (driver TARDriver) Open(ctx context.Context, input Input) (BackendSession, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if input == nil || input.Size() < 0 {
		return nil, fmt.Errorf("%w: invalid TAR input", ErrRead)
	}
	maximum := driver.MaxEntries
	if maximum == 0 {
		maximum = DefaultExtractionLimits().MaxEntries
	}
	counter := &countingReader{reader: &contextReader{ctx: ctx, reader: io.NewSectionReader(input, 0, input.Size())}}
	reader := archivetar.NewReader(counter)
	backend := &tarBackend{input: input, records: make(map[string]tarRecord)}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, tarError("index TAR", err)
		}
		if uint64(len(backend.entries)) >= maximum {
			return nil, fmt.Errorf("%w: TAR has more than %d entries", ErrLimitExceeded, maximum)
		}
		entry, err := tarArchiveEntry(header)
		if err != nil {
			return nil, err
		}
		canonical, err := NormalizePath(header.Name)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrCorruptArchive, err)
		}
		if _, duplicate := backend.records[canonical]; duplicate {
			return nil, fmt.Errorf("%w: duplicate TAR entry %q", ErrCorruptArchive, header.Name)
		}
		backend.entries = append(backend.entries, entry)
		backend.records[canonical] = tarRecord{offset: counter.count, size: header.Size, kind: entry.Kind}
	}
	return backend, nil
}

// TARGZDriver indexes the decompressed header stream once. Individual Open
// calls rescan from the beginning; Extractor uses ReadFiles to stream a
// selection in one pass. Neither path creates a decompressed cache.
type TARGZDriver struct{ MaxEntries uint64 }

func (TARGZDriver) Format() Format { return TARGZFormat }

func (TARGZDriver) Probe(ctx context.Context, input Input) (Match, error) {
	if err := contextError(ctx); err != nil {
		return NoMatch, err
	}
	if input == nil || input.Size() < 0 {
		return NoMatch, fmt.Errorf("%w: invalid TAR.GZ input", ErrRead)
	}
	magic, err := readPrefix(input, 2, input.Size())
	if err != nil {
		return NoMatch, err
	}
	if len(magic) < 2 || magic[0] != 0x1f || magic[1] != 0x8b {
		return NoMatch, nil
	}
	compressedSize := input.Size()
	if compressedSize > tarProbeCompressedLimit {
		compressedSize = tarProbeCompressedLimit
	}
	reader, err := gzip.NewReader(io.NewSectionReader(input, 0, compressedSize))
	if err != nil {
		// The gzip magic is authoritative; Open will provide the typed corrupt
		// diagnostic for a truncated or malformed stream.
		return DefiniteMatch, nil
	}
	decompressed := make([]byte, 1024)
	n, readErr := io.ReadFull(reader, decompressed)
	closeErr := reader.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return DefiniteMatch, nil
	}
	if closeErr != nil {
		return DefiniteMatch, nil
	}
	if validTARPrefix(decompressed[:n]) || hasTARSignature(decompressed[:n]) {
		return DefiniteMatch, nil
	}
	return NoMatch, nil
}

func (driver TARGZDriver) Open(ctx context.Context, input Input) (BackendSession, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	maximum := driver.MaxEntries
	if maximum == 0 {
		maximum = DefaultExtractionLimits().MaxEntries
	}
	entries, records, err := indexTARGZ(ctx, input, maximum)
	if err != nil {
		return nil, err
	}
	return &tarGZBackend{input: input, entries: entries, records: records}, nil
}

func indexTARGZ(ctx context.Context, input Input, maximum uint64) ([]ArchiveEntry, map[string]tarRecord, error) {
	gzipReader, err := gzip.NewReader(&contextReader{ctx: ctx, reader: io.NewSectionReader(input, 0, input.Size())})
	if err != nil {
		return nil, nil, tarError("open TAR.GZ", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = gzipReader.Close()
		}
	}()
	tarReader := archivetar.NewReader(gzipReader)
	entries := make([]ArchiveEntry, 0)
	records := make(map[string]tarRecord)
	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nil, nil, tarError("index TAR.GZ", nextErr)
		}
		if uint64(len(entries)) >= maximum {
			return nil, nil, fmt.Errorf("%w: TAR.GZ has more than %d entries", ErrLimitExceeded, maximum)
		}
		entry, entryErr := tarArchiveEntry(header)
		if entryErr != nil {
			return nil, nil, entryErr
		}
		canonical, normalizeErr := NormalizePath(header.Name)
		if normalizeErr != nil {
			return nil, nil, fmt.Errorf("%w: %w", ErrCorruptArchive, normalizeErr)
		}
		if _, duplicate := records[canonical]; duplicate {
			return nil, nil, fmt.Errorf("%w: duplicate TAR.GZ entry %q", ErrCorruptArchive, header.Name)
		}
		entries = append(entries, entry)
		records[canonical] = tarRecord{size: header.Size, kind: entry.Kind}
	}
	// tar.Reader stops at the end markers. Drain gzip so its trailer checksum
	// and truncation are validated before exposing a session.
	if _, err := io.Copy(io.Discard, &contextReader{ctx: ctx, reader: gzipReader}); err != nil {
		return nil, nil, tarError("validate TAR.GZ", err)
	}
	if err := gzipReader.Close(); err != nil {
		return nil, nil, tarError("close TAR.GZ", err)
	}
	closed = true
	return entries, records, nil
}

func RegisterTAR(registry *Registry) error {
	if registry == nil {
		return fmt.Errorf("register TAR: %w", ErrUnsupportedFormat)
	}
	return registry.Register(TARDriver{})
}

func RegisterTARGZ(registry *Registry) error {
	if registry == nil {
		return fmt.Errorf("register TAR.GZ: %w", ErrUnsupportedFormat)
	}
	return registry.Register(TARGZDriver{})
}

func readPrefix(input Input, wanted, size int64) ([]byte, error) {
	if size < wanted {
		wanted = size
	}
	if wanted <= 0 {
		return nil, nil
	}
	buffer := make([]byte, int(wanted))
	n, err := input.ReadAt(buffer, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buffer[:n], nil
}

func validTARPrefix(prefix []byte) bool {
	if len(prefix) >= 1024 && allZero(prefix[:1024]) {
		return true
	}
	if len(prefix) < 512 || allZero(prefix[:512]) {
		return false
	}
	block := append([]byte(nil), prefix[:512]...)
	stored, err := parseTAROctal(block[148:156])
	if err != nil {
		return false
	}
	for index := 148; index < 156; index++ {
		block[index] = ' '
	}
	var unsigned, signed int64
	for _, value := range block {
		unsigned += int64(value)
		signed += int64(int8(value))
	}
	return stored == unsigned || stored == signed
}

func hasTARSignature(prefix []byte) bool {
	return len(prefix) >= 265 && (string(prefix[257:262]) == "ustar" || string(prefix[257:265]) == "ustar  \x00")
}

func parseTAROctal(value []byte) (int64, error) {
	text := strings.Trim(string(bytes.Trim(value, "\x00 ")), " ")
	if text == "" {
		return 0, errors.New("empty TAR checksum")
	}
	return strconv.ParseInt(text, 8, 64)
}

func allZero(buffer []byte) bool {
	for _, value := range buffer {
		if value != 0 {
			return false
		}
	}
	return true
}

func tarArchiveEntry(header *archivetar.Header) (ArchiveEntry, error) {
	kind := EntrySpecial
	switch header.Typeflag {
	case archivetar.TypeReg, archivetar.TypeRegA:
		kind = EntryFile
	case archivetar.TypeDir:
		kind = EntryDirectory
	case archivetar.TypeSymlink:
		kind = EntrySymlink
	case archivetar.TypeLink:
		kind = EntryHardlink
	case archivetar.TypeChar, archivetar.TypeBlock, archivetar.TypeFifo:
		kind = EntrySpecial
	default:
		return ArchiveEntry{}, pathError(header.Name,
			fmt.Errorf("%w: TAR type %d", ErrUnsupportedEntry, header.Typeflag))
	}
	entry := ArchiveEntry{Path: header.Name, Kind: kind, LinkTarget: header.Linkname,
		Extra: map[string]string{
			"tar.mode":     strconv.FormatInt(header.Mode, 8),
			"tar.uid":      strconv.Itoa(header.Uid),
			"tar.gid":      strconv.Itoa(header.Gid),
			"tar.uname":    header.Uname,
			"tar.gname":    header.Gname,
			"tar.devmajor": strconv.FormatInt(header.Devmajor, 10),
			"tar.devminor": strconv.FormatInt(header.Devminor, 10),
			"tar.format":   header.Format.String(),
		}}
	if !header.AccessTime.IsZero() {
		entry.Extra["tar.access-time"] = header.AccessTime.Format(time.RFC3339Nano)
	}
	if !header.ChangeTime.IsZero() {
		entry.Extra["tar.change-time"] = header.ChangeTime.Format(time.RFC3339Nano)
	}
	for key, value := range header.PAXRecords {
		entry.Extra["tar.pax."+key] = value
	}
	if kind == EntryFile {
		size := header.Size
		entry.Size = &size
	}
	if !header.ModTime.IsZero() {
		modified := header.ModTime
		entry.ModifiedAt = &modified
	}
	return entry, nil
}

type tarRecord struct {
	offset int64
	size   int64
	kind   EntryKind
}

type tarBackend struct {
	mu      sync.Mutex
	input   Input
	entries []ArchiveEntry
	records map[string]tarRecord
	closed  bool
}

func (backend *tarBackend) Entries(ctx context.Context) ([]ArchiveEntry, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return nil, ErrClosed
	}
	return append([]ArchiveEntry(nil), backend.entries...), nil
}

func (backend *tarBackend) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return nil, ErrClosed
	}
	record, exists := backend.records[name]
	if !exists {
		return nil, pathError(name, ErrEntryNotFound)
	}
	return io.NopCloser(&contextReader{ctx: ctx, reader: io.NewSectionReader(backend.input, record.offset, record.size)}), nil
}

func (backend *tarBackend) Close() error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.closed = true
	return nil
}

type tarGZBackend struct {
	mu      sync.Mutex
	input   Input
	entries []ArchiveEntry
	records map[string]tarRecord
	closed  bool
}

func (backend *tarGZBackend) TotalCompressedSize() int64 { return backend.input.Size() }

func (backend *tarGZBackend) Entries(ctx context.Context) ([]ArchiveEntry, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return nil, ErrClosed
	}
	return append([]ArchiveEntry(nil), backend.entries...), nil
}

func (backend *tarGZBackend) ReadFiles(ctx context.Context, names []string,
	consume func(string, io.Reader) error) (resultErr error) {
	if err := contextError(ctx); err != nil {
		return err
	}
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return ErrClosed
	}
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, exists := backend.records[name]; !exists {
			backend.mu.Unlock()
			return pathError(name, ErrEntryNotFound)
		}
		wanted[name] = struct{}{}
	}
	backend.mu.Unlock()
	if len(wanted) == 0 {
		return nil
	}
	gzipReader, err := gzip.NewReader(&contextReader{ctx: ctx,
		reader: io.NewSectionReader(backend.input, 0, backend.input.Size())})
	if err != nil {
		return tarError("open TAR.GZ", err)
	}
	defer func() { resultErr = errors.Join(resultErr, gzipReader.Close()) }()
	tarReader := archivetar.NewReader(gzipReader)
	for len(wanted) > 0 {
		header, nextErr := tarReader.Next()
		if nextErr != nil {
			if errors.Is(nextErr, io.EOF) {
				return fmt.Errorf("%w: TAR.GZ batch is missing %d requested entries", ErrCorruptArchive, len(wanted))
			}
			return tarError("scan TAR.GZ", nextErr)
		}
		canonical, normalizeErr := NormalizePath(header.Name)
		if normalizeErr != nil {
			return fmt.Errorf("%w: %w", ErrCorruptArchive, normalizeErr)
		}
		if _, selected := wanted[canonical]; !selected {
			continue
		}
		record := backend.records[canonical]
		if record.kind != EntryFile || (header.Typeflag != archivetar.TypeReg && header.Typeflag != archivetar.TypeRegA) ||
			header.Size != record.size {
			return pathError(canonical, fmt.Errorf("%w: TAR.GZ entry metadata changed", ErrCorruptArchive))
		}
		entryReader := &countingReader{reader: &contextReader{ctx: ctx, reader: tarReader}}
		if err := consume(canonical, entryReader); err != nil {
			return err
		}
		if entryReader.count != header.Size {
			return readError("consume "+canonical,
				fmt.Errorf("%w: consumed %d of %d bytes", io.ErrUnexpectedEOF, entryReader.count, header.Size))
		}
		delete(wanted, canonical)
	}
	return nil
}

func (backend *tarGZBackend) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return nil, ErrClosed
	}
	record, exists := backend.records[name]
	backend.mu.Unlock()
	if !exists {
		return nil, pathError(name, ErrEntryNotFound)
	}
	gzipReader, err := gzip.NewReader(&contextReader{ctx: ctx, reader: io.NewSectionReader(backend.input, 0, backend.input.Size())})
	if err != nil {
		return nil, tarError("open TAR.GZ", err)
	}
	tarReader := archivetar.NewReader(gzipReader)
	for {
		header, nextErr := tarReader.Next()
		if nextErr != nil {
			_ = gzipReader.Close()
			if errors.Is(nextErr, io.EOF) {
				return nil, pathError(name, ErrEntryNotFound)
			}
			return nil, tarError("scan TAR.GZ", nextErr)
		}
		canonical, normalizeErr := NormalizePath(header.Name)
		if normalizeErr != nil {
			_ = gzipReader.Close()
			return nil, fmt.Errorf("%w: %w", ErrCorruptArchive, normalizeErr)
		}
		if canonical == name {
			if record.kind != EntryFile || (header.Typeflag != archivetar.TypeReg && header.Typeflag != archivetar.TypeRegA) ||
				header.Size != record.size {
				_ = gzipReader.Close()
				return nil, pathError(canonical, fmt.Errorf("%w: TAR.GZ entry metadata changed", ErrCorruptArchive))
			}
			return &tarGZEntryReader{ctx: ctx, reader: tarReader, gzip: gzipReader}, nil
		}
	}
}

func (backend *tarGZBackend) Close() error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.closed = true
	return nil
}

type tarGZEntryReader struct {
	ctx    context.Context
	reader io.Reader
	gzip   *gzip.Reader
	once   sync.Once
	err    error
}

func (reader *tarGZEntryReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := reader.reader.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, tarError("read TAR.GZ entry", err)
	}
	return n, err
}

func (reader *tarGZEntryReader) Close() error {
	reader.once.Do(func() { reader.err = reader.gzip.Close() })
	return reader.err
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (reader *countingReader) Read(buffer []byte) (int, error) {
	n, err := reader.reader.Read(buffer)
	reader.count += int64(n)
	return n, err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func tarError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, archivetar.ErrHeader) || errors.Is(err, gzip.ErrHeader) ||
		errors.Is(err, gzip.ErrChecksum) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w: %s: %w", ErrCorruptArchive, operation, err)
	}
	return err
}
