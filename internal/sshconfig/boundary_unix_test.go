//go:build unix

package sshconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseRootDirSymlinkFileEscapeSkipped(t *testing.T) {
	sshDir := t.TempDir()
	outside := t.TempDir()
	writeBoundaryTestFile(t, filepath.Join(outside, "secret.conf"), "Host secret\n  HostName secret.example\n")
	if err := os.Symlink(filepath.Join(outside, "secret.conf"), filepath.Join(sshDir, "linked.conf")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	writeBoundaryTestFile(t, filepath.Join(sshDir, "config"), "Include linked.conf\n")

	result, err := Parse(filepath.Join(sshDir, "config"), Limits{RootDir: sshDir})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if aliases := boundaryAliases(result); aliases["secret"] {
		t.Errorf("symlink escape was parsed: %+v", result.Hosts)
	}
	if !hasDiagnostic(result.Diagnostics, "include-out-of-bounds") {
		t.Errorf("missing include-out-of-bounds diagnostic: %+v", result.Diagnostics)
	}
}

func TestParseRootDirSymlinkDirEscapeSkipped(t *testing.T) {
	sshDir := t.TempDir()
	outside := t.TempDir()
	writeBoundaryTestFile(t, filepath.Join(outside, "secret.conf"), "Host secret\n")
	if err := os.Symlink(outside, filepath.Join(sshDir, "escape-dir")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	writeBoundaryTestFile(t, filepath.Join(sshDir, "config"), "Include escape-dir/secret.conf\n")

	result, err := Parse(filepath.Join(sshDir, "config"), Limits{RootDir: sshDir})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if aliases := boundaryAliases(result); aliases["secret"] {
		t.Errorf("symlinked directory escape was parsed: %+v", result.Hosts)
	}
	if !hasDiagnostic(result.Diagnostics, "include-out-of-bounds") {
		t.Errorf("missing include-out-of-bounds diagnostic: %+v", result.Diagnostics)
	}
}

func TestParseRootDirGlobSymlinkEscapeSkipped(t *testing.T) {
	sshDir := t.TempDir()
	outside := t.TempDir()
	writeBoundaryTestFile(t, filepath.Join(outside, "secret.conf"), "Host secret\n")
	if err := os.MkdirAll(filepath.Join(sshDir, "conf.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.conf"), filepath.Join(sshDir, "conf.d", "linked.conf")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	writeBoundaryTestFile(t, filepath.Join(sshDir, "conf.d", "ok.conf"), "Host ok\n")
	writeBoundaryTestFile(t, filepath.Join(sshDir, "config"), "Include conf.d/*.conf\n")

	result, err := Parse(filepath.Join(sshDir, "config"), Limits{RootDir: sshDir})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	aliases := boundaryAliases(result)
	if !aliases["ok"] {
		t.Errorf("in-bounds glob match lost: %+v", result.Hosts)
	}
	if aliases["secret"] {
		t.Errorf("escaping glob match was parsed: %+v", result.Hosts)
	}
	if countDiagnostic(result.Diagnostics, "include-out-of-bounds") != 1 {
		t.Errorf("want 1 include-out-of-bounds diagnostic, got %+v", result.Diagnostics)
	}
}

func TestParseRootDirSymlinkInsideAccepted(t *testing.T) {
	sshDir := t.TempDir()
	writeBoundaryTestFile(t, filepath.Join(sshDir, "real", "real.conf"), "Host real\n  HostName real.example\n")
	if err := os.Symlink(filepath.Join(sshDir, "real"), filepath.Join(sshDir, "alias")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	writeBoundaryTestFile(t, filepath.Join(sshDir, "config"), "Include alias/real.conf\n")

	result, err := Parse(filepath.Join(sshDir, "config"), Limits{RootDir: sshDir})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if aliases := boundaryAliases(result); !aliases["real"] {
		t.Errorf("symlink pointing inside the boundary was rejected: %+v", result.Hosts)
	}
	if len(result.Diagnostics) != 0 {
		t.Errorf("unexpected diagnostics: %+v", result.Diagnostics)
	}
}

func TestParseRootDirRootSymlinkEscapeRejected(t *testing.T) {
	sshDir := t.TempDir()
	outside := t.TempDir()
	writeBoundaryTestFile(t, filepath.Join(outside, "config"), "Host x\n")
	if err := os.Symlink(filepath.Join(outside, "config"), filepath.Join(sshDir, "config")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := Parse(filepath.Join(sshDir, "config"), Limits{RootDir: sshDir}); err == nil {
		t.Fatal("symlinked root config escaping the boundary was accepted")
	}
}

func TestParseRootDirBoundarySymlinkResolved(t *testing.T) {
	real := t.TempDir()
	writeBoundaryTestFile(t, filepath.Join(real, "config"), "Host x\n  HostName x.example\n")
	link := filepath.Join(t.TempDir(), "ssh")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	result, err := Parse(filepath.Join(link, "config"), Limits{RootDir: link})
	if err != nil {
		t.Fatalf("Parse through symlinked boundary: %v", err)
	}
	if len(result.Hosts) != 1 || result.Hosts[0].Alias != "x" {
		t.Errorf("symlinked boundary rejected its own file: %+v", result.Hosts)
	}
}
