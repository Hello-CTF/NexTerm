package applog

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingFileFailedMoveKeepsBackupsAndBacksOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nexterm.log")
	for i := 1; i <= 3; i++ {
		if err := os.WriteFile(fmt.Sprintf("%s.%d", path, i), []byte("backup\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var errOut bytes.Buffer
	file, err := openRotatingFile(path, 20, 3, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	attempts := 0
	file.rename = func(oldPath, newPath string) error {
		if oldPath == path {
			attempts++
			return errors.New("file is held by another process")
		}
		return os.Rename(oldPath, newPath)
	}
	line := []byte("abcd\n")
	for i := 0; i < 12; i++ {
		if _, err := file.Write(line); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	for i := 1; i <= 3; i++ {
		data, err := os.ReadFile(fmt.Sprintf("%s.%d", path, i))
		if err != nil || string(data) != "backup\n" {
			t.Fatalf("backup %d changed after a failed rotation: %q, %v", i, data, err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) != 12*len(line) {
		t.Fatalf("live file = %d bytes, %v; want every record kept", len(data), err)
	}
	// The first attempt fails at 20 bytes; the retry waits for another 20 bytes (at 40), instead of every write.
	if attempts != 2 {
		t.Fatalf("rotation attempts = %d, want 2", attempts)
	}
	if got := strings.Count(errOut.String(), "log rotation failed"); got != 1 {
		t.Fatalf("rotation failure reported %d times: %q", got, errOut.String())
	}

	// Once the file can be moved, the next write past the backed-off limit rotates normally.
	file.rename = os.Rename
	if _, err := file.Write(line); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path + ".1"); err != nil || len(data) != 12*len(line) {
		t.Fatalf("rotation did not resume with the grown file as backup 1: %d bytes, %v", len(data), err)
	}
	if data, err := os.ReadFile(path + ".2"); err != nil || string(data) != "backup\n" {
		t.Fatalf("older backups were not shifted: %q, %v", data, err)
	}
	if _, err := os.Stat(path + ".rotating"); !os.IsNotExist(err) {
		t.Fatalf("staging file left behind: %v", err)
	}
}

func TestRotatingFileWriteAfterCloseFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nexterm.log")
	file, err := openRotatingFile(path, 1024, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("late\n")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("write after close = %v, want os.ErrClosed", err)
	}
	if file.file != nil {
		t.Fatal("write after close reopened the file")
	}
	if data, _ := os.ReadFile(path); len(data) != 0 {
		t.Fatalf("write after close reached the file: %q", data)
	}
}
