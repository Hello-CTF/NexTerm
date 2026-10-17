package local

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestLocaleMissing(t *testing.T) {
	for _, test := range []struct {
		name string
		env  []string
		want bool
	}{
		{"unset", nil, true},
		{"blank", []string{"LC_ALL= ", "LC_CTYPE=", "LANG=  "}, true},
		{"lang", []string{"LANG=en_US.UTF-8"}, false},
		{"ctype", []string{"LC_CTYPE=C.UTF-8"}, false},
		{"all", []string{"LC_ALL=C"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := localeMissing(test.env); got != test.want {
				t.Fatalf("localeMissing(%v) = %v", test.env, got)
			}
		})
	}
}

func TestEnvironmentOverridesTerminalAndLocale(t *testing.T) {
	env := environment(map[string]string{"LC_ALL": "", "LC_CTYPE": "", "LANG": "", "NEXTERM_TEST": "值"}, true, "xterm-test")
	if got := environmentValue(env, "TERM"); got != "xterm-test" {
		t.Fatalf("TERM = %q", got)
	}
	if got := environmentValue(env, "LANG"); got != utf8Locale {
		t.Fatalf("LANG = %q", got)
	}
	if got := environmentValue(env, "NEXTERM_TEST"); got != "值" {
		t.Fatalf("custom environment = %q", got)
	}
	env = environment(map[string]string{"LANG": "zh_CN.UTF-8"}, false, "")
	if got := environmentValue(env, "LANG"); got != "zh_CN.UTF-8" {
		t.Fatalf("configured LANG = %q", got)
	}
}

func TestEnvironmentKeyCase(t *testing.T) {
	env := []string{"Term=old", "PATH=value"}
	env = setEnvironment(env, "TERM", "new")
	if runtime.GOOS == "windows" && len(env) != 2 {
		t.Fatalf("Windows environment kept a duplicate: %v", env)
	}
	if got := environmentValue(env, "TERM"); got != "new" {
		t.Fatalf("TERM = %q", got)
	}
}

func TestEnvironmentDefaultsColortermForTerminal(t *testing.T) {
	t.Setenv("COLORTERM", "")
	env := environment(nil, true, "")
	if got := environmentValue(env, "COLORTERM"); got != "truecolor" {
		t.Fatalf("COLORTERM = %q, want truecolor", got)
	}
}

func TestEnvironmentKeepsExplicitColorterm(t *testing.T) {
	t.Setenv("COLORTERM", "")
	env := environment(map[string]string{"COLORTERM": "24bit"}, true, "")
	if got := environmentValue(env, "COLORTERM"); got != "24bit" {
		t.Fatalf("COLORTERM = %q, want 24bit", got)
	}
}

func TestEnvironmentLeavesColortermOutsideTerminal(t *testing.T) {
	t.Setenv("COLORTERM", "24bit")
	env := environment(nil, false, "")
	if got := environmentValue(env, "COLORTERM"); got != "24bit" {
		t.Fatalf("COLORTERM = %q, want untouched 24bit", got)
	}
}

func TestEnvironmentDefaultsTermForExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec TERM default is unix-only")
	}
	if value, ok := os.LookupEnv("TERM"); ok {
		t.Cleanup(func() { _ = os.Setenv("TERM", value) })
	}
	if err := os.Unsetenv("TERM"); err != nil {
		t.Fatal(err)
	}
	env := environment(nil, false, "")
	if got := environmentValue(env, "TERM"); got != "xterm-256color" {
		t.Fatalf("TERM = %q, want xterm-256color", got)
	}

	emptied := environment(map[string]string{"TERM": ""}, false, "")
	if got := environmentValue(emptied, "TERM"); got != "xterm-256color" {
		t.Fatalf("empty TERM = %q, want fallback xterm-256color", got)
	}
	occurrences := 0
	for _, entry := range emptied {
		if strings.HasPrefix(entry, "TERM=") {
			occurrences++
		}
	}
	if occurrences != 1 {
		t.Fatalf("TERM entries = %d, want exactly one", occurrences)
	}
}

func TestEnvironmentKeepsExplicitTermForExec(t *testing.T) {
	env := environment(map[string]string{"TERM": "vt100"}, false, "")
	if got := environmentValue(env, "TERM"); got != "vt100" {
		t.Fatalf("TERM = %q, want vt100", got)
	}
}

func TestShellClassificationUsesExecutableWhitelist(t *testing.T) {
	for _, test := range []struct {
		executable string
		want       string
	}{
		{executable: "powershell.exe", want: "powershell"},
		{executable: `C:\Program Files\PowerShell\7\pwsh.exe`, want: "powershell"},
		{executable: "/usr/bin/pwsh", want: "powershell"},
		{executable: "CMD.EXE", want: "cmd"},
		{executable: "/bin/sh", want: "posix"},
		{executable: "/bin/bash", want: "posix"},
		{executable: "/usr/bin/zsh", want: "posix"},
		{executable: "/bin/dash", want: "posix"},
		{executable: "/bin/ksh", want: "posix"},
		{executable: "fish", want: "unknown"},
		{executable: "pwsh-preview", want: "unknown"},
		{executable: "python3", want: "unknown"},
	} {
		t.Run(test.executable, func(t *testing.T) {
			transport := NewWithConfig(Config{Shell: test.executable})
			if got := transport.Shell(); got != test.want {
				t.Fatalf("Shell() = %q, want %q", got, test.want)
			}
		})
	}
}
