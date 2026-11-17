package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/platform/psquote"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// shellTransport runs commands with /bin/sh in a scratch home, like an SSH exec channel to a POSIX host.
type shellTransport struct {
	home       string
	path       string
	generation uint64
	mu         sync.Mutex
	commands   []string
}

func (s *shellTransport) Kind() string       { return "ssh" }
func (s *shellTransport) Generation() uint64 { return s.generation }
func (s *shellTransport) Exec(ctx context.Context, command string, options base.ExecOptions) (base.ExecResult, error) {
	s.mu.Lock()
	s.commands = append(s.commands, command)
	s.mu.Unlock()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = s.home
	cmd.Env = []string{"HOME=" + s.home, "PATH=" + s.path, "LC_ALL=C.UTF-8"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := base.ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
		err = nil
	}
	result.ExitCode = &code
	return result, err
}
func (s *shellTransport) Ping(context.Context) (time.Duration, error) { return 0, nil }
func (s *shellTransport) IsAlive() bool                               { return true }
func (s *shellTransport) Close() error                                { return nil }
func (s *shellTransport) executed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// searchFixture is a project tree with a deep file, VCS and dependency directories, and a file whose name and
// content are shell metacharacters.
func searchFixture(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	project := filepath.Join(home, "proj")
	deep := "a/b/c/d/e/f/g/h/i/deep.go"
	writeTree(t, project, map[string]string{
		"main.go":                    "package main\n\nfunc main() {\n\tprintln(\"Hello\")\n}\n",
		"README.md":                  "# Title\nhello world\n",
		"pkg/util.go":                "package pkg\n// TODO: one\nfunc Util() {}\n// todo: two\n",
		deep:                         "package deep\n// TODO: deep\n",
		"node_modules/lib/index.js":  "// TODO: vendored\n",
		".git/config":                "TODO in git\n",
		"web/app.ts":                 "const x = 1; // TODO: ts\n",
		"weird/it's $(touch pwn).go": "package weird\n// '; touch pwn; ' $(touch pwn) `touch pwn`\n",
	})
	return home, project
}

func shellRegistry(t *testing.T, home string, withRG bool) (*Registry, *shellTransport) {
	t.Helper()
	searchPath := "/usr/local/bin:/usr/bin:/bin"
	if withRG {
		rg, err := exec.LookPath("rg")
		if err != nil {
			t.Skip("ripgrep (rg) is not installed")
		}
		searchPath = filepath.Dir(rg) + ":" + searchPath
	} else {
		for _, directory := range filepath.SplitList(searchPath) {
			if _, err := os.Stat(filepath.Join(directory, "rg")); err == nil {
				// Hide a system rg by searching with a PATH that has the other tools but no rg.
				shadow := t.TempDir()
				for _, tool := range []string{"sh", "grep", "find", "printf", "cat"} {
					if located, err := exec.LookPath(tool); err == nil {
						_ = os.Symlink(located, filepath.Join(shadow, tool))
					}
				}
				searchPath = shadow
				break
			}
		}
	}
	transport := &shellTransport{home: home, path: searchPath, generation: 1}
	registry := NewRegistry(Dependencies{Transport: func(context.Context, string) (base.Transport, error) { return transport, nil }})
	return registry, transport
}

func resultPaths(output Output) []string {
	var paths []string
	for _, line := range strings.Split(output.Text, "\n") {
		if line == "" || line == "无匹配" || strings.HasPrefix(line, "[") {
			continue
		}
		paths = append(paths, line)
	}
	return paths
}

func requireNoPwn(t *testing.T, home string) {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(home, "**", "pwn"))
	direct, _ := filepath.Glob(filepath.Join(home, "pwn"))
	project, _ := filepath.Glob(filepath.Join(home, "proj", "pwn"))
	if len(matches)+len(direct)+len(project) > 0 {
		t.Fatalf("a search argument ran as a command: %v %v %v", matches, direct, project)
	}
}

