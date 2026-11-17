package desktop

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/rikkimongoose/marten-commander/internal/viewer"
)

type viewerSourceStub struct {
	file viewer.File
	err  error
}

func (s viewerSourceStub) Open(context.Context, string) (viewer.File, error) { return s.file, s.err }

type viewerFileStub struct {
	data   []byte
	closed atomic.Int32
	block  bool
	size   int64
}

func (f *viewerFileStub) Metadata(context.Context) (viewer.Metadata, error) {
	size := f.size
	if size == 0 {
		size = int64(len(f.data))
	}
	return viewer.Metadata{Name: "sample.bin", Path: "/sample.bin", Size: size, ModifiedAt: "now"}, nil
}
func (f *viewerFileStub) ReadAt(ctx context.Context, offset int64, length int) ([]byte, error) {
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if offset >= int64(len(f.data)) {
		return nil, io.EOF
	}
	end := min(int(offset)+length, len(f.data))
	data := append([]byte(nil), f.data[offset:end]...)
	if end == len(f.data) && len(data) < length {
		return data, io.EOF
	}
	return data, nil
}
func (f *viewerFileStub) Close() error { f.closed.Add(1); return nil }

func TestViewerServiceOpenReadClose(t *testing.T) {
	data := []byte{0, 1, 2, 0xff, 4}
	file := &viewerFileStub{data: data}
	service := NewViewerService(viewerSourceStub{file: file})
	opened, err := service.OpenFile(context.Background(), encode(filepath.Join(t.TempDir(), "sample.bin")))
	if err != nil {
		t.Fatal(err)
	}
	if opened.ID == "" || opened.Name != "sample.bin" || opened.Path != "/sample.bin" ||
		opened.Size != strconv.Itoa(len(data)) || opened.InitialMode != "hex" {
		t.Fatalf("unexpected open result: %#v", opened)
	}
	chunk, err := service.ReadFile(context.Background(), opened.ID, "2", 10)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(chunk.DataBase64)
	if err != nil || string(decoded) != string(data[2:]) || !chunk.EOF || chunk.Offset != "2" {
		t.Fatalf("unexpected chunk: %#v, decoded %v, error %v", chunk, decoded, err)
	}
	if err := service.CloseFile(opened.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.CloseFile(opened.ID); err != nil {
		t.Fatal(err)
	}
	if file.closed.Load() != 1 {
		t.Fatalf("closed %d times", file.closed.Load())
	}
	if _, err := service.ReadFile(context.Background(), opened.ID, "0", 1); err == nil {
		t.Fatal("closed session remained readable")
	}
}

func TestViewerServiceReadValidationAndCancellation(t *testing.T) {
	file := &viewerFileStub{data: []byte("text")}
	service := NewViewerService(viewerSourceStub{file: file})
	opened, err := service.OpenFile(context.Background(), encode(filepath.Join(t.TempDir(), "sample")))
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(opened.ID)
	for _, offset := range []string{"", "-1", "1.5", "9223372036854775808"} {
		if _, err := service.ReadFile(context.Background(), opened.ID, offset, 1); err == nil {
			t.Fatalf("accepted offset %q", offset)
		}
	}
	if _, err := service.ReadFile(context.Background(), opened.ID, "0", viewer.MaxReadSize+1); err == nil {
		t.Fatal("accepted oversized read")
	}

	blocked := &viewerFileStub{data: []byte("x"), block: true}
	blockedService := NewViewerService(viewerSourceStub{file: blocked})
	// Open needs its sniff read, so enable blocking only after it succeeds.
	blocked.block = false
	blockedOpen, err := blockedService.OpenFile(context.Background(), encode(filepath.Join(t.TempDir(), "blocked")))
	if err != nil {
		t.Fatal(err)
	}
	blocked.block = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := blockedService.ReadFile(ctx, blockedOpen.ID, "0", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
	_ = blockedService.CloseFile(blockedOpen.ID)
}

func TestViewerServiceReadsBoundedDecodedWindows(t *testing.T) {
	data := append([]byte{0xef, 0xbb, 0xbf}, []byte("abcЖdef\r\nlast")...)
	file := &viewerFileStub{data: data}
	service := NewViewerService(viewerSourceStub{file: file})
	opened, err := service.OpenFile(context.Background(), encode(filepath.Join(t.TempDir(), "sample.txt")))
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(opened.ID)
	if opened.Encoding != "utf-8" || opened.EncodingSource != "bom" {
		t.Fatalf("open encoding: %#v", opened)
	}
	first, err := service.ReadText(context.Background(), opened.ID, "0", 4, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if first.Text != "abcЖ" || first.StartOffset != "3" || first.Encoding != "utf-8" || first.EncodingSource != "bom" || !first.Auto {
		t.Fatalf("first text chunk: %#v", first)
	}
	second, err := service.ReadText(context.Background(), opened.ID, first.NextOffset, 8, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if first.NextOffset != second.StartOffset || second.Text != "def\r\nlas" {
		t.Fatalf("second text chunk: %#v", second)
	}
	if _, err := service.ReadText(context.Background(), opened.ID, "0", viewer.MaxTextReadSize+1, "auto"); err == nil {
		t.Fatal("oversized text read accepted")
	}
	if _, err := service.ReadText(context.Background(), opened.ID, "0", 10, "made-up"); err == nil {
		t.Fatal("unknown encoding accepted")
	}
}

func TestViewerServiceUTF16BOMAndSurrogateBoundary(t *testing.T) {
	// BOM, "A", U+1F600 surrogate pair, "B" in UTF-16 LE.
	data := []byte{0xff, 0xfe, 0x41, 0x00, 0x3d, 0xd8, 0x00, 0xde, 0x42, 0x00}
	service := NewViewerService(viewerSourceStub{file: &viewerFileStub{data: data}})
	opened, err := service.OpenFile(context.Background(), encode(filepath.Join(t.TempDir(), "utf16.txt")))
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(opened.ID)
	chunk, err := service.ReadText(context.Background(), opened.ID, "0", 4, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if chunk.Text != "A😀" || chunk.StartOffset != "2" || chunk.NextOffset != "8" || chunk.Encoding != "utf-16le" {
		t.Fatalf("UTF-16 chunk = %#v", chunk)
	}
	// Jumping onto the low surrogate backs up to include the pair.
	jump, err := service.ReadText(context.Background(), opened.ID, "6", 2, "utf-16le")
	if err != nil {
		t.Fatal(err)
	}
	if jump.Text != "😀" || jump.StartOffset != "4" {
		t.Fatalf("UTF-16 jump = %#v", jump)
	}
}

func TestViewerServiceManualEncodingRereadsOriginalBytes(t *testing.T) {
	data := []byte{0xcf, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2} // Windows-1251 "Привет"
	service := NewViewerService(viewerSourceStub{file: &viewerFileStub{data: data}})
	opened, err := service.OpenFile(context.Background(), encode(filepath.Join(t.TempDir(), "legacy.txt")))
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(opened.ID)
	auto, err := service.ReadText(context.Background(), opened.ID, "0", 32, "auto")
	if err != nil {
		t.Fatal(err)
	}
	manual, err := service.ReadText(context.Background(), opened.ID, "0", 32, "koi8-r")
	if err != nil {
		t.Fatal(err)
	}
	if auto.Text != "Привет" || auto.EncodingSource != "default" || !auto.Auto {
		t.Fatalf("auto = %#v", auto)
	}
	if manual.Text == auto.Text || manual.EncodingSource != "manual" || manual.Auto {
		t.Fatalf("manual = %#v", manual)
	}
	if string(data) != string([]byte{0xcf, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2}) {
		t.Fatal("source bytes changed")
	}
}

func TestViewerTextReadCancellationAndLargeOffsets(t *testing.T) {
	file := &viewerFileStub{data: []byte("sample")}
	service := NewViewerService(viewerSourceStub{file: file})
	opened, err := service.OpenFile(context.Background(), encode(filepath.Join(t.TempDir(), "sample")))
	if err != nil {
		t.Fatal(err)
	}
	file.block = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.ReadText(ctx, opened.ID, "0", 4, "auto"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	file.block = false
	_ = service.CloseFile(opened.ID)

	large := &viewerFileStub{data: []byte("header"), size: 3 * 1024 * 1024 * 1024}
	largeService := NewViewerService(viewerSourceStub{file: large})
	largeOpen, err := largeService.OpenFile(context.Background(), encode(filepath.Join(t.TempDir(), "large")))
	if err != nil {
		t.Fatal(err)
	}
	defer largeService.CloseFile(largeOpen.ID)
	chunk, err := largeService.ReadText(context.Background(), largeOpen.ID, strconv.FormatInt(2*1024*1024*1024, 10), 64, "utf-8")
	if err != nil {
		t.Fatal(err)
	}
	if !chunk.EOF {
		t.Fatalf("sparse EOF = %#v", chunk)
	}
}

func TestViewerServiceShutdownClosesEverySession(t *testing.T) {
	files := []*viewerFileStub{{data: []byte("one")}, {data: []byte("two")}}
	index := 0
	source := fileSourceFunc(func(context.Context, string) (viewer.File, error) {
		file := files[index]
		index++
		return file, nil
	})
	service := NewViewerService(source)
	for range files {
		if _, err := service.OpenFile(context.Background(), encode(filepath.Join(t.TempDir(), "file"))); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.closed.Load() != 1 {
			t.Fatalf("shutdown close count = %d", file.closed.Load())
		}
	}
}

type fileSourceFunc func(context.Context, string) (viewer.File, error)

func (f fileSourceFunc) Open(ctx context.Context, path string) (viewer.File, error) {
	return f(ctx, path)
}

func TestViewerModeDetectionUsesContent(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"binary.txt", []byte{0, 1, 2}, "hex"},
		{"unknown.bin", []byte("Привет\n"), "text"},
		{"empty", nil, "text"},
		{"photo.bin", []byte("\x89PNG\r\n\x1a\nrest"), "image"},
		{"photo.txt", []byte("GIF89arest"), "image"},
	}
	for _, test := range tests {
		if got := detectMode(test.name, test.data); got != test.want {
			t.Errorf("%s: got %s, want %s", test.name, got, test.want)
		}
	}
}
