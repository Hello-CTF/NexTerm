package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenVerifiedCopyDeniesWritingAndReplacingTheCopy(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "installer.exe")
	if err := os.WriteFile(path, []byte("verified"), 0o600); err != nil {
		t.Fatal(err)
	}
	copied, err := openVerifiedCopy(path)
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	if writer, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		_ = writer.Close()
		t.Fatal("the verified copy opened for writing while its handle is open")
	}
	if err := os.Rename(path, path+".moved"); err == nil {
		t.Fatal("the verified copy was renamed while its handle is open")
	}
	replacement := filepath.Join(directory, "replacement.exe")
	if err := os.WriteFile(replacement, []byte("swapped"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err == nil {
		t.Fatal("the verified copy was replaced while its handle is open")
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatalf("reading the verified copy, as the installer does, failed: %v", err)
	}
	_ = reader.Close()
}
