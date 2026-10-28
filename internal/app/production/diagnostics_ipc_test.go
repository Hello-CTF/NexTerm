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

func TestDiagnosticsDeleteAndClearProtectTheActiveCapture(t *testing.T) {
	dispatcher, service := newDiagnosticsTestDispatcher(t, true)
	ctx := context.Background()
	root := diagcapture.Root(service.paths)
	names := []string{"20260101T000000Z", "20260102T000000Z", "20260103T000000Z"}
	for _, name := range names {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	active := filepath.Join(root, names[2])
	service.activeDir = func() string { return active }

	var status diagnosticsStatusDTO
	requireStoreTestResponse(t, dispatchDiagnostics(ctx, dispatcher, "diagnostics_delete", `{"path":`+quote(filepath.Join(root, names[0]))+`}`), &status)
	if len(status.Captures) != 2 {
		t.Fatalf("after delete: %+v", status.Captures)
	}
	if response := dispatchDiagnostics(ctx, dispatcher, "diagnostics_delete", `{"path":`+quote(active)+`}`); response.OK {
		t.Fatal("the active capture must not be deletable")
	}
	for _, bad := range []string{"", "../x", service.paths.DataDir} {
		if response := dispatchDiagnostics(ctx, dispatcher, "diagnostics_delete", `{"path":`+quote(bad)+`}`); response.OK {
			t.Fatalf("delete %q must be rejected", bad)
		}
	}
	requireStoreTestResponse(t, dispatchDiagnostics(ctx, dispatcher, "diagnostics_clear", `null`), &status)
	if len(status.Captures) != 1 || status.Captures[0].Name != names[2] {
		t.Fatalf("clear must keep only the active capture: %+v", status.Captures)
	}
	member := ipc.WithRole(ipc.WithUserID(ctx, "u1"), "member")
	serverDispatcher, _ := newDiagnosticsTestDispatcher(t, false)
	if response := dispatchDiagnostics(member, serverDispatcher, "diagnostics_clear", `null`); response.OK {
		t.Fatal("non-admin must not clear captures")
	}
}

func TestDiagnosticsFrontendReportIsValidatedAndOnlyAcceptedDuringACapture(t *testing.T) {
	dispatcher, _ := newDiagnosticsTestDispatcher(t, true)
	ctx := context.Background()
	if response := dispatchDiagnostics(ctx, dispatcher, "diagnostics_report_frontend", `{"kind":"window","fps":60}`); response.OK {
		t.Fatal("a report without a running capture must be rejected")
	}
	if response := dispatchDiagnostics(ctx, dispatcher, "diagnostics_report_frontend", `{"kind":"other"}`); response.OK {
		t.Fatal("an unknown kind must be rejected")
	}

	long := make([]byte, 300)
	for i := range long {
		long[i] = 'a'
	}
	report := diagnosticsFrontendReport{
		Kind: "window", Fps: 58.5, Agent: string(long),
		IPC: []diagnosticsNamedValue{{Name: "terminal_list\n" + string(long), Value: 3}},
	}
	line, err := report.encode()
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Agent string                  `json:"agent"`
		Fps   float64                 `json:"fps"`
		IPC   []diagnosticsNamedValue `json:"ipc"`
	}
	if err := json.Unmarshal(line, &decoded); err != nil {
		t.Fatal(err)
	}
	if len([]rune(decoded.Agent)) != diagnosticsMaxName || len([]rune(decoded.IPC[0].Name)) != diagnosticsMaxName || decoded.Fps != 58.5 {
		t.Fatalf("report was not bounded: %+v", decoded)
	}
	for _, r := range decoded.IPC[0].Name {
		if r == '\n' {
			t.Fatal("control characters must be stripped from names")
		}
	}
	items := make([]diagnosticsNamedValue, diagnosticsMaxItems+10)
	line, err = diagnosticsFrontendReport{Kind: "window", IPC: items}.encode()
	if err != nil {
		t.Fatal(err)
	}
	var bounded struct {
		IPC []diagnosticsNamedValue `json:"ipc"`
	}
	_ = json.Unmarshal(line, &bounded)
	if len(bounded.IPC) != diagnosticsMaxItems {
		t.Fatalf("list length = %d, want the cap %d", len(bounded.IPC), diagnosticsMaxItems)
	}
}

func TestDiagnosticsRevealArchiveValidatesAndRespectsMode(t *testing.T) {
	dispatcher, service := newDiagnosticsTestDispatcher(t, true)
	var revealed []string
	service.revealPath = func(path string) error { revealed = append(revealed, path); return nil }
	ctx := context.Background()
	root := diagcapture.Root(service.paths)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "a.zip")
	if err := os.WriteFile(archive, []byte("zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if response := dispatchDiagnostics(ctx, dispatcher, "diagnostics_reveal_archive", `{"path":`+quote(archive)+`}`); !response.OK {
		t.Fatalf("reveal: %+v", response.Error)
	}
	if len(revealed) != 1 || revealed[0] != archive {
		t.Fatalf("revealed %v", revealed)
	}
	outside := filepath.Join(t.TempDir(), "x.zip")
	if err := os.WriteFile(outside, []byte("zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if response := dispatchDiagnostics(ctx, dispatcher, "diagnostics_reveal_archive", `{"path":`+quote(outside)+`}`); response.OK {
		t.Fatal("reveal must reject archives outside the capture root")
	}
	if len(revealed) != 1 {
		t.Fatalf("rejected path must not reach the shell: %v", revealed)
	}

	server, serverService := newDiagnosticsTestDispatcher(t, false)
	serverService.revealPath = func(string) error { t.Fatal("server mode must not reveal"); return nil }
	member := ipc.WithRole(ipc.WithUserID(context.Background(), "u1"), "member")
	if response := dispatchDiagnostics(member, server, "diagnostics_reveal_archive", `{"path":"a.zip"}`); response.OK || response.Error.Code != ipc.CodeForbidden {
		t.Fatalf("non-admin must be forbidden: %+v", response)
	}
	admin := ipc.WithRole(ipc.WithUserID(context.Background(), "u2"), "superadmin")
	if response := dispatchDiagnostics(admin, server, "diagnostics_reveal_archive", `{"path":"a.zip"}`); response.OK || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("server mode must be unsupported: %+v", response)
	}
}
