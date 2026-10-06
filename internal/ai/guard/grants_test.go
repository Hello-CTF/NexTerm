package guard

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type grantAuditRecorder struct {
	mu     sync.Mutex
	events []GrantAuditEvent
}

func (r *grantAuditRecorder) record(_ context.Context, event GrantAuditEvent) error {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	return nil
}

func (r *grantAuditRecorder) snapshot() []GrantAuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]GrantAuditEvent(nil), r.events...)
}

func newTestGrants(t *testing.T, audit GrantAuditor) (*Grants, *memorySettings) {
	t.Helper()
	settings := &memorySettings{values: make(map[string]string)}
	manager, err := NewGrants(context.Background(), settings, audit)
	if err != nil {
		t.Fatal(err)
	}
	return manager, settings
}

func TestDeviceGrantsDefaultOff(t *testing.T) {
	grants, _ := newTestGrants(t, nil)
	ruling := Confirm(KindProcess, "终端控制键可能中断或改变程序")
	decision := grants.Evaluate(context.Background(), Config{Mode: ReadWrite}, ruling, nil, "device-a", "send_keys", "run-1")
	if decision.Action != ActionAsk {
		t.Fatalf("default-off decision = %+v", decision)
	}
	if len(grants.List()) != 0 {
		t.Fatal("grants not empty by default")
	}
}

func TestDeviceGrantKindsRestricted(t *testing.T) {
	if _, err := ParseGrantKinds([]string{"service"}); err == nil {
		t.Fatal("service kind accepted")
	}
	if _, err := ParseGrantKinds([]string{"daemon"}); err == nil {
		t.Fatal("daemon kind accepted")
	}
	if _, err := ParseGrantKinds(nil); err == nil {
		t.Fatal("empty kinds accepted")
	}
	kinds, err := ParseGrantKinds([]string{"terminal_write", "session_exec", "terminal_write"})
	if err != nil || len(kinds) != 2 {
		t.Fatalf("kinds=%v err=%v", kinds, err)
	}
	for operation, want := range map[string]GrantKind{
		"send_keys":      GrantKindTerminalWrite,
		"exec_commands":  GrantKindSessionExec,
		"docker_control": "",
		"docker_exec":    "",
		"db_query":       "",
		"write_file":     "",
		"send_reminder":  "",
		"read_screen":    "",
	} {
		if got := OperationGrantKind(operation); got != want {
			t.Fatalf("OperationGrantKind(%s) = %q, want %q", operation, got, want)
		}
	}
	grants, _ := newTestGrants(t, nil)
	if _, err := grants.Grant(context.Background(), "device-a", []GrantKind{"service"}); err == nil {
		t.Fatal("Grant accepted service kind")
	}
	if _, err := grants.Grant(context.Background(), " ", []GrantKind{GrantKindTerminalWrite}); err == nil {
		t.Fatal("Grant accepted empty device")
	}
}

