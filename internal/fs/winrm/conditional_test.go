package winrm

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/fs/conditional"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
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

func TestWriteFileVersionReplaceScriptLocksThroughCommit(t *testing.T) {
	executor := &fakeExecutor{result: success(`{"s":"committed"}`)}
	filesystem := New(executor)
	expected := winrmExpectation("old content")
	data := []byte("new content")
	if err := filesystem.WriteFileVersion(context.Background(), `C:\Users\O'Brien\a.txt`, data, true, expected); err != nil {
		t.Fatal(err)
	}
	if len(executor.scripts) != 1 {
		t.Fatalf("conditional write used %d remote operations, want exactly 1", len(executor.scripts))
	}
	script := executor.scripts[0]
	for _, fragment := range []string{
		`GetUnresolvedProviderPathFromPSPath('C:\Users\O''Brien\a.txt'`,
		"[IO.FileAccess]::ReadWrite,[IO.FileShare]::ReadWrite -bor [IO.FileShare]::Delete",
		"$__nextermStream.Lock(0,[long]::MaxValue)",
		"if ($__nextermLength -ne 11)",
		"if ($__nextermActual -ne '" + expected.SHA256 + "')",
		"[Convert]::FromBase64String('" + base64.StdEncoding.EncodeToString(data) + "')",
		"[IO.FileAttributes]::ReparsePoint",
		"$__nextermPathStream=[IO.File]::Open(",
		"$__nextermStream.CopyTo($__nextermBackup)",
		"[IO.File]::Replace($__nextermTemp,$__nextermPath,$null)",
		"ConvertTo-Json -InputObject $__nextermOutcome -Compress",
	} {
		if !strings.Contains(script, fragment) {
			t.Errorf("replace script missing %q: %s", fragment, script)
		}
	}
	if count := strings.Count(script, "ComputeHash"); count != 3 {
		t.Errorf("replace script hashes %d times, want 3 (locked twice, path once): %s", count, script)
	}
	lock := strings.Index(script, "$__nextermStream.Lock(")
	firstVerify := strings.Index(script, "ComputeHash($__nextermStream)")
	stage := strings.Index(script, "[IO.File]::WriteAllBytes($__nextermTemp")
	secondVerify := strings.Index(script, "$__nextermStream.Position=0; $__nextermLength")
	pathVerify := strings.Index(script, "ComputeHash($__nextermPathStream)")
	backup := strings.Index(script, "$__nextermStream.CopyTo($__nextermBackup)")
	commit := strings.Index(script, "[IO.File]::Replace(")
	dispose := strings.Index(script, "$__nextermStream.Dispose()")
	if !(lock < firstVerify && firstVerify < stage && stage < secondVerify && secondVerify < pathVerify && pathVerify < backup && backup < commit && commit < dispose) {
		t.Errorf("lock/verify/stage/backup/commit order is wrong (lock=%d first=%d stage=%d second=%d path=%d backup=%d commit=%d dispose=%d)", lock, firstVerify, stage, secondVerify, pathVerify, backup, commit, dispose)
	}

	executor.result = success(`{"s":"committed"}`)
	if err := filesystem.WriteFileVersion(context.Background(), "plain.txt", data, false, expected); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(executor.scripts[1], "$__nextermBackup") {
		t.Errorf("backup disabled but backup stream present: %s", executor.scripts[1])
	}
}

func TestWriteFileVersionCreateScriptUsesNoClobberMove(t *testing.T) {
	executor := &fakeExecutor{result: success(`{"s":"committed"}`)}
	filesystem := New(executor)
	if err := filesystem.WriteFileVersion(context.Background(), `C:\new\file.txt`, []byte("fresh"), true, conditional.Absent()); err != nil {
		t.Fatal(err)
	}
	if len(executor.scripts) != 1 {
		t.Fatalf("conditional create used %d remote operations, want exactly 1", len(executor.scripts))
	}
	script := executor.scripts[0]
	for _, fragment := range []string{
		"if (Test-Path -LiteralPath $__nextermPath) { return @{s='mismatch';r='file already exists'",
		"[IO.File]::Move($__nextermTemp,$__nextermPath)",
		"catch [IO.IOException]",
	} {
		if !strings.Contains(script, fragment) {
			t.Errorf("create script missing %q: %s", fragment, script)
		}
	}
	for _, forbidden := range []string{"[IO.File]::Replace(", "ComputeHash", "$__nextermBackup"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("create script unexpectedly contains %q: %s", forbidden, script)
		}
	}
}

