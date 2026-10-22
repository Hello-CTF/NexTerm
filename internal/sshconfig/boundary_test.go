package sshconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func writeBoundaryTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func boundaryAliases(result *Result) map[string]bool {
	aliases := map[string]bool{}
	for _, host := range result.Hosts {
		aliases[host.Alias] = true
	}
	return aliases
}

func countDiagnostic(diagnostics []Diagnostic, code string) int {
	count := 0
	for _, d := range diagnostics {
		if d.Code == code {
			count++
		}
	}
	return count
}

func TestParseRootDirConfinesIncludes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	sshDir := filepath.Join(root, ".ssh")
	writeBoundaryTestFile(t, filepath.Join(sshDir, "config"),
		"Include conf.d/ok.conf\nInclude "+filepath.Join(outside, "abs.conf")+"\nInclude ../escape.conf\nHost main\n  HostName main.example\n")
	writeBoundaryTestFile(t, filepath.Join(sshDir, "conf.d", "ok.conf"), "Host inside\n  HostName inside.example\n")
	writeBoundaryTestFile(t, filepath.Join(outside, "abs.conf"), "Host abs\n  HostName abs.example\n")
	writeBoundaryTestFile(t, filepath.Join(root, "escape.conf"), "Host esc\n  HostName esc.example\n")

	result, err := Parse(filepath.Join(sshDir, "config"), Limits{RootDir: sshDir})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	aliases := boundaryAliases(result)
	if !aliases["main"] || !aliases["inside"] {
		t.Errorf("in-bounds hosts missing: %+v", result.Hosts)
	}
	if aliases["abs"] || aliases["esc"] {
		t.Errorf("escaping includes were parsed: %+v", result.Hosts)
	}
	if countDiagnostic(result.Diagnostics, "include-out-of-bounds") != 2 {
		t.Errorf("want 2 include-out-of-bounds diagnostics, got %+v", result.Diagnostics)
	}
}

func TestParseRootDirConfinesRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeBoundaryTestFile(t, filepath.Join(outside, "config"), "Host x\n")
	if _, err := Parse(filepath.Join(outside, "config"), Limits{RootDir: filepath.Join(root, ".ssh")}); err == nil {
		t.Fatal("root config outside the boundary was accepted")
	}
}

func TestParseRootDirUnsetKeepsIncludeBehavior(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ssh")
	outside := t.TempDir()
	writeBoundaryTestFile(t, filepath.Join(root, "config"), "Include "+filepath.Join(outside, "abs.conf")+"\nInclude ../escape.conf\n")
	writeBoundaryTestFile(t, filepath.Join(outside, "abs.conf"), "Host abs\n")
	writeBoundaryTestFile(t, filepath.Join(base, "escape.conf"), "Host esc\n")

	result, err := Parse(filepath.Join(root, "config"), Limits{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	aliases := boundaryAliases(result)
	if !aliases["abs"] || !aliases["esc"] {
		t.Errorf("unconfined parse lost includes: %+v", result.Hosts)
	}
	if countDiagnostic(result.Diagnostics, "include-out-of-bounds") != 0 {
		t.Errorf("unexpected boundary diagnostics: %+v", result.Diagnostics)
	}
}

func TestParseRootDirNestedIncludesAllowed(t *testing.T) {
	sshDir := t.TempDir()
	writeBoundaryTestFile(t, filepath.Join(sshDir, "config"), "Include conf.d/mid.conf\n")
	writeBoundaryTestFile(t, filepath.Join(sshDir, "conf.d", "mid.conf"), "Include deep/leaf.conf\n")
	writeBoundaryTestFile(t, filepath.Join(sshDir, "conf.d", "deep", "leaf.conf"), "Host leaf\n  HostName leaf.example\n")

	result, err := Parse(filepath.Join(sshDir, "config"), Limits{RootDir: sshDir})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if aliases := boundaryAliases(result); !aliases["leaf"] {
		t.Errorf("nested in-bounds include lost: %+v", result.Hosts)
	}
	if len(result.Diagnostics) != 0 {
		t.Errorf("unexpected diagnostics: %+v", result.Diagnostics)
	}
}
