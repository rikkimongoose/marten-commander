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
