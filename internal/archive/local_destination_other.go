//go:build !linux

package archive

import (
	"context"
	"fmt"
)

type LocalExtractionDestination struct{}

func NewLocalExtractionDestination(context.Context, string) (*LocalExtractionDestination, error) {
	return nil, fmt.Errorf("%w: descriptor-relative extraction is not supported on this platform", ErrUnsafeDestination)
}

func (*LocalExtractionDestination) Close() error { return nil }
func (*LocalExtractionDestination) EnsureDirectory(context.Context, string) error {
	return ErrUnsafeDestination
}
func (*LocalExtractionDestination) Inspect(context.Context, string) (DestinationEntry, error) {
	return DestinationEntry{}, ErrUnsafeDestination
}
func (*LocalExtractionDestination) CreateTemporary(context.Context, string) (ExtractionFile, error) {
	return nil, ErrUnsafeDestination
}
