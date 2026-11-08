// Package operations owns the lifecycle of long-running file operations.
// It deliberately knows nothing about Wails or React; desktop adapters expose
// snapshots and events at the process boundary.
package operations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type State string

const (
	Queued         State = "queued"
	Running        State = "running"
	WaitingForUser State = "waiting-for-user"
	Completed      State = "completed"
	Failed         State = "failed"
	Cancelled      State = "cancelled"
)

var (
	ErrNotFound          = errors.New("operation not found")
	ErrInvalidTransition = errors.New("invalid operation state transition")
	ErrManagerClosed     = errors.New("operation manager is closed")
)

// ContextSnapshot contains opaque, immutable native-path locations captured
// from CommandContext. ParametersJSON is the command's frozen JSON object.
type ContextSnapshot struct {
	ActivePanel          string   `json:"activePanel"`
	Sources              []string `json:"sources"`
	SourceDirectory      string   `json:"sourceDirectory"`
	DestinationDirectory *string  `json:"destinationDirectory"`
	ParametersJSON       string   `json:"parametersJSON"`
}

type Progress struct {
	ProcessedFiles uint64  `json:"processedFiles"`
	TotalFiles     *uint64 `json:"totalFiles"`
	ProcessedBytes uint64  `json:"processedBytes"`
	TotalBytes     *uint64 `json:"totalBytes"`
	CurrentFile    string  `json:"currentFile"`
}

type OperationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Operation struct {
	ID            string          `json:"operationId"`
	Type          string          `json:"type"`
	Context       ContextSnapshot `json:"context"`
	CreatedAt     time.Time       `json:"createdAt"`
	FinishedAt    *time.Time      `json:"finishedAt"`
	State         State           `json:"state"`
	Progress      Progress        `json:"progress"`
	Error         *OperationError `json:"error"`
	UpdateVersion uint64          `json:"updateVersion"`
}

type Event struct {
	OperationID   string    `json:"operationId"`
	State         State     `json:"state"`
	Progress      Progress  `json:"progress"`
	UpdateVersion uint64    `json:"updateVersion"`
	Operation     Operation `json:"operation"`
}

type Runner interface {
	Run(context.Context, Reporter) error
}

type RunnerFunc func(context.Context, Reporter) error

func (f RunnerFunc) Run(ctx context.Context, reporter Reporter) error { return f(ctx, reporter) }

// Reporter is the runner-facing extension point. WaitingForUser and Resume
// model Issue #11's future pause without defining a conflict protocol here.
type Reporter interface {
	Report(Progress) error
	WaitingForUser() error
	Resume() error
}

// RunError lets a runner return a stable machine-readable failure code.
type RunError struct {
	Code    string
	Message string
	Cause   error
}

func (e *RunError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return e.Code
}

func (e *RunError) Unwrap() error { return e.Cause }

type record struct {
	operation Operation
	cancel    context.CancelFunc
}

type Manager struct {
	mu         sync.RWMutex
	operations map[string]*record
	order      []string
	closed     bool
	root       context.Context
	rootCancel context.CancelFunc
	runs       sync.WaitGroup

	eventMu    sync.Mutex
	eventQueue []Event
	overflow   map[string]Event
	notify     chan struct{}
	stopEvents chan struct{}
	eventsDone chan struct{}
	publisher  func(Event)
	closeOnce  sync.Once
}

func NewManager() *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{operations: make(map[string]*record), root: ctx, rootCancel: cancel}
}

// SetPublisher installs the process-boundary event sink before operations are
// started. Delivery runs off worker goroutines. A bounded backlog keeps all
// ordinary updates and coalesces only under subscriber pressure; API reads are
// always the recovery path for a missed intermediate event.
func (m *Manager) SetPublisher(publisher func(Event)) error {
	if publisher == nil {
		return errors.New("nil operation event publisher")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return ErrManagerClosed
	}
	m.eventMu.Lock()
	defer m.eventMu.Unlock()
	if m.publisher != nil {
		return errors.New("operation event publisher already set")
	}
	m.publisher = publisher
	m.overflow = make(map[string]Event)
	m.notify = make(chan struct{}, 1)
	m.stopEvents = make(chan struct{})
	m.eventsDone = make(chan struct{})
	go m.publishLoop()
	return nil
}

