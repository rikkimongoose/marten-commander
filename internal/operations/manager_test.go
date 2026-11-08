package operations

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

func snapshot() ContextSnapshot {
	destination := "right"
	return ContextSnapshot{ActivePanel: "left", Sources: []string{"one", "two"}, SourceDirectory: "left",
		DestinationDirectory: &destination, ParametersJSON: `{"replace":false}`}
}

// testOperationRunner simulates a long operation without touching the file
// system. Production registers no instance of it and F5/F6/F8 remain disabled.
type testOperationRunner struct {
	steps   []Progress
	release <-chan struct{}
}

func (r testOperationRunner) Run(ctx context.Context, reporter Reporter) error {
	for _, progress := range r.steps {
		if err := reporter.Report(progress); err != nil {
			return err
		}
	}
	if r.release != nil {
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func waitState(t *testing.T, manager *Manager, id string, state State) Operation {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		operation, err := manager.Get(id)
		if err == nil && operation.State == state {
			return operation
		}
		time.Sleep(time.Millisecond)
	}
	operation, err := manager.Get(id)
	t.Fatalf("operation did not reach %s: %#v, %v", state, operation, err)
	return Operation{}
}

func TestCreateUsesUniqueIDsAndCopiesContext(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	input := snapshot()
	first, err := manager.Create("copy", input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Create("copy", input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.ID == "" {
		t.Fatalf("IDs are not unique: %q", first.ID)
	}
	input.Sources[0] = "changed"
	*input.DestinationDirectory = "changed"
	first.Context.Sources[1] = "caller-change"
	got, _ := manager.Get(first.ID)
	if got.Context.Sources[0] != "one" || got.Context.Sources[1] != "two" || *got.Context.DestinationDirectory != "right" {
		t.Fatalf("manager retained mutable context: %#v", got.Context)
	}
}

func TestQueuedRunningCompletedAndProgress(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	created, _ := manager.Create("copy", snapshot())
	if created.State != Queued || created.UpdateVersion != 1 {
		t.Fatalf("unexpected created operation: %#v", created)
	}
	totalFiles, totalBytes := uint64(2), uint64(100)
	started, err := manager.Start(created.ID, RunnerFunc(func(_ context.Context, reporter Reporter) error {
		return reporter.Report(Progress{ProcessedFiles: 1, TotalFiles: &totalFiles, ProcessedBytes: 40, TotalBytes: &totalBytes, CurrentFile: "one"})
	}))
	if err != nil {
		t.Fatal(err)
	}
	if started.State != Running {
		t.Fatalf("start state = %s", started.State)
	}
	completed := waitState(t, manager, created.ID, Completed)
	if completed.FinishedAt == nil || completed.Progress.ProcessedFiles != 1 || completed.Progress.ProcessedBytes != 40 ||
		completed.Progress.TotalFiles == nil || *completed.Progress.TotalFiles != 2 || completed.Progress.CurrentFile != "" {
		t.Fatalf("unexpected completed operation: %#v", completed)
	}
}

func TestUnknownTotalsStayUnknown(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	created, _ := manager.Create("scan", snapshot())
	gate := make(chan struct{})
	_, err := manager.Start(created.ID, testOperationRunner{steps: []Progress{{ProcessedFiles: 3, ProcessedBytes: 12, CurrentFile: "three"}}, release: gate})
	if err != nil {
		t.Fatal(err)
	}
	running := waitState(t, manager, created.ID, Running)
	deadline := time.Now().Add(time.Second)
	for running.Progress.ProcessedFiles != 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		running, _ = manager.Get(created.ID)
	}
	if running.Progress.TotalFiles != nil || running.Progress.TotalBytes != nil {
		t.Fatalf("unknown totals became known: %#v", running.Progress)
	}
	close(gate)
	waitState(t, manager, created.ID, Completed)
}

func TestRunnerFailureIsStructured(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	events := make(chan Event, 8)
	if err := manager.SetPublisher(func(event Event) { events <- event }); err != nil {
		t.Fatal(err)
	}
	created, _ := manager.Create("copy", snapshot())
	_, _ = manager.Start(created.ID, RunnerFunc(func(context.Context, Reporter) error {
		return &RunError{Code: "read_failed", Message: "Файл недоступен", Cause: errors.New("permission denied")}
	}))
	failed := waitState(t, manager, created.ID, Failed)
	if failed.Error == nil || failed.Error.Code != "read_failed" || failed.Error.Message != "Файл недоступен" {
		t.Fatalf("unexpected error: %#v", failed.Error)
	}
	states := map[State]bool{}
	deadline := time.After(time.Second)
	for !states[Failed] {
		select {
		case event := <-events:
			if event.OperationID == created.ID {
				states[event.State] = true
				if event.State == Failed && (event.Operation.Error == nil || event.Operation.Error.Code != "read_failed") {
					t.Fatalf("failure event lost structured error: %#v", event)
				}
			}
		case <-deadline:
			t.Fatalf("failure lifecycle events not delivered: %#v", states)
		}
	}
	for _, state := range []State{Queued, Running, Failed} {
		if !states[state] {
			t.Fatalf("missing %s event: %#v", state, states)
		}
	}
}

func TestCancelQueuedIsIdempotentAndCannotStart(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	created, _ := manager.Create("copy", snapshot())
	first, err := manager.Cancel(created.ID)
	if err != nil || first.State != Cancelled {
		t.Fatalf("cancel: %#v, %v", first, err)
	}
	second, err := manager.Cancel(created.ID)
	if err != nil || second.State != Cancelled || second.UpdateVersion != first.UpdateVersion {
		t.Fatalf("repeat cancel: %#v, %v", second, err)
	}
	if _, err := manager.Start(created.ID, RunnerFunc(func(context.Context, Reporter) error { return nil })); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("starting cancelled operation: %v", err)
	}
}

func TestCancelRunningSignalsContextAndCompletedStaysCompleted(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	created, _ := manager.Create("copy", snapshot())
	started := make(chan struct{})
	exited := make(chan struct{})
	_, _ = manager.Start(created.ID, RunnerFunc(func(ctx context.Context, _ Reporter) error {
		close(started)
		<-ctx.Done()
		close(exited)
		return ctx.Err()
	}))
	<-started
	cancelled, err := manager.Cancel(created.ID)
	if err != nil || cancelled.State != Cancelled {
		t.Fatalf("cancel running: %#v, %v", cancelled, err)
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("runner did not observe cancellation")
	}

	completed, _ := manager.Create("copy", snapshot())
	_, _ = manager.Start(completed.ID, RunnerFunc(func(context.Context, Reporter) error { return nil }))
	before := waitState(t, manager, completed.ID, Completed)
	after, err := manager.Cancel(completed.ID)
	if err != nil || after.State != Completed || after.UpdateVersion != before.UpdateVersion {
		t.Fatalf("cancel changed completion: %#v, %v", after, err)
	}
}

func TestCancelCompletionRaceEndsInOneTerminalState(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	for i := 0; i < 100; i++ {
		created, _ := manager.Create("race", snapshot())
		gate := make(chan struct{})
		_, _ = manager.Start(created.ID, RunnerFunc(func(context.Context, Reporter) error { <-gate; return nil }))
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); close(gate) }()
		go func() { defer wg.Done(); _, _ = manager.Cancel(created.ID) }()
		wg.Wait()
		deadline := time.Now().Add(time.Second)
		for {
			operation, _ := manager.Get(created.ID)
			if terminal(operation.State) {
				if operation.State != Completed && operation.State != Cancelled {
					t.Fatalf("race state = %s", operation.State)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("race did not terminate")
			}
		}
	}
}

