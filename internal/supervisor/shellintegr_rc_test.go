//go:build !windows

package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runWrapped launches the wrapped shell under a temp HOME whose rc files
// append their own name to a marker file, and returns the marker lines.
func runWrapped(t *testing.T, shellName string, args []string, files map[string]string) []string {
	t.Helper()
	path, err := exec.LookPath(shellName)
	if err != nil {
		t.Skipf("%s not installed", shellName)
	}
	home := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wrapped, ok := wrapShellIntegration(append([]string{path}, args...), []string{
		"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TERM=dumb", "skip_global_compinit=1",
	})
	if !ok {
		t.Fatal("shell integration was not applied")
	}
	defer wrapped.cleanup()
	cmd := exec.Command(wrapped.command[0], wrapped.command[1:]...)
	cmd.Env = wrapped.env
	cmd.Dir = home
	cmd.Stdin = strings.NewReader("exit\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shell failed: %v\n%s", err, out)
	}
	data, _ := os.ReadFile(filepath.Join(home, "marker"))
	return strings.Fields(string(data))
}

func mark(name string) string { return "echo " + name + " >> \"$HOME/marker\"\n" }

func assertMarkers(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rc files ran %v, want %v", got, want)
	}
}

func TestBashLoginSourcesBashrcOnceViaProfile(t *testing.T) {
	got := runWrapped(t, "bash", []string{"-l", "-i"}, map[string]string{
		".bash_profile": mark("profile") + "[ -r ~/.bashrc ] && . ~/.bashrc\n",
		".bashrc":       mark("bashrc"),
	})
	assertMarkers(t, got, "profile", "bashrc")
}

func TestBashLoginSkipsBashrcWhenProfileDoesNot(t *testing.T) {
	got := runWrapped(t, "bash", []string{"-l", "-i"}, map[string]string{
		".bash_profile": mark("profile"),
		".bashrc":       mark("bashrc"),
	})
	assertMarkers(t, got, "profile")
}

func TestBashNonLoginSourcesBashrcOnce(t *testing.T) {
	got := runWrapped(t, "bash", []string{"-i"}, map[string]string{
		".bash_profile": mark("profile"),
		".bashrc":       mark("bashrc"),
	})
	assertMarkers(t, got, "bashrc")
}

func TestZshStartupFilesRunOnce(t *testing.T) {
	files := map[string]string{
		".zshenv":   mark("zshenv"),
		".zprofile": mark("zprofile"),
		".zshrc":    mark("zshrc"),
		".zlogin":   mark("zlogin"),
	}
	assertMarkers(t, runWrapped(t, "zsh", []string{"-l", "-i"}, files), "zshenv", "zprofile", "zshrc", "zlogin")
	assertMarkers(t, runWrapped(t, "zsh", []string{"-i"}, files), "zshenv", "zshrc")
}
