package desktop

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
)

// ViewerWindow is the small native-window surface needed by the viewer
// manager. Keeping it behind an interface makes lifecycle behaviour testable
// without constructing a platform webview.
type ViewerWindow interface {
	Show() error
	Close()
	OnClosing(func())
}

type ViewerWindowOptions struct {
	Name   string
	Title  string
	URL    string
	Width  int
	Height int
}

type ViewerWindowFactory func(ViewerWindowOptions) (ViewerWindow, error)

type viewerWindowRecord struct {
	file   ViewerFile
	window ViewerWindow
}

// ViewerWindowService atomically pairs one native window with one file
// session. Reopening the same path intentionally creates another independent
// session and window.
type ViewerWindowService struct {
	viewers *ViewerService
	factory ViewerWindowFactory
	mu      sync.RWMutex
	windows map[string]viewerWindowRecord
}

func NewViewerWindowService(viewers *ViewerService, factory ViewerWindowFactory) *ViewerWindowService {
	return &ViewerWindowService{viewers: viewers, factory: factory, windows: make(map[string]viewerWindowRecord)}
}

func (s *ViewerWindowService) OpenViewer(ctx context.Context, location string) (file ViewerFile, err error) {
	if s.factory == nil {
		return ViewerFile{}, &CommandError{"viewer_failed", "Создание окна Viewer недоступно."}
	}
	file, err = s.viewers.OpenFile(ctx, location)
	if err != nil {
		return ViewerFile{}, err
	}
	sessionID := file.ID
	keep := false
	defer func() {
		if !keep {
			_ = s.viewers.CloseFile(sessionID)
		}
	}()

	window, err := s.createWindow(ViewerWindowOptions{
		Name: "viewer-" + file.ID, Title: file.Name + " — Marten Commander",
		URL: "/?viewer=" + url.QueryEscape(file.ID), Width: 960, Height: 720,
	})
	if err != nil {
		return ViewerFile{}, &CommandError{"viewer_failed", fmt.Sprintf("Не удалось создать окно Viewer: %v", err)}
	}
	if window == nil {
		return ViewerFile{}, &CommandError{"viewer_failed", "Не удалось создать окно Viewer."}
	}

	s.mu.Lock()
	s.windows[file.ID] = viewerWindowRecord{file: file, window: window}
	s.mu.Unlock()
	if err := s.prepareWindow(window, file.ID); err != nil {
		s.release(file.ID)
		closeViewerWindow(window)
		return ViewerFile{}, &CommandError{"viewer_failed", fmt.Sprintf("Не удалось показать окно Viewer: %v", err)}
	}
	keep = true
	return file, nil
}

func (s *ViewerWindowService) createWindow(options ViewerWindowOptions) (window ViewerWindow, err error) {
	defer func() {
		if reason := recover(); reason != nil {
			err = fmt.Errorf("%v", reason)
		}
	}()
	return s.factory(options)
}

func (s *ViewerWindowService) prepareWindow(window ViewerWindow, id string) (err error) {
	defer func() {
		if reason := recover(); reason != nil {
			err = fmt.Errorf("%v", reason)
		}
	}()
	window.OnClosing(func() { s.release(id) })
	return window.Show()
}

// GetViewer resolves only opaque IDs belonging to live native viewer windows.
func (s *ViewerWindowService) GetViewer(id string) (ViewerFile, error) {
	s.mu.RLock()
	record, ok := s.windows[id]
	s.mu.RUnlock()
	if !ok {
		return ViewerFile{}, &CommandError{"viewer_closed", "Окно Viewer уже закрыто или имеет неизвестный идентификатор."}
	}
	return record.file, nil
}

func (s *ViewerWindowService) release(id string) {
	s.mu.Lock()
	_, ok := s.windows[id]
	if ok {
		delete(s.windows, id)
	}
	s.mu.Unlock()
	if ok {
		_ = s.viewers.CloseFile(id)
	}
}

func (s *ViewerWindowService) ServiceShutdown() error {
	s.mu.Lock()
	records := make(map[string]viewerWindowRecord, len(s.windows))
	for id, record := range s.windows {
		records[id] = record
	}
	clear(s.windows)
	s.mu.Unlock()
	var result error
	for id, record := range records {
		result = errors.Join(result, s.viewers.CloseFile(id))
		closeViewerWindow(record.window)
	}
	return result
}

func closeViewerWindow(window ViewerWindow) {
	defer func() { _ = recover() }()
	window.Close()
}
