package production

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestLogTailSaveIsDesktopOnlyAndWritesSelectedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "merged.log")
	desktop := newLogTailIPCService(nil, true)
	dispatcher := ipc.NewDispatcher()
	if err := desktop.register(dispatcher); err != nil {
		t.Fatal(err)
	}
	content := "[host-a] first\n[host-b] second\n"
	response := dispatchExecBatchTest(dispatcher, t.Context(), "log_tail_save", logTailSaveRequest{
		Path: path, ContentBase64: base64.StdEncoding.EncodeToString([]byte(content)),
	})
	result := decodeExecBatchTest[logTailSaveResult](t, response)
	if result.Path != path || result.Bytes != len(content) {
		t.Fatalf("save result = %+v", result)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Fatalf("saved content = %q", data)
	}

	server := newLogTailIPCService(nil, false)
	serverDispatcher := ipc.NewDispatcher()
	if err := server.register(serverDispatcher); err != nil {
		t.Fatal(err)
	}
	response = dispatchExecBatchTest(serverDispatcher, t.Context(), "log_tail_save", logTailSaveRequest{Path: path})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
		t.Fatalf("server exposed local log save: %+v", response)
	}
}

func TestLogTailResolverEnforcesOwnerSSHAndConnectedState(t *testing.T) {
	transport := newExecBatchTestTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
		return base.ExecResult{}, nil
	})
	manager := newExecBatchTestManager(t, transport)
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	ownerB := ipc.WithUserID(t.Context(), "owner-b")
	sshSession, err := manager.ConnectFor(ownerA, session.Asset{ID: "ssh-log", Name: "ssh", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	localSession, err := manager.ConnectFor(ownerA, session.Asset{ID: "local-log", Name: "local", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	resolver := logTailResolver{sessions: manager}
	if _, err := resolver.Resolve(ownerB, sshSession.ID); !errors.Is(err, session.ErrForbidden) {
		t.Fatalf("foreign owner resolve = %v, want forbidden", err)
	}
	if _, err := resolver.Resolve(ownerA, localSession.ID); !errors.Is(err, session.ErrUnsupported) {
		t.Fatalf("local resolve = %v, want unsupported", err)
	}
	if _, err := resolver.Resolve(ownerA, sshSession.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.DisconnectFor(ownerA, sshSession.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(ownerA, sshSession.ID); !errors.Is(err, session.ErrDisconnected) {
		t.Fatalf("disconnected resolve = %v, want disconnected", err)
	}
}
