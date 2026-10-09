package session

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/terminalgrid"
)

func newGridDispatcher(t *testing.T, manager *Manager) *ipc.Dispatcher {
	t.Helper()
	dispatcher := ipc.NewDispatcher()
	if err := manager.RegisterGridCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func dispatchGrid(dispatcher *ipc.Dispatcher, command, args string) ipc.Response {
	return dispatcher.Dispatch(context.Background(), ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
}

func dispatchGridAsync(dispatcher *ipc.Dispatcher, command, args string) <-chan ipc.Response {
	result := make(chan ipc.Response, 1)
	go func() { result <- dispatchGrid(dispatcher, command, args) }()
	return result
}

func requireGridResponse(t *testing.T, response ipc.Response) {
	t.Helper()
	if !response.OK {
		t.Fatalf("dispatch failed: %+v", response.Error)
	}
	if string(response.Data) != "null" {
		t.Fatalf("dispatch data = %s, want null", response.Data)
	}
}

func TestGridCommandsResizeFlushAndErrorClassification(t *testing.T) {
	events := make(chan ControlEvent, 16)
	manager, connector, _, session := openGridTestSession(t, events)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	dispatcher := newGridDispatcher(t, manager)
	commands := dispatcher.Commands()
	if len(commands) != 2 || commands[0] != CommandTerminalResize || commands[1] != CommandTerminalResizeFlush {
		t.Fatalf("registered commands = %v", commands)
	}
	requireGridResponse(t, dispatchGrid(dispatcher, CommandTerminalResize, `{"tabId":"`+tab.ID+`","clientId":"client-a","cols":100,"rows":30}`))
	if calls, cols, rows := fakeChannelGrid(channel); calls != 1 || cols != 100 || rows != 30 {
		t.Fatalf("dispatch resize = calls %d, %dx%d", calls, cols, rows)
	}
	revision := tab.Info().GridRevision
	requireGridResponse(t, dispatchGrid(dispatcher, CommandTerminalResizeFlush, `{"tabId":"`+tab.ID+`","clientId":"client-a"}`))
	if calls, cols, rows := fakeChannelGrid(channel); calls != 2 || cols != 100 || rows != 30 {
		t.Fatalf("dispatch flush = calls %d, %dx%d", calls, cols, rows)
	}
	if got := tab.Info().GridRevision; got != revision {
		t.Fatalf("unchanged flush revision = %d, want %d", got, revision)
	}
	for _, test := range []struct {
		name    string
		command string
		args    string
		code    ipc.Code
	}{
		{name: "observer flush", command: CommandTerminalResizeFlush, args: `{"tabId":"` + tab.ID + `","clientId":"client-b"}`, code: ipc.CodeNotController},
		{name: "invalid resize", command: CommandTerminalResize, args: `{"tabId":"` + tab.ID + `","clientId":"client-a","cols":0,"rows":30}`, code: ipc.CodeBadParam},
		{name: "missing tab", command: CommandTerminalResizeFlush, args: `{"tabId":"missing","clientId":"client-a"}`, code: ipc.CodeNotFound},
		{name: "malformed payload", command: CommandTerminalResizeFlush, args: `{`, code: ipc.CodeBadParam},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := dispatchGrid(dispatcher, test.command, test.args)
			if response.OK || response.Error == nil || response.Error.Code != test.code {
				t.Fatalf("response = %+v, want code %s", response, test.code)
			}
		})
	}
}

func TestGridCommandsConcurrentResizeLatestWins(t *testing.T) {
	events := make(chan ControlEvent, 32)
	manager, connector, _, session := openGridTestSession(t, events)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	dispatcher := newGridDispatcher(t, manager)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	channel.resizeHook = func(ctx context.Context, cols, _ uint32) error {
		if cols == 90 {
			once.Do(func() { close(started) })
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	first := dispatchGridAsync(dispatcher, CommandTerminalResize, `{"tabId":"`+tab.ID+`","clientId":"client-a","cols":90,"rows":30}`)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first dispatched resize did not start")
	}
	second := dispatchGridAsync(dispatcher, CommandTerminalResize, `{"tabId":"`+tab.ID+`","clientId":"client-a","cols":100,"rows":40}`)
	waitFor(t, func() bool {
		snapshot, err := manager.GridSnapshot(tab.ID)
		return err == nil && snapshot.Pending && snapshot.DesiredGrid == (terminalgrid.Grid{Cols: 100, Rows: 40})
	})
	third := dispatchGridAsync(dispatcher, CommandTerminalResize, `{"tabId":"`+tab.ID+`","clientId":"client-a","cols":110,"rows":50}`)
	waitFor(t, func() bool {
		snapshot, err := manager.GridSnapshot(tab.ID)
		return err == nil && snapshot.DesiredGrid == (terminalgrid.Grid{Cols: 110, Rows: 50})
	})
	close(release)
	for _, result := range []<-chan ipc.Response{first, second, third} {
		select {
		case response := <-result:
			requireGridResponse(t, response)
		case <-time.After(time.Second):
			t.Fatal("dispatched resize did not complete")
		}
	}
	if calls, cols, rows := fakeChannelGrid(channel); calls != 2 || cols != 110 || rows != 50 {
		t.Fatalf("concurrent dispatch grid = calls %d, %dx%d", calls, cols, rows)
	}
}

func TestGridCommandsFlushReconnectAndDisconnect(t *testing.T) {
	events := make(chan ControlEvent, 32)
	manager, connector, _, session := openGridTestSession(t, events)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	dispatcher := newGridDispatcher(t, manager)
	requireGridResponse(t, dispatchGrid(dispatcher, CommandTerminalResize, `{"tabId":"`+tab.ID+`","clientId":"client-a","cols":90,"rows":30}`))
	if err := manager.Reconnect(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	newChannel := connector.transport(1).channel(0)
	revision := tab.Info().GridRevision
	requireGridResponse(t, dispatchGrid(dispatcher, CommandTerminalResizeFlush, `{"tabId":"`+tab.ID+`","clientId":"client-a"}`))
	if calls, cols, rows := fakeChannelGrid(newChannel); calls < 1 || cols != 90 || rows != 30 {
		t.Fatalf("reconnect flush = calls %d, %dx%d", calls, cols, rows)
	}
	snapshot, err := manager.GridSnapshot(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Attached || snapshot.CommittedGrid != (terminalgrid.Grid{Cols: 90, Rows: 30}) {
		t.Fatalf("reconnected flush snapshot = %+v", snapshot)
	}
	if got := tab.Info().GridRevision; got != revision {
		t.Fatalf("reconnect flush revision = %d, want %d", got, revision)
	}
	if err := manager.Disconnect(session.ID); err != nil {
		t.Fatal(err)
	}
	response := dispatchGrid(dispatcher, CommandTerminalResizeFlush, `{"tabId":"`+tab.ID+`","clientId":"client-a"}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeDisconnected {
		t.Fatalf("disconnected flush response = %+v", response)
	}
}
