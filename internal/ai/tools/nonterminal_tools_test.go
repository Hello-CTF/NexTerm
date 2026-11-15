package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/outcome"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type scriptedCommand struct {
	result base.ExecResult
	err    error
	// waitDeadline makes the command run until its context ends, like a command waiting on a prompt or a lock.
	waitDeadline bool
}

type scriptedTransport struct {
	mu     sync.Mutex
	script map[string]scriptedCommand
	calls  []string
}

func (s *scriptedTransport) Kind() string       { return "fake" }
func (s *scriptedTransport) Generation() uint64 { return 1 }
func (s *scriptedTransport) Exec(ctx context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
	s.mu.Lock()
	s.calls = append(s.calls, command)
	step := s.script[command]
	s.mu.Unlock()
	if step.waitDeadline {
		<-ctx.Done()
		return step.result, ctx.Err()
	}
	return step.result, step.err
}
func (s *scriptedTransport) Ping(context.Context) (time.Duration, error) { return 0, nil }
func (s *scriptedTransport) IsAlive() bool                               { return true }
func (s *scriptedTransport) Close() error                                { return nil }
func (s *scriptedTransport) executed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func exitCode(code int) *int { return &code }

func scriptedRegistry(transport base.Transport) *Registry {
	return NewRegistry(Dependencies{Transport: func(context.Context, string) (base.Transport, error) { return transport, nil }})
}

func execArgs(t *testing.T, args ExecCommandsArgs) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func requireContains(t *testing.T, label, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Fatalf("%s is missing %q:\n%s", label, want, text)
		}
	}
}

func TestExecCommandsTimeoutKeepsOutputAndStopsRemainingCommands(t *testing.T) {
	transport := &scriptedTransport{script: map[string]scriptedCommand{
		"echo one": {result: base.ExecResult{Stdout: "one", ExitCode: exitCode(0)}},
		// The SSH channel reports its own deadline with whatever the command printed before it.
		"sudo apt-get update": {result: base.ExecResult{Stdout: "Reading package lists...", Stderr: "[sudo] password for dev:"}, err: context.DeadlineExceeded},
	}}
	registry := scriptedRegistry(transport)
	commands := []string{"echo one", "sudo apt-get update", "echo three", "echo four"}
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s"}, Call{ID: "c", Name: "exec_commands", Args: execArgs(t, ExecCommandsArgs{Commands: commands})}, nil)
	if result.OK || !result.ExitUnknown {
		t.Fatalf("timed-out run must be a failed result with an unknown exit: %+v", result)
	}
	requireContains(t, "display text", result.Text, "$ echo one\none", "Reading package lists...", "[sudo] password for dev:",
		"[超时（60 秒），命令可能仍在运行，退出码未知]", "$ echo three\n[未执行", "$ echo four\n[未执行", "第 2 条命令超过 60 秒超时")
	requireContains(t, "model text", result.ModelText, "one", "Reading package lists...",
		"[timeout after 60s; may still be running; exit code unknown]", "$ echo three\n[not run: an earlier command timed out]",
		"command 2 hit the 60s timeout and may still be running; the 2 commands after it were not run")
	if strings.Contains(result.ModelText, "context deadline exceeded") {
		t.Fatalf("model text still carries the raw deadline error: %q", result.ModelText)
	}
	if got := transport.executed(); len(got) != 2 {
		t.Fatalf("commands after the timeout were started: %v", got)
	}
}

func TestExecCommandsPerCommandDeadlineKeepsPartialOutput(t *testing.T) {
	transport := &scriptedTransport{script: map[string]scriptedCommand{
		"tail -f /var/log/syslog": {result: base.ExecResult{Stdout: "line before deadline"}, waitDeadline: true},
	}}
	registry := scriptedRegistry(transport)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s"}, Call{ID: "c", Name: "exec_commands", Args: execArgs(t, ExecCommandsArgs{Commands: []string{"tail -f /var/log/syslog"}, Timeout: 1})}, nil)
	if result.OK || !result.ExitUnknown {
		t.Fatalf("deadline result = %+v", result)
	}
	requireContains(t, "display text", result.Text, "line before deadline", "[超时（1 秒），命令可能仍在运行，退出码未知]")
}

