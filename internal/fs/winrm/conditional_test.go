package winrm

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/fs/conditional"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func winrmExpectation(content string) conditional.Expectation {
	return conditional.VersionOf([]byte(content)).Expectation()
}

func requireWinrmIndeterminate(t *testing.T, err error) *conditional.IndeterminateError {
	t.Helper()
	var indeterminate *conditional.IndeterminateError
	if !errors.As(err, &indeterminate) {
		t.Fatalf("error = %v, want *conditional.IndeterminateError", err)
	}
	if !errors.Is(err, conditional.ErrCommitIndeterminate) {
		t.Fatalf("error does not match ErrCommitIndeterminate: %v", err)
	}
	if errors.Is(err, conditional.ErrVersionMismatch) {
		t.Fatalf("indeterminate must not look like a mismatch: %v", err)
	}
	return indeterminate
}

func TestWriteFileVersionReplaceIsRefusedBeforeDispatch(t *testing.T) {
	executor := &fakeExecutor{result: success(`{"s":"committed"}`)}
	filesystem := New(executor)
	err := filesystem.WriteFileVersion(context.Background(), "file.txt", []byte("new"), true, winrmExpectation("old content"))
	if !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("replacement = %v, want base.ErrUnsupported", err)
	}
	if errors.Is(err, conditional.ErrVersionMismatch) || errors.Is(err, conditional.ErrCommitIndeterminate) {
		t.Fatalf("unsupported must be a distinct outcome: %v", err)
	}
	if len(executor.scripts) != 0 {
		t.Fatalf("refused replacement reached the remote executor: %d scripts", len(executor.scripts))
	}
}

func TestWriteFileVersionCreateScriptUsesNoClobberMove(t *testing.T) {
	executor := &fakeExecutor{result: success(`{"s":"committed"}`)}
	filesystem := New(executor)
	data := []byte("fresh")
	if err := filesystem.WriteFileVersion(context.Background(), `C:\new\O'Brien.txt`, data, true, conditional.Absent()); err != nil {
		t.Fatal(err)
	}
	if len(executor.scripts) != 1 {
		t.Fatalf("conditional create used %d remote operations, want exactly 1", len(executor.scripts))
	}
	script := executor.scripts[0]
	for _, fragment := range []string{
		`GetUnresolvedProviderPathFromPSPath('C:\new\O''Brien.txt'`,
		"[Convert]::FromBase64String('" + base64.StdEncoding.EncodeToString(data) + "')",
		"if (Test-Path -LiteralPath $__nextermPath) { $__nextermOutcome=@{s='mismatch';r='file already exists'",
		"[IO.File]::Move($__nextermTemp,$__nextermPath)",
		"catch [IO.IOException]",
		"@{s='error';c=$__nextermCommitted;m=$_.Exception.Message}",
		"ConvertTo-Json -InputObject $__nextermOutcome -Compress",
	} {
		if !strings.Contains(script, fragment) {
			t.Errorf("create script missing %q: %s", fragment, script)
		}
	}
	move := strings.Index(script, "[IO.File]::Move(")
	committed := strings.Index(script, "$__nextermCommitted=$true")
	outcome := strings.Index(script, "$__nextermOutcome=@{s='committed'}")
	if !(move < committed && committed < outcome) {
		t.Errorf("commit flag must be set between the atomic move and its outcome: %s", script)
	}
	for _, forbidden := range []string{"[IO.File]::Replace(", "ComputeHash", "$__nextermBackup", ".Lock("} {
		if strings.Contains(script, forbidden) {
			t.Errorf("create script unexpectedly contains %q: %s", forbidden, script)
		}
	}
}

func TestWriteFileVersionMapsRemoteMismatch(t *testing.T) {
	expected := conditional.Absent()
	executor := &fakeExecutor{result: success(`{"s":"mismatch","r":"file already exists","e":true,"l":0,"h":""}`)}
	filesystem := New(executor)
	err := filesystem.WriteFileVersion(context.Background(), "file.txt", []byte("new"), true, expected)
	var mismatch *conditional.MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error = %v, want *conditional.MismatchError", err)
	}
	if !errors.Is(err, conditional.ErrVersionMismatch) {
		t.Fatalf("mismatch does not match ErrVersionMismatch: %v", err)
	}
	if mismatch.Reason != "file already exists" || mismatch.Expected != expected || mismatch.Actual != (conditional.Version{Exists: true}) {
		t.Fatalf("mismatch = %+v", mismatch)
	}
	if len(executor.scripts) != 1 {
		t.Fatalf("mismatch retried or continued: %d scripts", len(executor.scripts))
	}
}

