package desktop

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rikkimongoose/marten-commander/internal/viewer"
)

type viewerWindowStub struct {
	mu        sync.Mutex
	callbacks []func()
	showErr   error
	shown     atomic.Int32
	closed    atomic.Int32
}

func (w *viewerWindowStub) Show() error { w.shown.Add(1); return w.showErr }
func (w *viewerWindowStub) Close()      { w.closed.Add(1) }
func (w *viewerWindowStub) OnClosing(callback func()) {
	w.mu.Lock()
	w.callbacks = append(w.callbacks, callback)
	w.mu.Unlock()
}
func (w *viewerWindowStub) systemClose() {
	w.mu.Lock()
	callbacks := append([]func(){}, w.callbacks...)
	w.mu.Unlock()
	for _, callback := range callbacks {
		callback()
	}
}

type viewerWindowFactoryStub struct {
	mu      sync.Mutex
	windows []*viewerWindowStub
	options []ViewerWindowOptions
	err     error
	showErr error
}

func (f *viewerWindowFactoryStub) create(options ViewerWindowOptions) (ViewerWindow, error) {
	if f.err != nil {
		return nil, f.err
	}
	window := &viewerWindowStub{showErr: f.showErr}
	f.mu.Lock()
	f.windows = append(f.windows, window)
	f.options = append(f.options, options)
	f.mu.Unlock()
	return window, nil
}

func newWindowTestServices(t *testing.T) (*ViewerService, *ViewerWindowService, *viewerWindowFactoryStub, *[]*viewerFileStub) {
	t.Helper()
	files := &[]*viewerFileStub{}
	viewers := NewViewerService(fileSourceFunc(func(context.Context, string) (viewer.File, error) {
		file := &viewerFileStub{data: []byte("independent viewer")}
		*files = append(*files, file)
		return file, nil
	}))
	factory := &viewerWindowFactoryStub{}
	return viewers, NewViewerWindowService(viewers, factory.create), factory, files
}

func TestViewerWindowsOpenIndependentlyAndCloseOne(t *testing.T) {
	viewers, windows, factory, files := newWindowTestServices(t)
	first, err := windows.OpenViewer(context.Background(), encode(filepath.Join(t.TempDir(), "a.txt")))
	if err != nil {
		t.Fatal(err)
	}
	second, err := windows.OpenViewer(context.Background(), encode(filepath.Join(t.TempDir(), "b.txt")))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || len(windows.windows) != 2 || len(viewers.files) != 2 {
		t.Fatalf("independent sessions: first=%q second=%q windows=%d files=%d", first.ID, second.ID,
			len(windows.windows), len(viewers.files))
	}
	if factory.options[0].Name != "viewer-"+first.ID || factory.options[0].URL != "/?viewer="+first.ID ||
		factory.options[0].Title != first.Name+" — Marten Commander" {
		t.Fatalf("first window options = %#v", factory.options[0])
	}
	factory.windows[0].systemClose()
	if len(windows.windows) != 1 || len(viewers.files) != 1 || (*files)[0].closed.Load() != 1 || (*files)[1].closed.Load() != 0 {
		t.Fatalf("closing first affected second: windows=%d sessions=%d closes=%d/%d", len(windows.windows), len(viewers.files),
			(*files)[0].closed.Load(), (*files)[1].closed.Load())
	}
	if got, err := windows.GetViewer(second.ID); err != nil || got.ID != second.ID {
		t.Fatalf("second viewer after first close = %#v, %v", got, err)
	}
	if _, err := windows.GetViewer(first.ID); err == nil {
		t.Fatal("closed viewer remained addressable")
	}
}

