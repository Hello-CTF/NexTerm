package mount

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestApplicationModuleContract(t *testing.T) {
	runner := &fakeRunner{
		lookups: map[string]string{"mount": "mount"},
		runs:    []fakeRun{{result: commandResult{stdout: "host:/srv on /mnt/remote type fuse.sshfs (rw)\n"}}},
	}
	service := newTestService("linux", runner, nil)
	application, err := app.New(app.Config{Modules: []app.Module{{
		Name:             "mount",
		RegisterCommands: service.RegisterCommands,
		Component:        service,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	response := application.Dispatcher.Dispatch(t.Context(), ipc.Request{
		Command: "mount_list",
		Args:    json.RawMessage(`{"forceRefresh":true}`),
	}, ipc.Environment{})
	if !response.OK {
		t.Fatalf("mount_list through app.Module = %+v", response.Error)
	}
	var entries []Entry
	if err := json.Unmarshal(response.Data, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].LocalPoint != "/mnt/remote" {
		t.Fatalf("entries = %+v", entries)
	}
	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