func TestSearchRealShellBackends(t *testing.T) {
	for _, backend := range []struct {
		name   string
		withRG bool
	}{{"grep-find", false}, {"ripgrep", true}} {
		t.Run(backend.name, func(t *testing.T) {
			home, _ := searchFixture(t)
			registry, transport := shellRegistry(t, home, backend.withRG)
			run := func(tool, args string) Output {
				t.Helper()
				return executeTool(registry, "j", tool, args)
			}

			// find_files: deep trees are found (no depth cap), skipped directories stay out by default.
			found := run(FindFilesTool, `{"path":"~/proj","pattern":"*.go"}`)
			if !found.OK {
				t.Fatalf("find_files = %+v", found)
			}
			paths := resultPaths(found)
			for _, want := range []string{"main.go", "pkg/util.go", "a/b/c/d/e/f/g/h/i/deep.go", "weird/it's $(touch pwn).go"} {
				if !strings.Contains(strings.Join(paths, "\n"), want) {
					t.Fatalf("find_files is missing %q: %v\n%s", want, paths, found.Text)
				}
			}
			requireContains(t, "find_files notes", found.ModelText, "[searched "+home+"/proj with ")
			if got := resultPaths(run(FindFilesTool, `{"path":"~/proj","pattern":"index.js"}`)); len(got) != 0 {
				t.Fatalf("node_modules was searched by default: %v", got)
			}
			if got := resultPaths(run(FindFilesTool, `{"path":"~/proj","pattern":"index.js","include_ignored":true}`)); len(got) != 1 || got[0] != "node_modules/lib/index.js" {
				t.Fatalf("include_ignored did not reach node_modules: %v", got)
			}
			if got := resultPaths(run(FindFilesTool, `{"path":"~/proj","pattern":"pkg/**/*.go"}`)); strings.Join(got, ",") != "pkg/util.go" {
				t.Fatalf("path glob = %v", got)
			}
			if got := resultPaths(run(FindFilesTool, `{"path":"~/proj","pattern":"*.{md,ts}"}`)); len(got) != 2 {
				t.Fatalf("brace glob = %v", got)
			}
			// Without path the search starts in the exec shell's home directory.
			if got := resultPaths(run(FindFilesTool, `{"pattern":"proj/main.go"}`)); strings.Join(got, ",") != "proj/main.go" {
				t.Fatalf("default path = %v", got)
			}

			// grep_files: default mode lists files, sorted by path, VCS and dependency directories skipped.
			files := run(GrepFilesTool, `{"path":"~/proj","pattern":"TODO"}`)
			if got := strings.Join(resultPaths(files), ","); got != "a/b/c/d/e/f/g/h/i/deep.go,pkg/util.go,web/app.ts" {
				t.Fatalf("files_with_matches = %q\n%s", got, files.Text)
			}
			if got := strings.Join(resultPaths(run(GrepFilesTool, `{"path":"~/proj","pattern":"todo","case_insensitive":true,"type":"go"}`)), ","); got != "a/b/c/d/e/f/g/h/i/deep.go,pkg/util.go" {
				t.Fatalf("case-insensitive type search = %q", got)
			}
			if got := strings.Join(resultPaths(run(GrepFilesTool, `{"path":"~/proj","pattern":"todo","case_insensitive":true,"output_mode":"count","glob":"pkg/*.go"}`)), ","); got != "pkg/util.go:2" {
				t.Fatalf("count = %q", got)
			}
			content := run(GrepFilesTool, `{"path":"~/proj","pattern":"Hello","output_mode":"content","context":1}`)
			if got := strings.Join(resultPaths(content), "\n"); got != "main.go-3-func main() {\nmain.go:4:\tprintln(\"Hello\")\nmain.go-5-}" {
				t.Fatalf("content with context = %q\n%s", got, content.Text)
			}
			noNumbers := run(GrepFilesTool, `{"path":"~/proj/main.go","pattern":"Hello","output_mode":"content","line_numbers":false}`)
			if got := strings.Join(resultPaths(noNumbers), "\n"); got != "main.go:\tprintln(\"Hello\")" {
				t.Fatalf("single file without line numbers = %q\n%s", got, noNumbers.Text)
			}

			// Paging reports what remains and how to continue.
			page := run(FindFilesTool, `{"path":"~/proj","pattern":"*","head_limit":2}`)
			if len(resultPaths(page)) != 2 || !page.Truncated {
				t.Fatalf("page = %+v", page)
			}
			requireContains(t, "paging note", page.ModelText, "[showing results 1-2 of ", "offset=2]")
			next := run(FindFilesTool, `{"path":"~/proj","pattern":"*","head_limit":2,"offset":2}`)
			if strings.Join(resultPaths(next), ",") == strings.Join(resultPaths(page), ",") {
				t.Fatalf("offset returned the same page: %v", resultPaths(next))
			}

			// Hostile arguments stay literals: a file whose name and content are shell syntax is found, nothing runs.
			hostile := run(GrepFilesTool, `{"path":"~/proj","pattern":"'; touch pwn; '","output_mode":"content"}`)
			if got := strings.Join(resultPaths(hostile), ","); !strings.Contains(got, "weird/it's $(touch pwn).go:2:") {
				t.Fatalf("hostile pattern = %q\n%s", got, hostile.Text)
			}
			for _, args := range []string{
				`{"path":"~/proj","pattern":"$(touch pwn)"}`,
				"{\"path\":\"~/proj\",\"pattern\":\"`touch pwn`\"}",
				`{"path":"~/proj","pattern":"--pre=touch"}`,
				`{"path":"~/proj","pattern":"-rf"}`,
				`{"path":"$(touch pwn)","pattern":"x"}`,
				`{"path":"~/proj; touch pwn","pattern":"x"}`,
				`{"path":"~/proj","pattern":"x","glob":"*'; touch pwn; '*"}`,
			} {
				result := run(GrepFilesTool, args)
				if result.OK && strings.Contains(result.Text, "touch pwn") && !strings.Contains(result.Text, "weird/") {
					t.Fatalf("%s: unexpected output %q", args, result.Text)
				}
			}
			for _, args := range []string{`{"path":"~/proj","pattern":"$(touch pwn)*"}`, `{"path":"~/proj'; touch pwn; '","pattern":"*"}`} {
				run(FindFilesTool, args)
			}
			requireNoPwn(t, home)
			for _, command := range transport.executed() {
				if ruling := guard.ClassifyCommand(command, nil); ruling.Risk != guard.Safe {
					t.Fatalf("generated command is not read-only (%s): %s", ruling.Reason, command)
				}
			}

			// A missing path and an invalid regex are failures, not empty results.
			if missing := run(GrepFilesTool, `{"path":"~/nope","pattern":"x"}`); missing.OK || !strings.Contains(missing.ModelText, "does not exist") {
				t.Fatalf("missing path = %+v", missing)
			}
			if notDir := run(FindFilesTool, `{"path":"~/proj/main.go","pattern":"*"}`); notDir.OK {
				t.Fatalf("find_files on a file = %+v", notDir)
			}
			if bad := run(GrepFilesTool, `{"path":"~/proj","pattern":"(unclosed"}`); bad.OK {
				t.Fatalf("invalid regex = %+v", bad)
			}
		})
	}
}