func (m *Manager) Create(operationType string, snapshot ContextSnapshot) (Operation, error) {
	if strings.TrimSpace(operationType) == "" {
		return Operation{}, errors.New("operation type is empty")
	}
	id, err := newID()
	if err != nil {
		return Operation{}, fmt.Errorf("create operation id: %w", err)
	}
	now := time.Now().UTC()
	op := Operation{ID: id, Type: operationType, Context: cloneContext(snapshot), CreatedAt: now, State: Queued, UpdateVersion: 1}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Operation{}, ErrManagerClosed
	}
	m.operations[id] = &record{operation: op}
	m.order = append(m.order, id)
	copy := cloneOperation(op)
	m.mu.Unlock()
	m.enqueue(copy)
	return copy, nil
}

// Start transitions a previously created operation synchronously and runs its
// worker asynchronously. Runner ownership remains in Go, not in the UI.
func (m *Manager) Start(id string, runner Runner) (Operation, error) {
	if runner == nil {
		return Operation{}, errors.New("nil operation runner")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Operation{}, ErrManagerClosed
	}
	rec, ok := m.operations[id]
	if !ok {
		m.mu.Unlock()
		return Operation{}, ErrNotFound
	}
	if err := m.transitionLocked(rec, Running); err != nil {
		m.mu.Unlock()
		return Operation{}, err
	}
	ctx, cancel := context.WithCancel(m.root)
	rec.cancel = cancel
	copy := cloneOperation(rec.operation)
	m.runs.Add(1)
	m.mu.Unlock()
	m.enqueue(copy)
	go m.run(ctx, id, runner)
	return copy, nil
}

func (m *Manager) Get(id string) (Operation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.operations[id]
	if !ok {
		return Operation{}, ErrNotFound
	}
	return cloneOperation(rec.operation), nil
}

func (m *Manager) List() []Operation {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]Operation, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0; i-- {
		result = append(result, cloneOperation(m.operations[m.order[i]].operation))
	}
	return result
}

func (m *Manager) Cancel(id string) (Operation, error) {
	m.mu.Lock()
	rec, ok := m.operations[id]
	if !ok {
		m.mu.Unlock()
		return Operation{}, ErrNotFound
	}
	if terminal(rec.operation.State) {
		copy := cloneOperation(rec.operation)
		m.mu.Unlock()
		return copy, nil
	}
	cancel := rec.cancel
	if err := m.transitionLocked(rec, Cancelled); err != nil {
		m.mu.Unlock()
		return Operation{}, err
	}
	copy := cloneOperation(rec.operation)
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.enqueue(copy)
	return copy, nil
}

func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		ids := make([]string, 0, len(m.operations))
		for id, rec := range m.operations {
			if !terminal(rec.operation.State) {
				ids = append(ids, id)
			}
		}
		m.mu.Unlock()
		m.rootCancel()
		for _, id := range ids {
			_, _ = m.Cancel(id)
		}
		m.runs.Wait()
		m.eventMu.Lock()
		stop, done := m.stopEvents, m.eventsDone
		m.eventMu.Unlock()
		if stop != nil {
			close(stop)
			<-done
		}
	})
}

func (m *Manager) run(ctx context.Context, id string, runner Runner) {
	defer m.runs.Done()
	err := runner.Run(ctx, reporter{manager: m, id: id})
	m.mu.Lock()
	rec, ok := m.operations[id]
	if !ok || terminal(rec.operation.State) {
		m.mu.Unlock()
		return
	}
	var transition State
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		transition = Cancelled
	} else if err != nil {
		transition = Failed
		rec.operation.Error = failure(err)
	} else if rec.operation.State == WaitingForUser {
		transition = Failed
		rec.operation.Error = &OperationError{Code: "invalid_runner_state", Message: "Операция завершилась в состоянии ожидания ответа пользователя."}
	} else {
		transition = Completed
	}
	if transition == Completed {
		rec.operation.Progress.CurrentFile = ""
	}
	if transitionErr := m.transitionLocked(rec, transition); transitionErr != nil {
		rec.operation.Error = &OperationError{Code: "invalid_transition", Message: transitionErr.Error()}
		_ = m.transitionLocked(rec, Failed)
	}
	copy := cloneOperation(rec.operation)
	m.mu.Unlock()
	m.enqueue(copy)
}

type reporter struct {
	manager *Manager
	id      string
}

func (r reporter) Report(progress Progress) error {
	if progress.TotalFiles != nil && progress.ProcessedFiles > *progress.TotalFiles {
		return errors.New("processed files exceed total files")
	}
	if progress.TotalBytes != nil && progress.ProcessedBytes > *progress.TotalBytes {
		return errors.New("processed bytes exceed total bytes")
	}
	r.manager.mu.Lock()
	rec, ok := r.manager.operations[r.id]
	if !ok {
		r.manager.mu.Unlock()
		return ErrNotFound
	}
	if rec.operation.State != Running && rec.operation.State != WaitingForUser {
		state := rec.operation.State
		r.manager.mu.Unlock()
		return fmt.Errorf("%w: cannot report progress while %s", ErrInvalidTransition, state)
	}
	rec.operation.Progress = cloneProgress(progress)
	rec.operation.UpdateVersion++
	copy := cloneOperation(rec.operation)
	r.manager.mu.Unlock()
	r.manager.enqueue(copy)
	return nil
}

