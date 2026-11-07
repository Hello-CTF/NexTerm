package diagcapture

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureLogAndArgsWriteHomeAsTilde(t *testing.T) {
	paths, logger := newEnv(t)
	opts := fastOptions()
	opts.HomeDir = "/Users/alice"
	s, err := Start(context.Background(), paths, logger, Info{Mode: "test", Args: []string{"--data-dir=/Users/alice/data"}}, opts)
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("moved", "dir", "/Users/alice/Library/Application Support/NexTerm")
	logger.Debug("other user", "dir", "/Users/alicex/work")
	logger.Debug("home itself", "dir", "/Users/alice")
	s.Stop()

	debugLog, _ := os.ReadFile(filepath.Join(s.Dir(), "app.debug.log"))
	text := string(debugLog)
	if strings.Contains(text, "/Users/alice/") || strings.Contains(text, "dir=/Users/alice\n") {
		t.Fatalf("home path leaked into app.debug.log: %s", text)
	}
	for _, want := range []string{`dir="~/Library/Application Support/NexTerm"`, "dir=/Users/alicex/work", "dir=~\n"} {
		if !strings.Contains(text, want) {
			t.Fatalf("app.debug.log missing %q: %s", want, text)
		}
	}
	meta, _ := os.ReadFile(filepath.Join(s.Dir(), "meta.txt"))
	if !strings.Contains(string(meta), "args: --data-dir=~/data\n") {
		t.Fatalf("meta args not scrubbed: %s", meta)
	}
}

func TestHomePatternWindowsEscapedForm(t *testing.T) {
	pattern := homePattern(`C:\Users\Bob`)
	got := string(scrubHome(pattern, []byte(`dir="C:\\Users\\Bob\\AppData" raw=C:\Users\Bob\x other=C:\Users\Bobby`)))
	want := `dir="~\\AppData" raw=~\x other=C:\Users\Bobby`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestHomePatternIgnoresUnsafeHomes(t *testing.T) {
	for _, home := range []string{"", "/", " ", "."} {
		if homePattern(home) != nil {
			t.Fatalf("home %q must not produce a pattern", home)
		}
	}
}
