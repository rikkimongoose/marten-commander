package desktop

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/rikkimongoose/marten-commander/internal/viewer"
)

type ViewerFile struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Path           string `json:"path"`
	Size           string `json:"size"`
	ModifiedAt     string `json:"modifiedAt"`
	InitialMode    string `json:"initialMode"`
	Encoding       string `json:"encoding"`
	EncodingSource string `json:"encodingSource"`
}

type ViewerChunk struct {
	Offset     string `json:"offset"`
	DataBase64 string `json:"dataBase64"`
	Length     int    `json:"length"`
	EOF        bool   `json:"eof"`
	Size       string `json:"size"`
}

type ViewerTextChunk struct {
	StartOffset    string `json:"startOffset"`
	NextOffset     string `json:"nextOffset"`
	Text           string `json:"text"`
	EOF            bool   `json:"eof"`
	Encoding       string `json:"encoding"`
	EncodingSource string `json:"encodingSource"`
	Auto           bool   `json:"auto"`
}

type viewerSession struct {
	file      viewer.File
	size      int64
	detection viewer.EncodingDetection
}

// ViewerService owns open handles. Every successful OpenFile must be paired
// with CloseFile; Wails call contexts cancel individual range reads.
type ViewerService struct {
	source viewer.FileSource
	mu     sync.RWMutex
	files  map[string]*viewerSession
}

func NewViewerService(source viewer.FileSource) *ViewerService {
	return &ViewerService{source: source, files: make(map[string]*viewerSession)}
}

func (s *ViewerService) OpenFile(ctx context.Context, location string) (ViewerFile, error) {
	path, ok := decodeLocation(location)
	if !ok {
		return ViewerFile{}, &CommandError{"invalid_params", "Некорректный адрес файла."}
	}
	f, err := s.source.Open(ctx, path)
	if err != nil {
		return ViewerFile{}, viewerError("Не удалось открыть файл", path, err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = f.Close()
		}
	}()
	meta, err := f.Metadata(ctx)
	if err != nil {
		return ViewerFile{}, viewerError("Не удалось прочитать сведения о файле", path, err)
	}
	sample, readErr := f.ReadAt(ctx, 0, int64ToInt(meta.Size))
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return ViewerFile{}, viewerError("Не удалось прочитать файл", path, readErr)
	}
	id, err := newViewerID()
	if err != nil {
		return ViewerFile{}, &CommandError{"viewer_failed", "Не удалось создать сеанс просмотра."}
	}
	s.mu.Lock()
	detection := viewer.DetectEncoding(sample)
	s.files[id] = &viewerSession{file: f, size: meta.Size, detection: detection}
	s.mu.Unlock()
	keep = true
	return ViewerFile{ID: id, Name: meta.Name, Path: meta.Path, Size: strconv.FormatInt(meta.Size, 10),
		ModifiedAt: meta.ModifiedAt, InitialMode: detectMode(meta.Name, sample),
		Encoding: string(detection.Encoding), EncodingSource: detection.Source}, nil
}

// ReadText decodes one bounded byte-addressed window. The returned next byte
// offset is the only cursor needed for sequential reads; callers may also jump
// to an approximate byte offset without building a whole-file line index.
func (s *ViewerService) ReadText(ctx context.Context, id, offset string, length int, requestedEncoding string) (ViewerTextChunk, error) {
	value, err := strconv.ParseInt(offset, 10, 64)
	if err != nil || value < 0 {
		return ViewerTextChunk{}, &CommandError{"invalid_offset", "Смещение должно быть неотрицательным целым числом."}
	}
	if length <= 0 || length > viewer.MaxTextReadSize {
		return ViewerTextChunk{}, &CommandError{"invalid_length", fmt.Sprintf("За один текстовый запрос можно прочитать от 1 до %d байт.", viewer.MaxTextReadSize)}
	}
	requested, err := viewer.ParseEncoding(requestedEncoding)
	if err != nil {
		return ViewerTextChunk{}, &CommandError{"invalid_encoding", "Неподдерживаемая кодировка."}
	}
	s.mu.RLock()
	session := s.files[id]
	s.mu.RUnlock()
	if session == nil {
		return ViewerTextChunk{}, &CommandError{"viewer_closed", "Файл уже закрыт."}
	}
	enc := requested
	source := "manual"
	auto := requested == viewer.EncodingAuto
	if auto {
		enc = session.detection.Encoding
		source = session.detection.Source
	}
	bomSize := viewer.EncodingBOMSize(enc, session.detection)
	if value >= session.size {
		return ViewerTextChunk{StartOffset: strconv.FormatInt(session.size, 10), NextOffset: strconv.FormatInt(session.size, 10),
			EOF: true, Encoding: string(enc), EncodingSource: source, Auto: auto}, nil
	}
	rawStart := value
	if enc == viewer.EncodingUTF8 || enc == viewer.EncodingUTF16LE || enc == viewer.EncodingUTF16BE {
		rawStart = max(bomSize, value-3)
	} else if rawStart < bomSize {
		rawStart = bomSize
	}
	extra := 4
	if enc == viewer.EncodingUTF16LE || enc == viewer.EncodingUTF16BE {
		extra = 6
	}
	readLength := int(value-rawStart) + length + extra
	readLength = min(readLength, viewer.MaxReadSize)
	data, readErr := session.file.ReadAt(ctx, rawStart, readLength)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
			return ViewerTextChunk{}, readErr
		}
		return ViewerTextChunk{}, viewerError("Не удалось прочитать файл", "", readErr)
	}
	if len(data) == 0 {
		return ViewerTextChunk{StartOffset: offset, NextOffset: offset, EOF: true,
			Encoding: string(enc), EncodingSource: source, Auto: auto}, nil
	}
	start, startIndex := viewer.AlignStart(data, rawStart, value, bomSize, enc)
	endIndex := viewer.CompleteEnd(data, startIndex, length, enc)
	if endIndex < startIndex {
		endIndex = startIndex
	}
	next := rawStart + int64(endIndex)
	if next > session.size {
		next = session.size
	}
	eof := errors.Is(readErr, io.EOF) || next >= session.size
	return ViewerTextChunk{StartOffset: strconv.FormatInt(start, 10), NextOffset: strconv.FormatInt(next, 10),
		Text: viewer.DecodeText(data[startIndex:endIndex], enc), EOF: eof,
		Encoding: string(enc), EncodingSource: source, Auto: auto}, nil
}

