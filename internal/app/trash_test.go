package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/operations"
)

type fakeTrash struct {
	mu               sync.Mutex
	paths            []string
	failAt           int
	entered, release chan struct{}
	blockAt          int
}

func (f *fakeTrash) InspectTrash(_ context.Context, path string) (core.TrashSource, error) {
	return core.TrashSource{Path: path, Name: path, Kind: core.KindFile, Revision: "identity"}, nil
}
func (f *fakeTrash) Trash(_ context.Context, source core.TrashSource) error {
	f.mu.Lock()
	n := len(f.paths) + 1
	f.mu.Unlock()
	if n == f.blockAt {
		close(f.entered)
		<-f.release
	}
	if n == f.failAt {
		return errors.New("trash unavailable")
	}
	f.mu.Lock()
	f.paths = append(f.paths, source.Path)
	f.mu.Unlock()
	return nil
}
func waitTrash(t *testing.T, m *operations.Manager, id string) operations.Operation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		op, err := m.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if op.State == operations.Completed || op.State == operations.Cancelled || op.State == operations.Failed {
			return op
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("operation timed out")
	return operations.Operation{}
}
func TestTrashBatchResults(t *testing.T) {
	for _, failAt := range []int{0, 1, 3} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			m := operations.NewManager()
			defer m.Close()
			backend := &fakeTrash{failAt: failAt}
			service := NewTrashService(backend, m)
			sources, err := service.Prepare(context.Background(), []string{"/one", "/empty dir", "/Привет мир", "/link", "/tree"})
			if err != nil {
				t.Fatal(err)
			}
			if len(backend.paths) != 0 {
				t.Fatal("prepare mutated files")
			}
			op, err := service.Start(context.Background(), operations.ContextSnapshot{ActivePanel: "left", SourceDirectory: "fixed"}, sources)
			if err != nil {
				t.Fatal(err)
			}
			sources[0].Path = "/changed"
			op = waitTrash(t, m, op.ID)
			want := uint64(5)
			state := operations.Completed
			if failAt > 0 {
				want = uint64(failAt - 1)
				state = operations.Failed
			}
			if op.State != state || op.Progress.ProcessedFiles != want || *op.Progress.TotalFiles != 5 || op.Progress.TotalBytes != nil || op.Progress.ProcessedBytes != 0 || op.Context.SourceDirectory != "fixed" {
				t.Fatalf("%+v", op)
			}
			if failAt > 0 && op.Error == nil {
				t.Fatal("missing partial error")
			}
			backend.mu.Lock()
			defer backend.mu.Unlock()
			if len(backend.paths) > 0 && backend.paths[0] != "/one" {
				t.Fatal("snapshot changed")
			}
		})
	}
}
func TestTrashCancelDuringItemAccountsForSuccess(t *testing.T) {
	m := operations.NewManager()
	defer m.Close()
	backend := &fakeTrash{blockAt: 2, entered: make(chan struct{}), release: make(chan struct{})}
	service := NewTrashService(backend, m)
	sources, _ := service.Prepare(context.Background(), []string{"/1", "/2", "/3", "/4", "/5"})
	op, err := service.Start(context.Background(), operations.ContextSnapshot{}, sources)
	if err != nil {
		t.Fatal(err)
	}
	<-backend.entered
	cancelDone := make(chan operations.Operation, 1)
	go func() { cancelled, _ := m.Cancel(op.ID); cancelDone <- cancelled }()
	select {
	case pending := <-cancelDone:
		if pending.State != operations.Running || !pending.CancellationRequested || pending.Progress.ProcessedFiles != 1 {
			t.Errorf("%+v", pending)
		}
	case <-time.After(time.Second):
		close(backend.release)
		t.Fatal("cancel blocked by OS call")
	}
	if len(m.List()) != 1 {
		t.Fatal("list blocked or lost operation")
	}
	close(backend.release)
	op = waitTrash(t, m, op.ID)
	if op.State != operations.Cancelled || op.Progress.ProcessedFiles != 2 || *op.Progress.TotalFiles != 5 {
		t.Fatalf("%+v", op)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.paths) != 2 {
		t.Fatal(backend.paths)
	}
}
func TestTrashCancellationOfFinalSuccessfulItemCompletes(t *testing.T) {
	m := operations.NewManager()
	defer m.Close()
	backend := &fakeTrash{blockAt: 1, entered: make(chan struct{}), release: make(chan struct{})}
	service := NewTrashService(backend, m)
	sources, _ := service.Prepare(context.Background(), []string{"/1"})
	op, _ := service.Start(context.Background(), operations.ContextSnapshot{}, sources)
	<-backend.entered
	m.Cancel(op.ID)
	close(backend.release)
	op = waitTrash(t, m, op.ID)
	if op.State != operations.Completed || op.Progress.ProcessedFiles != 1 {
		t.Fatalf("%+v", op)
	}
}
