package winrm

import (
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type fakeExecutor struct {
	result  base.ExecResult
	err     error
	scripts []string
	options []base.ExecOptions
	handle  func(string) (base.ExecResult, error)
}

func success(output string) base.ExecResult {
	code := 0
	return base.ExecResult{Stdout: output, ExitCode: &code}
}

func (f *fakeExecutor) Exec(_ context.Context, script string, options base.ExecOptions) (base.ExecResult, error) {
	f.scripts = append(f.scripts, script)
	f.options = append(f.options, options)
	if f.handle != nil {
		return f.handle(script)
	}
	return f.result, f.err
}

func TestListParsesArrayFixtures(t *testing.T) {
	array, err := os.ReadFile("testdata/list-array.json")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := parseList(`C:\root`, string(array))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].Name != "目录" || entries[0].Kind != base.FileDirectory || entries[1].Name != "a-file.txt" {
		t.Fatalf("unexpected sorted entries: %+v", entries)
	}
	if entries[1].Path != `C:\root\a-file.txt` || entries[1].Size != 7 || !entries[1].ModTime.Equal(time.Date(2026, 10, 2, 12, 2, 0, 0, time.UTC)) {
		t.Fatalf("unexpected file entry: %+v", entries[1])
	}
	single, err := os.ReadFile("testdata/list-single.json")
	if err != nil {
		t.Fatal(err)
	}
	entries, err = parseList(`C:\root\`, string(single))
	if err != nil || len(entries) != 1 || entries[0].Path != `C:\root\O'Brien.txt` || entries[0].Size != 42 {
		t.Fatalf("single entry = %+v, %v", entries, err)
	}
	if _, err := parseList(`C:\root`, `{"n":"legacy.txt","d":false,"l":1,"m":"2026-10-02T12:03:04Z"}`); err == nil {
		t.Fatal("single-object JSON succeeded")
	}
	if entries, err = parseList(`C:\empty`, " \r\n"); err != nil || len(entries) != 0 {
		t.Fatalf("empty directory = %+v, %v", entries, err)
	}
}

