package sshconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseBasic(t *testing.T) {
	result, err := Parse(filepath.Join("..", "..", "testdata", "sshconfig", "basic"), DefaultLimits)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	home, _ := os.UserHomeDir()
	want := []Host{
		{Alias: "web", Hostname: "web.example.com", Port: 2222, Username: "deploy", IdentityFiles: []string{filepath.Join(home, ".ssh", "id_ed25519")}, ProxyJump: "bastion"},
		{Alias: "bastion", Hostname: "bastion.example.com", Port: 22, Username: "admin"},
		{Alias: "db", Hostname: "db.example.com", Port: 5432, Username: "dbuser", IdentityFiles: []string{filepath.Join(home, ".ssh", "db key")}},
		{Alias: "multi", Hostname: "multi.example.com", Port: 22},
		{Alias: "one", Hostname: "multi.example.com", Port: 22},
	}
	if len(result.Hosts) != len(want) {
		t.Fatalf("got %d hosts, want %d: %+v", len(result.Hosts), len(want), result.Hosts)
	}
	for i, w := range want {
		got := result.Hosts[i]
		if got.Alias != w.Alias || got.Hostname != w.Hostname || got.Port != w.Port || got.Username != w.Username || got.ProxyJump != w.ProxyJump {
			t.Errorf("host %d = %+v, want %+v", i, got, w)
		}
		if strings.Join(got.IdentityFiles, ",") != strings.Join(w.IdentityFiles, ",") {
			t.Errorf("host %s identity files = %v, want %v", got.Alias, got.IdentityFiles, w.IdentityFiles)
		}
	}
	if len(result.Diagnostics) != 0 {
		t.Errorf("unexpected diagnostics: %+v", result.Diagnostics)
	}
}

func TestParseIncludes(t *testing.T) {
	result, err := Parse(filepath.Join("..", "..", "testdata", "sshconfig", "includes_main"), DefaultLimits)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	aliases := map[string]int{}
	for _, host := range result.Hosts {
		aliases[host.Alias] = host.Port
	}
	if len(result.Hosts) != 3 || aliases["main"] != 22 || aliases["child"] != 2200 || aliases["extra"] != 22 {
		t.Errorf("unexpected hosts: %+v", result.Hosts)
	}
}

func TestParseIncludeCycle(t *testing.T) {
	result, err := Parse(filepath.Join("..", "..", "testdata", "sshconfig", "cycle_a"), DefaultLimits)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(result.Hosts) != 2 {
		t.Errorf("got %d hosts, want 2", len(result.Hosts))
	}
	if !hasDiagnostic(result.Diagnostics, "include-cycle") {
		t.Errorf("missing include-cycle diagnostic: %+v", result.Diagnostics)
	}
}

func TestParseMatchAndWildcards(t *testing.T) {
	result, err := Parse(filepath.Join("..", "..", "testdata", "sshconfig", "match_wildcard"), DefaultLimits)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(result.Hosts) != 2 || result.Hosts[0].Alias != "concrete" || result.Hosts[1].Alias != "aftermatch" {
		t.Errorf("unexpected hosts: %+v", result.Hosts)
	}
	if result.Hosts[1].Username != "after" {
		t.Errorf("host after match not parsed: %+v", result.Hosts[1])
	}
	for _, code := range []string{"match-skipped", "host-pattern-skipped"} {
		if !hasDiagnostic(result.Diagnostics, code) {
			t.Errorf("missing %s diagnostic: %+v", code, result.Diagnostics)
		}
	}
}

func TestParseHostLimit(t *testing.T) {
	result, err := Parse(filepath.Join("..", "..", "testdata", "sshconfig", "many_hosts"), Limits{MaxHosts: 5})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(result.Hosts) != 5 || !result.Truncated {
		t.Errorf("got %d hosts truncated=%v, want 5 true", len(result.Hosts), result.Truncated)
	}
	if !hasDiagnostic(result.Diagnostics, "limit-truncated") {
		t.Errorf("missing limit-truncated diagnostic: %+v", result.Diagnostics)
	}
}

func TestParseFileTooLarge(t *testing.T) {
	if _, err := Parse(filepath.Join("..", "..", "testdata", "sshconfig", "basic"), Limits{MaxFileBytes: 4}); err == nil {
		t.Fatal("expected size limit error")
	}
}

func TestParseMissingFile(t *testing.T) {
	if _, err := Parse(filepath.Join("..", "..", "testdata", "sshconfig", "does-not-exist"), DefaultLimits); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestSplitDirective(t *testing.T) {
	cases := []struct {
		line string
		key  string
		val  string
		ok   bool
	}{
		{"Host web", "Host", "web", true},
		{"Port=22", "Port", "22", true},
		{"Host = web", "Host", "web", true},
		{`IdentityFile "~/.ssh/my key"`, "IdentityFile", `~/.ssh/my key`, true},
		{"ProxyCommand FOO=1 ssh -W %h:%p jump", "ProxyCommand", "FOO=1 ssh -W %h:%p jump", true},
		{"Host", "", "", false},
		{"   ", "", "", false},
	}
	for _, tc := range cases {
		key, val, ok := splitDirective(tc.line)
		if key != tc.key || val != tc.val || ok != tc.ok {
			t.Errorf("splitDirective(%q) = %q,%q,%v, want %q,%q,%v", tc.line, key, val, ok, tc.key, tc.val, tc.ok)
		}
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	if got := expandPath("~/x"); got != filepath.Join(home, "x") {
		t.Errorf("expandPath(~/x) = %q", got)
	}
	if got := expandPath("%d/x"); got != filepath.Join(home, "x") {
		t.Errorf("expandPath(%%d/x) = %q", got)
	}
	if got := expandPath(`"~/x"`); got != filepath.Join(home, "x") {
		t.Errorf("expandPath quoted = %q", got)
	}
}

func TestSanitizeValue(t *testing.T) {
	if got := sanitizeValue("a\x1b[31m"); got != "a[31m" {
		t.Errorf("sanitizeValue = %q", got)
	}
	if got := sanitizeValue("  x  "); got != "x" {
		t.Errorf("sanitizeValue = %q", got)
	}
}

func hasDiagnostic(diagnostics []Diagnostic, code string) bool {
	for _, d := range diagnostics {
		if d.Code == code {
			return true
		}
	}
	return false
}
