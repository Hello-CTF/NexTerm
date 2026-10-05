package shellintegr

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		path string
		want Shell
	}{
		{"zsh", ShellZsh},
		{"/usr/bin/zsh", ShellZsh},
		{"bash", ShellBash},
		{"/bin/bash", ShellBash},
		{"fish", ShellFish},
		{"/usr/local/bin/fish", ShellFish},
	}
	for _, tt := range tests {
		got, err := Detect(tt.path)
		if err != nil || got != tt.want {
			t.Errorf("Detect(%q) = %q, %v; want %q, nil", tt.path, got, err, tt.want)
		}
	}
	for _, path := range []string{"sh", "/bin/sh", "tcsh", "dash", ""} {
		if _, err := Detect(path); err == nil {
			t.Errorf("Detect(%q) succeeded; want error", path)
		}
	}
}

func TestWrapBash(t *testing.T) {
	launch, err := Wrap(ShellBash, "/bin/bash")
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Cleanup()
	if launch.Path != "/bin/bash" {
		t.Fatalf("Path = %q", launch.Path)
	}
	if len(launch.Args) != 3 || launch.Args[0] != "--rcfile" || launch.Args[2] != "-i" {
		t.Fatalf("Args = %v", launch.Args)
	}
	if len(launch.Env) != 0 {
		t.Fatalf("Env = %v; want none", launch.Env)
	}
	body, err := os.ReadFile(launch.Args[1])
	if err != nil {
		t.Fatal(err)
	}
	content := string(body)
	for _, want := range []string{"source $HOME/.bashrc", "PROMPT_COMMAND", "]7;file://"} {
		if !strings.Contains(content, want) {
			t.Errorf("bash wrapper missing %q:\n%s", want, content)
		}
	}
}

func TestWrapZsh(t *testing.T) {
	launch, err := Wrap(ShellZsh, "")
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Cleanup()
	if launch.Path != "zsh" {
		t.Fatalf("Path = %q; want default zsh", launch.Path)
	}
	if !reflect.DeepEqual(launch.Args, []string{"-i"}) {
		t.Fatalf("Args = %v", launch.Args)
	}
	if len(launch.Env) != 1 || !strings.HasPrefix(launch.Env[0], "ZDOTDIR=") {
		t.Fatalf("Env = %v; want single ZDOTDIR entry", launch.Env)
	}
	dir := strings.TrimPrefix(launch.Env[0], "ZDOTDIR=")
	for _, name := range []string{".zshenv", ".zprofile", ".zshrc", ".zlogin"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("wrapper file %s: %v", name, err)
		}
		if !strings.Contains(string(body), "source $HOME/"+name) {
			t.Errorf("%s does not pass through the user file:\n%s", name, body)
		}
	}
	rc, err := os.ReadFile(filepath.Join(dir, ".zshrc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"precmd_functions+=", "chpwd_functions+=", "]7;file://"} {
		if !strings.Contains(string(rc), want) {
			t.Errorf("zsh wrapper missing %q:\n%s", want, rc)
		}
	}
}

func TestWrapFish(t *testing.T) {
	launch, err := Wrap(ShellFish, "/usr/bin/fish")
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Cleanup()
	if len(launch.Args) != 3 || launch.Args[0] != "-i" || launch.Args[1] != "--init-command" {
		t.Fatalf("Args = %v", launch.Args)
	}
	init := launch.Args[2]
	if !strings.HasPrefix(init, "source '") || !strings.HasSuffix(init, "'") {
		t.Fatalf("init command = %q; want quoted source", init)
	}
	body, err := os.ReadFile(strings.TrimSuffix(strings.TrimPrefix(init, "source '"), "'"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--on-event fish_prompt", "--on-variable PWD", "string escape --style=url", "]7;file://"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("fish wrapper missing %q:\n%s", want, body)
		}
	}
}

func TestWrapUnsupported(t *testing.T) {
	if _, err := Wrap(Shell("tcsh"), "tcsh"); err == nil {
		t.Fatal("Wrap(tcsh) succeeded; want error")
	}
}

func TestWrapCleanupRemovesFiles(t *testing.T) {
	launch, err := Wrap(ShellBash, "bash")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(launch.Args[1])
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	launch.Cleanup()
	launch.Cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("wrapper dir still present after Cleanup: %v", err)
	}
}