func (s *ViewerService) ReadFile(ctx context.Context, id, offset string, length int) (ViewerChunk, error) {
	value, err := strconv.ParseInt(offset, 10, 64)
	if err != nil || value < 0 {
		return ViewerChunk{}, &CommandError{"invalid_offset", "Смещение должно быть неотрицательным целым числом."}
	}
	if length < 0 || length > viewer.MaxReadSize {
		return ViewerChunk{}, &CommandError{"invalid_length", fmt.Sprintf("За один запрос можно прочитать не более %d байт.", viewer.MaxReadSize)}
	}
	s.mu.RLock()
	session := s.files[id]
	s.mu.RUnlock()
	if session == nil {
		return ViewerChunk{}, &CommandError{"viewer_closed", "Файл уже закрыт."}
	}
	currentSize := session.size
	if meta, metaErr := session.file.Metadata(ctx); metaErr == nil {
		currentSize = meta.Size
	} else if errors.Is(metaErr, context.Canceled) || errors.Is(metaErr, context.DeadlineExceeded) {
		return ViewerChunk{}, metaErr
	}
	if value >= currentSize {
		return ViewerChunk{Offset: offset, DataBase64: "", EOF: true, Size: strconv.FormatInt(currentSize, 10)}, nil
	}
	data, readErr := session.file.ReadAt(ctx, value, length)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
			return ViewerChunk{}, readErr
		}
		return ViewerChunk{}, viewerError("Не удалось прочитать файл", "", readErr)
	}
	if errors.Is(readErr, io.EOF) {
		if meta, metaErr := session.file.Metadata(ctx); metaErr == nil {
			currentSize = meta.Size
		}
	}
	eof := errors.Is(readErr, io.EOF) || int64(len(data)) < int64(length) || int64(len(data)) >= currentSize-value
	return ViewerChunk{Offset: offset, DataBase64: base64.StdEncoding.EncodeToString(data), Length: len(data), EOF: eof,
		Size: strconv.FormatInt(currentSize, 10)}, nil
}

func (s *ViewerService) CloseFile(id string) error {
	s.mu.Lock()
	session := s.files[id]
	delete(s.files, id)
	s.mu.Unlock()
	if session == nil {
		return nil
	}
	if err := session.file.Close(); err != nil {
		return &CommandError{"close_failed", fmt.Sprintf("Не удалось закрыть файл: %v", err)}
	}
	return nil
}

// ServiceShutdown is a Wails lifecycle hook and closes sessions even if the
// webview disappears without running its React cleanup.
func (s *ViewerService) ServiceShutdown() error {
	s.mu.Lock()
	sessions := make([]*viewerSession, 0, len(s.files))
	for _, session := range s.files {
		sessions = append(sessions, session)
	}
	s.files = make(map[string]*viewerSession)
	s.mu.Unlock()
	var result error
	for _, session := range sessions {
		result = errors.Join(result, session.file.Close())
	}
	return result
}

func viewerError(action, path string, err error) error {
	message := action
	if path != "" {
		message += fmt.Sprintf(" %q", display(path))
	}
	if errors.Is(err, viewer.ErrNotRegular) {
		message += ": выбранный объект не является обычным файлом."
	} else {
		message += ": " + err.Error()
	}
	return &CommandError{"viewer_failed", message}
}

func newViewerID() (string, error) {
	var value [18]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func detectMode(name string, sample []byte) string {
	if isImage(sample) {
		return "image"
	}
	if len(sample) == 0 || looksLikeText(sample) {
		return "text"
	}
	// Extension is only a weak fallback after content inspection.
	if strings.HasPrefix(mime.TypeByExtension(strings.ToLower(filepath.Ext(name))), "text/") && looksPossiblyLegacyText(sample) {
		return "text"
	}
	return "hex"
}

func looksPossiblyLegacyText(data []byte) bool {
	controls := 0
	for _, b := range data {
		if b == 0 {
			return false
		}
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' && b != '\f' && b != '\b' {
			controls++
		}
	}
	return controls*20 <= len(data)
}

func isImage(data []byte) bool {
	return len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n" ||
		len(data) >= 3 && string(data[:3]) == "\xff\xd8\xff" ||
		len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a")
}

func looksLikeText(data []byte) bool {
	if len(data) >= 2 && ((data[0] == 0xff && data[1] == 0xfe) || (data[0] == 0xfe && data[1] == 0xff)) {
		return true
	}
	if len(data) >= 3 && string(data[:3]) == "\xef\xbb\xbf" {
		return true
	}
	if !utf8.Valid(data) {
		return false
	}
	controls := 0
	for _, b := range data {
		if b == 0 {
			return false
		}
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' && b != '\f' && b != '\b' {
			controls++
		}
	}
	return controls*20 <= len(data)
}

func int64ToInt(value int64) int {
	if value <= 0 {
		return 0
	}
	if value > 512 {
		return 512
	}
	return int(value)
}
