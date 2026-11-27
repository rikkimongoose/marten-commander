package archive

import (
	archivezip "archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"sync"
)

const ZIPFormat Format = "zip"

// ZIPDriver implements the format-independent Driver contract with the Go
// standard library. It borrows Input; ArchiveSession remains its owner.
// MaxEntries bounds the central-directory index before ArchiveSession builds a
// second index. Zero uses DefaultExtractionLimits().MaxEntries.
type ZIPDriver struct{ MaxEntries uint64 }

func (ZIPDriver) Format() Format { return ZIPFormat }

func (ZIPDriver) Probe(ctx context.Context, input Input) (Match, error) {
	if err := contextError(ctx); err != nil {
		return NoMatch, err
	}
	if input == nil || input.Size() < 0 {
		return NoMatch, fmt.Errorf("%w: invalid ZIP input", ErrRead)
	}
	var signature [4]byte
	n, err := input.ReadAt(signature[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return NoMatch, err
	}
	hasSignature := n == len(signature) && signature[0] == 'P' && signature[1] == 'K' &&
		((signature[2] == 3 && signature[3] == 4) || (signature[2] == 5 && signature[3] == 6))
	hasEndRecord, err := probeZIPEndRecord(input)
	if err != nil {
		return NoMatch, err
	}
	if hasSignature && hasEndRecord {
		return DefiniteMatch, nil
	}
	if hasSignature {
		// Let Open return the typed corrupt-ZIP diagnostic instead of silently
		// treating a truncated ZIP as an unknown format.
		return DefiniteMatch, nil
	}
	if hasEndRecord {
		// A prepended executable or other self-extracting prefix is legal. Open
		// performs the full central-directory validation before selection wins.
		return PossibleMatch, nil
	}
	return NoMatch, nil
}

func probeZIPEndRecord(input Input) (bool, error) {
	const maximumEndRecord = 22 + (1 << 16) - 1 // fixed EOCD plus maximum comment
	size := input.Size()
	amount := int64(maximumEndRecord)
	if size < amount {
		amount = size
	}
	if amount < 4 {
		return false, nil
	}
	buffer := make([]byte, int(amount))
	n, err := input.ReadAt(buffer, size-amount)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return findZIPEndRecord(buffer[:n]) >= 0, nil
}

func (driver ZIPDriver) Open(ctx context.Context, input Input) (BackendSession, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	maximum := driver.MaxEntries
	if maximum == 0 {
		maximum = DefaultExtractionLimits().MaxEntries
	}
	if count, known, err := probeZIPEntryCount(input); err != nil {
		return nil, zipError("read ZIP central directory", err)
	} else if known && count > maximum {
		return nil, fmt.Errorf("%w: ZIP central directory has %d entries, maximum %d", ErrLimitExceeded, count, maximum)
	}
	reader, err := archivezip.NewReader(input, input.Size())
	if err != nil {
		return nil, zipError("open ZIP", err)
	}
	if uint64(len(reader.File)) > maximum {
		return nil, fmt.Errorf("%w: ZIP central directory has %d entries, maximum %d", ErrLimitExceeded, len(reader.File), maximum)
	}
	backend := &zipBackend{files: make(map[string]*archivezip.File), entries: make([]ArchiveEntry, 0, len(reader.File))}
	for _, file := range reader.File {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		if file.Flags&0x1 != 0 {
			return nil, pathError(file.Name, ErrEncrypted)
		}
		if file.Method != archivezip.Store && file.Method != archivezip.Deflate {
			return nil, pathError(file.Name, fmt.Errorf("%w: ZIP compression method %d", ErrUnsupportedEntry, file.Method))
		}
		mode := file.Mode()
		kind := EntryFile
		switch {
		case file.FileInfo().IsDir():
			kind = EntryDirectory
		case mode&os.ModeSymlink != 0:
			kind = EntrySymlink
		case !mode.IsRegular():
			kind = EntrySpecial
		}
		if file.UncompressedSize64 > math.MaxInt64 || file.CompressedSize64 > math.MaxInt64 {
			return nil, pathError(file.Name, ErrInvalidMetadata)
		}
		size, compressed := int64(file.UncompressedSize64), int64(file.CompressedSize64)
		entry := ArchiveEntry{Path: file.Name, Kind: kind, Size: &size, CompressedSize: &compressed,
			Extra: map[string]string{"zip.method": strconv.Itoa(int(file.Method)), "zip.crc32": fmt.Sprintf("%08x", file.CRC32)}}
		if !file.Modified.IsZero() {
			modified := file.Modified
			entry.ModifiedAt = &modified
		}
		backend.entries = append(backend.entries, entry)
		canonical, normalizeErr := NormalizePath(file.Name)
		if normalizeErr != nil {
			return nil, fmt.Errorf("%w: %w", ErrCorruptArchive, normalizeErr)
		}
		backend.files[canonical] = file
	}
	return backend, nil
}

func probeZIPEntryCount(input Input) (uint64, bool, error) {
	const maximumEndRecord = 22 + (1 << 16) - 1
	size := input.Size()
	amount := int64(maximumEndRecord)
	if size < amount {
		amount = size
	}
	if amount < 22 {
		return 0, false, nil
	}
	buffer := make([]byte, int(amount))
	n, err := input.ReadAt(buffer, size-amount)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, false, err
	}
	buffer = buffer[:n]
	end := findZIPEndRecord(buffer)
	if end < 0 || len(buffer)-end < 22 {
		return 0, false, nil
	}
	count := binary.LittleEndian.Uint16(buffer[end+10 : end+12])
	if count != math.MaxUint16 {
		return uint64(count), true, nil
	}
	zip64 := bytes.LastIndex(buffer[:end], []byte{'P', 'K', 6, 6})
	if zip64 < 0 || len(buffer)-zip64 < 40 {
		return 0, false, nil
	}
	return binary.LittleEndian.Uint64(buffer[zip64+32 : zip64+40]), true, nil
}