func TestExecCommandsReportsFirstNonZeroExitAndRecordsFailure(t *testing.T) {
	_, ledger, _ := outcomeFixture(t)
	transport := &scriptedTransport{script: map[string]scriptedCommand{
		"true":   {result: base.ExecResult{ExitCode: exitCode(0)}},
		"false":  {result: base.ExecResult{ExitCode: exitCode(3)}},
		"exit 2": {result: base.ExecResult{ExitCode: exitCode(2)}},
	}}
	registry := scriptedRegistry(transport)
	registry.deps.Outcome = ledger
	call := Call{ID: "c", Name: "exec_commands", Args: execArgs(t, ExecCommandsArgs{Commands: []string{"true", "false", "exit 2", "true"}}), AuthorizationID: "guard:test"}
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s"}, call, nil)
	if !result.OK || result.ExitCode != 3 {
		t.Fatalf("exit code = %d, want the first non-zero exit 3: %+v", result.ExitCode, result)
	}
	requireContains(t, "display text", result.Text, "第 2 条命令最先以非零退出码 3 结束")
	requireContains(t, "model text", result.ModelText, "command 2 was the first to exit non-zero (exit code 3)")
	if got := transport.executed(); len(got) != 4 {
		t.Fatalf("a failing command stopped the later ones: %v", got)
	}
	record, err := ledger.Get(context.Background(), OutcomeKey("job", "c"))
	if err != nil {
		t.Fatal(err)
	}
	if record.Outcome != outcome.OutcomeFailed || record.Result.ExitCode == nil || *record.Result.ExitCode != 3 {
		t.Fatalf("recorded outcome = %s exit=%v, want failed with exit 3", record.Outcome, record.Result.ExitCode)
	}

	// A run whose last command succeeds after a failure is still failed.
	call = Call{ID: "c2", Name: "exec_commands", Args: execArgs(t, ExecCommandsArgs{Commands: []string{"false", "true"}}), AuthorizationID: "guard:test"}
	if result := registry.Execute(context.Background(), "job", Scope{SessionID: "s"}, call, nil); result.ExitCode != 3 {
		t.Fatalf("[false true] exit code = %d", result.ExitCode)
	}
}

func TestExecCommandsDescriptionStatesSeparateShells(t *testing.T) {
	description := einoDescriptions["exec_commands"]
	requireContains(t, "exec_commands description", description, "do not carry over", "&&", "first non-zero exit code", "remaining commands are not run")
	if strings.Contains(description, "in the current session (") {
		t.Fatalf("description still implies one shared session: %q", description)
	}
}

func multiHostRegistry(batches func(execbatch.Request) execbatch.Batch, calls *[]string) *Registry {
	return NewRegistry(Dependencies{
		ResolveExecTargets: func(context.Context, []string, []string) (ExecTargetSet, error) {
			return ExecTargetSet{SessionIDs: []string{"session-a", "session-b"}}, nil
		},
		ExecBatch: func(_ context.Context, request execbatch.Request) (execbatch.Batch, error) {
			*calls = append(*calls, request.Command)
			return batches(request), nil
		},
	})
}

func TestExecCommandsMultiHostTimeoutStopsRemainingCommands(t *testing.T) {
	var calls []string
	registry := multiHostRegistry(func(request execbatch.Request) execbatch.Batch {
		success := func(asset, session, stdout string) execbatch.Result {
			return execbatch.Result{AssetID: asset, SessionID: session, Status: execbatch.StatusSuccess, Stdout: stdout, ExitCode: exitCode(0)}
		}
		if request.Command == "apt-get install -y nginx" {
			return execbatch.Batch{Results: []execbatch.Result{
				{AssetID: "asset-a", SessionID: "session-a", Status: execbatch.StatusTimeout, Stdout: "Waiting for cache lock", Error: "context deadline exceeded"},
				success("asset-b", "session-b", "installed"),
			}}
		}
		return execbatch.Batch{Results: []execbatch.Result{success("asset-a", "session-a", "a-ok"), success("asset-b", "session-b", "b-ok")}}
	}, &calls)
	args := json.RawMessage(`{"commands":["uptime","apt-get install -y nginx","systemctl restart nginx"],"asset_ids":["asset-a","asset-b"]}`)
	result := registry.Execute(context.Background(), "job", Scope{}, Call{ID: "batch", Name: "exec_commands", Args: args}, nil)
	if result.OK || !result.ExitUnknown || result.ExitCode != 1 {
		t.Fatalf("multi-host timeout result = %+v", result)
	}
	requireContains(t, "display text", result.Text, "a-ok", "Waiting for cache lock", "installed",
		"[超时（60 秒），命令可能仍在运行，退出码未知]", "$ systemctl restart nginx\n[未执行", "第 2 条命令在 asset-a 上超过 60 秒超时")
	requireContains(t, "model text", result.ModelText, "Waiting for cache lock", "[timeout after 60s; may still be running; exit code unknown]",
		"command 2 hit the 60s timeout on asset-a and may still be running; the 1 commands after it were not run on any host")
	if len(calls) != 2 {
		t.Fatalf("commands after the timeout were sent: %v", calls)
	}
}

