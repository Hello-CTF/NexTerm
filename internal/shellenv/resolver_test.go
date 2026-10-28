//go:build unix

package shellenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeScript(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func testResolver(shell string, environ []string) *Resolver {
	return New(Options{
		Explicit: func() string { return "" },
		Account:  func() string { return shell },
		Getenv:   func(string) string { return "" },
		Environ:  func() []string { return append([]string(nil), environ...) },
		GOOS:     "darwin",
		Timeout:  400 * time.Millisecond,
	})
}

var baseEnviron = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "SHELL=/bin/bash"}

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		out[key] = value
	}
	return out
}

func TestChooseShellPrecedence(t *testing.T) {
	request := writeScript(t, "request-sh", "")
	explicit := writeScript(t, "explicit-sh", "")
	account := writeScript(t, "account-sh", "")
	inherited := writeScript(t, "env-sh", "")
	nologin := writeScript(t, "nologin", "")
	accountOf := func(path string) func() string { return func() string { return path } }
	tests := []struct {
		name   string
		in     Inputs
		want   string
		source Source
		login  bool
		note   string
	}{
		{"request wins", Inputs{Request: request, Explicit: explicit, Account: accountOf(account), Env: inherited}, request, SourceRequest, true, ""},
		{"explicit beats account and env", Inputs{Explicit: explicit, Account: accountOf(account), Env: inherited}, explicit, SourceExplicit, true, ""},
		{"account beats inherited SHELL", Inputs{Account: accountOf(account), Env: inherited}, account, SourceAccount, true, ""},
		{"inherited SHELL is the last fallback", Inputs{Account: accountOf(""), Env: inherited}, inherited, SourceEnv, true, ""},
		{"nothing usable falls back to sh", Inputs{Account: accountOf(""), Env: ""}, "/bin/sh", SourceFallback, false, ""},
		{"unusable explicit is skipped and reported", Inputs{Explicit: nologin, Account: accountOf(account)}, account, SourceAccount, true, "explicit shell"},
		{"relative explicit is rejected", Inputs{Explicit: "zsh", Account: accountOf(account)}, account, SourceAccount, true, "not a usable shell"},
		{"missing account shell falls to env", Inputs{Account: accountOf("/does/not/exist"), Env: inherited}, inherited, SourceEnv, true, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Choose(test.in)
			if got.Path != test.want || got.Source != test.source || got.Login != test.login {
				t.Fatalf("Choose = %+v, want %s/%s login=%t", got, test.want, test.source, test.login)
			}
			if !strings.Contains(got.Note, test.note) {
				t.Fatalf("note = %q, want it to contain %q", got.Note, test.note)
			}
		})
	}
}

func TestMergePATHKeepsResolvedFirstAndDedupes(t *testing.T) {
	got := MergePATH("/opt/homebrew/bin:/usr/bin:rel:/usr/bin", "/usr/bin:./x::/usr/local/bin:/opt/homebrew/bin")
	if want := "/opt/homebrew/bin:/usr/bin:/usr/local/bin"; got != want {
		t.Fatalf("MergePATH = %q, want %q", got, want)
	}
	if got := MergePATH("", "/a:/b"); got != "/a:/b" {
		t.Fatalf("MergePATH = %q", got)
	}
}

func TestParseMarkedIgnoresChatterAndMultilineValues(t *testing.T) {
	out := []byte("banner\nM1\nPATH=/a:/b\nMULTI=line1\nline2 not a var\nHOME=/h\nM1\nzsh: warning\n")
	env, err := parseMarked(out, "M1")
	if err != nil {
		t.Fatal(err)
	}
	if env["PATH"] != "/a:/b" || env["HOME"] != "/h" || env["MULTI"] != "line1" || len(env) != 3 {
		t.Fatalf("env = %v", env)
	}
	if _, err := parseMarked([]byte("no markers"), "M1"); err == nil {
		t.Fatal("missing markers must fail")
	}
	if _, err := parseMarked([]byte("M1\nPATH=/a\n"), "M1"); err == nil {
		t.Fatal("missing end marker must fail")
	}
}

func TestValidatePATH(t *testing.T) {
	if got, err := validatePATH("/a:rel:/b"); err != nil || got != "/a:/b" {
		t.Fatalf("got %q %v", got, err)
	}
	for _, bad := range []string{"", "rel:./x", "/a\n/b"} {
		if _, err := validatePATH(bad); err == nil {
			t.Fatalf("validatePATH(%q) must fail", bad)
		}
	}
}

func TestResolverIgnoresChatterAndReplacesPollutedShell(t *testing.T) {
	shell := writeScript(t, "chatty", `echo "banner before"
echo "zsh: compinit warning" >&2
for a; do cmd="$a"; done
PATH=/resolved/bin:$PATH
export PATH EXTRA=from-rc
eval "$cmd"
echo "chatter after"
`)
	r := testResolver(shell, baseEnviron)
	env := envMap(r.Env(5 * time.Second))
	if !strings.HasPrefix(env["PATH"], "/resolved/bin:/usr/bin") {
		t.Fatalf("PATH = %q", env["PATH"])
	}
	if env["EXTRA"] != "from-rc" {
		t.Fatalf("rc variable missing: %v", env)
	}
	if env["SHELL"] != shell {
		t.Fatalf("SHELL = %q, want the chosen shell %q instead of the polluted /bin/bash", env["SHELL"], shell)
	}
	for _, leaked := range []string{"TERM", ResolvingEnv} {
		if _, ok := env[leaked]; ok {
			t.Fatalf("%s leaked from the probe environment", leaked)
		}
	}
	status := r.Status()
	if status.Method != MethodInteractive || status.Error != "" || status.ShellSource != SourceAccount {
		t.Fatalf("status = %+v", status)
	}
}