func TestSearchRealShellMultilineNeedsRipgrep(t *testing.T) {
	home, _ := searchFixture(t)
	registry, _ := shellRegistry(t, home, false)
	result := executeTool(registry, "j", GrepFilesTool, `{"path":"~/proj","pattern":"main\\(\\) \\{\\n","multiline":true}`)
	if result.OK || !strings.Contains(result.ModelText, "needs ripgrep") {
		t.Fatalf("multiline without rg = %+v", result)
	}
	if _, err := exec.LookPath("rg"); err != nil {
		return
	}
	registry, _ = shellRegistry(t, home, true)
	result = executeTool(registry, "j", GrepFilesTool, `{"path":"~/proj","pattern":"main\\(\\) \\{\\n\\s+println","multiline":true}`)
	if got := strings.Join(resultPaths(result), ","); got != "main.go" {
		t.Fatalf("multiline with rg = %q\n%s", got, result.Text)
	}
}

// A minimal grep and find (BusyBox-like: no --null, -P, -I, --exclude-dir, --include or -printf) still gives the
// same answers, filtered and parsed in Go.
func TestSearchMinimalPOSIXToolsParseAndFilter(t *testing.T) {
	home, project := searchFixture(t)
	transport := &shellTransport{home: home, path: "/usr/local/bin:/usr/bin:/bin", generation: 1}
	host := searchHost{platform: searchPlatformPOSIX}
	find, failure := normalizeFindArgs(FindFilesArgs{Pattern: "**/*.go", Path: project})
	if failure != nil {
		t.Fatal(failure.Text)
	}
	result := runPOSIXSearch(context.Background(), transport, host, find)
	if got := strings.Join(resultPaths(result), ","); got != "a/b/c/d/e/f/g/h/i/deep.go,main.go,pkg/util.go,weird/it's $(touch pwn).go" {
		t.Fatalf("plain find = %q\n%s", got, result.Text)
	}
	grep, failure := normalizeGrepArgs(GrepFilesArgs{Pattern: "TODO", Path: project, OutputMode: "content", Glob: "**/*.{go,ts}"})
	if failure != nil {
		t.Fatal(failure.Text)
	}
	result = runPOSIXSearch(context.Background(), transport, host, grep)
	if got := strings.Join(resultPaths(result), "\n"); got != "a/b/c/d/e/f/g/h/i/deep.go:2:// TODO: deep\npkg/util.go:2:// TODO: one\nweb/app.ts:1:const x = 1; // TODO: ts" {
		t.Fatalf("plain grep content = %q\n%s", got, result.Text)
	}
	requireContains(t, "backend note", result.ModelText, "with grep -E;")
	requireNoPwn(t, home)
}