func (r reporter) WaitingForUser() error { return r.transition(WaitingForUser) }
func (r reporter) Resume() error         { return r.transition(Running) }

func (r reporter) transition(next State) error {
	r.manager.mu.Lock()
	rec, ok := r.manager.operations[r.id]
	if !ok {
		r.manager.mu.Unlock()
		return ErrNotFound
	}
	if err := r.manager.transitionLocked(rec, next); err != nil {
		r.manager.mu.Unlock()
		return err
	}
	copy := cloneOperation(rec.operation)
	r.manager.mu.Unlock()
	r.manager.enqueue(copy)
	return nil
}

func (m *Manager) transitionLocked(rec *record, next State) error {
	current := rec.operation.State
	allowed := current == Queued && (next == Running || next == Cancelled) ||
		current == Running && (next == WaitingForUser || next == Completed || next == Failed || next == Cancelled) ||
		current == WaitingForUser && (next == Running || next == Failed || next == Cancelled)
	if !allowed {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, current, next)
	}
	rec.operation.State = next
	rec.operation.UpdateVersion++
	if terminal(next) {
		now := time.Now().UTC()
		rec.operation.FinishedAt = &now
	}
	return nil
}

func terminal(state State) bool { return state == Completed || state == Failed || state == Cancelled }

func failure(err error) *OperationError {
	var runError *RunError
	if errors.As(err, &runError) {
		code := runError.Code
		if code == "" {
			code = "operation_failed"
		}
		return &OperationError{Code: code, Message: runError.Error()}
	}
	return &OperationError{Code: "operation_failed", Message: err.Error()}
}

func (m *Manager) enqueue(operation Operation) {
	m.eventMu.Lock()
	if m.publisher == nil {
		m.eventMu.Unlock()
		return
	}
	event := Event{OperationID: operation.ID, State: operation.State,
		Progress: cloneProgress(operation.Progress), UpdateVersion: operation.UpdateVersion, Operation: cloneOperation(operation)}
	if len(m.eventQueue) < 128 {
		m.eventQueue = append(m.eventQueue, event)
	} else {
		m.overflow[operation.ID] = event
	}
	notify := m.notify
	m.eventMu.Unlock()
	select {
	case notify <- struct{}{}:
	default:
	}
}

func (m *Manager) publishLoop() {
	defer close(m.eventsDone)
	for {
		select {
		case <-m.notify:
			m.flushEvents()
		case <-m.stopEvents:
			m.flushEvents()
			return
		}
	}
}

func (m *Manager) flushEvents() {
	for {
		m.eventMu.Lock()
		if len(m.eventQueue) == 0 && len(m.overflow) > 0 {
			ids := make([]string, 0, len(m.overflow))
			for id := range m.overflow {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				m.eventQueue = append(m.eventQueue, m.overflow[id])
			}
			m.overflow = make(map[string]Event)
		}
		if len(m.eventQueue) == 0 {
			m.eventMu.Unlock()
			return
		}
		event := m.eventQueue[0]
		m.eventQueue = m.eventQueue[1:]
		publisher := m.publisher
		m.eventMu.Unlock()
		publisher(event)
	}
}

func newID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func cloneContext(value ContextSnapshot) ContextSnapshot {
	value.Sources = append([]string(nil), value.Sources...)
	if value.DestinationDirectory != nil {
		copy := *value.DestinationDirectory
		value.DestinationDirectory = &copy
	}
	return value
}

func cloneProgress(value Progress) Progress {
	if value.TotalFiles != nil {
		copy := *value.TotalFiles
		value.TotalFiles = &copy
	}
	if value.TotalBytes != nil {
		copy := *value.TotalBytes
		value.TotalBytes = &copy
	}
	return value
}

func cloneOperation(value Operation) Operation {
	value.Context = cloneContext(value.Context)
	value.Progress = cloneProgress(value.Progress)
	if value.FinishedAt != nil {
		copy := *value.FinishedAt
		value.FinishedAt = &copy
	}
	if value.Error != nil {
		copy := *value.Error
		value.Error = &copy
	}
	return value
}