func TestWaitingForUserTransitionsAndInvalidTransition(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	created, _ := manager.Create("copy", snapshot())
	retained := make(chan Reporter, 1)
	_, _ = manager.Start(created.ID, RunnerFunc(func(_ context.Context, reporter Reporter) error {
		if err := reporter.WaitingForUser(); err != nil {
			return err
		}
		retained <- reporter
		if err := reporter.Resume(); err != nil {
			return err
		}
		return nil
	}))
	waitState(t, manager, created.ID, Completed)
	reporter := <-retained
	if err := reporter.Resume(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("completed -> running was allowed: %v", err)
	}
}

func TestCancelWaitingForUser(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	created, _ := manager.Create("copy", snapshot())
	waiting := make(chan struct{})
	_, _ = manager.Start(created.ID, RunnerFunc(func(ctx context.Context, reporter Reporter) error {
		if err := reporter.WaitingForUser(); err != nil {
			return err
		}
		close(waiting)
		<-ctx.Done()
		return ctx.Err()
	}))
	<-waiting
	if operation := waitState(t, manager, created.ID, WaitingForUser); operation.FinishedAt != nil {
		t.Fatal("waiting operation was marked finished")
	}
	cancelled, err := manager.Cancel(created.ID)
	if err != nil || cancelled.State != Cancelled {
		t.Fatalf("cancel waiting: %#v, %v", cancelled, err)
	}
}