func TestSearchHostProbeAndCache(t *testing.T) {
	probe := searchSentinel + " posix\nrg\ngrep-null\n"
	transport := &scriptedTransport{script: map[string]scriptedCommand{searchPOSIXProbe: {result: base.ExecResult{Stdout: probe, ExitCode: exitCode(0)}}}}
	registry := NewRegistry(Dependencies{Transport: func(context.Context, string) (base.Transport, error) { return transport, nil }})
	host, err := registry.searchHost(context.Background(), "s", transport)
	if err != nil || host.platform != searchPlatformPOSIX || !host.rg || !host.grepNull || host.grepPCRE {
		t.Fatalf("host = %+v, %v", host, err)
	}
	if _, err := registry.searchHost(context.Background(), "s", transport); err != nil || len(transport.executed()) != 1 {
		t.Fatalf("the probe was not cached: %v", transport.executed())
	}

	// A host whose exec shell is not POSIX but runs PowerShell is Windows OpenSSH.
	windows := &scriptedTransport{script: map[string]scriptedCommand{searchPowerShellProbe: {result: base.ExecResult{Stdout: searchSentinel + " powershell", ExitCode: exitCode(0)}}}}
	host, _ = registry.searchHost(context.Background(), "w", windows)
	if host.platform != searchPlatformWindows {
		t.Fatalf("windows host = %+v", host)
	}
	// Neither answers: no usable exec channel, so the file walk is used.
	silent := &scriptedTransport{}
	if host, _ = registry.searchHost(context.Background(), "n", silent); host.platform != searchPlatformNone {
		t.Fatalf("silent host = %+v", host)
	}
	// A transport without exec never probes again.
	unsupported := &fakeTransport{execErr: base.ErrUnsupported}
	if host, _ = registry.searchHost(context.Background(), "u", unsupported); host.platform != searchPlatformNone {
		t.Fatalf("unsupported host = %+v", host)
	}
	registry.searchHost(context.Background(), "u", unsupported)
	if unsupported.calls() != 1 {
		t.Fatalf("unsupported exec was probed %d times", unsupported.calls())
	}
}

type kindTransport struct {
	scriptedTransport
	kind string
}

func (k *kindTransport) Kind() string { return k.kind }

func TestSearchPowerShellScriptQuotesEveryInput(t *testing.T) {
	hostile := "it's ‘smart’ $(Remove-Item C:\\) \"x\" `n ; & | @' x"
	request, failure := normalizeGrepArgs(GrepFilesArgs{Pattern: hostile, Path: `C:\Users\o'neil\src`, Glob: "*.{cs,ps1}", Type: "cs", CaseInsensitive: true, OutputMode: "content", Context: 2})
	if failure != nil {
		t.Fatal(failure.Text)
	}
	command, failed := powerShellSearchScript(request)
	if failed != nil {
		t.Fatal(failed.Text)
	}
	if len(command) > 8191 {
		t.Fatalf("command is %d bytes, over the cmd.exe limit", len(command))
	}
	script, err := psquote.DecodeCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	firstLine, _, _ := strings.Cut(strings.TrimPrefix(script, searchPowerShellPrelude), "\n")
	values := map[string]string{}
	for _, assignment := range strings.Split(firstLine, ";$") {
		name, value, _ := strings.Cut(strings.TrimPrefix(assignment, "$"), "=")
		values[name] = value
	}
	for name, want := range map[string]string{"P": `C:\Users\o'neil\src`, "X": hostile, "M": "content"} {
		got, ok := psquote.Unquote(values[name])
		if !ok || got != want {
			t.Fatalf("$%s = %q (literal %v), want %q", name, values[name], ok, want)
		}
	}
	if values["I"] != "$true" || values["C"] != "2" {
		t.Fatalf("flags = %v", values)
	}
	glob, ok := psquote.Unquote(values["G"])
	if !ok || !regexp.MustCompile(glob).MatchString("a/b.ps1") || regexp.MustCompile(glob).MatchString("a/b.ps2") {
		t.Fatalf("glob regex = %q", values["G"])
	}

	home, _ := normalizeFindArgs(FindFilesArgs{Pattern: "*.log", Path: "~/logs"})
	command, _ = powerShellSearchScript(home)
	script, _ = psquote.DecodeCommand(command)
	if !strings.Contains(script, "$P=(Join-Path $HOME 'logs')") {
		t.Fatalf("home path = %q", script)
	}
	multiline, _ := normalizeGrepArgs(GrepFilesArgs{Pattern: "a", Multiline: true})
	if _, failed := powerShellSearchScript(multiline); failed == nil {
		t.Fatal("multiline was accepted for PowerShell")
	}
}