func TestExecCommandsMultiHostReportsFirstNonZeroExit(t *testing.T) {
	var calls []string
	codes := map[string]int{"first": 2, "second": 5}
	registry := multiHostRegistry(func(request execbatch.Request) execbatch.Batch {
		code := codes[request.Command]
		return execbatch.Batch{Results: []execbatch.Result{
			{AssetID: "asset-a", SessionID: "session-a", Status: execbatch.StatusSuccess, ExitCode: exitCode(0)},
			{AssetID: "asset-b", SessionID: "session-b", Status: execbatch.StatusExitError, ExitCode: exitCode(code)},
		}}
	}, &calls)
	args := json.RawMessage(`{"commands":["first","second"],"asset_ids":["asset-a","asset-b"]}`)
	result := registry.Execute(context.Background(), "job", Scope{}, Call{ID: "batch", Name: "exec_commands", Args: args}, nil)
	if !result.OK || result.ExitCode != 2 {
		t.Fatalf("multi-host exit code = %d, want the first non-zero exit 2: %+v", result.ExitCode, result)
	}
	requireContains(t, "display text", result.Text, "第 1 条命令在 asset_id=asset-b 上最先以非零退出码 2 结束")
}

func numberedLines(count int) string {
	var builder strings.Builder
	for line := 1; line <= count; line++ {
		fmt.Fprintf(&builder, "line %d\n", line)
	}
	return builder.String()
}

func TestReadFileLineRanges(t *testing.T) {
	files := newFakeFS()
	files.files["/big"] = []byte(numberedLines(1000))
	files.files["/small"] = []byte("alpha\nbeta")
	registry, _ := fileRegistry(files)
	read := func(args string) Output {
		return executeTool(registry, "j", "read_file", args)
	}

	small := read(`{"path":"/small"}`)
	if !small.OK || small.Text != "alpha\nbeta" || small.ModelText != "" || small.Truncated {
		t.Fatalf("a whole small file must read unchanged: %+v", small)
	}

	first := read(`{"path":"/big"}`)
	if !first.OK || !first.Truncated || !strings.HasPrefix(first.Text, "line 1\n") || strings.Contains(first.Text, "line 401\n") {
		t.Fatalf("default read = %+v", first)
	}
	requireContains(t, "default read", first.Text, "line 400\n", "[显示第 1-400 行，共 1000 行]", "offset=401 limit=400")
	requireContains(t, "default read model text", first.ModelText, "[showing lines 1-400 of 1000]", "call read_file with offset=401 limit=400")

	next := read(`{"path":"/big","offset":401,"limit":400}`)
	if !next.OK || !next.Truncated || !strings.HasPrefix(next.Text, "line 401\n") {
		t.Fatalf("second range = %+v", next)
	}
	requireContains(t, "second range", next.ModelText, "[showing lines 401-800 of 1000]", "offset=801")

	last := read(`{"path":"/big","offset":801,"limit":400}`)
	if !last.OK || last.Truncated || !strings.HasPrefix(last.Text, "line 801\n") || strings.Contains(last.ModelText, "to continue") {
		t.Fatalf("last range = %+v", last)
	}
	requireContains(t, "last range", last.ModelText, "line 1000\n", "[showing lines 801-1000 of 1000]")

	past := read(`{"path":"/big","offset":1001}`)
	if !past.OK || !strings.Contains(past.ModelText, "the file has 1000 lines; offset=1001 is past the end") {
		t.Fatalf("offset past the end = %+v", past)
	}

	if invalid := read(`{"path":"/big","limit":2001}`); invalid.OK {
		t.Fatalf("limit over the maximum was accepted: %+v", invalid)
	} else if projected := provider.ProjectModelOutput("read_file", false, invalid.Text, false, false); !strings.Contains(projected, "limit must be between 1 and 2000 lines") {
		t.Fatalf("invalid range error is not translated for the model: %q", projected)
	}

	// The byte cap ends a range at a line boundary and names where to continue.
	files.files["/wide"] = []byte(strings.Repeat(strings.Repeat("w", 20<<10)+"\n", 10))
	wide := read(`{"path":"/wide","max_bytes":1048576}`)
	if !wide.OK || !wide.Truncated || len(wide.Text) > MaxOutputBytes+200 {
		t.Fatalf("wide read = ok %v truncated %v len %d", wide.OK, wide.Truncated, len(wide.Text))
	}
	requireContains(t, "wide read", wide.ModelText, "[showing lines 1-5 of 10]", "offset=6")

	// A ranged read still records the whole file's version, so a write after it is accepted.
	read(`{"path":"/big","offset":900,"limit":5}`)
	edit := Call{ID: "e", Name: "edit_file", Args: json.RawMessage(`{"path":"/big","old_string":"line 1000\n","new_string":"last\n"}`)}
	preparation, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, edit)
	if err != nil {
		t.Fatalf("edit after a ranged read: %v", err)
	}
	if result := registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, edit, preparation); !result.OK {
		t.Fatalf("edit after a ranged read = %+v", result)
	}
}

