package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rikkimongoose/marten-commander/internal/operations"
)

func commandFixture(t *testing.T) CommandRequest {
	t.Helper()
	dir := t.TempDir()
	left, right, selected, focused := encode(filepath.Join(dir, "left")), encode(filepath.Join(dir, "right")), encode(filepath.Join(dir, "left", "selected")), encode(filepath.Join(dir, "left", "focused"))
	return CommandRequest{
		ID: "call-1", Type: "unsupported.type", Params: json.RawMessage(`{}`),
		Context: CommandContext{ActivePanel: "left",
			Left:  PanelContext{&left, []string{selected}, &focused},
			Right: PanelContext{&right, []string{}, nil}},
	}
}

type fileServiceStub struct {
	seen          string
	createdParent string
	createdName   string
	err           error
}

type copyServiceStub struct {
	snapshot    operations.ContextSnapshot
	sources     []string
	destination string
	err         error
}

func (s *copyServiceStub) Start(_ context.Context, snapshot operations.ContextSnapshot, sources []string, destination string) (operations.Operation, error) {
	s.snapshot, s.sources, s.destination = snapshot, append([]string(nil), sources...), destination
	return operations.Operation{ID: "copy-operation", State: operations.Running}, s.err
}

func (s *fileServiceStub) Open(_ context.Context, path string) error {
	s.seen = path
	return s.err
}

func (s *fileServiceStub) CreateDirectory(_ context.Context, parent, name string) error {
	s.createdParent, s.createdName = parent, name
	return s.err
}

func TestCommandSourceAndTarget(t *testing.T) {
	request := commandFixture(t)
	for _, active := range []string{"left", "right"} {
		request.Context.ActivePanel = active
		source, target := request.Context.sourceAndTarget()
		wantSource, wantTarget := request.Context.Left, request.Context.Right
		if active == "right" {
			wantSource, wantTarget = wantTarget, wantSource
		}
		if !reflect.DeepEqual(source, wantSource) || !reflect.DeepEqual(target, wantTarget) {
			t.Fatalf("wrong source/target for %s", active)
		}
	}
}

func TestCommandEffectiveSelection(t *testing.T) {
	request := commandFixture(t)
	panel := request.Context.Left
	if got := panel.effectiveSelection(); !reflect.DeepEqual(got, panel.SelectedLocations) {
		t.Fatalf("explicit selection must win over cursor: %v", got)
	}
	panel.SelectedLocations = []string{}
	if got := panel.effectiveSelection(); !reflect.DeepEqual(got, []string{*panel.FocusedLocation}) {
		t.Fatalf("empty selection must fall back to cursor: %v", got)
	}
	panel.FocusedLocation = nil
	if got := panel.effectiveSelection(); got == nil || len(got) != 0 {
		t.Fatalf("missing selection and cursor must produce an empty array: %#v", got)
	}
}

func TestFileOpenCommandPassesExactDecodedLocation(t *testing.T) {
	request := commandFixture(t)
	path := filepath.Join(t.TempDir(), "отчёт с пробелами.txt")
	request.Type = "file.open"
	request.Params = json.RawMessage(`{"location":"` + encode(path) + `"}`)
	files := &fileServiceStub{}
	if err := NewCommandService(files).ExecuteCommand(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if files.seen != path {
		t.Fatalf("opened %q, want %q", files.seen, path)
	}
}

func TestFileOpenCommandRejectsBadParametersAndReportsServiceErrors(t *testing.T) {
	request := commandFixture(t)
	request.Type = "file.open"
	path := filepath.Join(t.TempDir(), "file.txt")
	failure := errors.New("handler unavailable")
	tests := []struct {
		name   string
		params string
		files  fileCommandService
		code   string
	}{
		{"missing location", `{}`, &fileServiceStub{}, "invalid_params"},
		{"unknown parameter", `{"location":"` + encode(path) + `","other":true}`, &fileServiceStub{}, "invalid_params"},
		{"invalid location", `{"location":"relative"}`, &fileServiceStub{}, "invalid_params"},
		{"unavailable service", `{"location":"` + encode(path) + `"}`, nil, "unavailable"},
		{"system handler error", `{"location":"` + encode(path) + `"}`, &fileServiceStub{err: failure}, "open_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request.Params = json.RawMessage(test.params)
			err := NewCommandService(test.files).ExecuteCommand(context.Background(), request)
			var commandErr *CommandError
			if !errors.As(err, &commandErr) || commandErr.Code != test.code || commandErr.Message == "" {
				t.Fatalf("wanted %s, got %#v", test.code, err)
			}
		})
	}
}