func TestSearchPowerShellOutputParsing(t *testing.T) {
	stdout := searchSentinel + " powershell\n" + searchSentinel + " root C:\\src\n" + searchSentinel + " backend PowerShell\n" +
		"b/Main.CS\x002-// before\n" + "b/Main.CS\x003:// TODO here\n" + "a/x.cs\x001:TODO first\n" + "NODE_MODULES/y.cs\x001:TODO vendored\n"
	transport := &kindTransport{kind: "winrm"}
	request, _ := normalizeGrepArgs(GrepFilesArgs{Pattern: "TODO", Path: `C:\src`, OutputMode: "content", Context: 1, Glob: "*.cs"})
	transport.script = map[string]scriptedCommand{}
	command, _ := powerShellSearchScript(request)
	transport.script[command] = scriptedCommand{result: base.ExecResult{Stdout: stdout, Stderr: "C:\\src\\locked: Access is denied\n", ExitCode: exitCode(0)}}
	registry := NewRegistry(Dependencies{Transport: func(context.Context, string) (base.Transport, error) { return transport, nil }})
	result := executeTool(registry, "j", GrepFilesTool, `{"path":"C:\\src","pattern":"TODO","output_mode":"content","context":1,"glob":"*.cs"}`)
	if got := strings.Join(resultPaths(result), "\n"); got != "a/x.cs:1:TODO first\n--\nb/Main.CS-2-// before\nb/Main.CS:3:// TODO here" {
		t.Fatalf("parsed = %q\n%+v", got, result)
	}
	requireContains(t, "notes", result.ModelText, `[searched C:\src with PowerShell; paths are relative to it]`, "[skipped: 1 paths could not be searched")
}

func TestSearchArgumentDefaultsAndValidation(t *testing.T) {
	request, failure := normalizeGrepArgs(GrepFilesArgs{Pattern: "x"})
	if failure != nil || request.mode != searchOutputFiles || request.headLimit != searchDefaultHeadLimit || !request.lineNumbers || request.context != 0 {
		t.Fatalf("grep defaults = %+v %v", request, failure)
	}
	request, _ = normalizeGrepArgs(GrepFilesArgs{Pattern: "x", OutputMode: "content"})
	if request.headLimit != searchDefaultContentHeadLimit {
		t.Fatalf("content default head_limit = %d", request.headLimit)
	}
	request, _ = normalizeFindArgs(FindFilesArgs{Pattern: "/etc/nginx/**/*.conf"})
	if request.path != "/etc/nginx" || request.pattern != "**/*.conf" {
		t.Fatalf("absolute glob = %q in %q", request.pattern, request.path)
	}
	request, _ = normalizeFindArgs(FindFilesArgs{Pattern: "/var/log/syslog"})
	if request.path != "/var/log" || request.pattern != "syslog" {
		t.Fatalf("absolute file = %q in %q", request.pattern, request.path)
	}
	for _, test := range []struct {
		name string
		find *FindFilesArgs
		grep *GrepFilesArgs
		want string
	}{
		{name: "find without pattern", find: &FindFilesArgs{Path: "/"}, want: "pattern cannot be empty"},
		{name: "grep without pattern", grep: &GrepFilesArgs{Path: "/"}, want: "pattern cannot be empty"},
		{name: "bad glob", find: &FindFilesArgs{Pattern: "[a"}, want: "invalid file glob"},
		{name: "unclosed brace", find: &FindFilesArgs{Pattern: "*.{go"}, want: "unclosed {"},
		{name: "bad mode", grep: &GrepFilesArgs{Pattern: "x", OutputMode: "lines"}, want: "output_mode must be"},
		{name: "context", grep: &GrepFilesArgs{Pattern: "x", Context: 11}, want: "context must be between 0 and 10"},
		{name: "type", grep: &GrepFilesArgs{Pattern: "x", Type: "cobol"}, want: "unsupported file type cobol; available types: c, conf, cpp"},
		{name: "head limit", grep: &GrepFilesArgs{Pattern: "x", HeadLimit: 5000}, want: "head_limit must be between 1 and 2000"},
		{name: "offset", find: &FindFilesArgs{Pattern: "*", Offset: -1}, want: "offset cannot be negative"},
		{name: "newline", grep: &GrepFilesArgs{Pattern: "a\nb"}, want: "cannot contain a newline"},
		{name: "glob outside path", grep: &GrepFilesArgs{Pattern: "x", Path: "/srv", Glob: "/etc/*.conf"}, want: "is not under path /srv"},
	} {
		var failure *Output
		if test.find != nil {
			_, failure = normalizeFindArgs(*test.find)
		} else {
			_, failure = normalizeGrepArgs(*test.grep)
		}
		if failure == nil || !strings.Contains(failure.ModelText, test.want) || !strings.HasPrefix(failure.Text, "工具参数无效: ") {
			t.Errorf("%s: failure = %+v, want %q", test.name, failure, test.want)
		}
	}
}

