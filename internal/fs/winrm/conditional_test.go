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

func TestWriteFileVersionReplaceScriptLocksVerifiesAndReplaces(t *testing.T) {
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
		"[IO.FileAccess]::Read,[IO.FileShare]::Read",
		"if ($__nextermLength -ne 11)",
		"if ($__nextermActual -ne '" + expected.SHA256 + "')",
		"[Convert]::FromBase64String('" + base64.StdEncoding.EncodeToString(data) + "')",
		"[IO.FileAttributes]::ReparsePoint",
		"[IO.File]::Copy($__nextermPath,$__nextermPath+'.nexterm-bak',$true)",
		"[IO.File]::Replace($__nextermTemp,$__nextermPath,$null)",
		"$__nextermStream.Dispose()",
		"ConvertTo-Json -InputObject $__nextermOutcome -Compress",
	} {
		if !strings.Contains(script, fragment) {
			t.Errorf("replace script missing %q: %s", fragment, script)
		}
	}
	if count := strings.Count(script, "ComputeHash"); count != 2 {
		t.Errorf("replace script verifies %d times, want 2: %s", count, script)
	}
	firstVerify := strings.Index(script, "ComputeHash")
	stage := strings.Index(script, "[IO.File]::WriteAllBytes($__nextermTemp")
	secondVerify := strings.LastIndex(script, "ComputeHash")
	backup := strings.Index(script, "[IO.File]::Copy(")
	commit := strings.Index(script, "[IO.File]::Replace(")
	dispose := strings.Index(script, "$__nextermStream.Dispose()")
	if !(firstVerify < stage && stage < secondVerify && secondVerify < backup && backup < dispose && dispose < commit) {
		t.Errorf("verify/stage/backup/commit order is wrong: %s", script)
	}

	executor.result = success(`{"s":"committed"}`)
	if err := filesystem.WriteFileVersion(context.Background(), "plain.txt", data, false, expected); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(executor.scripts[1], "[IO.File]::Copy(") {
		t.Errorf("backup disabled but copy present: %s", executor.scripts[1])
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
	for _, forbidden := range []string{"[IO.File]::Replace(", "ComputeHash", "[IO.File]::Copy("} {
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

func TestWriteFileVersionRejectsBadResponsesAndInput(t *testing.T) {
	expected := winrmExpectation("old content")
	executor := &fakeExecutor{result: success("not json")}
	filesystem := New(executor)
	if err := filesystem.WriteFileVersion(context.Background(), "file.txt", []byte("new"), false, expected); err == nil || errors.Is(err, conditional.ErrVersionMismatch) {
		t.Fatalf("malformed outcome = %v", err)
	}
	executor.result = success(`{"s":"unknown"}`)
	if err := filesystem.WriteFileVersion(context.Background(), "file.txt", []byte("new"), false, expected); err == nil || !strings.Contains(err.Error(), "unexpected WinRM conditional write response") {
		t.Fatalf("unknown outcome = %v", err)
	}
	code := 1
	executor.result = base.ExecResult{Stderr: "remote failure", ExitCode: &code}
	if err := filesystem.WriteFileVersion(context.Background(), "file.txt", []byte("new"), false, expected); err == nil || !strings.Contains(err.Error(), "remote failure") {
		t.Fatalf("remote failure = %v", err)
	}
	scripts := len(executor.scripts)
	if err := filesystem.WriteFileVersion(context.Background(), "file.txt", []byte("new"), false, conditional.Expectation{Exists: true, Size: 1, SHA256: "bad"}); err == nil {
		t.Fatal("invalid expectation succeeded")
	}
	if err := filesystem.WriteFileVersion(context.Background(), "file.txt", make([]byte, MaxWriteBytes+1), false, expected); err == nil {
		t.Fatal("oversized conditional write succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := filesystem.WriteFileVersion(ctx, "file.txt", []byte("new"), false, expected); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled conditional write = %v", err)
	}
	if len(executor.scripts) != scripts {
		t.Fatalf("invalid input reached the remote executor: %d -> %d scripts", scripts, len(executor.scripts))
	}
}
