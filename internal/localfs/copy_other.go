//go:build !linux

package localfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/rikkimongoose/marten-commander/internal/core"
)

func (LocalFileSystem) OpenCopyFile(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("safe no-follow source opening is not supported on this platform")
}

func (LocalFileSystem) ReadCopyDirectory(context.Context, string) ([]core.FileEntry, error) {
	return nil, errors.New("safe no-follow directory reading is not supported on this platform")
}

func fingerprintTokens(info fs.FileInfo) (string, string, bool) {
	if info.Sys() == nil {
		return "", "", false
	}
	identity := fmt.Sprintf("%T:%#v:%d:%d:%d", info.Sys(), info.Sys(), uint32(info.Mode()), info.Size(), info.ModTime().UnixNano())
	return identity, identity, true
}

func (LocalFileSystem) PublishNew(context.Context, string, string) error {
	return errors.New("safe no-replace publication is not supported on this platform")
}

func (LocalFileSystem) PublishReplace(context.Context, string, string, core.FileFingerprint) error {
	return errors.New("safe conditional replacement is not supported on this platform")
}
