package operations

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type conflictOutcome struct {
	decision ConflictDecision
	err      error
}

func conflictRequest(conflictType ConflictType) ConflictRequest {
	sourceKind, destinationKind := File, File
	switch conflictType {
	case DirectoryDirectory:
		sourceKind, destinationKind = Directory, Directory
	case FileDirectory:
		destinationKind = Directory
	case DirectoryFile:
		sourceKind = Directory
	}
	sourceSize, destinationSize := uint64(12), uint64(20)
	sourceTime, destinationTime := time.Unix(10, 0).UTC(), time.Unix(20, 0).UTC()
	return ConflictRequest{Type: conflictType,
		Source:      ConflictMetadata{Path: "/source/файл.txt", Kind: sourceKind, Size: &sourceSize, ModifiedAt: &sourceTime},
		Destination: ConflictMetadata{Path: "/target/файл.txt", Kind: destinationKind, Size: &destinationSize, ModifiedAt: &destinationTime}}
}

func waitConflict(t *testing.T, manager *Manager, operationID string) ConflictRequest {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		operation, err := manager.Get(operationID)
		if err == nil && operation.State == WaitingForUser && operation.PendingConflict != nil {
			return cloneConflictRequest(*operation.PendingConflict)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("operation %s did not expose a pending conflict", operationID)
	return ConflictRequest{}
}

func startConflictRunner(t *testing.T, manager *Manager, request ConflictRequest) (Operation, <-chan conflictOutcome, chan<- struct{}) {
	t.Helper()
	created, err := manager.Create("test-conflict", snapshot())
	if err != nil {
		t.Fatal(err)
	}
	outcome := make(chan conflictOutcome, 1)
	release := make(chan struct{})
	_, err = manager.Start(created.ID, RunnerFunc(func(ctx context.Context, reporter Reporter) error {
		decision, err := reporter.RequestConflict(request)
		outcome <- conflictOutcome{decision, err}
		if err != nil {
			return err
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	return created, outcome, release
}

func resolution(request ConflictRequest, action ConflictAction, applyToAll bool) ConflictResolution {
	return ConflictResolution{OperationID: request.OperationID, ConflictID: request.ConflictID,
		Revision: request.Revision, Action: action, ApplyToAll: applyToAll}
}

func TestConflictLifecycleAndFileActions(t *testing.T) {
	for _, action := range []ConflictAction{Replace, Skip, KeepBoth} {
		t.Run(string(action), func(t *testing.T) {
			manager := NewManager()
			defer manager.Close()
			created, outcome, release := startConflictRunner(t, manager, conflictRequest(FileFile))
			pending := waitConflict(t, manager, created.ID)
			if pending.OperationID != created.ID || pending.ConflictID == "" || pending.Revision == 0 ||
				!slices.Equal(pending.AllowedActions, []ConflictAction{Replace, Skip, KeepBoth, CancelOperation}) {
				t.Fatalf("bad pending conflict: %#v", pending)
			}
			resumed, err := manager.ResolveConflict(resolution(pending, action, false))
			if err != nil || resumed.State != Running || resumed.PendingConflict != nil {
				t.Fatalf("resolve: %#v, %v", resumed, err)
			}
			result := <-outcome
			if result.err != nil || result.decision.Resolution.Action != action {
				t.Fatalf("decision: %#v", result)
			}
			wantRevalidation := action == Replace || action == KeepBoth
			if result.decision.RequiresDestinationRevalidation != wantRevalidation {
				t.Fatalf("revalidation=%v", result.decision.RequiresDestinationRevalidation)
			}
			close(release)
			waitState(t, manager, created.ID, Completed)
		})
	}
}

func TestConflictCancelActionCancelsWholeOperation(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	created, outcome, _ := startConflictRunner(t, manager, conflictRequest(FileFile))
	pending := waitConflict(t, manager, created.ID)
	cancelled, err := manager.ResolveConflict(resolution(pending, CancelOperation, true))
	if err != nil || cancelled.State != Cancelled || cancelled.PendingConflict != nil {
		t.Fatalf("cancel: %#v, %v", cancelled, err)
	}
	result := <-outcome
	if !errors.Is(result.err, ErrConflictCancelled) || result.decision.Resolution.ApplyToAll {
		t.Fatalf("cancel outcome: %#v", result)
	}
}

func TestConflictRejectsStaleDuplicateAndCrossOperationResponses(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	first, firstOutcome, firstRelease := startConflictRunner(t, manager, conflictRequest(FileFile))
	second, _, secondRelease := startConflictRunner(t, manager, conflictRequest(FileFile))
	firstConflict, secondConflict := waitConflict(t, manager, first.ID), waitConflict(t, manager, second.ID)

	invalid := []ConflictResolution{
		{OperationID: "missing", ConflictID: firstConflict.ConflictID, Revision: firstConflict.Revision, Action: Skip},
		{OperationID: first.ID, ConflictID: secondConflict.ConflictID, Revision: firstConflict.Revision, Action: Skip},
		{OperationID: first.ID, ConflictID: firstConflict.ConflictID, Revision: firstConflict.Revision + 1, Action: Skip},
	}
	for _, answer := range invalid {
		if _, err := manager.ResolveConflict(answer); err == nil {
			t.Fatalf("accepted invalid response: %#v", answer)
		}
		if current, _ := manager.Get(first.ID); current.State != WaitingForUser || current.PendingConflict == nil {
			t.Fatalf("invalid response changed operation: %#v", current)
		}
	}
	valid := resolution(firstConflict, Skip, false)
	if _, err := manager.ResolveConflict(valid); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ResolveConflict(valid); err == nil {
		t.Fatal("duplicate response was accepted")
	}
	if result := <-firstOutcome; result.err != nil || result.decision.Resolution.Action != Skip {
		t.Fatalf("result: %#v", result)
	}
	close(firstRelease)
	if _, err := manager.Cancel(second.ID); err != nil {
		t.Fatal(err)
	}
	close(secondRelease)
}

func TestCancelAndShutdownWakeConflictWaiter(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		manager := NewManager()
		defer manager.Close()
		created, outcome, _ := startConflictRunner(t, manager, conflictRequest(FileFile))
		waitConflict(t, manager, created.ID)
		if _, err := manager.Cancel(created.ID); err != nil {
			t.Fatal(err)
		}
		select {
		case result := <-outcome:
			if !errors.Is(result.err, context.Canceled) {
				t.Fatalf("waiter error: %v", result.err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancel did not wake conflict waiter")
		}
		if len(manager.PendingConflicts()) != 0 {
			t.Fatal("cancel retained pending conflict")
		}
	})

	t.Run("shutdown", func(t *testing.T) {
		manager := NewManager()
		created, outcome, _ := startConflictRunner(t, manager, conflictRequest(FileFile))
		waitConflict(t, manager, created.ID)
		closed := make(chan struct{})
		go func() { manager.Close(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("shutdown left a waiting goroutine")
		}
		if result := <-outcome; !errors.Is(result.err, context.Canceled) {
			t.Fatalf("shutdown waiter error: %v", result.err)
		}
	})
}

func TestApplyToAllUsesOnlyCompatibleTypeAndCurrentOperation(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	created, err := manager.Create("policy", snapshot())
	if err != nil {
		t.Fatal(err)
	}
	decisions := make(chan ConflictDecision, 3)
	_, err = manager.Start(created.ID, RunnerFunc(func(ctx context.Context, reporter Reporter) error {
		for _, conflictType := range []ConflictType{FileFile, FileFile, DirectoryDirectory} {
			decision, err := reporter.RequestConflict(conflictRequest(conflictType))
			if err != nil {
				return err
			}
			decisions <- decision
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	first := waitConflict(t, manager, created.ID)
	applyToAll := resolution(first, KeepBoth, true)
	applyToAll.CustomTargetPath = "/target/specific-to-first-conflict"
	if _, err := manager.ResolveConflict(applyToAll); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		decision := <-decisions
		if decision.Resolution.Action != KeepBoth || !decision.Resolution.ApplyToAll {
			t.Fatalf("file policy %d: %#v", index, decision)
		}
		if index == 1 && decision.Resolution.CustomTargetPath != "" {
			t.Fatalf("file policy reused a conflict-specific target: %#v", decision)
		}
	}
	directory := waitConflict(t, manager, created.ID)
	if directory.Type != DirectoryDirectory || actionAllowed(directory.AllowedActions, Replace) {
		t.Fatalf("file policy leaked to directory: %#v", directory)
	}
	if _, err := manager.ResolveConflict(resolution(directory, Merge, false)); err != nil {
		t.Fatal(err)
	}
	if decision := <-decisions; decision.Resolution.Action != Merge {
		t.Fatalf("directory decision: %#v", decision)
	}
	waitState(t, manager, created.ID, Completed)
	manager.mu.RLock()
	policies := manager.operations[created.ID].policies
	manager.mu.RUnlock()
	if policies != nil {
		t.Fatalf("completed operation retained policies: %#v", policies)
	}

	other, _, _ := startConflictRunner(t, manager, conflictRequest(FileFile))
	if pending := waitConflict(t, manager, other.ID); pending.Type != FileFile {
		t.Fatalf("policy leaked to another operation: %#v", pending)
	}
	_, _ = manager.Cancel(other.ID)
}

func TestDirectoryAndTypeMismatchActionsAreSafe(t *testing.T) {
	for _, test := range []struct {
		conflictType ConflictType
		allowed      []ConflictAction
		action       ConflictAction
	}{
		{DirectoryDirectory, []ConflictAction{Merge, Skip, CancelOperation}, Merge},
		{FileDirectory, []ConflictAction{Skip, CancelOperation}, Skip},
		{DirectoryFile, []ConflictAction{Skip, CancelOperation}, Skip},
	} {
		t.Run(string(test.conflictType), func(t *testing.T) {
			manager := NewManager()
			defer manager.Close()
			created, outcome, release := startConflictRunner(t, manager, conflictRequest(test.conflictType))
			pending := waitConflict(t, manager, created.ID)
			if !slices.Equal(pending.AllowedActions, test.allowed) {
				t.Fatalf("allowed: %#v", pending.AllowedActions)
			}
			unsafe := resolution(pending, Replace, false)
			if _, err := manager.ResolveConflict(unsafe); err == nil {
				t.Fatal("unsafe replace was accepted")
			}
			if _, err := manager.ResolveConflict(resolution(pending, test.action, false)); err != nil {
				t.Fatal(err)
			}
			if result := <-outcome; result.err != nil || result.decision.Resolution.Action != test.action {
				t.Fatalf("outcome: %#v", result)
			}
			close(release)
		})
	}
}

func TestPendingConflictSnapshotIsRecoverableAndImmutable(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	events := make(chan Event, 8)
	if err := manager.SetPublisher(func(event Event) { events <- event }); err != nil {
		t.Fatal(err)
	}
	created, _, _ := startConflictRunner(t, manager, conflictRequest(FileFile))
	pending := waitConflict(t, manager, created.ID)
	list := manager.PendingConflicts()
	if len(list) != 1 || list[0].ConflictID != pending.ConflictID {
		t.Fatalf("pending API: %#v", list)
	}
	list[0].AllowedActions[0] = Skip
	if current := manager.PendingConflicts(); current[0].AllowedActions[0] != Replace {
		t.Fatal("pending API leaked mutable data")
	}
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-events:
			if event.OperationID == created.ID && event.State == WaitingForUser {
				if event.Operation.PendingConflict == nil || event.Operation.PendingConflict.ConflictID != pending.ConflictID {
					t.Fatalf("event lost conflict: %#v", event)
				}
				_, _ = manager.Cancel(created.ID)
				return
			}
		case <-deadline:
			t.Fatal("waiting event was not delivered")
		}
	}
}

func TestConcurrentStaleResponsesDoNotResolveConflict(t *testing.T) {
	manager := NewManager()
	defer manager.Close()
	created, outcome, release := startConflictRunner(t, manager, conflictRequest(FileFile))
	pending := waitConflict(t, manager, created.ID)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			answer := resolution(pending, Skip, false)
			answer.ConflictID = answer.ConflictID + "-stale"
			_, _ = manager.ResolveConflict(answer)
			_, _ = manager.Get(created.ID)
			_ = manager.List()
		}(i)
	}
	wg.Wait()
	if operation, _ := manager.Get(created.ID); operation.State != WaitingForUser {
		t.Fatalf("stale response changed state: %#v", operation)
	}
	if _, err := manager.ResolveConflict(resolution(pending, Skip, false)); err != nil {
		t.Fatal(err)
	}
	if result := <-outcome; result.err != nil {
		t.Fatal(result.err)
	}
	close(release)
}

func TestNextKeepBothPathPreservesUnicodeAndExtension(t *testing.T) {
	occupied := map[string]bool{
		filepath.Join("/tmp", "отчёт.данные (1).pdf"): true,
		filepath.Join("/tmp", "отчёт.данные (2).pdf"): true,
	}
	path, err := NextKeepBothPath(filepath.Join("/tmp", "отчёт.данные.pdf"), func(candidate string) (bool, error) {
		return occupied[candidate], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/tmp", "отчёт.данные (3).pdf")
	if path != want {
		t.Fatalf("path=%q want %q", path, want)
	}
}

func TestNextKeepBothPathRechecksChangingDestination(t *testing.T) {
	firstChecked := make(chan struct{})
	continueCheck := make(chan struct{})
	var mu sync.Mutex
	occupied := map[string]bool{}
	destination := filepath.Join("/tmp", "файл.txt")
	done := make(chan string, 1)
	go func() {
		path, _ := NextKeepBothPath(destination, func(candidate string) (bool, error) {
			if strings.HasSuffix(candidate, " (1).txt") {
				close(firstChecked)
				<-continueCheck
			}
			mu.Lock()
			defer mu.Unlock()
			return occupied[candidate], nil
		})
		done <- path
	}()
	<-firstChecked
	mu.Lock()
	occupied[filepath.Join("/tmp", "файл (1).txt")] = true
	mu.Unlock()
	close(continueCheck)
	if got := <-done; got != filepath.Join("/tmp", "файл (2).txt") {
		t.Fatalf("changing destination was not rechecked: %q", got)
	}
}