func TestMkdirCommandUsesSnapshotSourceDirectory(t *testing.T) {
	request := commandFixture(t)
	request.Type = "file.mkdir"
	request.Params = json.RawMessage(`{"name":"Каталог с пробелами 🦊"}`)
	files := &fileServiceStub{}
	if err := NewCommandService(files).ExecuteCommand(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	want, _ := decodeLocation(*request.Context.Left.DirectoryLocation)
	if files.createdParent != want || files.createdName != "Каталог с пробелами 🦊" {
		t.Fatalf("CreateDirectory(%q, %q), want (%q, exact name)", files.createdParent, files.createdName, want)
	}
	if files.seen != "" {
		t.Fatalf("mkdir unexpectedly opened %q", files.seen)
	}
}

func TestMkdirCommandValidatesAndClassifiesErrors(t *testing.T) {
	request := commandFixture(t)
	request.Type = "file.mkdir"
	tests := []struct {
		name   string
		params string
		err    error
		code   string
	}{
		{"empty", `{"name":""}`, nil, "invalid_params"},
		{"dot", `{"name":"."}`, nil, "invalid_params"},
		{"dotdot", `{"name":".."}`, nil, "invalid_params"},
		{"nested", `{"name":"nested/child"}`, nil, "invalid_params"},
		{"unknown parameter", `{"name":"child","other":true}`, nil, "invalid_params"},
		{"exists", `{"name":"child"}`, fs.ErrExist, "mkdir_failed"},
		{"parent removed", `{"name":"child"}`, fs.ErrNotExist, "mkdir_failed"},
		{"permission", `{"name":"child"}`, fs.ErrPermission, "mkdir_failed"},
		{"other", `{"name":"child"}`, errors.New("disk error"), "mkdir_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request.Params = json.RawMessage(test.params)
			err := NewCommandService(&fileServiceStub{err: test.err}).ExecuteCommand(context.Background(), request)
			var commandErr *CommandError
			if !errors.As(err, &commandErr) || commandErr.Code != test.code || commandErr.Message == "" {
				t.Fatalf("wanted %s, got %#v", test.code, err)
			}
		})
	}
}

func TestCopyCommandUsesEffectiveSelectionAndEditedDestination(t *testing.T) {
	request := commandFixture(t)
	request.Type = "file.copy"
	destination := filepath.Join(t.TempDir(), "назначение с пробелами")
	request.Params = json.RawMessage(`{"destination":` + string(mustJSON(t, destination)) + `}`)
	copies := &copyServiceStub{}
	if err := NewCommandService(&fileServiceStub{}, copies).ExecuteCommand(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	selected, _ := decodeLocation(request.Context.Left.SelectedLocations[0])
	if !reflect.DeepEqual(copies.sources, []string{selected}) || copies.destination != destination {
		t.Fatalf("copy start: sources=%q destination=%q", copies.sources, copies.destination)
	}
	if copies.snapshot.ActivePanel != "left" || len(copies.snapshot.Sources) != 1 ||
		copies.snapshot.DestinationDirectory == nil || *copies.snapshot.DestinationDirectory != encode(destination) {
		t.Fatalf("snapshot: %#v", copies.snapshot)
	}
	request.Context.Left.SelectedLocations = []string{}
	focused, _ := decodeLocation(*request.Context.Left.FocusedLocation)
	if err := NewCommandService(&fileServiceStub{}, copies).ExecuteCommand(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(copies.sources, []string{focused}) {
		t.Fatalf("cursor fallback: %q", copies.sources)
	}
}

func TestCopyCommandRejectsInvalidContextAndDestination(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*CommandRequest)
		code string
	}{
		{"relative destination", func(r *CommandRequest) { r.Params = json.RawMessage(`{"destination":"relative"}`) }, "invalid_params"},
		{"no source", func(r *CommandRequest) {
			r.Context.Left.SelectedLocations = []string{}
			r.Context.Left.FocusedLocation = nil
		}, "unavailable"},
		{"source outside panel", func(r *CommandRequest) {
			r.Context.Left.SelectedLocations = []string{encode(filepath.Join(t.TempDir(), "outside"))}
		}, "invalid_context"},
		{"backend unavailable", func(*CommandRequest) {}, "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := commandFixture(t)
			request.Type = "file.copy"
			request.Params = json.RawMessage(`{"destination":"/tmp"}`)
			test.edit(&request)
			service := NewCommandService(&fileServiceStub{})
			if test.name != "backend unavailable" {
				service = NewCommandService(&fileServiceStub{}, &copyServiceStub{})
			}
			err := service.ExecuteCommand(context.Background(), request)
			var commandErr *CommandError
			if !errors.As(err, &commandErr) || commandErr.Code != test.code {
				t.Fatalf("wanted %s, got %v", test.code, err)
			}
		})
	}
}