func findZIPEndRecord(buffer []byte) int {
	for offset := len(buffer) - 22; offset >= 0; offset-- {
		if buffer[offset] != 'P' || buffer[offset+1] != 'K' || buffer[offset+2] != 5 || buffer[offset+3] != 6 {
			continue
		}
		commentLength := int(binary.LittleEndian.Uint16(buffer[offset+20 : offset+22]))
		if offset+22+commentLength == len(buffer) {
			return offset
		}
	}
	return -1
}

// RegisterZIP installs the standard ZIP driver into an existing registry.
func RegisterZIP(registry *Registry) error {
	if registry == nil {
		return fmt.Errorf("register ZIP: %w", ErrUnsupportedFormat)
	}
	return registry.Register(ZIPDriver{})
}

type zipBackend struct {
	mu      sync.Mutex
	entries []ArchiveEntry
	files   map[string]*archivezip.File
	closed  bool
}

func (z *zipBackend) Entries(ctx context.Context) ([]ArchiveEntry, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.closed {
		return nil, ErrClosed
	}
	return append([]ArchiveEntry(nil), z.entries...), nil
}

func (z *zipBackend) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.closed {
		return nil, ErrClosed
	}
	file, exists := z.files[name]
	if !exists {
		return nil, pathError(name, ErrEntryNotFound)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, zipError("open ZIP entry", err)
	}
	return &zipReadCloser{reader: reader}, nil
}

func (z *zipBackend) Close() error {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.closed = true
	return nil
}

type zipReadCloser struct{ reader io.ReadCloser }

func (r *zipReadCloser) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, zipError("read ZIP entry", err)
	}
	return n, err
}

func (r *zipReadCloser) Close() error { return r.reader.Close() }

func zipError(operation string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, archivezip.ErrChecksum), errors.Is(err, archivezip.ErrFormat):
		return fmt.Errorf("%w: %s: %w", ErrCorruptArchive, operation, err)
	case errors.Is(err, archivezip.ErrAlgorithm):
		return fmt.Errorf("%w: %s: %w", ErrUnsupportedEntry, operation, err)
	default:
		return err
	}
}