// searchFS extends fakeFS with per-path list failures and blocking.
type searchFS struct {
	*fakeFS
	listErr   map[string]error
	blockList map[string]bool
}

func (s *searchFS) List(ctx context.Context, name string) ([]base.FileEntry, error) {
	if s.blockList[name] {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err := s.listErr[name]; err != nil {
		return nil, err
	}
	return s.fakeFS.List(ctx, name)
}

func searchRegistry(files base.FileSystem) *Registry {
	transport := &fileTransport{files: files}
	return NewRegistry(Dependencies{Transport: func(context.Context, string) (base.Transport, error) { return transport, nil }})
}

type fileTransport struct {
	scriptedTransport
	files base.FileSystem
}

func (f *fileTransport) FileSystem(context.Context) (base.FileSystem, error) { return f.files, nil }

func searchTree() *fakeFS {
	files := newFakeFS()
	files.dirs["/src"] = []base.FileEntry{
		{Name: "main.go", Path: "/src/main.go", Kind: base.FileFile},
		{Name: "README.md", Path: "/src/README.md", Kind: base.FileFile},
		{Name: "pkg", Path: "/src/pkg", Kind: base.FileDirectory},
	}
	files.dirs["/src/pkg"] = []base.FileEntry{
		{Name: "util.go", Path: "/src/pkg/util.go", Kind: base.FileFile},
		{Name: "deep", Path: "/src/pkg/deep", Kind: base.FileDirectory},
	}
	files.dirs["/src/pkg/deep"] = []base.FileEntry{{Name: "inner.go", Path: "/src/pkg/deep/inner.go", Kind: base.FileFile}}
	return files
}

func searchResults(t *testing.T, registry *Registry, path, pattern string) []string {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"path": path, "pattern": pattern, "by": "name"})
	result := executeTool(registry, "j", "search_files", string(args))
	if !result.OK {
		t.Fatalf("search %q failed: %+v", pattern, result)
	}
	if result.Text == "无匹配" {
		return nil
	}
	return strings.Split(result.Text, "\n")
}