func TestResolveSearchPath(t *testing.T) {
	for _, test := range []struct{ requested, cwd, want string }{
		{"", "", "~"},
		{"", "/srv/app", "/srv/app"},
		{"src", "/srv/app", "/srv/app/src"},
		{"./src", "/srv/app/", "/srv/app/src"},
		{"/etc", "/srv/app", "/etc"},
		{"~/x", "/srv/app", "~/x"},
		{"src", "", "src"},
		{"src", `C:\work`, `C:\work\src`},
		{`D:\data`, `C:\work`, `D:\data`},
	} {
		if got := resolveSearchPath(test.requested, test.cwd); got != test.want {
			t.Errorf("resolveSearchPath(%q, %q) = %q, want %q", test.requested, test.cwd, got, test.want)
		}
	}
}

type cwdTerminal struct {
	*fakeTerminal
	detail TabDetail
}

func (c *cwdTerminal) TabDetail(context.Context, string) (TabDetail, error) { return c.detail, nil }

func TestSearchDefaultsToTerminalDirectory(t *testing.T) {
	home, project := searchFixture(t)
	transport := &shellTransport{home: home, path: "/usr/local/bin:/usr/bin:/bin", generation: 1}
	terminal := &cwdTerminal{fakeTerminal: &fakeTerminal{}, detail: TabDetail{TabID: "t", SessionID: "s", CWD: project + "/pkg"}}
	registry := NewRegistry(Dependencies{Terminal: terminal, Transport: func(context.Context, string) (base.Transport, error) { return transport, nil }})
	result := registry.Execute(context.Background(), "j", Scope{SessionID: "s", TabID: "t"}, Call{ID: "c", Name: FindFilesTool, Args: json.RawMessage(`{"pattern":"*.go"}`)}, nil)
	if got := strings.Join(resultPaths(result), ","); got != "util.go" {
		t.Fatalf("default directory search = %q\n%s", got, result.Text)
	}
	// A terminal of another session does not lend its directory.
	terminal.detail.SessionID = "other"
	result = registry.Execute(context.Background(), "j", Scope{SessionID: "s", TabID: "t"}, Call{ID: "c", Name: FindFilesTool, Args: json.RawMessage(`{"pattern":"util.go"}`)}, nil)
	if got := strings.Join(resultPaths(result), ","); got != "proj/pkg/util.go" {
		t.Fatalf("foreign terminal directory was used: %q", got)
	}
}

func TestSearchFilesAliasMapsToNewTools(t *testing.T) {
	files := searchTree()
	files.files["/src/main.go"] = []byte("package main\n// TODO: wire flags\n")
	registry := searchRegistry(files)
	for _, test := range []struct {
		args, want, tool string
	}{
		{`{"path":"/src","pattern":"*.go","by":"name"}`, "pkg/util.go", FindFilesTool},
		{`{"path":"/src","pattern":"TODO","by":"content"}`, "main.go:2:// TODO: wire flags", GrepFilesTool},
		{`{"path":"/src","pattern":"**/*.go"}`, "pkg/deep/inner.go", FindFilesTool},
		{`{"path":"/src","pattern":"TODO|FIXME"}`, "main.go:2:// TODO: wire flags", GrepFilesTool},
		{`{"path":"/src","pattern":"TODO"}`, "main.go:2:// TODO: wire flags", GrepFilesTool},
	} {
		result := executeTool(registry, "j", SearchFilesAlias, test.args)
		if !result.OK || !strings.Contains(result.Text, test.want) || !strings.Contains(result.ModelText, "search_files is deprecated and was run as "+test.tool) {
			t.Errorf("%s: %+v", test.args, result)
		}
	}
	if result := executeTool(registry, "j", SearchFilesAlias, `{"path":"/src","pattern":"x","by":"regex"}`); result.OK || !strings.Contains(result.ModelText, "by must be name or content") {
		t.Fatalf("invalid by = %+v", result)
	}
	for pattern, glob := range map[string]bool{"*.go": true, "src/**/*.ts": true, "nginx": false, "TODO.*fix": false, `\bmain\b`: false, "a|b": false, "conf.d/": true} {
		if looksLikeGlob(pattern) != glob {
			t.Errorf("looksLikeGlob(%q) = %v", pattern, !glob)
		}
	}
}

