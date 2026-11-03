package mount

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestMultiUserRejectsMountOperationsBeforeAnyCommand(t *testing.T) {
	ownerCtx := ipc.WithUserID(context.Background(), "account-a")
	cases := map[string]struct {
		ctx       context.Context
		multiUser bool
	}{
		"owner ctx":       {ctx: ownerCtx},
		"structural flag": {ctx: context.Background(), multiUser: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for _, goos := range []string{"linux", "windows"} {
				runner := &fakeRunner{lookups: map[string]string{"sshfs": "sshfs", "net": "net", "fusermount3": "fusermount3"}}
				service := newTestService(goos, runner, nil)
				service.multiUser = tc.multiUser
				if reason := service.CapabilityFor(tc.ctx); reason == nil || *reason != MultiUserUnavailableReason {
					t.Fatalf("%s capability = %v", goos, reason)
				}
				dispatcher := ipc.NewDispatcher()
				if err := service.RegisterCommands(dispatcher); err != nil {
					t.Fatal(err)
				}
				dispatch := func(command, args string) ipc.Response {
					return dispatcher.Dispatch(tc.ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
				}
				capability := dispatch("mount_capability", `{}`)
				if !capability.OK || string(capability.Data) != `"`+MultiUserUnavailableReason+`"` {
					t.Fatalf("%s mount_capability = %+v", goos, capability)
				}
				for command, args := range map[string]string{
					"mount_create": `{"args":{"sessionId":"s","remotePath":"host:/srv","localPoint":"/tmp/x"}}`,
					"mount_list":   `{"sessionId":"s"}`,
					"mount_remove": `{"localPoint":"/tmp/x","sessionId":"s"}`,
				} {
					response := dispatch(command, args)
					if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
						t.Fatalf("%s %s = %+v, want unsupported", goos, command, response)
					}
				}
				requireCommandCount(t, runner, 0)
				if len(runner.lookupsUsed()) != 0 {
					t.Fatalf("%s looked up executables: %v", goos, runner.lookupsUsed())
				}
			}
		})
	}
}

func TestDesktopCapabilityIgnoresMultiUserRestriction(t *testing.T) {
	service := newTestService("linux", &fakeRunner{}, nil)
	if reason := service.CapabilityFor(context.Background()); reason != nil {
		t.Fatalf("desktop capability = %v", *reason)
	}
}