func TestWriteFileVersionMapsRemoteMismatch(t *testing.T) {
	expected := winrmExpectation("old content")
	digest := conditional.VersionOf([]byte("actual")).SHA256
	for _, test := range []struct {
		name    string
		outcome string
		reason  string
		actual  conditional.Version
	}{
		{name: "digest", outcome: `{"s":"mismatch","r":"content digest differs","e":true,"l":6,"h":"` + digest + `"}`, reason: "content digest differs", actual: conditional.Version{Exists: true, Size: 6, SHA256: digest}},
		{name: "missing", outcome: `{"s":"mismatch","r":"file is missing","e":false,"l":0,"h":""}`, reason: "file is missing", actual: conditional.Version{}},
		{name: "exists", outcome: `{"s":"mismatch","r":"file already exists","e":true,"l":0,"h":""}`, reason: "file already exists", actual: conditional.Version{Exists: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor := &fakeExecutor{result: success(test.outcome)}
			filesystem := New(executor)
			err := filesystem.WriteFileVersion(context.Background(), "file.txt", []byte("new"), true, expected)
			var mismatch *conditional.MismatchError
			if !errors.As(err, &mismatch) {
				t.Fatalf("error = %v, want *conditional.MismatchError", err)
			}
			if !errors.Is(err, conditional.ErrVersionMismatch) {
				t.Fatalf("mismatch does not match ErrVersionMismatch: %v", err)
			}
			if mismatch.Reason != test.reason || mismatch.Expected != expected || mismatch.Actual != test.actual {
				t.Fatalf("mismatch = %+v", mismatch)
			}
			if len(executor.scripts) != 1 {
				t.Fatalf("mismatch retried or continued: %d scripts", len(executor.scripts))
			}
		})
	}
}

func TestWriteFileVersionCommitFaultOutcomes(t *testing.T) {
	expected := winrmExpectation("old content")
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

func TestWriteFileVersionNonzeroExitIsDeterminateAndUncommitted(t *testing.T) {
	code := 1
	executor := &fakeExecutor{result: base.ExecResult{Stderr: "remote failure", ExitCode: &code}}
	filesystem := New(executor)
	err := filesystem.WriteFileVersion(context.Background(), "file.txt", []byte("new"), true, winrmExpectation("old content"))
	if err == nil || !strings.Contains(err.Error(), "remote failure") {
		t.Fatalf("remote failure = %v", err)
	}
	if errors.Is(err, conditional.ErrCommitIndeterminate) || errors.Is(err, conditional.ErrVersionMismatch) {
		t.Fatalf("script throw must be determinate: %v", err)
	}
	if len(executor.scripts) != 1 {
		t.Fatalf("failed script was retried: %d scripts", len(executor.scripts))
	}
}

func TestWriteFileVersionRejectsInvalidInputBeforeDispatch(t *testing.T) {
	expected := winrmExpectation("old content")
	executor := &fakeExecutor{result: success(`{"s":"committed"}`)}
	filesystem := New(executor)
	if err := filesystem.WriteFileVersion(context.Background(), "file.txt", []byte("new"), false, conditional.Expectation{Exists: true, Size: 1, SHA256: "bad"}); err == nil {
		t.Fatal("invalid expectation succeeded")
	}
	if err := filesystem.WriteFileVersion(context.Background(), "file.txt", make([]byte, MaxWriteBytes+1), false, expected); err == nil {
		t.Fatal("oversized conditional write succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := filesystem.WriteFileVersion(ctx, "file.txt", []byte("new"), false, expected)
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