func TestListUsesStableJSONAndUnlimitedFileOutput(t *testing.T) {
	executor := &fakeExecutor{result: success("[]")}
	filesystem := New(executor)
	if _, err := filesystem.List(context.Background(), `C:\O'Brien`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(executor.scripts[0], `-LiteralPath 'C:\O''Brien'`) || !strings.Contains(executor.scripts[0], "ConvertTo-Json -InputObject $items -Compress") {
		t.Fatalf("unsafe or unstable list script: %s", executor.scripts[0])
	}
	limits := executor.options[0].Limits
	if limits.Stdout != -1 || limits.Stderr != -1 {
		t.Fatalf("file output unexpectedly limited: %+v", limits)
	}
}

func TestReadFileBase64AndLimit(t *testing.T) {
	content := []byte{0, 1, 2, 0xff, 'x'}
	executor := &fakeExecutor{result: success(base64.StdEncoding.EncodeToString(content) + "\r\n")}
	filesystem := New(executor)
	got, err := filesystem.ReadFile(context.Background(), `C:\O'Brien\bin.dat`, 100)
	if err != nil || string(got) != string(content) {
		t.Fatalf("ReadFile = %v, %v", got, err)
	}
	script := executor.scripts[0]
	if !strings.Contains(script, `GetUnresolvedProviderPathFromPSPath('C:\O''Brien\bin.dat'`) || !strings.Contains(script, "[IO.File]::ReadAllBytes($f.FullName)") || !strings.Contains(script, "$f.Length -gt 100") {
		t.Fatalf("read script = %s", script)
	}
	if _, err := filesystem.ReadFile(context.Background(), "file", -1); err == nil {
		t.Fatal("negative read limit succeeded")
	}
	executor.result = success("not base64!")
	if _, err := filesystem.ReadFile(context.Background(), "file", 100); err == nil {
		t.Fatal("invalid base64 succeeded")
	}
	executor.result = success(base64.StdEncoding.EncodeToString([]byte("too long")))
	if _, err := filesystem.ReadFile(context.Background(), "file", 4); err == nil {
		t.Fatal("content that grew past the remote length check succeeded")
	}
}

func TestWriteFileQuotesBackupAndEnforcesLimit(t *testing.T) {
	executor := &fakeExecutor{result: success("")}
	filesystem := New(executor)
	path := `C:\Users\O'Brien\a.txt`
	data := []byte("new content")
	if err := filesystem.WriteFile(context.Background(), path, data, true); err != nil {
		t.Fatal(err)
	}
	script := executor.scripts[0]
	for _, fragment := range []string{
		`GetUnresolvedProviderPathFromPSPath('C:\Users\O''Brien\a.txt'`,
		`$__nextermBackup=$__nextermPath+'.nexterm-bak'`,
		`-Destination $__nextermBackup -Force`,
		`[IO.File]::WriteAllBytes($__nextermPath`,
		`[Convert]::FromBase64String('` + base64.StdEncoding.EncodeToString(data) + `')`,
	} {
		if !strings.Contains(script, fragment) {
			t.Errorf("write script missing %q: %s", fragment, script)
		}
	}
	if err := filesystem.WriteFile(context.Background(), path, make([]byte, MaxWriteBytes+1), false); err == nil {
		t.Fatal("oversized write succeeded")
	}
	if len(executor.scripts) != 1 {
		t.Fatal("oversized write reached the remote executor")
	}
	if err := filesystem.WriteFile(context.Background(), path, make([]byte, MaxWriteBytes), false); err != nil {
		t.Fatalf("write at documented limit failed: %v", err)
	}
}

func TestRelativeFileIOUsesResolvedFilesystemProviderPath(t *testing.T) {
	executor := &fakeExecutor{result: success(base64.StdEncoding.EncodeToString([]byte("content")))}
	filesystem := New(executor)
	if _, err := filesystem.ReadFile(context.Background(), "after-cwd.txt", 100); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.WriteFile(context.Background(), "after-cwd.txt", []byte("replacement"), true); err != nil {
		t.Fatal(err)
	}
	for i, script := range executor.scripts {
		resolve := strings.Index(script, "GetUnresolvedProviderPathFromPSPath('after-cwd.txt'")
		if resolve < 0 || !strings.Contains(script, "if ($__nextermProvider.Name -ne 'FileSystem')") {
			t.Fatalf("script %d does not resolve and validate the provider path: %s", i, script)
		}
		if strings.Contains(script, "[IO.File]::ReadAllBytes('after-cwd.txt'") || strings.Contains(script, "[IO.File]::WriteAllBytes('after-cwd.txt'") {
			t.Fatalf("script %d passes a relative path to .NET: %s", i, script)
		}
	}
	if !strings.Contains(executor.scripts[0], "[IO.File]::ReadAllBytes($f.FullName)") {
		t.Fatalf("relative read does not use the resolved file: %s", executor.scripts[0])
	}
	write := executor.scripts[1]
	if !strings.Contains(write, "[IO.File]::WriteAllBytes($__nextermPath") || !strings.Contains(write, "$__nextermBackup=$__nextermPath+'.nexterm-bak'") {
		t.Fatalf("relative write/backup does not share the resolved path: %s", write)
	}
}

func TestDirectoryAndMetadataOperations(t *testing.T) {
	executor := &fakeExecutor{handle: func(script string) (base.ExecResult, error) {
		switch {
		case strings.Contains(script, "Get-FileHash"):
			return success("A1B2C3\r\n"), nil
		case strings.Contains(script, "Test-Path"):
			return success("yes"), nil
		case strings.Contains(script, ".Length"):
			return success(" \r\n12345\r\n"), nil
		default:
			return success(""), nil
		}
	}}
	filesystem := New(executor)
	ctx := context.Background()
	if err := filesystem.Mkdir(ctx, `C:\a'b`); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Rename(ctx, "from", "to"); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Delete(ctx, `C:\a'b`, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(executor.scripts[2], `'C:\a''b' -Recurse -Force`) {
		t.Fatalf("recursive delete = %s", executor.scripts[2])
	}
	checksum, err := filesystem.Checksum(ctx, "file", "SHA-256")
	if err != nil || checksum != "a1b2c3" || !strings.Contains(executor.scripts[3], "-Algorithm SHA256") {
		t.Fatalf("checksum = %q, %v", checksum, err)
	}
	exists, err := filesystem.Exists(ctx, "file")
	if err != nil || !exists {
		t.Fatalf("exists = %v, %v", exists, err)
	}
	size, err := filesystem.Size(ctx, "file")
	if err != nil || size != 12345 {
		t.Fatalf("size = %d, %v", size, err)
	}
	if _, err := filesystem.Checksum(ctx, "file", "sha1"); err == nil {
		t.Fatal("unsupported checksum succeeded")
	}
}

func TestFailuresAndUnsupportedCapabilities(t *testing.T) {
	code := 9
	executor := &fakeExecutor{result: base.ExecResult{Stderr: "remote failure", ExitCode: &code}}
	filesystem := New(executor)
	if err := filesystem.Mkdir(context.Background(), "path"); err == nil || !strings.Contains(err.Error(), "remote failure") {
		t.Fatalf("nonzero status = %v", err)
	}
	executor.result = base.ExecResult{}
	if _, err := filesystem.Exists(context.Background(), "path"); !errors.Is(err, base.ErrExitStatusMissing) {
		t.Fatalf("missing status = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := filesystem.Chmod(ctx, "path", fs.FileMode(0)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled chmod = %v", err)
	}
	if err := filesystem.Chmod(context.Background(), "path", 0o755); !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("chmod = %v", err)
	}
	if _, err := filesystem.OpenRead(context.Background(), "path"); !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("OpenRead = %v", err)
	}
	if _, err := filesystem.OpenWrite(context.Background(), "path", false); !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("OpenWrite = %v", err)
	}
}