func TestMoveCommandUsesImmutableSelectionAndDestination(t *testing.T) {
	request := commandFixture(t)
	request.Type = "file.move"
	destination := t.TempDir()
	request.Params = json.RawMessage(`{"destination":` + string(mustJSON(t, destination)) + `}`)
	moves := &copyServiceStub{}
	service := NewCommandService(&fileServiceStub{}).withMoves(moves)
	if err := service.ExecuteCommand(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	selected, _ := decodeLocation(request.Context.Left.SelectedLocations[0])
	if !reflect.DeepEqual(moves.sources, []string{selected}) || moves.destination != destination {
		t.Fatalf("move start: %#v", moves)
	}
	if moves.snapshot.ActivePanel != "left" || moves.snapshot.SourceDirectory != *request.Context.Left.DirectoryLocation ||
		moves.snapshot.DestinationDirectory == nil || *moves.snapshot.DestinationDirectory != encode(filepath.Dir(destination)) {
		t.Fatalf("snapshot: %#v", moves.snapshot)
	}
	request.Context.Left.SelectedLocations = []string{}
	focused, _ := decodeLocation(*request.Context.Left.FocusedLocation)
	if err := service.ExecuteCommand(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(moves.sources, []string{focused}) {
		t.Fatalf("cursor fallback: %q", moves.sources)
	}
}

func TestMoveCommandRejectsParentAndInvalidMultipleTarget(t *testing.T) {
	request := commandFixture(t)
	request.Type = "file.move"
	request.Params = json.RawMessage(`{"destination":"relative"}`)
	err := NewCommandService(&fileServiceStub{}).withMoves(&copyServiceStub{}).ExecuteCommand(context.Background(), request)
	var commandErr *CommandError
	if !errors.As(err, &commandErr) || commandErr.Code != "invalid_params" {
		t.Fatalf("relative: %#v", err)
	}
	parent := filepath.Join(filepath.Dir(mustDecode(t, *request.Context.Left.DirectoryLocation)), "..")
	request.Context.Left.SelectedLocations = []string{encode(parent)}
	request.Params = json.RawMessage(`{"destination":"/tmp"}`)
	err = NewCommandService(&fileServiceStub{}).withMoves(&copyServiceStub{}).ExecuteCommand(context.Background(), request)
	if !errors.As(err, &commandErr) || commandErr.Code != "invalid_context" {
		t.Fatalf("parent: %#v", err)
	}
}

func mustDecode(t *testing.T, value string) string {
	t.Helper()
	decoded, ok := decodeLocation(value)
	if !ok {
		t.Fatal("decode")
	}
	return decoded
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCommandValidationAndUnsupportedTypes(t *testing.T) {
	tests := []struct {
		name string
		edit func(*CommandRequest)
		code string
	}{
		{"unsupported", func(*CommandRequest) {}, "unknown_command"},
		{"frontend-only", func(r *CommandRequest) { r.Type = "panel.selectAll" }, "unknown_command"},
		{"missing id", func(r *CommandRequest) { r.ID = " " }, "invalid_request"},
		{"missing type", func(r *CommandRequest) { r.Type = "" }, "invalid_request"},
		{"invalid active", func(r *CommandRequest) { r.Context.ActivePanel = "other" }, "invalid_context"},
		{"invalid address", func(r *CommandRequest) { *r.Context.Left.DirectoryLocation = "!invalid" }, "invalid_context"},
		{"relative address", func(r *CommandRequest) { *r.Context.Left.DirectoryLocation = encode("relative") }, "invalid_context"},
		{"empty address", func(r *CommandRequest) { *r.Context.Left.DirectoryLocation = "" }, "invalid_context"},
		{"nul in address", func(r *CommandRequest) {
			*r.Context.Left.DirectoryLocation = encode(filepath.Join(t.TempDir(), "bad\x00"))
		}, "invalid_context"},
		{"bad selected address", func(r *CommandRequest) { r.Context.Right.SelectedLocations = []string{encode("relative")} }, "invalid_context"},
		{"bad focused address", func(r *CommandRequest) { value := "bad"; r.Context.Right.FocusedLocation = &value }, "invalid_context"},
		{"missing selection", func(r *CommandRequest) { r.Context.Right.SelectedLocations = nil }, "invalid_context"},
		{"unloaded with selection", func(r *CommandRequest) { r.Context.Left.DirectoryLocation = nil }, "invalid_context"},
		{"empty unloaded panel", func(r *CommandRequest) { r.Context.Right.DirectoryLocation = nil }, "unknown_command"},
		{"missing params", func(r *CommandRequest) { r.Params = nil }, "invalid_params"},
		{"null params", func(r *CommandRequest) { r.Params = json.RawMessage(`null`) }, "invalid_params"},
		{"array params", func(r *CommandRequest) { r.Params = json.RawMessage(`[]`) }, "invalid_params"},
		{"scalar params", func(r *CommandRequest) { r.Params = json.RawMessage(`42`) }, "invalid_params"},
		{"malformed params", func(r *CommandRequest) { r.Params = json.RawMessage(`{`) }, "invalid_params"},
		{"unknown type with params", func(r *CommandRequest) { r.Params = json.RawMessage(`{"destination":"unused"}`) }, "unknown_command"},
	}
	service := &CommandService{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := commandFixture(t)
			tt.edit(&request)
			err := service.ExecuteCommand(context.Background(), request)
			var commandErr *CommandError
			if !errors.As(err, &commandErr) || commandErr.Code != tt.code || commandErr.Error() == "" {
				t.Fatalf("wanted %s, got %#v", tt.code, err)
			}
			// This is the error payload Wails includes in RuntimeError.cause.
			data, err := json.Marshal(err)
			if err != nil {
				t.Fatal(err)
			}
			var payload CommandError
			if err := json.Unmarshal(data, &payload); err != nil || payload.Code != tt.code || payload.Message == "" {
				t.Fatalf("error lost in JSON: %s (%v)", data, err)
			}
		})
	}
}

func TestCommandContextPreservesNativePathBytesAndEnvelope(t *testing.T) {
	request := commandFixture(t)
	raw := filepath.Join(t.TempDir(), "raw-\xff\n")
	location := encode(raw)
	request.Context.Left.SelectedLocations = []string{location}
	request.Context.Left.FocusedLocation = &location
	request.Params = json.RawMessage(`{"name":"confirmed","options":{"overwrite":false}}`)
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var restored CommandRequest
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request, restored) {
		t.Fatalf("lossy request round trip: %s", data)
	}
	if err := validateCommandContext(restored.Context); err != nil {
		t.Fatal(err)
	}
}

func TestCommandCancelledCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (&CommandService{}).ExecuteCommand(ctx, commandFixture(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost call cancellation: %v", err)
	}
}
