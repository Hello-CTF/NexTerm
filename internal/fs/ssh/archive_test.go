package ssh

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveCommandGoldens(t *testing.T) {
	golden := readGolden(t, "archive.golden")
	pack, err := PackCommand("/home/deploy/my dir", "/tmp/out.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	extract, target, err := ExtractCommand("/home/deploy/my dir.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	optionSafe, err := PackCommand("-checkpoint-action=exec=touch pwned", "/tmp/out.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	actual := "pack: " + pack + "\n" +
		"extract: " + extract + "\n" +
		"target: " + target + "\n" +
		"option-safe: " + optionSafe + "\n" +
		"quote: " + ShellQuote("it's $HOME`id`") + "\n"
	if actual != golden {
		t.Fatalf("archive commands differ from golden\n--- want ---\n%s--- got ---\n%s", golden, actual)
	}
}

func TestExtractFormatsAndRejections(t *testing.T) {
	for name, fragment := range map[string]string{
		"a.tgz": "tar -xzf", "a.tar.bz2": "tar -xjf", "a.tbz": "tar -xjf",
		"a.tar.xz": "tar -xJf", "a.txz": "tar -xJf", "a.tar": "tar -xf", "a.zip": "unzip -o",
	} {
		command, _, err := ExtractCommand(name)
		if err != nil || !strings.Contains(command, fragment) {
			t.Errorf("ExtractCommand(%q) = %q, %v", name, command, err)
		}
	}
	for _, name := range []string{"a.rar", ".tar.gz", "/"} {
		if _, _, err := ExtractCommand(name); err == nil {
			t.Errorf("ExtractCommand(%q) unexpectedly succeeded", name)
		}
	}
}

func TestCopyBufferLimitsAndCancellation(t *testing.T) {
	var output strings.Builder
	written, err := copyBuffer(context.Background(), &output, strings.NewReader("abc"), nil, make([]byte, 2), 2)
	if err != nil || written != 2 || output.String() != "ab" {
		t.Fatalf("bounded copy = %d %q %v", written, output.String(), err)
	}
	written, err = copyBuffer(context.Background(), io.Discard, strings.NewReader("a"), nil, nil, 2)
	if written != 1 || err != io.ErrUnexpectedEOF {
		t.Fatalf("short source = %d, %v", written, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := copyBuffer(ctx, io.Discard, strings.NewReader("a"), nil, nil, -1); err != context.Canceled {
		t.Fatalf("canceled copy = %v", err)
	}
}

func readGolden(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