func TestSearchFilesAliasHiddenFromModelButCallable(t *testing.T) {
	for _, schema := range einoToolSchemas() {
		if schema.Name == SearchFilesAlias {
			t.Fatal("search_files is still offered to the model")
		}
	}
	files := searchTree()
	registry := searchRegistry(files)
	execution := &Execution{JobID: "j", Registry: registry, Scope: Scope{SessionID: "s"}}
	available, err := execution.Tools()
	if err != nil {
		t.Fatal(err)
	}
	names, _ := ToolNames(context.Background(), available)
	if !containsString(names, FindFilesTool) || !containsString(names, GrepFilesTool) || containsString(names, SearchFilesAlias) {
		t.Fatalf("tools = %v", names)
	}
	_, unknown := NewToolSafety(names)
	node, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{Tools: available, UnknownToolsHandler: execution.DeprecatedToolHandler(unknown)})
	if err != nil {
		t.Fatal(err)
	}
	call := func(name, args string) Output {
		t.Helper()
		messages, err := node.Invoke(context.Background(), schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Function: schema.FunctionCall{Name: name, Arguments: args}}}))
		if err != nil || len(messages) != 1 {
			t.Fatalf("invoke %s: %v %v", name, messages, err)
		}
		var output Output
		if err := json.Unmarshal([]byte(messages[0].Content), &output); err != nil {
			t.Fatal(err)
		}
		return output
	}
	if output := call(SearchFilesAlias, `{"path":"/src","pattern":"*.go","by":"name"}`); !output.OK || !strings.Contains(output.Text, "pkg/util.go") {
		t.Fatalf("alias call = %+v", output)
	}
	if output := call("ghost_tool", `{}`); output.OK || !strings.Contains(output.ModelText, "does not exist") {
		t.Fatalf("other unknown tools still go to the unknown-tool reply: %+v", output)
	}
}

func TestSplitSearchOutputSkipsStartupNoise(t *testing.T) {
	stdout := "Welcome to host\n" + searchSentinel + " posix\n" + searchSentinel + " root /srv\n" + searchSentinel + " backend find\n./a.go\n"
	header, data := splitSearchOutput(stdout, searchPOSIXSentinel)
	if !header.shell || header.root != "/srv" || header.backend != "find" || data != "./a.go\n" {
		t.Fatalf("header = %+v, data = %q", header, data)
	}
}

