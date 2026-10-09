package mount

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestIPCCommandsUseNestedCreateAndSafeCredentials(t *testing.T) {
	runner := &fakeRunner{lookups: map[string]string{"net": "net"}}
	service := newTestService("windows", runner, nil)
	dispatcher := ipc.NewDispatcher()
	if err := service.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	dispatch := func(command, args string) ipc.Response {
		return dispatcher.Dispatch(context.Background(), ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
	}
	if response := dispatch("mount_capability", `{}`); !response.OK || string(response.Data) != "null" {
		t.Fatalf("capability = %+v", response)
	}
	response := dispatch("mount_create", `{"args":{"sessionId":"s","remotePath":"\\\\server\\share","localPoint":"Z:","username":"alice","password":"ipc-secret"}}`)
	if !response.OK {
		t.Fatalf("create = %+v", response)
	}
	var entry Entry
	if err := json.Unmarshal(response.Data, &entry); err != nil || entry.LocalPoint != "Z:" {
		t.Fatalf("entry = %+v, %v", entry, err)
	}
	commands := requireCommandCount(t, runner, 1)
	if strings.Contains(strings.Join(commands[0].args, " "), "ipc-secret") || string(commands[0].stdin) != "ipc-secret\r\n" {
		t.Fatalf("command = %+v", commands[0])
	}
	if response := dispatch("mount_list", `{"forceRefresh":true}`); !response.OK || string(response.Data) != "[]" {
		t.Fatalf("list = %+v", response)
	}
	if response := dispatch("mount_remove", `{"localPoint":"Z:","sessionId":"s"}`); !response.OK || string(response.Data) != "null" {
		t.Fatalf("remove = %+v", response)
	}
}
