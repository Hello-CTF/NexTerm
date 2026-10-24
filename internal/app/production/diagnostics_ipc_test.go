package production

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/diagcapture"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func newDiagnosticsTestDispatcher(t *testing.T, desktop bool) (*ipc.Dispatcher, *diagnosticsService) {
	t.Helper()
	service := newDiagnosticsService(t.TempDir(), desktop)
	var opened []string
	service.openPath = func(path string) error { opened = append(opened, path); return nil }
	dispatcher := ipc.NewDispatcher()
	if err := registerDiagnosticsCommands(dispatcher, service); err != nil {
		t.Fatal(err)
	}
	return dispatcher, service
}

func dispatchDiagnostics(ctx context.Context, dispatcher *ipc.Dispatcher, command, args string) ipc.Response {
	return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
}

func TestDiagnosticsArmStatusAndPackOnDesktop(t *testing.T) {
	dispatcher, service := newDiagnosticsTestDispatcher(t, true)
	ctx := context.Background()

	var status diagnosticsStatusDTO
	requireStoreTestResponse(t, dispatchDiagnostics(ctx, dispatcher, "diagnostics_arm", `{"armed":true}`), &status)
	if !status.Armed || !status.CanOpenFolder || status.Active {
		t.Fatalf("after arm: %+v", status)
	}
	requireStoreTestResponse(t, dispatchDiagnostics(ctx, dispatcher, "diagnostics_arm", `{"armed":false}`), &status)
	if status.Armed {
		t.Fatalf("after disarm: %+v", status)
	}

	capture := filepath.Join(diagcapture.Root(service.paths), "20260101T000000Z")
	if err := os.MkdirAll(capture, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(capture, "meta.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	requireStoreTestResponse(t, dispatchDiagnostics(ctx, dispatcher, "diagnostics_status", `null`), &status)
	if len(status.Captures) != 1 || status.Captures[0].Name != "20260101T000000Z" {
		t.Fatalf("captures: %+v", status.Captures)
	}

	var packed diagnosticsPackDTO
	requireStoreTestResponse(t, dispatchDiagnostics(ctx, dispatcher, "diagnostics_pack", `{"path":`+quote(capture)+`}`), &packed)
	if packed.Path != capture+".zip" {
		t.Fatalf("pack path %q", packed.Path)
	}
	for _, bad := range []string{filepath.Join(capture, ".."), filepath.Join(service.paths.DataDir), "../x", ""} {
		response := dispatchDiagnostics(ctx, dispatcher, "diagnostics_pack", `{"path":`+quote(bad)+`}`)
		if response.OK {
			t.Fatalf("pack %q must be rejected", bad)
		}
	}
	if response := dispatchDiagnostics(ctx, dispatcher, "diagnostics_open_folder", `{"path":`+quote(filepath.Join(capture, ".."))+`}`); response.OK {
		t.Fatal("open_folder must reject paths outside the capture root")
	}
	if response := dispatchDiagnostics(ctx, dispatcher, "diagnostics_open_folder", `{"path":`+quote(capture)+`}`); !response.OK {
		t.Fatalf("open_folder: %+v", response.Error)
	}
}

func quote(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

func TestDiagnosticsServerModeAuthorizationAndOpenFolder(t *testing.T) {
	dispatcher, _ := newDiagnosticsTestDispatcher(t, false)

	anonymous := dispatchDiagnostics(context.Background(), dispatcher, "diagnostics_status", `null`)
	if anonymous.OK || anonymous.Error == nil || anonymous.Error.Code != ipc.CodeForbidden {
		t.Fatalf("anonymous caller must be forbidden: %+v", anonymous)
	}
	member := ipc.WithRole(ipc.WithUserID(context.Background(), "u1"), "member")
	if response := dispatchDiagnostics(member, dispatcher, "diagnostics_arm", `{"armed":true}`); response.OK {
		t.Fatal("non-admin must not arm")
	}
	admin := ipc.WithRole(ipc.WithUserID(context.Background(), "u2"), "superadmin")
	var status diagnosticsStatusDTO
	requireStoreTestResponse(t, dispatchDiagnostics(admin, dispatcher, "diagnostics_status", `null`), &status)
	if status.CanOpenFolder {
		t.Fatal("server mode must not offer opening folders")
	}
	response := dispatchDiagnostics(admin, dispatcher, "diagnostics_open_folder", `{"path":""}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("open_folder in server mode must be unsupported: %+v", response)
	}
	open := owner.WithOpenAccess(context.Background())
	requireStoreTestResponse(t, dispatchDiagnostics(open, dispatcher, "diagnostics_status", `null`), &status)
}