func TestDeviceGrantCoversOnlyItsDeviceAndKind(t *testing.T) {
	grants, _ := newTestGrants(t, nil)
	if _, err := grants.Grant(context.Background(), "device-a", []GrantKind{GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	ruling := Confirm(KindProcess, "终端控制键可能中断或改变程序")
	if decision := grants.Evaluate(context.Background(), Config{Mode: ReadWrite}, ruling, nil, "device-a", "send_keys", "run-1"); decision.Action != ActionAllow {
		t.Fatalf("granted device decision = %+v", decision)
	}
	if decision := grants.Evaluate(context.Background(), Config{Mode: ReadWrite}, ruling, nil, "device-b", "send_keys", "run-1"); decision.Action != ActionAsk {
		t.Fatalf("cross-device decision = %+v", decision)
	}
	if decision := grants.Evaluate(context.Background(), Config{Mode: ReadWrite}, ruling, nil, "device-a", "exec_commands", "run-1"); decision.Action != ActionAsk {
		t.Fatalf("cross-kind decision = %+v", decision)
	}
	if decision := grants.Evaluate(context.Background(), Config{Mode: ReadWrite}, ruling, nil, "", "send_keys", "run-1"); decision.Action != ActionAsk {
		t.Fatalf("empty device decision = %+v", decision)
	}
}

func TestDeviceGrantRevokeIsImmediate(t *testing.T) {
	grants, settings := newTestGrants(t, nil)
	if _, err := grants.Grant(context.Background(), "device-a", []GrantKind{GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	ruling := Confirm(KindProcess, "终端控制键可能中断或改变程序")
	if decision := grants.Evaluate(context.Background(), Config{Mode: ReadWrite}, ruling, nil, "device-a", "send_keys", "run-1"); decision.Action != ActionAllow {
		t.Fatalf("granted decision = %+v", decision)
	}
	if err := grants.Revoke(context.Background(), "device-a"); err != nil {
		t.Fatal(err)
	}
	if decision := grants.Evaluate(context.Background(), Config{Mode: ReadWrite}, ruling, nil, "device-a", "send_keys", "run-1"); decision.Action != ActionAsk {
		t.Fatalf("post-revoke decision = %+v", decision)
	}
	if _, ok := grants.Get("device-a"); ok {
		t.Fatal("revoked grant still present")
	}
	if err := grants.Revoke(context.Background(), "device-a"); err != nil {
		t.Fatalf("repeated revoke: %v", err)
	}
	reloaded, err := NewGrants(context.Background(), settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.List()) != 0 {
		t.Fatal("revoke not persisted")
	}
}

func TestDeviceGrantEvaluationOrder(t *testing.T) {
	grants, _ := newTestGrants(t, nil)
	if _, err := grants.Grant(context.Background(), "device-a", []GrantKind{GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	forbidden := Deny("禁止")
	if decision := grants.Evaluate(context.Background(), Config{Mode: ReadWrite}, forbidden, nil, "device-a", "send_keys", "run-1"); decision.Action != ActionDeny {
		t.Fatalf("explicit-deny order = %+v", decision)
	}
	danger := Dangerous("删除容器需要始终确认")
	if decision := grants.Evaluate(context.Background(), Config{Mode: ReadWrite}, danger, nil, "device-a", "send_keys", "run-1"); decision.Action != ActionAsk {
		t.Fatalf("danger grant coverage = %+v", decision)
	}
	unknowable := Indeterminate("命令无法可靠解析")
	if decision := grants.Evaluate(context.Background(), Config{Mode: ReadWrite}, unknowable, nil, "device-a", "send_keys", "run-1"); decision.Action != ActionAsk {
		t.Fatalf("unknowable grant coverage = %+v", decision)
	}
	confirm := Confirm(KindProcess, "终端控制键可能中断或改变程序")
	if decision := grants.Evaluate(context.Background(), Config{Mode: ReadOnly}, confirm, nil, "device-a", "send_keys", "run-1"); decision.Action != ActionAllow {
		t.Fatalf("device-grant before global-mode = %+v", decision)
	}
}

func TestDeviceGrantAuditTrail(t *testing.T) {
	recorder := &grantAuditRecorder{}
	grants, _ := newTestGrants(t, recorder.record)
	if _, err := grants.Grant(context.Background(), "device-a", []GrantKind{GrantKindTerminalWrite, GrantKindSessionExec}); err != nil {
		t.Fatal(err)
	}
	ruling := Confirm(KindProcess, "终端控制键可能中断或改变程序")
	grants.Evaluate(context.Background(), Config{Mode: ReadWrite}, ruling, nil, "device-a", "send_keys", "run-1")
	grants.Evaluate(context.Background(), Config{Mode: ReadWrite}, ruling, nil, "device-b", "send_keys", "run-1")
	if err := grants.Revoke(context.Background(), "device-a"); err != nil {
		t.Fatal(err)
	}
	events := recorder.snapshot()
	if len(events) != 3 {
		t.Fatalf("audit events = %+v", events)
	}
	if events[0].Action != "grant" || events[0].DeviceID != "device-a" || len(events[0].Kinds) != 2 {
		t.Fatalf("grant event = %+v", events[0])
	}
	if events[1].Action != "use" || events[1].Operation != "send_keys" || events[1].RunID != "run-1" {
		t.Fatalf("use event = %+v", events[1])
	}
	if events[2].Action != "revoke" || events[2].DeviceID != "device-a" {
		t.Fatalf("revoke event = %+v", events[2])
	}
}

func TestDeviceGrantPersistenceRoundTrip(t *testing.T) {
	grants, settings := newTestGrants(t, nil)
	if _, err := grants.Grant(context.Background(), "device-a", []GrantKind{GrantKindSessionExec}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewGrants(context.Background(), settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	grant, ok := reloaded.Get("device-a")
	if !ok || !grant.Covers(GrantKindSessionExec) || grant.Covers(GrantKindTerminalWrite) {
		t.Fatalf("reloaded grant = %+v", grant)
	}
}

func TestDeviceGrantPersistenceFailureRollsBack(t *testing.T) {
	grants, settings := newTestGrants(t, nil)
	settings.setErr = errors.New("disk full")
	if _, err := grants.Grant(context.Background(), "device-a", []GrantKind{GrantKindTerminalWrite}); err == nil {
		t.Fatal("persist failure ignored")
	}
	if len(grants.List()) != 0 {
		t.Fatal("runtime grant kept after persistence failure")
	}
}

func TestDeviceGrantCorruptSettingsRejected(t *testing.T) {
	settings := &memorySettings{values: map[string]string{DeviceGrantsSettingKey: "{"}}
	if _, err := NewGrants(context.Background(), settings, nil); err == nil {
		t.Fatal("corrupt settings accepted")
	}
}