func TestSearchFilesMatchesPathGlobs(t *testing.T) {
	registry := searchRegistry(searchTree())
	for _, test := range []struct {
		pattern string
		want    []string
	}{
		{"*.go", []string{"/src/main.go", "/src/pkg/deep/inner.go", "/src/pkg/util.go"}},
		{"**/*.go", []string{"/src/main.go", "/src/pkg/deep/inner.go", "/src/pkg/util.go"}},
		{"pkg/*.go", []string{"/src/pkg/util.go"}},
		{"pkg/**/*.go", []string{"/src/pkg/deep/inner.go", "/src/pkg/util.go"}},
		{"./pkg/deep/*.go", []string{"/src/pkg/deep/inner.go"}},
		{"/src/pkg/**", []string{"/src/pkg/deep/inner.go", "/src/pkg/util.go"}},
		{"**/README.md", []string{"/src/README.md"}},
		{"deep/*.go", nil},
	} {
		got := searchResults(t, registry, "/src", test.pattern)
		if strings.Join(got, ",") != strings.Join(test.want, ",") {
			t.Fatalf("pattern %q matched %v, want %v", test.pattern, got, test.want)
		}
	}
	if result := executeTool(registry, "j", "search_files", `{"path":"/src","pattern":"pkg/[","by":"name"}`); result.OK {
		t.Fatalf("an invalid path glob was accepted: %+v", result)
	}
}

func TestSearchFilesReportsSkippedFiles(t *testing.T) {
	files := searchTree()
	files.dirs["/src"] = append(files.dirs["/src"],
		base.FileEntry{Name: "huge.log", Path: "/src/huge.log", Kind: base.FileFile, Size: 2 << 20},
		base.FileEntry{Name: "image.bin", Path: "/src/image.bin", Kind: base.FileFile},
		base.FileEntry{Name: "locked.txt", Path: "/src/locked.txt", Kind: base.FileFile},
		base.FileEntry{Name: "private", Path: "/src/private", Kind: base.FileDirectory},
	)
	files.files["/src/main.go"] = []byte("package main\n// TODO: wire flags\n")
	files.files["/src/image.bin"] = []byte{0xff, 0xfe, 0x00, 'T', 'O', 'D', 'O'}
	registry := searchRegistry(&searchFS{fakeFS: files, listErr: map[string]error{"/src/private": errors.New("permission denied")}})
	result := executeTool(registry, "j", "search_files", `{"path":"/src","pattern":"TODO","by":"content"}`)
	if !result.OK {
		t.Fatalf("search = %+v", result)
	}
	requireContains(t, "display text", result.Text, "/src/main.go:2:// TODO: wire flags",
		"[已跳过：1 个超过 1 MiB 的文件，1 个非 UTF-8 文件，", "无法读取的文件", "1 个无法列出的目录]")
	requireContains(t, "model text", result.ModelText, "/src/main.go:2:// TODO: wire flags", "[skipped: 1 files over 1 MiB, 1 non-UTF-8 files, ",
		"unreadable files", "1 directories that could not be listed]")

	// A root that cannot be listed is still an error.
	failing := searchRegistry(&searchFS{fakeFS: files, listErr: map[string]error{"/src": errors.New("permission denied")}})
	if result := executeTool(failing, "j", "search_files", `{"path":"/src","pattern":"*","by":"name"}`); result.OK {
		t.Fatalf("unlistable root = %+v", result)
	}
}

func TestSearchFilesTimeoutReturnsPartialResults(t *testing.T) {
	previous := searchFilesTimeout
	searchFilesTimeout = 50 * time.Millisecond
	t.Cleanup(func() { searchFilesTimeout = previous })
	files := searchTree()
	registry := searchRegistry(&searchFS{fakeFS: files, blockList: map[string]bool{"/src/pkg/deep": true}})
	result := executeTool(registry, "j", "search_files", `{"path":"/src","pattern":"*.go","by":"name"}`)
	if !result.OK || !result.Truncated {
		t.Fatalf("timed-out search = %+v", result)
	}
	requireContains(t, "display text", result.Text, "/src/main.go", "[搜索超时", "以上为部分结果]")
	requireContains(t, "model text", result.ModelText, "/src/main.go", "search timed out")
	if strings.Contains(result.ModelText, "context deadline exceeded") {
		t.Fatalf("timeout surfaced as a raw error: %q", result.ModelText)
	}
}

// blockingFS never answers: every call waits for its context, like a file system on a hung mount.
type blockingFS struct{ *fakeFS }

