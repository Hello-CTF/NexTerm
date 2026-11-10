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
		t.Fatalf("unexpected hosts: %+v", result.Hosts)
	}
	// The leading Host * block applies to every concrete host and, as in OpenSSH, its User wins over values set
	// later; the Match block and the web-*/db-? patterns contribute nothing to these aliases.
	concrete := result.Hosts[0]
	if concrete.Hostname != "concrete.example.com" || concrete.Username != "globaluser" || concrete.Port != 22 {
		t.Errorf("concrete = %+v, want Host * defaults applied", concrete)
	}
	after := result.Hosts[1]
	if after.Hostname != "aftermatch.example.com" || after.Username != "globaluser" || after.Port != 22 {
		t.Errorf("host after match = %+v, want its HostName with the earlier Host * user", after)
	}
	for _, code := range []string{"match-skipped", "host-pattern-skipped"} {
		if !hasDiagnostic(result.Diagnostics, code) {
			t.Errorf("missing %s diagnostic: %+v", code, result.Diagnostics)
		}
	}
}

func TestParseWildcardDefaults(t *testing.T) {
	result, err := Parse(filepath.Join("..", "..", "testdata", "sshconfig", "wildcard_defaults"), DefaultLimits)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	home, _ := os.UserHomeDir()
	webKey := filepath.Join(home, ".ssh", "web_key")
	defaultKey := filepath.Join(home, ".ssh", "id_default")
	want := []Host{
		{Alias: "web", Hostname: "web.example.com", Port: 2200, Username: "webuser", IdentityFiles: []string{webKey, defaultKey}, ProxyJump: "bastion"},
		{Alias: "db", Hostname: "db-%1.example.com", Port: 2200, Username: "deploy", IdentityFiles: []string{defaultKey}, ProxyJump: "bastion"},
		{Alias: "bastion", Hostname: "bastion.example.com", Port: 2200, Username: "deploy", IdentityFiles: []string{defaultKey}},
		{Alias: "tok", Hostname: "%n.example.com", Port: 2200, Username: "deploy", IdentityFiles: []string{defaultKey}, ProxyJump: "bastion"},
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
	for _, code := range []string{"duplicate-alias", "host-pattern-skipped", "hostname-token-unsupported"} {
		if !hasDiagnostic(result.Diagnostics, code) {
			t.Errorf("missing %s diagnostic: %+v", code, result.Diagnostics)
		}
	}
}

func TestParseGlobalSectionAcrossIncludes(t *testing.T) {
	dir := t.TempDir()
	child := "User childuser\nHost child\n    HostName child.example.com\n"
	main := "Include child.conf\nPort 2022\nHost main\n    HostName main.example.com\n"
	if err := os.WriteFile(filepath.Join(dir, "child.conf"), []byte(child), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Parse(filepath.Join(dir, "config"), DefaultLimits)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(result.Hosts) != 2 {
		t.Fatalf("unexpected hosts: %+v", result.Hosts)
	}
	for _, host := range result.Hosts {
		if host.Username != "childuser" || host.Port != 2022 {
			t.Errorf("host %+v, want global User and Port from both files", host)
		}
	}
}

func TestHostPatternsMatch(t *testing.T) {
	cases := []struct {
		patterns []string
		alias    string
		want     bool
	}{
		{[]string{"*"}, "web", true},
		{[]string{"web-*"}, "web-1", true},
		{[]string{"web-*"}, "web", false},
		{[]string{"db-?"}, "db-1", true},
		{[]string{"db-?"}, "db-12", false},
		{[]string{"*", "!bastion"}, "bastion", false},
		{[]string{"!bastion", "*"}, "bastion", false},
		{[]string{"*", "!bastion"}, "web", true},
		{[]string{"!bastion"}, "web", false},
		{[]string{"*.example.com"}, "a.example.com", true},
		{[]string{"a*b*c"}, "aXXbYc", true},
		{[]string{"a*b*c"}, "aXXbY", false},
	}
	for _, tc := range cases {
		if got := hostPatternsMatch(tc.patterns, tc.alias); got != tc.want {
			t.Errorf("hostPatternsMatch(%v, %q) = %v, want %v", tc.patterns, tc.alias, got, tc.want)
		}
	}
}

func TestExpandHostnameTokens(t *testing.T) {
	cases := []struct {
		value       string
		want        string
		unsupported []string
	}{
		{"%h.example.com", "web.example.com", nil},
		{"100%%", "100%", nil},
		{"%h-%n", "web-%n", []string{"%n"}},
		{"plain", "plain", nil},
		{"trailing%", "trailing%", nil},
	}
	for _, tc := range cases {
		got, unsupported := expandHostnameTokens(tc.value, "web")
		if got != tc.want || strings.Join(unsupported, ",") != strings.Join(tc.unsupported, ",") {
			t.Errorf("expandHostnameTokens(%q) = %q %v, want %q %v", tc.value, got, unsupported, tc.want, tc.unsupported)
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
	want := filepath.Join(home, "x")
	for _, path := range []string{"~/x", "%d/x", filepath.Join("~", "x"), filepath.Join("%d", "x"), `"~/x"`} {
		if got := expandPath(path); got != want {
			t.Errorf("expandPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestExpandPathUnixPreservesExpandedForm(t *testing.T) {
	if filepath.Separator == '\\' {
		t.Skip("requires Unix-style path separators")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	for _, test := range []struct {
		path string
		want string
	}{
		{"%d/./x", home + "/./x"},
		{"%d//x", home + "//x"},
		{"%d/../x", home + "/../x"},
		{"%d/link/../x", home + "/link/../x"},
		{`~\x`, `~\x`},
	} {
		if got := expandPath(test.path); got != test.want {
			t.Errorf("expandPath(%q) = %q, want original expanded form %q", test.path, got, test.want)
		}
	}
}

func TestExpandPathWindowsNormalizesOnlySeparators(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("requires Windows path separators")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	normalizedHome := filepath.FromSlash(home)
	for _, test := range []struct {
		path string
		want string
	}{
		{"%d/x", normalizedHome + `\x`},
		{"%d/./x", normalizedHome + `\.\x`},
		{"%d//x", normalizedHome + `\\x`},
		{"%d/../x", normalizedHome + `\..\x`},
		{"%d/link/../x", normalizedHome + `\link\..\x`},
		{`~\x`, filepath.Join(home, "x")},
		{`"~\x"`, filepath.Join(home, "x")},
	} {
		if got := expandPath(test.path); got != test.want {
			t.Errorf("expandPath(%q) = %q, want separator-only normalization %q", test.path, got, test.want)
		}
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