func TestListAndConcurrentAccess(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	var ids []string
	for i := 0; i < 20; i++ {
		operation, _ := manager.Create(fmt.Sprintf("type-%d", i), snapshot())
		ids = append(ids, operation.ID)
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_, _ = manager.Get(ids[(i+offset)%len(ids)])
				_ = manager.List()
			}
		}(worker)
	}
	wg.Wait()
	listed := manager.List()
	if len(listed) != len(ids) || listed[0].ID != ids[len(ids)-1] {
		t.Fatalf("unexpected list order/count")
	}
}

func TestConcurrentMutationsProgressAndQueries(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	running, err := manager.Create("progress", snapshot())
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	_, err = manager.Start(running.ID, RunnerFunc(func(_ context.Context, reporter Reporter) error {
		<-start
		for i := uint64(0); i <= 250; i++ {
			if err := reporter.Report(Progress{ProcessedFiles: i, ProcessedBytes: i * 1024}); err != nil {
				return err
			}
			runtime.Gosched()
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for worker := 0; worker < 6; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 400; i++ {
				if _, err := manager.Get(running.ID); err != nil {
					t.Error(err)
					return
				}
				_ = manager.List()
			}
		}()
	}
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				operation, err := manager.Create(fmt.Sprintf("queued-%d-%d", worker, i), snapshot())
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := manager.Cancel(operation.ID); err != nil {
					t.Error(err)
					return
				}
			}
		}(worker)
	}
	close(start)
	wg.Wait()
	completed := waitState(t, manager, running.ID, Completed)
	if completed.Progress.ProcessedFiles != 250 || len(manager.List()) != 121 {
		t.Fatalf("unexpected final concurrent state: progress=%d count=%d", completed.Progress.ProcessedFiles, len(manager.List()))
	}
}

func TestSlowPublisherDoesNotBlockRunnerAndLatestStateIsDelivered(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	blocked := make(chan struct{})
	events := make(chan Event, 20)
	if err := manager.SetPublisher(func(event Event) { <-blocked; events <- event }); err != nil {
		t.Fatal(err)
	}
	created, _ := manager.Create("copy", snapshot())
	done := make(chan struct{})
	_, _ = manager.Start(created.ID, RunnerFunc(func(_ context.Context, reporter Reporter) error {
		for i := uint64(0); i < 10; i++ {
			if err := reporter.Report(Progress{ProcessedFiles: i}); err != nil {
				return err
			}
		}
		close(done)
		return nil
	}))
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slow event sink blocked runner")
	}
	close(blocked)
	completed := waitState(t, manager, created.ID, Completed)
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-events:
			if event.OperationID != created.ID || event.Operation.ID != created.ID || event.UpdateVersion != event.Operation.UpdateVersion {
				t.Fatalf("bad event: %#v", event)
			}
			if event.State == Completed && event.UpdateVersion == completed.UpdateVersion {
				return
			}
		case <-deadline:
			t.Fatal("latest event was not delivered")
		}
	}
}

func TestPublisherEmitsOrdinaryLifecycleTransitions(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	events := make(chan Event, 8)
	if err := manager.SetPublisher(func(event Event) { events <- event }); err != nil {
		t.Fatal(err)
	}
	created, _ := manager.Create("copy", snapshot())
	_, _ = manager.Start(created.ID, RunnerFunc(func(context.Context, Reporter) error { return nil }))
	waitState(t, manager, created.ID, Completed)
	states := map[State]bool{}
	deadline := time.After(time.Second)
	for !states[Completed] {
		select {
		case event := <-events:
			if event.OperationID == created.ID {
				states[event.State] = true
			}
		case <-deadline:
			t.Fatalf("lifecycle events not delivered: %#v", states)
		}
	}
	for _, state := range []State{Queued, Running, Completed} {
		if !states[state] {
			t.Fatalf("missing %s event: %#v", state, states)
		}
	}
}

func TestCloseCancelsWorkAndRejectsNewOperations(t *testing.T) {
	manager := NewManager()
	created, _ := manager.Create("copy", snapshot())
	exited := make(chan struct{})
	_, _ = manager.Start(created.ID, RunnerFunc(func(ctx context.Context, _ Reporter) error { <-ctx.Done(); close(exited); return ctx.Err() }))
	manager.Close()
	select {
	case <-exited:
	default:
		t.Fatal("shutdown did not wait for runner")
	}
	if _, err := manager.Create("copy", snapshot()); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("create after close: %v", err)
	}
}
