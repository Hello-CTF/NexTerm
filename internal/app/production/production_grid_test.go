//go:build darwin || linux

package production

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/terminalgrid"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
)

func TestProductionApplicationGridCommandsWithRealLocalTerminal(t *testing.T) {
	connector := session.ConnectorFunc(func(_ context.Context, _ session.Asset, _ uint64) (base.Transport, error) {
		return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
	})
	manager := session.NewManager(session.Config{Connector: connector})
	production, err := NewProductionWithServices(Config{}, ProductionServices{Sessions: manager})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	for _, command := range []string{session.CommandTerminalResize, session.CommandTerminalResizeFlush} {
		if !slices.Contains(production.Dispatcher.Commands(), command) {
			t.Fatalf("production dispatcher is missing %s: %v", command, production.Dispatcher.Commands())
		}
	}

	connected, err := manager.Connect(t.Context(), session.Asset{ID: "app-grid-local", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.OpenTab(t.Context(), session.OpenTabOptions{
		SessionID: connected.ID,
		ClientID:  "client-a",
		ChannelID: "app-grid-channel",
		Cols:      80,
		Rows:      24,
	})
	if err != nil {
		t.Fatal(err)
	}

	response := dispatchProduction(t, production, session.CommandTerminalResize,
		`{"tabId":"`+tab.ID+`","cols":100,"rows":30}`, "client-a")
	requireProductionNull(t, response)
	assertProductionGrid(t, manager, tab.ID, terminalgrid.Grid{Cols: 100, Rows: 30})

	response = dispatchProduction(t, production, session.CommandTerminalResize,
		`{"tabId":"`+tab.ID+`","clientId":"client-a","cols":110,"rows":40}`, "")
	requireProductionNull(t, response)
	assertProductionGrid(t, manager, tab.ID, terminalgrid.Grid{Cols: 110, Rows: 40})
	tabState, err := manager.Tab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	revision := tabState.Info().GridRevision
	response = dispatchProduction(t, production, session.CommandTerminalResizeFlush,
		`{"tabId":"`+tab.ID+`","clientId":"client-a"}`, "")
	requireProductionNull(t, response)
	if got := tabState.Info().GridRevision; got != revision {
		t.Fatalf("unchanged flush revision = %d, want %d", got, revision)
	}
	assertProductionGrid(t, manager, tab.ID, terminalgrid.Grid{Cols: 110, Rows: 40})

	for _, test := range []struct {
		name    string
		command string
		args    string
		code    ipc.Code
	}{
		{name: "observer", command: session.CommandTerminalResizeFlush, args: `{"tabId":"` + tab.ID + `","clientId":"client-b"}`, code: ipc.CodeNotController},
		{name: "invalid size", command: session.CommandTerminalResize, args: `{"tabId":"` + tab.ID + `","clientId":"client-a","cols":0,"rows":30}`, code: ipc.CodeBadParam},
		{name: "missing tab", command: session.CommandTerminalResizeFlush, args: `{"tabId":"missing","clientId":"client-a"}`, code: ipc.CodeNotFound},
		{name: "malformed", command: session.CommandTerminalResizeFlush, args: `{`, code: ipc.CodeBadParam},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := dispatchProduction(t, production, test.command, test.args, "")
			if response.OK || response.Error == nil || response.Error.Code != test.code {
				t.Fatalf("response = %+v, want code %s", response, test.code)
			}
		})
	}
}

func dispatchProduction(t *testing.T, production *Production, command, args, environmentClient string) ipc.Response {
	t.Helper()
	return production.Dispatcher.Dispatch(t.Context(), ipc.Request{
		Command: command,
		Args:    json.RawMessage(args),
	}, production.Environment(environmentClient))
}

func assertProductionGrid(t *testing.T, manager *session.Manager, tabID string, want terminalgrid.Grid) {
	t.Helper()
	snapshot, err := manager.GridSnapshot(tabID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CommittedGrid != want {
		t.Fatalf("committed grid = %+v, want %+v", snapshot.CommittedGrid, want)
	}
}
