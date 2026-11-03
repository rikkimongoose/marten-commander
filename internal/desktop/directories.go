// Package desktop adapts application services to the Wails transport.
package desktop

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rikkimongoose/marten-commander/internal/app"
	"github.com/rikkimongoose/marten-commander/internal/core"
)

// DirectoryRequest accepts either a typed native path or an opaque location.
// An empty request opens the initial directory. Location is an address, not an
// access-control token; this desktop application can browse the user's files.
type DirectoryRequest struct {
	Path     string `json:"path"`
	Location string `json:"location"`
}

type Entry struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Extension  string `json:"extension"`
	Kind       string `json:"kind"`
	Size       string `json:"size"` // Decimal int64, avoiding JavaScript precision loss.
	ModifiedAt string `json:"modifiedAt"`
	Hidden     bool   `json:"hidden"`
	Navigable  bool   `json:"navigable"`
	Detail     string `json:"detail"`
}

type Directory struct {
	Location       string  `json:"location"`
	ParentLocation string  `json:"parentLocation"`
	DisplayPath    string  `json:"displayPath"`
	Entries        []Entry `json:"entries"`
}

type DirectoryService struct {
	directories *app.DirectoryService
}

func NewDirectoryService(directories *app.DirectoryService) *DirectoryService {
	return &DirectoryService{directories: directories}
}

// Wails injects the call context; cancelling its generated promise cancels reads.
func (s *DirectoryService) ReadDirectory(ctx context.Context, request DirectoryRequest) (Directory, error) {
	path := request.Path
	if request.Location != "" {
		if path != "" {
			return Directory{}, fmt.Errorf("укажите путь или идентификатор каталога, но не оба")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(request.Location)
		if err != nil || len(decoded) == 0 {
			return Directory{}, fmt.Errorf("некорректный идентификатор каталога")
		}
		path = string(decoded)
	}
	directory, err := s.directories.ReadDirectory(ctx, path)
	if err != nil {
		return Directory{}, fmt.Errorf("Не удалось прочитать каталог %s: %s", display(path), display(err.Error()))
	}
	result := Directory{
		Location: encode(directory.Path), ParentLocation: encode(directory.Parent),
		DisplayPath: display(directory.Path), Entries: make([]Entry, 0, len(directory.Entries)),
	}
	for _, entry := range directory.Entries {
		item := Entry{
			ID: encode(entry.Path), Name: display(entry.Name), Kind: string(entry.Kind),
			ModifiedAt: entry.ModifiedAt.UTC().Format("2006-01-02T15:04:05.000000000Z"),
			Hidden:     entry.Hidden, Navigable: entry.Navigable,
		}
		if entry.Kind == core.KindFile {
			item.Size = strconv.FormatInt(entry.Size, 10)
		}
		if dot := strings.LastIndexByte(entry.Name, '.'); dot > 0 && !entry.Navigable {
			item.Extension = display(entry.Name[dot+1:])
		}
		if entry.TargetError != nil {
			item.Detail = display(entry.TargetError.Error())
		}
		result.Entries = append(result.Entries, item)
	}
	return result, nil
}

func encode(path string) string { return base64.RawURLEncoding.EncodeToString([]byte(path)) }

// Escape ambiguous bytes and control characters for display only. Navigation
// always uses the ID, so JSON cannot corrupt a non-UTF-8 filesystem address.
func display(raw string) string {
	var out strings.Builder
	for len(raw) > 0 {
		r, n := utf8.DecodeRuneInString(raw)
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&out, "\\x%02x", raw[0])
		case r == '\\':
			out.WriteString("\\\\")
		case !unicode.IsPrint(r):
			quoted := strconv.QuoteRune(r)
			out.WriteString(quoted[1 : len(quoted)-1])
		default:
			out.WriteRune(r)
		}
		raw = raw[n:]
	}
	return out.String()
}
