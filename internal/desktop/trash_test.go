package desktop

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rikkimongoose/marten-commander/internal/core"
	"github.com/rikkimongoose/marten-commander/internal/operations"
)

type trashServiceStub struct {
	prepared []string
	started  []core.TrashSource
	snapshot operations.ContextSnapshot
}

func (s *trashServiceStub) Prepare(_ context.Context, paths []string) ([]core.TrashSource, error) {
	s.prepared = append([]string(nil), paths...)
	out := []core.TrashSource{}
	for _, path := range paths {
		out = append(out, core.TrashSource{Path: path, Name: path, Kind: core.KindFile, Revision: "rev"})
	}
	return out, nil
}
func (s *trashServiceStub) Start(_ context.Context, snapshot operations.ContextSnapshot, sources []core.TrashSource) (operations.Operation, error) {
	s.snapshot = snapshot
	s.started = append([]core.TrashSource(nil), sources...)
	return operations.Operation{}, nil
}
func TestTrashTransportRequiresConfirmedExactSnapshot(t *testing.T) {
	stub := &trashServiceStub{}
	service := NewCommandServiceWithTrash(nil, nil, nil, nil, nil, stub)
	request := commandFixture(t)
	request.Type = "file.trash"
	preview, err := service.PrepareTrash(context.Background(), request.Context)
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.started) != 0 || len(preview.Entries) != 1 {
		t.Fatal(stub)
	}
	for _, confirmed := range []bool{false, true} {
		request.Params, _ = json.Marshal(map[string]any{"confirmed": confirmed, "entries": preview.Entries})
		err := service.ExecuteCommand(context.Background(), request)
		if confirmed && err != nil || !confirmed && err == nil {
			t.Fatal(confirmed, err)
		}
	}
	if len(stub.started) != 1 || stub.started[0].Revision != "rev" || stub.snapshot.DestinationDirectory != nil {
		t.Fatal(stub)
	}
	path, _ := decodeLocation(request.Context.Left.SelectedLocations[0])
	if stub.started[0].Path != path {
		t.Fatal(stub.started)
	}
	preview.Entries[0].Location = encode("/somewhere/else")
	request.Params, _ = json.Marshal(map[string]any{"confirmed": true, "entries": preview.Entries})
	if err := service.ExecuteCommand(context.Background(), request); err == nil {
		t.Fatal("mismatched source accepted")
	}
	request.Context.Left.SelectedLocations = []string{}
	preview, err = service.PrepareTrash(context.Background(), request.Context)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Entries) != 1 || preview.Entries[0].Location != *request.Context.Left.FocusedLocation {
		t.Fatal("cursor fallback", preview)
	}
	request.Context.Left.FocusedLocation = nil
	if _, err = service.PrepareTrash(context.Background(), request.Context); err == nil {
		t.Fatal("empty accepted")
	}
	for _, location := range []string{"..", encode("/"), encode(path + "/.."), "sftp://host/file"} {
		request.Context.Left.SelectedLocations = []string{location}
		if _, err = service.PrepareTrash(context.Background(), request.Context); err == nil {
			t.Fatal("protected accepted", location)
		}
	}
}
