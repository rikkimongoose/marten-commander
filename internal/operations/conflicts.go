package operations

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type ConflictType string

const (
	FileFile           ConflictType = "file/file"
	DirectoryDirectory ConflictType = "directory/directory"
	FileDirectory      ConflictType = "file/directory"
	DirectoryFile      ConflictType = "directory/file"
)

type EntryKind string

const (
	File      EntryKind = "file"
	Directory EntryKind = "directory"
)

type ConflictAction string

const (
	Replace         ConflictAction = "replace"
	Skip            ConflictAction = "skip"
	KeepBoth        ConflictAction = "keep-both"
	Merge           ConflictAction = "merge"
	CancelOperation ConflictAction = "cancel-operation"
)

type ConflictMetadata struct {
	Path       string     `json:"path"`
	Kind       EntryKind  `json:"kind"`
	Size       *uint64    `json:"size"`
	ModifiedAt *time.Time `json:"modifiedAt"`
}

type ConflictRequest struct {
	OperationID    string           `json:"operationId"`
	ConflictID     string           `json:"conflictId"`
	Type           ConflictType     `json:"type"`
	Source         ConflictMetadata `json:"source"`
	Destination    ConflictMetadata `json:"destination"`
	AllowedActions []ConflictAction `json:"allowedActions"`
	Revision       uint64           `json:"revision"`
}

type ConflictResolution struct {
	OperationID      string         `json:"operationId"`
	ConflictID       string         `json:"conflictId"`
	Revision         uint64         `json:"revision"`
	Action           ConflictAction `json:"action"`
	ApplyToAll       bool           `json:"applyToAll"`
	CustomTargetPath string         `json:"customTargetPath"`
}

// ConflictDecision is an instruction to the future file runner, not permission
// to write blindly. The runner must revalidate Destination immediately before
// an atomic write/rename; Issue #13 owns that filesystem contract.
type ConflictDecision struct {
	Resolution                      ConflictResolution `json:"resolution"`
	RequiresDestinationRevalidation bool               `json:"requiresDestinationRevalidation"`
}

type ConflictError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *ConflictError) Error() string { return e.Message }

var ErrConflictCancelled = errors.New("operation cancelled while waiting for conflict resolution")

func AllowedConflictActions(conflictType ConflictType) []ConflictAction {
	switch conflictType {
	case FileFile:
		return []ConflictAction{Replace, Skip, KeepBoth, CancelOperation}
	case DirectoryDirectory:
		return []ConflictAction{Merge, Skip, CancelOperation}
	case FileDirectory, DirectoryFile:
		return []ConflictAction{Skip, CancelOperation}
	default:
		return nil
	}
}

func validateConflictRequest(request ConflictRequest) error {
	if strings.TrimSpace(request.Source.Path) == "" || strings.TrimSpace(request.Destination.Path) == "" {
		return &ConflictError{"invalid_conflict", "Не указаны источник или назначение конфликта."}
	}
	wantSource, wantDestination := File, File
	switch request.Type {
	case FileFile:
	case DirectoryDirectory:
		wantSource, wantDestination = Directory, Directory
	case FileDirectory:
		wantDestination = Directory
	case DirectoryFile:
		wantSource = Directory
	default:
		return &ConflictError{"invalid_conflict", "Неизвестный тип файлового конфликта."}
	}
	if request.Source.Kind != wantSource || request.Destination.Kind != wantDestination {
		return &ConflictError{"invalid_conflict", "Типы источника и назначения не соответствуют типу конфликта."}
	}
	return nil
}

func actionAllowed(actions []ConflictAction, action ConflictAction) bool {
	for _, allowed := range actions {
		if allowed == action {
			return true
		}
	}
	return false
}

// NextKeepBothPath generates report (1).pdf, report (2).pdf, ... while
// preserving Unicode and the final extension. The caller supplies the latest
// existence check and must still revalidate/claim the returned path atomically.
func NextKeepBothPath(destination string, exists func(string) (bool, error)) (string, error) {
	if strings.TrimSpace(destination) == "" || exists == nil {
		return "", errors.New("destination and existence check are required")
	}
	directory, name := filepath.Split(destination)
	extension := filepath.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	for index := 1; ; index++ {
		candidate := filepath.Join(directory, fmt.Sprintf("%s (%d)%s", stem, index, extension))
		occupied, err := exists(candidate)
		if err != nil {
			return "", err
		}
		if !occupied {
			return candidate, nil
		}
	}
}

func cloneConflictMetadata(value ConflictMetadata) ConflictMetadata {
	if value.Size != nil {
		copy := *value.Size
		value.Size = &copy
	}
	if value.ModifiedAt != nil {
		copy := *value.ModifiedAt
		value.ModifiedAt = &copy
	}
	return value
}

func cloneConflictRequest(value ConflictRequest) ConflictRequest {
	value.Source = cloneConflictMetadata(value.Source)
	value.Destination = cloneConflictMetadata(value.Destination)
	value.AllowedActions = append([]ConflictAction(nil), value.AllowedActions...)
	return value
}

func conflictDecision(resolution ConflictResolution) ConflictDecision {
	return ConflictDecision{Resolution: resolution,
		RequiresDestinationRevalidation: resolution.Action == Replace || resolution.Action == KeepBoth || resolution.Action == Merge}
}

func waitForConflict(ctx context.Context, manager *Manager, operationID string, response <-chan ConflictResolution) (ConflictDecision, error) {
	// Prefer an explicit Cancel operation response that was queued immediately
	// before its context cancellation signal.
	select {
	case resolution := <-response:
		if resolution.Action == CancelOperation {
			return conflictDecision(resolution), ErrConflictCancelled
		}
		return conflictDecision(resolution), nil
	default:
	}
	select {
	case resolution := <-response:
		if resolution.Action == CancelOperation {
			return conflictDecision(resolution), ErrConflictCancelled
		}
		return conflictDecision(resolution), nil
	case <-ctx.Done():
		_, _ = manager.Cancel(operationID)
		return ConflictDecision{}, ctx.Err()
	}
}
