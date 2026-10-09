package mount

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestWindowsWildcardCannotCreateOrDeleteAllMounts(t *testing.T) {
	runner := &fakeRunner{lookups: map[string]string{"net": "net"}}
	service := newTestService("windows", runner, nil)
	_, err := service.Create(t.Context(), CreateArgs{RemotePath: `\\server\share`, LocalPoint: "*"})
	assertBadParam(t, err)
	err = service.Remove(t.Context(), RemoveArgs{LocalPoint: "*"})
	assertBadParam(t, err)
	requireCommandCount(t, runner, 0)
}

func TestWindowsPasswordUsesInteractiveConPTYRoute(t *testing.T) {
	runner := &fakeRunner{lookups: map[string]string{"net": "net"}}
	service := newTestService("windows", runner, nil)
	_, err := service.Create(t.Context(), CreateArgs{
		RemotePath: `\\server\share`,
		LocalPoint: "Z:",
		Username:   "alice",
		Password:   "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	commands := requireCommandCount(t, runner, 1)
	if !commands[0].interactive {
		t.Fatal("password command did not request the ConPTY route")
	}
}

func TestCommandCancellationDoesNotOverwriteSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := commandContextError(ctx, nil); err != nil {
		t.Fatalf("successful command became an error: %v", err)
	}
	failure := errors.New("failed")
	if err := commandContextError(ctx, failure); !errors.Is(err, context.Canceled) {
		t.Fatalf("failed command error = %v", err)
	}
}

func TestInteractiveExitStatusRemainsInternal(t *testing.T) {
	runner := &fakeRunner{
		lookups: map[string]string{"net": "net"},
		runs:    []fakeRun{{err: &commandExitError{code: 2}}},
	}
	service := newTestService("windows", runner, nil)
	_, err := service.Create(t.Context(), CreateArgs{RemotePath: `\\server\share`, LocalPoint: "Z:"})
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeInternal {
		t.Fatalf("error = %v", err)
	}
}

func TestParseWindowsPreservesUNCWhitespace(t *testing.T) {
	output := "OK  S:  \\\\server\\share with spaces        Microsoft Windows Network\r\n" +
		"OK  Q:  \"\\\\server name\\share name\"        Microsoft Windows Network\r\n"
	entries := ParseTable("windows", output)
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Remote != `\\server name\share name` || entries[1].Remote != `\\server\share with spaces` {
		t.Fatalf("entries = %+v", entries)
	}
}

func assertBadParam(t *testing.T, err error) {
	t.Helper()
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam {
		t.Fatalf("error = %v", err)
	}
}