func (blockingFS) ReadFile(ctx context.Context, _ string, _ int64) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (blockingFS) List(ctx context.Context, _ string) ([]base.FileEntry, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (blockingFS) Exists(ctx context.Context, _ string) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

type blockingDatabase struct{ fakeDatabase }

func (blockingDatabase) Tables(ctx context.Context, _, _ string) ([]string, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (blockingDatabase) Describe(ctx context.Context, _, _, _ string) (db.TableDescribe, error) {
	<-ctx.Done()
	return db.TableDescribe{}, ctx.Err()
}

func TestFileAndDatabaseMetadataToolsTimeOut(t *testing.T) {
	previousFile, previousDB := fileToolTimeout, dbMetadataTimeout
	fileToolTimeout, dbMetadataTimeout = 20*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { fileToolTimeout, dbMetadataTimeout = previousFile, previousDB })
	files := searchRegistry(blockingFS{newFakeFS()})
	database := NewRegistry(Dependencies{Database: &blockingDatabase{}})
	for _, test := range []struct {
		registry *Registry
		scope    Scope
		tool     string
		args     string
	}{
		{files, Scope{SessionID: "s"}, "read_file", `{"path":"/hung/file"}`},
		{files, Scope{SessionID: "s"}, "list_dir", `{"path":"/hung"}`},
		{database, Scope{ConnID: "c"}, "db_list_tables", `{}`},
		{database, Scope{ConnID: "c"}, "db_describe", `{"table":"users"}`},
	} {
		done := make(chan Output, 1)
		go func() {
			done <- test.registry.Execute(context.Background(), "j", test.scope, Call{ID: test.tool, Name: test.tool, Args: json.RawMessage(test.args)}, nil)
		}()
		select {
		case result := <-done:
			if result.OK || !strings.Contains(result.Text, test.tool+" 超时") {
				t.Fatalf("%s timeout result = %+v", test.tool, result)
			}
			if projected := provider.ProjectModelOutput(test.tool, false, result.Text, false, false); !strings.Contains(projected, test.tool+" timed out after") {
				t.Fatalf("%s timeout is not translated for the model: %q", test.tool, projected)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not stop at its timeout", test.tool)
		}
	}
}

// pagedRedis serves SCAN pages keyed by the requested cursor.
type pagedRedis struct {
	fakeDatabase
	mu      sync.Mutex
	pages   map[uint64]db.RedisScanResult
	cursors []uint64
}

func (p *pagedRedis) RedisScan(_ context.Context, _ string, cursor uint64, _ string, _ uint64) (db.RedisScanResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cursors = append(p.cursors, cursor)
	return p.pages[cursor], nil
}

func TestRedisScanFollowsCursorUntilComplete(t *testing.T) {
	pages := &pagedRedis{pages: map[uint64]db.RedisScanResult{
		0:  {Cursor: 17},
		17: {Cursor: 42, Keys: []string{"session:1"}},
		42: {Cursor: 0, Keys: []string{"session:2", "session:1"}},
	}}
	registry := NewRegistry(Dependencies{Database: pages})
	result := registry.Execute(context.Background(), "j", Scope{ConnID: "c"}, Call{ID: "s", Name: "redis_scan", Args: json.RawMessage(`{"pattern":"session:*"}`)}, nil)
	if !result.OK || !strings.HasPrefix(result.Text, "cursor=0 keys:\nsession:1\nsession:2\n") {
		t.Fatalf("complete scan = %+v", result)
	}
	requireContains(t, "model text", result.ModelText, "scan complete (this call ran 3 SCAN iterations)")

	// Continuing from a returned cursor starts there.
	result = registry.Execute(context.Background(), "j", Scope{ConnID: "c"}, Call{ID: "s2", Name: "redis_scan", Args: json.RawMessage(`{"pattern":"session:*","cursor":42}`)}, nil)
	if !result.OK || !strings.HasPrefix(result.Text, "cursor=0 keys:\nsession:2\nsession:1\n") {
		t.Fatalf("continued scan = %+v", result)
	}
}

func TestRedisScanReportsUnfinishedCursor(t *testing.T) {
	keys := make([]string, redisScanKeyBudget)
	for index := range keys {
		keys[index] = fmt.Sprintf("key:%d", index)
	}
	pages := &pagedRedis{pages: map[uint64]db.RedisScanResult{0: {Cursor: 9, Keys: keys}, 9: {Cursor: 0, Keys: []string{"late"}}}}
	registry := NewRegistry(Dependencies{Database: pages})
	result := registry.Execute(context.Background(), "j", Scope{ConnID: "c"}, Call{ID: "s", Name: "redis_scan", Args: json.RawMessage(`{}`)}, nil)
	if !result.OK || !strings.HasPrefix(result.Text, "cursor=9 keys:\n") {
		t.Fatalf("budgeted scan = %+v", result)
	}
	requireContains(t, "model text", result.ModelText, "cursor≠0: more keys may match", "call redis_scan with cursor=9")
	requireContains(t, "display text", result.Text, "cursor≠0：可能还有更多匹配的键", "cursor=9]")
}

func TestOperationIDsResolveThroughOperationResult(t *testing.T) {
	storage, ledger, _ := outcomeFixture(t)
	terminal := &fakeTerminal{screen: Screen{Text: "$ "}}
	transport := &scriptedTransport{script: map[string]scriptedCommand{"false": {result: base.ExecResult{ExitCode: exitCode(3)}}}}
	registry := NewRegistry(Dependencies{
		Terminal:        terminal,
		Transport:       func(context.Context, string) (base.Transport, error) { return transport, nil },
		Outcome:         ledger,
		DurableOutcomes: DurableStoreOutcomes(storage),
	})
	scope := Scope{SessionID: "s", TabID: "t"}
	lookup := func(operation string) Output {
		t.Helper()
		if operation == "" {
			t.Fatal("tool returned no operation_id")
		}
		return registry.Execute(context.Background(), "job", scope, Call{ID: "q-" + operation, Name: "operation_result", Args: json.RawMessage(`{"operation_id":"` + operation + `"}`)}, nil)
	}

	sent := registry.Execute(context.Background(), "job", scope, Call{ID: "keys", Name: "send_keys", Args: json.RawMessage(`{"keys":"ls"}`), AuthorizationID: "guard:test"}, nil)
	if !sent.OK {
		t.Fatalf("send_keys = %+v", sent)
	}
	if found := lookup(sent.OperationID); !found.OK || !strings.Contains(found.Text, "state=succeeded kind=send_keys") {
		t.Fatalf("send_keys outcome = %+v", found)
	}

	// terminal_create without an asset and outside the recorded path.
	created := registry.Execute(context.Background(), "job", scope, Call{ID: "create", Name: "terminal_create", Args: json.RawMessage(`{"title":"build"}`)}, nil)
	if !created.OK {
		t.Fatalf("terminal_create = %+v", created)
	}
	if found := lookup(created.OperationID); !found.OK || !strings.Contains(found.Text, "kind=terminal_create") {
		t.Fatalf("terminal_create outcome = %+v", found)
	}

	// A replayed mutation returns an operation ID that answers with the original effect's result.
	call := Call{ID: "exec", Name: "exec_commands", Args: execArgs(t, ExecCommandsArgs{Commands: []string{"false"}}), AuthorizationID: "guard:test"}
	if first := registry.Execute(context.Background(), "job", scope, call, nil); first.ExitCode != 3 {
		t.Fatalf("first exec = %+v", first)
	}
	replayed := registry.Execute(context.Background(), "job", scope, call, nil)
	if !strings.Contains(replayed.Text, "调用已执行过") {
		t.Fatalf("second exec did not replay: %+v", replayed)
	}
	if found := lookup(replayed.OperationID); !found.OK || !strings.Contains(found.Text, "state=failed kind=exec_commands") || !strings.Contains(found.Text, `"exit_code":3`) {
		t.Fatalf("replayed exec outcome = %+v", found)
	}
	if got := transport.executed(); len(got) != 1 {
		t.Fatalf("replay executed again: %v", got)
	}

	missing := lookup("op-never-recorded")
	if missing.OK {
		t.Fatalf("unknown operation = %+v", missing)
	}
	projected := provider.ProjectModelOutput("operation_result", false, missing.Text, false, false)
	requireContains(t, "unknown operation model text", projected, "has no record", "does not mean it never ran", "check the actual state")
}