func TestWriteFileVersionStructuredPreCommitErrorIsDeterminate(t *testing.T) {
	executor := &fakeExecutor{result: success(`{"s":"error","c":false,"m":"remote failure"}`)}
	filesystem := New(executor)
	err := filesystem.WriteFileVersion(context.Background(), "file.txt", []byte("new"), true, conditional.Absent())
	if err == nil || !strings.Contains(err.Error(), "remote failure") {
		t.Fatalf("structured pre-commit error = %v", err)
	}
	if errors.Is(err, conditional.ErrCommitIndeterminate) || errors.Is(err, conditional.ErrVersionMismatch) {
		t.Fatalf("script-proven pre-commit failure must be determinate: %v", err)
	}
	if len(executor.scripts) != 1 {
		t.Fatalf("failed script was retried: %d scripts", len(executor.scripts))
	}
}

func TestWriteFileVersionCommitFaultOutcomes(t *testing.T) {
	expected := conditional.Absent()
	nonzero := 1
	for _, test := range []struct {
		name     string
		executor *fakeExecutor
		cause    error
	}{
		{name: "transport error after dispatch", executor: &fakeExecutor{err: errors.New("synthetic transport drop")}},
		{name: "cancel after dispatch", executor: &fakeExecutor{err: context.Canceled}, cause: context.Canceled},
		{name: "missing exit status", executor: &fakeExecutor{result: base.ExecResult{}}, cause: base.ErrExitStatusMissing},
		{name: "garbled success output", executor: &fakeExecutor{result: success("not json")}},
		{name: "unknown success outcome", executor: &fakeExecutor{result: success(`{"s":"unknown"}`)}},
		{name: "nonzero exit after possible commit", executor: &fakeExecutor{result: base.ExecResult{Stderr: "synthetic engine failure", ExitCode: &nonzero}}},
		{name: "structured error after commit", executor: &fakeExecutor{result: success(`{"s":"error","c":true,"m":"synthetic dispose failure"}`)}},
		{name: "structured error with missing commit flag", executor: &fakeExecutor{result: success(`{"s":"error","m":"synthetic incomplete outcome"}`)}},
		{name: "structured error with null commit flag", executor: &fakeExecutor{result: success(`{"s":"error","c":null,"m":"synthetic incomplete outcome"}`)}},
		{name: "structured error with mistyped commit flag", executor: &fakeExecutor{result: success(`{"s":"error","c":"false","m":"synthetic mistyped outcome"}`)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			filesystem := New(test.executor)
			err := filesystem.WriteFileVersion(context.Background(), "file.txt", []byte("new"), true, expected)
			indeterminate := requireWinrmIndeterminate(t, err)
			if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("cause lost: %v", err)
			}
			if indeterminate.Expected != expected || indeterminate.New != conditional.VersionOf([]byte("new")) {
				t.Fatalf("reconciliation details = %+v", indeterminate)
			}
			if len(test.executor.scripts) != 1 {
				t.Fatalf("ambiguous commit was retried: %d scripts", len(test.executor.scripts))
			}
		})
	}
}

func TestWriteFileVersionRejectsInvalidInputBeforeDispatch(t *testing.T) {
	executor := &fakeExecutor{result: success(`{"s":"committed"}`)}
	filesystem := New(executor)
	if err := filesystem.WriteFileVersion(context.Background(), "file.txt", []byte("new"), false, conditional.Expectation{Exists: true, Size: 1, SHA256: "bad"}); err == nil {
		t.Fatal("invalid expectation succeeded")
	}
	if err := filesystem.WriteFileVersion(context.Background(), "file.txt", make([]byte, MaxWriteBytes+1), false, conditional.Absent()); err == nil {
		t.Fatal("oversized conditional write succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := filesystem.WriteFileVersion(ctx, "file.txt", []byte("new"), false, conditional.Absent())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled-before-dispatch = %v", err)
	}
	if errors.Is(err, conditional.ErrCommitIndeterminate) {
		t.Fatalf("nothing was dispatched, outcome cannot be indeterminate: %v", err)
	}
	if len(executor.scripts) != 0 {
		t.Fatalf("invalid input reached the remote executor: %d scripts", len(executor.scripts))
	}
}
