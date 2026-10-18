package mount

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

func TestWindowsMountCredentialsUseStdinAndAudit(t *testing.T) {
	runner := &fakeRunner{lookups: map[string]string{"net": "net"}}
	auditor := &fakeAuditor{}
	service := newTestService("windows", runner, auditor)
	const password = "p@ss word-secret"
	entry, err := service.Create(t.Context(), CreateArgs{
		SessionID:  "session-1",
		RemotePath: `\\server\share`,
		LocalPoint: "Z:",
		Username:   `DOMAIN\alice`,
		Password:   password,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "mount-id" || entry.CreatedAt == nil || *entry.CreatedAt != 123456 || entry.SessionID == nil || *entry.SessionID != "session-1" {
		t.Fatalf("entry = %+v", entry)
	}
	if err := service.Remove(t.Context(), RemoveArgs{LocalPoint: "Z:", SessionID: "session-1"}); err != nil {
		t.Fatal(err)
	}
	commands := requireCommandCount(t, runner, 2)
	wantCreate := []string{"use", "Z:", `\\server\share`, "*", `/user:DOMAIN\alice`, "/persistent:yes"}
	if !reflect.DeepEqual(commands[0].args, wantCreate) {
		t.Fatalf("create args = %#v", commands[0].args)
	}
	if string(commands[0].stdin) != password+"\r\n" {
		t.Fatalf("stdin = %q", commands[0].stdin)
	}
	if strings.Contains(strings.Join(commands[0].args, " "), password) {
		t.Fatal("password appeared in process arguments")
	}
	wantRemove := []string{"use", "Z:", "/delete", "/y"}
	if !reflect.DeepEqual(commands[1].args, wantRemove) {
		t.Fatalf("remove args = %#v", commands[1].args)
	}
	audits := auditor.all()
	if len(audits) != 2 || audits[0].ExitCode == nil || *audits[0].ExitCode != 0 || audits[1].ExitCode == nil || *audits[1].ExitCode != 0 {
		t.Fatalf("audits = %+v", audits)
	}
	encoded, err := json.Marshal(audits[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), password) || strings.Contains(string(encoded), "alice") {
		t.Fatalf("credentials appeared in audit: %s", encoded)
	}
}

func TestLinuxSSHFSAndFusermountCommands(t *testing.T) {
	runner := &fakeRunner{lookups: map[string]string{"sshfs": "sshfs", "fusermount3": "fusermount3", "fusermount": "fusermount"}}
	service := newTestService("linux", runner, &fakeAuditor{})
	const password = "sshfs-secret"
	entry, err := service.Create(t.Context(), CreateArgs{
		SessionID:  "session-1",
		RemotePath: "[2001:db8::1]:/srv",
		LocalPoint: "/mnt/remote dir",
		Username:   "alice",
		Password:   password,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Remote != "alice@[2001:db8::1]:/srv" {
		t.Fatalf("remote = %q", entry.Remote)
	}
	if err := service.Remove(t.Context(), RemoveArgs{LocalPoint: "/mnt/remote dir", SessionID: "session-1"}); err != nil {
		t.Fatal(err)
	}
	commands := requireCommandCount(t, runner, 2)
	wantCreate := []string{"alice@[2001:db8::1]:/srv", "/mnt/remote dir", "-o", "reconnect", "-o", "ServerAliveInterval=30", "-o", "password_stdin"}
	if !reflect.DeepEqual(commands[0].args, wantCreate) {
		t.Fatalf("create args = %#v", commands[0].args)
	}
	if string(commands[0].stdin) != password+"\n" || strings.Contains(strings.Join(commands[0].args, " "), password) {
		t.Fatalf("credential handling args=%v stdin=%q", commands[0].args, commands[0].stdin)
	}
	if !reflect.DeepEqual(commands[1].args, []string{"-u", "/mnt/remote dir"}) || commands[1].name != "fusermount3" {
		t.Fatalf("remove command = %+v", commands[1])
	}
}

func TestLinuxFallsBackToFusermount(t *testing.T) {
	runner := &fakeRunner{lookups: map[string]string{"fusermount": "fusermount"}}
	service := newTestService("linux", runner, nil)
	service.owned["/mnt/remote"] = ownedMount{entry: Entry{LocalPoint: "/mnt/remote", SessionID: stringPointer("session-1")}}
	if err := service.Remove(t.Context(), RemoveArgs{LocalPoint: "/mnt/remote", SessionID: "session-1"}); err != nil {
		t.Fatal(err)
	}
	commands := requireCommandCount(t, runner, 1)
	if commands[0].name != "fusermount" {
		t.Fatalf("command = %+v", commands[0])
	}
}

func TestMissingSSHFSIsUnsupportedAndAudited(t *testing.T) {
	runner := &fakeRunner{lookups: map[string]string{}}
	auditor := &fakeAuditor{}
	service := newTestService("linux", runner, auditor)
	_, err := service.Create(t.Context(), CreateArgs{RemotePath: "host:/srv", LocalPoint: "/mnt/remote"})
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeUnsupported || !strings.Contains(err.Error(), "apt install sshfs") {
		t.Fatalf("error = %v", err)
	}
	audits := auditor.all()
	if len(audits) != 1 || audits[0].ExitCode == nil || *audits[0].ExitCode != 1 {
		t.Fatalf("audits = %+v", audits)
	}
	payload := audits[0].Payload.(auditPayload)
	if payload.OK || payload.Error == "" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestCommandOutputAndAuditRedactPassword(t *testing.T) {
	const password = "never-expose-me"
	runner := &fakeRunner{
		lookups: map[string]string{"net": "net"},
		runs:    []fakeRun{{result: commandResult{stderr: "authentication failed for " + password}, err: errors.New("exit status 1")}},
	}
	auditor := &fakeAuditor{}
	service := newTestService("windows", runner, auditor)
	_, err := service.Create(t.Context(), CreateArgs{RemotePath: `\\server\share`, LocalPoint: "Z:", Username: "alice", Password: password})
	if err == nil || strings.Contains(err.Error(), password) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("error = %v", err)
	}
	payload := auditor.all()[0].Payload.(auditPayload)
	if strings.Contains(payload.Error, password) {
		t.Fatalf("audit error = %q", payload.Error)
	}
}

func TestMacOSCapabilityAndFailuresAreAudited(t *testing.T) {
	runner := &fakeRunner{lookups: map[string]string{"mount": "mount"}}
	auditor := &fakeAuditor{}
	service := newTestService("darwin", runner, auditor)
	if reason := service.Capability(); reason == nil || *reason != MacOSUnavailableReason {
		t.Fatalf("capability = %v", reason)
	}
	_, err := service.Create(t.Context(), CreateArgs{RemotePath: "host:/srv", LocalPoint: "/Volumes/remote"})
	assertUnsupported(t, err)
	err = service.Remove(t.Context(), RemoveArgs{LocalPoint: "/Volumes/remote"})
	assertUnsupported(t, err)
	if got := len(auditor.all()); got != 2 {
		t.Fatalf("audit count = %d", got)
	}
	if got := len(requireCommandCount(t, runner, 0)); got != 0 {
		t.Fatal("macOS mutation executed a command")
	}
	_, err = service.Create(t.Context(), CreateArgs{})
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam || len(auditor.all()) != 2 {
		t.Fatalf("empty args error=%v audits=%d", err, len(auditor.all()))
	}
}

func TestListAlwaysReadsRealTable(t *testing.T) {
	runner := &fakeRunner{
		lookups: map[string]string{"mount": "mount"},
		runs: []fakeRun{
			{result: commandResult{stdout: "host:/one on /mnt/one type fuse.sshfs (rw)\n"}},
			{result: commandResult{stdout: "host:/two on /mnt/two type fuse.sshfs (rw)\n"}},
		},
	}
	service := newTestService("linux", runner, nil)
	first, err := service.List(t.Context(), false)
	if err != nil || len(first) != 1 || first[0].LocalPoint != "/mnt/one" {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := service.List(t.Context(), true)
	if err != nil || len(second) != 1 || second[0].LocalPoint != "/mnt/two" {
		t.Fatalf("second = %+v, %v", second, err)
	}
	requireCommandCount(t, runner, 2)
}

func TestCommandCancellationIsReturnedAndAudited(t *testing.T) {
	runner := &fakeRunner{lookups: map[string]string{"sshfs": "sshfs"}}
	runner.run = func(ctx context.Context, _ command) (commandResult, error) {
		<-ctx.Done()
		return commandResult{}, ctx.Err()
	}
	auditor := &fakeAuditor{}
	service := newTestService("linux", runner, auditor)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := service.Create(ctx, CreateArgs{RemotePath: "host:/srv", LocalPoint: "/mnt/remote"})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if got := len(auditor.all()); got != 1 {
		t.Fatalf("audit count = %d", got)
	}
}

func TestIncompleteOrInjectedCredentialsAreRejected(t *testing.T) {
	for _, test := range []struct {
		name     string
		platform string
		args     CreateArgs
	}{
		{name: "Windows password without user", platform: "windows", args: CreateArgs{RemotePath: `\\server\share`, LocalPoint: "Z:", Password: "secret"}},
		{name: "Windows option local", platform: "windows", args: CreateArgs{RemotePath: `\\server\share`, LocalPoint: "/delete"}},
		{name: "SSHFS option remote", platform: "linux", args: CreateArgs{RemotePath: "-oProxyCommand=bad", LocalPoint: "/mnt/x"}},
		{name: "SSHFS duplicate user", platform: "linux", args: CreateArgs{RemotePath: "bob@host:/srv", LocalPoint: "/mnt/x", Username: "alice"}},
		{name: "multiline password", platform: "linux", args: CreateArgs{RemotePath: "host:/srv", LocalPoint: "/mnt/x", Password: "one\ntwo"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{lookups: map[string]string{}}
			service := newTestService(test.platform, runner, nil)
			_, err := service.Create(t.Context(), test.args)
			var appErr *ipc.Error
			if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam {
				t.Fatalf("error = %v", err)
			}
			requireCommandCount(t, runner, 0)
		})
	}
}

func TestSessionMountScopeAndUnknownRemoval(t *testing.T) {
	const table = "host:/srv on /mnt/shared type fuse.sshfs (rw)\n"
	runner := &fakeRunner{
		lookups: map[string]string{"sshfs": "sshfs", "mount": "mount", "fusermount3": "fusermount3"},
		runs: []fakeRun{
			{},
			{result: commandResult{stdout: table}},
			{result: commandResult{stdout: table}},
			{result: commandResult{stdout: table}},
			{result: commandResult{stdout: table}},
			{},
		},
	}
	service := newTestService("linux", runner, nil)
	service.sessionHost = func(_ context.Context, sessionID string) (string, error) {
		switch sessionID {
		case "first":
			return "asset-one", nil
		case "second":
			return "asset-two", nil
		default:
			return "", session.ErrSessionNotFound
		}
	}
	created, err := service.Create(t.Context(), CreateArgs{SessionID: "first", RemotePath: "host:/srv", LocalPoint: "/mnt/shared"})
	if err != nil || !created.Managed {
		t.Fatalf("created = %+v, %v", created, err)
	}
	first, err := service.ListFor(t.Context(), listRequest{SessionID: "first"})
	if err != nil || len(first) != 1 || first[0].ID != created.ID || !first[0].Managed {
		t.Fatalf("first list = %+v, %v", first, err)
	}
	second, err := service.ListFor(t.Context(), listRequest{SessionID: "second"})
	if err != nil || len(second) != 0 {
		t.Fatalf("second list = %+v, %v", second, err)
	}
	global, err := service.ListFor(t.Context(), listRequest{Global: true})
	if err != nil || len(global) != 1 || global[0].Managed || global[0].SessionID != nil {
		t.Fatalf("global list = %+v, %v", global, err)
	}
	if err := service.Remove(t.Context(), RemoveArgs{SessionID: "second", LocalPoint: "/mnt/shared"}); err == nil {
		t.Fatal("another session removed the mount")
	}
	if err := service.Remove(t.Context(), RemoveArgs{SessionID: "first", LocalPoint: "/mnt/shared"}); err != nil {
		t.Fatal(err)
	}
	commands := requireCommandCount(t, runner, 6)
	if commands[5].name != "fusermount3" {
		t.Fatalf("last command = %+v", commands[5])
	}
	restarted := newTestService("linux", runner, nil)
	restarted.sessionHost = service.sessionHost
	if err := restarted.Remove(t.Context(), RemoveArgs{SessionID: "first", LocalPoint: "/mnt/shared"}); err == nil || !strings.Contains(err.Error(), "应用重启") {
		t.Fatalf("removal after restart = %v", err)
	}
	requireCommandCount(t, runner, 6)
}

func TestMountPointReplacementIsNotUnmounted(t *testing.T) {
	runner := &fakeRunner{
		lookups: map[string]string{"sshfs": "sshfs", "mount": "mount", "fusermount3": "fusermount3"},
		runs:    []fakeRun{{}, {result: commandResult{stdout: "other:/srv on /mnt/shared type fuse.sshfs (rw)\n"}}},
	}
	service := newTestService("linux", runner, nil)
	service.sessionHost = func(context.Context, string) (string, error) { return "asset-one", nil }
	if _, err := service.Create(t.Context(), CreateArgs{SessionID: "first", RemotePath: "host:/srv", LocalPoint: "/mnt/shared"}); err != nil {
		t.Fatal(err)
	}
	if err := service.Remove(t.Context(), RemoveArgs{SessionID: "first", LocalPoint: "/mnt/shared"}); err == nil || !strings.Contains(err.Error(), "未执行卸载") {
		t.Fatalf("replacement removal = %v", err)
	}
	requireCommandCount(t, runner, 2)
}

func assertUnsupported(t *testing.T, err error) {
	t.Helper()
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeUnsupported {
		t.Fatalf("error = %v", err)
	}
}
