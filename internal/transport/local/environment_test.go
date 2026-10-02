package local

import (
	"runtime"
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