func TestViewerWindowsAllowSameFileTwiceAndReopenAfterClose(t *testing.T) {
	_, windows, factory, _ := newWindowTestServices(t)
	location := encode(filepath.Join(t.TempDir(), "same.txt"))
	first, err := windows.OpenViewer(context.Background(), location)
	if err != nil {
		t.Fatal(err)
	}
	second, err := windows.OpenViewer(context.Background(), location)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || len(windows.windows) != 2 {
		t.Fatalf("same-file opens were not independent: %q %q", first.ID, second.ID)
	}
	factory.windows[0].systemClose()
	third, err := windows.OpenViewer(context.Background(), location)
	if err != nil {
		t.Fatal(err)
	}
	if third.ID == first.ID || len(windows.windows) != 2 {
		t.Fatalf("reopen failed: third=%q windows=%d", third.ID, len(windows.windows))
	}
}

func TestViewerWindowCreationFailuresReleaseFileSession(t *testing.T) {
	for _, test := range []struct {
		name    string
		factory func(*viewerWindowFactoryStub)
	}{
		{"factory error", func(factory *viewerWindowFactoryStub) { factory.err = errors.New("native failure") }},
		{"show error", func(factory *viewerWindowFactoryStub) { factory.showErr = errors.New("show failure") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			viewers, windows, factory, files := newWindowTestServices(t)
			test.factory(factory)
			if _, err := windows.OpenViewer(context.Background(), encode(filepath.Join(t.TempDir(), "file.txt"))); err == nil {
				t.Fatal("window failure was accepted")
			}
			if len(viewers.files) != 0 || len(windows.windows) != 0 || len(*files) != 1 || (*files)[0].closed.Load() != 1 {
				t.Fatalf("failure leaked resources: sessions=%d windows=%d files=%d closes=%d", len(viewers.files),
					len(windows.windows), len(*files), (*files)[0].closed.Load())
			}
		})
	}
}

func TestViewerWindowDoubleCloseAndShutdownAreIdempotent(t *testing.T) {
	viewers, windows, factory, files := newWindowTestServices(t)
	first, _ := windows.OpenViewer(context.Background(), encode(filepath.Join(t.TempDir(), "one.txt")))
	second, _ := windows.OpenViewer(context.Background(), encode(filepath.Join(t.TempDir(), "two.txt")))
	factory.windows[0].systemClose()
	factory.windows[0].systemClose()
	if (*files)[0].closed.Load() != 1 {
		t.Fatalf("double close count = %d", (*files)[0].closed.Load())
	}
	if err := windows.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	if len(windows.windows) != 0 || len(viewers.files) != 0 || (*files)[1].closed.Load() != 1 || factory.windows[1].closed.Load() != 1 {
		t.Fatalf("shutdown cleanup: windows=%d sessions=%d closes=%d/%d native=%d", len(windows.windows), len(viewers.files),
			(*files)[0].closed.Load(), (*files)[1].closed.Load(), factory.windows[1].closed.Load())
	}
	if first.ID == second.ID {
		t.Fatal("test did not create independent sessions")
	}
}

func TestViewerSystemCloseCancelsOnlyItsActiveRead(t *testing.T) {
	firstFile := &viewerFileStub{data: []byte("first"), entered: make(chan struct{}, 1)}
	secondFile := &viewerFileStub{data: []byte("second")}
	files := []viewer.File{firstFile, secondFile}
	var index atomic.Int32
	viewers := NewViewerService(fileSourceFunc(func(context.Context, string) (viewer.File, error) {
		return files[index.Add(1)-1], nil
	}))
	factory := &viewerWindowFactoryStub{}
	windows := NewViewerWindowService(viewers, factory.create)
	first, _ := windows.OpenViewer(context.Background(), encode(filepath.Join(t.TempDir(), "one")))
	second, _ := windows.OpenViewer(context.Background(), encode(filepath.Join(t.TempDir(), "two")))
	firstFile.block = true
	result := make(chan error, 1)
	go func() {
		_, err := viewers.ReadFile(context.Background(), first.ID, "0", 1)
		result <- err
	}()
	select {
	case <-firstFile.entered:
	case <-time.After(time.Second):
		t.Fatal("read did not start")
	}
	factory.windows[0].systemClose()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("closed read = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("system close did not cancel read")
	}
	if _, err := viewers.ReadFile(context.Background(), second.ID, "0", 1); err != nil {
		t.Fatalf("second viewer was cancelled: %v", err)
	}
}