func TestResolverHangingInteractiveFallsBackToLoginAttempt(t *testing.T) {
	shell := writeScript(t, "hangs-interactive", `for a; do cmd="$a"; done
case " $* " in *" -i "*) sleep 30;; esac
PATH=/login/bin:$PATH
export PATH
eval "$cmd"
`)
	r := testResolver(shell, baseEnviron)
	started := time.Now()
	env := envMap(r.Env(10 * time.Second))
	if !strings.HasPrefix(env["PATH"], "/login/bin") {
		t.Fatalf("PATH = %q", env["PATH"])
	}
	status := r.Status()
	if status.Method != MethodLogin || !strings.Contains(status.Error, "timed out") {
		t.Fatalf("status = %+v", status)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatalf("hang was not bounded: %s", time.Since(started))
	}
}

func TestResolverRejectsInvalidPATHAndKeepsProcessEnvironment(t *testing.T) {
	shell := writeScript(t, "bad-path", `for a; do cmd="$a"; done
env() { echo "PATH=relative/bin:./x"; }
export PATH
eval "$cmd"
`)
	r := testResolver(shell, baseEnviron)
	env := envMap(r.Env(5 * time.Second))
	if env["PATH"] != "/usr/bin:/bin" {
		t.Fatalf("PATH = %q, want the process PATH", env["PATH"])
	}
	status := r.Status()
	if status.Method != MethodProcess || !strings.Contains(status.Error, "no absolute entry") {
		t.Fatalf("status = %+v", status)
	}
	if env["SHELL"] != shell {
		t.Fatalf("SHELL = %q, want %q", env["SHELL"], shell)
	}
}

func TestResolverAllHangingStillReturnsWithinWait(t *testing.T) {
	shell := writeScript(t, "hangs", "sleep 30\n")
	r := testResolver(shell, baseEnviron)
	started := time.Now()
	env := r.Env(100 * time.Millisecond)
	if time.Since(started) > 2*time.Second {
		t.Fatalf("Env blocked for %s", time.Since(started))
	}
	if envMap(env)["PATH"] != "/usr/bin:/bin" {
		t.Fatalf("env = %v", env)
	}
}

func TestResolverCachesUntilRefresh(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "count")
	shell := writeScript(t, "counting", `for a; do cmd="$a"; done
echo x >> `+counter+`
PATH=/counted/bin:$PATH
export PATH
eval "$cmd"
`)
	r := testResolver(shell, baseEnviron)
	r.Env(5 * time.Second)
	r.Env(5 * time.Second)
	if lines := countLines(t, counter); lines != 1 {
		t.Fatalf("resolutions = %d, want 1 (cached)", lines)
	}
	r.Refresh()
	deadline := time.Now().Add(5 * time.Second)
	for countLines(t, counter) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if lines := countLines(t, counter); lines != 2 {
		t.Fatalf("resolutions after refresh = %d, want 2", lines)
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "\n")
}

func TestResolverLinuxFastPathKeepsInheritedEnvironment(t *testing.T) {
	shell := writeScript(t, "never-run", "echo ran > "+filepath.Join(t.TempDir(), "x")+"\nexit 1\n")
	r := New(Options{
		Explicit: func() string { return "" },
		Account:  func() string { return "" },
		Getenv:   func(key string) string { return map[string]string{"SHELL": shell}[key] },
		Environ:  func() []string { return []string{"PATH=/home/u/bin:/usr/bin", "SHELL=" + shell} },
		GOOS:     "linux",
	})
	env := envMap(r.Env(time.Second))
	if env["PATH"] != "/home/u/bin:/usr/bin" || r.Status().Method != MethodInherited {
		t.Fatalf("env = %v status = %+v", env, r.Status())
	}
}

func TestResolverFallbackShellKeepsProcessEnvironment(t *testing.T) {
	r := testResolver("", []string{"PATH=/usr/bin"})
	env := envMap(r.Env(time.Second))
	status := r.Status()
	if status.ShellSource != SourceEnv && status.ShellSource != SourceFallback {
		t.Fatalf("status = %+v", status)
	}
	if env["PATH"] != "/usr/bin" {
		t.Fatalf("PATH = %q", env["PATH"])
	}
}

func TestResolverRealZshReadsZshrc(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}
	home := t.TempDir()
	rc := "echo zshrc-banner\nexport PATH=/zshrc/only/bin:$PATH\nexport FROM_ZSHRC=1\n"
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(rc), 0o644); err != nil {
		t.Fatal(err)
	}
	environ := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "SHELL=/bin/bash"}
	r := testResolver(zsh, environ)
	r.opts.Timeout = 10 * time.Second
	env := envMap(r.Env(15 * time.Second))
	if !strings.HasPrefix(env["PATH"], "/zshrc/only/bin:") || env["FROM_ZSHRC"] != "1" {
		t.Fatalf("zsh did not contribute ~/.zshrc (status %+v): PATH=%q", r.Status(), env["PATH"])
	}
	if r.Status().Method != MethodInteractive {
		t.Fatalf("status = %+v", r.Status())
	}
}