func TestSubagentAllowListMapsSearchFiles(t *testing.T) {
	execution := &Execution{Registry: searchRegistry(searchTree()), Scope: Scope{SessionID: "s"}, Subagents: &SubagentConfig{AllowedTools: []string{"read_file", SearchFilesAlias}}}
	if got := strings.Join(execution.subagentAllowedTools(), ","); got != "read_file,find_files,grep_files" {
		t.Fatalf("allowed = %q", got)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestSearchFinishPagingAndTruncation(t *testing.T) {
	request, _ := normalizeGrepArgs(GrepFilesArgs{Pattern: "x", OutputMode: "content", Context: 1, HeadLimit: 3, Offset: 1})
	records := []searchRecord{
		{path: "b.go", line: 1, match: true, text: "x"},
		{path: "a.go", line: 1, match: true, text: "x1"},
		{path: "a.go", line: 2, text: "ctx"},
		{path: "a.go", line: 2, match: true, text: "ctx"},
		{path: "a.go", line: 9, match: true, text: "x9"},
		{path: "node_modules/z.go", line: 1, match: true, text: "x"},
	}
	result := request.finish(searchResult{backend: "grep -E", root: "/r", records: records, incomplete: []string{"[输出达到上限]"}, incompleteModel: []string{"[hit limit]"}}, false)
	want := "a.go:2:ctx\n--\na.go:9:x9\n--\nb.go:1:x\n[已显示第 2-4 行，共 4 行]\n[输出达到上限]\n[目录：/r；路径相对于该目录；后端：grep -E]"
	if result.Text != want || !result.Truncated {
		t.Fatalf("text = %q, truncated %v", result.Text, result.Truncated)
	}
	beyond, _ := normalizeFindArgs(FindFilesArgs{Pattern: "*", Offset: 10})
	result = beyond.finish(searchResult{backend: "find", root: "/r", records: []searchRecord{{path: "a"}}}, false)
	requireContains(t, "past offset", result.ModelText, "offset=10 is past the 1 results")
	long, _ := normalizeGrepArgs(GrepFilesArgs{Pattern: "x", OutputMode: "content"})
	result = long.finish(searchResult{backend: "rg", root: "/r", records: []searchRecord{{path: "min.js", line: 1, match: true, text: strings.Repeat("y", 5000)}}}, false)
	if !strings.Contains(result.Text, "…[+4000]") {
		t.Fatalf("long line was not clipped: %d bytes", len(result.Text))
	}
	newest, _ := normalizeFindArgs(FindFilesArgs{Pattern: "*"})
	result = newest.finish(searchResult{backend: "find", root: "/r", records: []searchRecord{{path: "old", mtime: 1, hasTime: true}, {path: "new", mtime: 2, hasTime: true}}}, false)
	if got := strings.Join(resultPaths(result), ","); got != "new,old" || !strings.Contains(result.ModelText, "newest first") {
		t.Fatalf("mtime order = %q %q", got, result.ModelText)
	}
}

func TestGlobRegexpMatchesGlobSemantics(t *testing.T) {
	paths := []string{"main.go", "pkg/util.go", "pkg/deep/inner.go", "README.md", "web/app.ts", "web/app.tsx", "conf.d/a.conf", "x/conf.d/b.conf", ".env", "a[1].txt"}
	for _, pattern := range []string{"*.go", "**/*.go", "pkg/*.go", "pkg/**", "pkg/**/*.go", "*.{ts,tsx}", "conf.d/*.conf", "**/conf.d/*.conf", ".*", "a\\[1\\].txt", "[!m]*.go", "?EADME.md"} {
		matcher, err := compileGlob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		expression := regexp.MustCompile(matcher.globRegexp())
		for _, candidate := range paths {
			if got, want := expression.MatchString(candidate), matcher.match(candidate); got != want {
				t.Errorf("pattern %q path %q: regex %v, glob %v (regex %s)", pattern, candidate, got, want, expression)
			}
		}
	}
}

func TestDisplaySearchCall(t *testing.T) {
	for _, test := range []struct{ name, args, want string }{
		{GrepFilesTool, `{"pattern":"TODO","path":"/src","glob":"*.go","case_insensitive":true,"output_mode":"content","context":2}`, `grep_files "TODO" in /src (glob *.go, -i, content, -C 2)`},
		{FindFilesTool, `{"pattern":"**/*.conf"}`, `find_files "**/*.conf"`},
		{SearchFilesAlias, `{"pattern":"*.go","path":"/src","by":"name"}`, `search_files name "*.go" in /src`},
	} {
		if got := DisplayCall(Call{Name: test.name, Args: json.RawMessage(test.args)}); got != test.want {
			t.Errorf("DisplayCall(%s) = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestSearchToolsAreReadOnlyAndPlanAllowed(t *testing.T) {
	for _, name := range []string{FindFilesTool, GrepFilesTool, SearchFilesAlias} {
		if ruling := guard.ClassifyTool(name, json.RawMessage(`{"pattern":"x"}`), guard.Config{}); ruling.Risk != guard.Safe {
			t.Errorf("%s ruling = %+v", name, ruling)
		}
		if !PlanAllowed(name) {
			t.Errorf("%s is not allowed in plan mode", name)
		}
	}
	types := map[string]bool{}
	for name := range searchFileTypes {
		types[name] = true
	}
	for _, schema := range einoToolSchemas() {
		if schema.Name != GrepFilesTool {
			continue
		}
		property := schema.Parameters["properties"].(map[string]any)["type"].(map[string]any)
		enum, _ := property["enum"].([]any)
		if len(enum) != len(types) {
			t.Fatalf("type enum %v does not match the type table", enum)
		}
		for _, value := range enum {
			if !types[value.(string)] {
				t.Fatalf("type enum has %v, which the table lacks", value)
			}
		}
	}
}
