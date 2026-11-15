package core

import "context"

// TrashSource identifies the entry itself, never a symlink target. Revision is
// opaque and is captured before confirmation, then checked before each move.
type TrashSource struct {
	Path     string
	Name     string
	Kind     EntryKind
	Revision string
}

type TrashBackend interface {
	InspectTrash(context.Context, string) (TrashSource, error)
	Trash(context.Context, TrashSource) error
}
