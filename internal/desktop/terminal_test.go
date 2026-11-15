package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type terminalServiceStub struct {
	command, directory string
	err                error
	calls              int
}

func (s *terminalServiceStub) Run(_ context.Context, command, directory string) error {
	s.command = command
	s.directory = directory
	s.calls++
	return s.err
}
func TestTerminalCommandTransport(t *testing.T) {
	stub := &terminalServiceStub{}
	service := NewCommandServiceWithOperations(nil, nil, nil, nil, stub)
	for _, side := range []string{"left", "right"} {
		request := commandFixture(t)
		request.Type = "terminal.run"
		request.Context.ActivePanel = side
		request.Params = json.RawMessage(`{"command":"git status | cat"}`)
		source, _ := request.Context.sourceAndTarget()
		want, _ := decodeLocation(*source.DirectoryLocation)
		if err := service.ExecuteCommand(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		*source.DirectoryLocation = encode("/changed")
		if stub.directory != want || stub.command != "git status | cat" {
			t.Fatal(stub)
		}
	}
	request := commandFixture(t)
	request.Type = "terminal.run"
	for _, raw := range []string{`{}`, `{"command":null}`, `{"command":5}`, `{"command":"pwd","cwd":"/tmp"}`, `{"command":"\u0000"}`} {
		request.Params = json.RawMessage(raw)
		if err := service.ExecuteCommand(context.Background(), request); err == nil {
			t.Fatal(raw)
		}
	}
	request.Params = json.RawMessage(`{"command":" "}`)
	if err := service.ExecuteCommand(context.Background(), request); err != nil || stub.calls != 2 {
		t.Fatalf("%v %+v", err, stub)
	}
	request.Params = json.RawMessage(`{"command":"pwd"}`)
	remote := "sftp://host/path"
	request.Context.Left.DirectoryLocation = &remote
	if err := service.ExecuteCommand(context.Background(), request); err == nil || stub.calls != 2 {
		t.Fatal("remote launch")
	}
	request = commandFixture(t)
	request.Type = "terminal.run"
	request.Params = json.RawMessage(`{"command":"pwd"}`)
	stub.err = errors.New("terminal missing")
	var commandError *CommandError
	if err := service.ExecuteCommand(context.Background(), request); !errors.As(err, &commandError) || commandError.Code != "terminal_failed" {
		t.Fatal(err)
	}
}
