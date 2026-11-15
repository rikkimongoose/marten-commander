//go:build !linux

package localfs

import (
	"context"
	"errors"
	"github.com/rikkimongoose/marten-commander/internal/core"
)

func (LocalFileSystem) RenameNew(context.Context, string, string, core.FileFingerprint) error {
	return errors.ErrUnsupported
}
func (LocalFileSystem) RenameReplace(context.Context, string, string, core.FileFingerprint, core.FileFingerprint) error {
	return errors.ErrUnsupported
}
func (LocalFileSystem) RemoveSource(context.Context, string, core.FileFingerprint) error {
	return errors.ErrUnsupported
}
