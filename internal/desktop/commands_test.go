package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
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
	seen string
	err  error
}

func (s *fileServiceStub) Open(_ context.Context, path string) error {
	s.seen = path
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
		files  fileOpenService
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
