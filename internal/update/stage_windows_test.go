package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// installerChildEnv makes the test binary act as the NSIS installer: it reads its own executable the way NSIS reads
// its appended data and exits.
const installerChildEnv = "NEXTERM_UPDATE_TEST_INSTALLER_CHILD"

// runAsInstallerChild opens the running executable with GENERIC_READ and FILE_SHARE_READ, the strictest access the
// NSIS stub uses to read its own file, reads from it and exits 0, or 3 when that fails.
func runAsInstallerChild() {
	if os.Getenv(installerChildEnv) != "1" {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(3)
	}
	name, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		os.Exit(3)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		os.Exit(3)
	}
	self := os.NewFile(uintptr(handle), executable)
	if _, err := io.ReadFull(self, make([]byte, 512)); err != nil {
		os.Exit(3)
	}
	_ = self.Close()
	os.Exit(0)
}

// The installer runs from the verified copy while copyVerified's handle keeps it locked: CreateProcess must be able
// to map the image and the installer must be able to read its own file, or every Windows update would fail with a
// sharing violation. The test binary stands in for the NSIS installer and is launched through runInstaller.
func TestInstallerRunsFromTheLockedVerifiedCopy(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(t.TempDir(), "nexterm-desktop_windows_amd64_1.0.0.exe")
	if err := os.WriteFile(staged, content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	parent := t.TempDir()
	verified, cleanup, err := copyVerified(staged, parent, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if writer, err := os.OpenFile(verified.Name(), os.O_WRONLY, 0); err == nil {
		_ = writer.Close()
		t.Fatal("the verified copy must stay locked while the installer runs")
	}
	t.Setenv(installerChildEnv, "1")
	if err := runInstaller(context.Background(), verified.Name()); err != nil {
		t.Fatalf("running the installer from the locked verified copy failed: %v", err)
	}
}

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
