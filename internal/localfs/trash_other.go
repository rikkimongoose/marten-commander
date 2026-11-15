//go:build !linux

package localfs

import (
	"context"
	"errors"
	"github.com/rikkimongoose/marten-commander/internal/core"
)

type SystemTrash struct{}

func (SystemTrash) InspectTrash(context.Context, string) (core.TrashSource, error) {
	return core.TrashSource{}, errors.New("системная корзина пока поддерживается только в Linux")
}
func (SystemTrash) Trash(context.Context, core.TrashSource) error {
	return errors.New("системная корзина пока поддерживается только в Linux")
}
