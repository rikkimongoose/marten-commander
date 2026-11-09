package desktop

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rikkimongoose/marten-commander/internal/operations"
)

func TestOperationContextSnapshotUsesEffectiveSelectionAndTarget(t *testing.T) {
	leftDir, rightDir := encode("/left"), encode("/right")
	selected, focused := encode("/left/selected"), encode("/left/focused")
	command := CommandContext{ActivePanel: "left",
		Left:  PanelContext{DirectoryLocation: &leftDir, SelectedLocations: []string{selected}, FocusedLocation: &focused},
		Right: PanelContext{DirectoryLocation: &rightDir, SelectedLocations: []string{}},
	}
	snapshot, err := OperationContextSnapshot(command, json.RawMessage(`{"mode":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ActivePanel != "left" || snapshot.SourceDirectory != leftDir || snapshot.DestinationDirectory == nil ||
		*snapshot.DestinationDirectory != rightDir || len(snapshot.Sources) != 1 || snapshot.Sources[0] != selected ||
		snapshot.ParametersJSON != `{"mode":"test"}` {
		t.Fatalf("unexpected operation snapshot: %#v", snapshot)
	}
	command.Left.SelectedLocations[0] = encode("/changed")
	command.Right.DirectoryLocation = nil
	if snapshot.Sources[0] != selected || *snapshot.DestinationDirectory != rightDir {
		t.Fatal("snapshot retained command context references")
	}
}

func TestOperationContextSnapshotFallsBackToCursor(t *testing.T) {
	leftDir, focused := encode("/left"), encode("/left/focused")
	command := CommandContext{ActivePanel: "left",
		Left:  PanelContext{DirectoryLocation: &leftDir, SelectedLocations: []string{}, FocusedLocation: &focused},
		Right: PanelContext{SelectedLocations: []string{}},
	}
	snapshot, err := OperationContextSnapshot(command, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Sources) != 1 || snapshot.Sources[0] != focused || snapshot.DestinationDirectory != nil {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
}

func TestOperationContextSnapshotRejectsInvalidContextAndParams(t *testing.T) {
	for _, test := range []struct {
		context CommandContext
		params  json.RawMessage
	}{
		{CommandContext{}, json.RawMessage(`{}`)},
		{CommandContext{ActivePanel: "left", Left: PanelContext{SelectedLocations: []string{}}, Right: PanelContext{SelectedLocations: []string{}}}, json.RawMessage(`{}`)},
		{CommandContext{ActivePanel: "left", Left: PanelContext{SelectedLocations: []string{}}, Right: PanelContext{SelectedLocations: []string{}}}, json.RawMessage(`[]`)},
	} {
		if _, err := OperationContextSnapshot(test.context, test.params); err == nil {
			t.Fatalf("accepted invalid snapshot: %#v", test)
		}
	}
}

type operationManagerStub struct {
	operation operations.Operation
	closed    bool
}

func (s *operationManagerStub) Get(string) (operations.Operation, error) { return s.operation, nil }
func (s *operationManagerStub) List() []operations.Operation {
	return []operations.Operation{s.operation}
}
func (s *operationManagerStub) Cancel(string) (operations.Operation, error) {
	s.operation.State = operations.Cancelled
	return s.operation, nil
}
func (s *operationManagerStub) ResolveConflict(operations.ConflictResolution) (operations.Operation, error) {
	return s.operation, nil
}
func (s *operationManagerStub) PendingConflicts() []operations.ConflictRequest { return nil }
func (s *operationManagerStub) Close()                                         { s.closed = true }

func TestOperationServiceListsGetsCancelsAndShutsDown(t *testing.T) {
	manager := &operationManagerStub{operation: operations.Operation{ID: "one", State: operations.Running}}
	service := NewOperationService(manager)
	if listed, err := service.ListOperations(context.Background()); err != nil || len(listed) != 1 {
		t.Fatalf("list: %#v, %v", listed, err)
	}
	if got, err := service.GetOperation(context.Background(), "one"); err != nil || got.ID != "one" {
		t.Fatalf("get: %#v, %v", got, err)
	}
	if cancelled, err := service.CancelOperation(context.Background(), "one"); err != nil || cancelled.State != operations.Cancelled {
		t.Fatalf("cancel: %#v, %v", cancelled, err)
	}
	if err := service.ServiceShutdown(); err != nil || !manager.closed {
		t.Fatalf("shutdown: %v, closed=%v", err, manager.closed)
	}
}

func TestOperationServiceReadsCurrentManagerState(t *testing.T) {
	manager := operations.NewManager()
	defer manager.Close()
	service := NewOperationService(manager)
	created, err := manager.Create("copy", operations.ContextSnapshot{ActivePanel: "left", Sources: []string{"source"}})
	if err != nil {
		t.Fatal(err)
	}
	reported := make(chan struct{})
	_, err = manager.Start(created.ID, operations.RunnerFunc(func(ctx context.Context, reporter operations.Reporter) error {
		totalFiles, totalBytes := uint64(4), uint64(4096)
		if err := reporter.Report(operations.Progress{ProcessedFiles: 2, TotalFiles: &totalFiles,
			ProcessedBytes: 2048, TotalBytes: &totalBytes, CurrentFile: "second"}); err != nil {
			return err
		}
		close(reported)
		<-ctx.Done()
		return ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-reported:
	case <-time.After(time.Second):
		t.Fatal("runner did not report progress")
	}

	got, err := service.GetOperation(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != operations.Running || got.Progress.ProcessedFiles != 2 || got.Progress.ProcessedBytes != 2048 ||
		got.Progress.TotalFiles == nil || *got.Progress.TotalFiles != 4 || got.Progress.CurrentFile != "second" {
		t.Fatalf("GetOperation returned stale state: %#v", got)
	}
	listed, err := service.ListOperations(context.Background())
	if err != nil || len(listed) != 1 || listed[0].UpdateVersion != got.UpdateVersion || listed[0].Progress.ProcessedFiles != 2 {
		t.Fatalf("ListOperations returned stale state: %#v, %v", listed, err)
	}
	cancelled, err := service.CancelOperation(context.Background(), created.ID)
	if err != nil || cancelled.State != operations.Cancelled {
		t.Fatalf("CancelOperation: %#v, %v", cancelled, err)
	}
	after, err := service.GetOperation(context.Background(), created.ID)
	if err != nil || after.State != operations.Cancelled || after.UpdateVersion <= got.UpdateVersion {
		t.Fatalf("GetOperation did not expose cancellation: %#v, %v", after, err)
	}
}

func TestOperationServiceRestoresAndResolvesPendingConflict(t *testing.T) {
	manager := operations.NewManager()
	defer manager.Close()
	service := NewOperationService(manager)
	created, err := manager.Create("copy", operations.ContextSnapshot{ActivePanel: "left"})
	if err != nil {
		t.Fatal(err)
	}
	decision := make(chan operations.ConflictDecision, 1)
	_, err = manager.Start(created.ID, operations.RunnerFunc(func(ctx context.Context, reporter operations.Reporter) error {
		result, err := reporter.RequestConflict(operations.ConflictRequest{Type: operations.FileFile,
			Source:      operations.ConflictMetadata{Path: "/source.txt", Kind: operations.File},
			Destination: operations.ConflictMetadata{Path: "/destination.txt", Kind: operations.File}})
		if err != nil {
			return err
		}
		decision <- result
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	var pending []operations.ConflictRequest
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		pending, err = service.ListPendingConflicts(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(pending) != 1 || pending[0].OperationID != created.ID {
		t.Fatalf("pending API: %#v", pending)
	}
	snapshot, err := service.GetOperation(context.Background(), created.ID)
	if err != nil || snapshot.PendingConflict == nil || snapshot.PendingConflict.ConflictID != pending[0].ConflictID {
		t.Fatalf("operation snapshot: %#v, %v", snapshot, err)
	}
	resolved, err := service.ResolveConflict(context.Background(), operations.ConflictResolution{
		OperationID: created.ID, ConflictID: pending[0].ConflictID, Revision: pending[0].Revision, Action: operations.Skip})
	if err != nil || resolved.State != operations.Running || resolved.PendingConflict != nil {
		t.Fatalf("resolve API: %#v, %v", resolved, err)
	}
	if result := <-decision; result.Resolution.Action != operations.Skip {
		t.Fatalf("runner decision: %#v", result)
	}
}
