package sshconfig

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/keys"
)

func clientFixture(parts ...string) string {
	return filepath.Join(append([]string{"..", "..", "testdata", "sshconfig", "clients"}, parts...)...)
}

type wantClientHost struct {
	alias, hostname, username, group, auth string
	port                                   int
	warning                                string
}

func checkClientHosts(t *testing.T, preview *ImportPreview, want []wantClientHost) {
	t.Helper()
	if len(preview.Hosts) != len(want) {
		t.Fatalf("hosts = %+v, want %d entries", preview.Hosts, len(want))
	}
	byAlias := map[string]HostPreview{}
	for _, host := range preview.Hosts {
		byAlias[host.Alias] = host
	}
	for _, w := range want {
		host, ok := byAlias[w.alias]
		if !ok {
			t.Fatalf("host %q missing from %+v", w.alias, preview.Hosts)
		}
		if host.Hostname != w.hostname || host.Port != w.port || host.Username != w.username || host.Group != w.group || host.AuthMethod != w.auth {
			t.Errorf("host %q = %s@%s:%d group %q auth %q, want %s@%s:%d group %q auth %q", w.alias,
				host.Username, host.Hostname, host.Port, host.Group, host.AuthMethod, w.username, w.hostname, w.port, w.group, w.auth)
		}
		if host.Source == "" {
			t.Errorf("host %q has no source", w.alias)
		}
		if w.warning != "" && !slices.ContainsFunc(host.Warnings, func(s string) bool { return strings.Contains(s, w.warning) }) {
			t.Errorf("host %q warnings %q lack %q", w.alias, host.Warnings, w.warning)
		}
	}
}

func diagnosticCodes(preview *ImportPreview) map[string]int {
	codes := map[string]int{}
	for _, diagnostic := range preview.Diagnostics {
		codes[diagnostic.Code]++
	}
	return codes
}

func requireDiagnostic(t *testing.T, preview *ImportPreview, code, fragment string) {
	t.Helper()
	for _, diagnostic := range preview.Diagnostics {
		if diagnostic.Code == code && strings.Contains(diagnostic.Message, fragment) {
			return
		}
	}
	t.Fatalf("no %s diagnostic containing %q in %+v", code, fragment, preview.Diagnostics)
}

func TestPreviewPuTTYRegExport(t *testing.T) {
	t.Parallel()
	preview, err := PreviewPuTTY(clientFixture("putty", "sessions.reg"), nil, nil, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Source != SourcePuTTY {
		t.Fatalf("source = %q", preview.Source)
	}
	checkClientHosts(t, preview, []wantClientHost{
		{alias: "Prod Web 01", hostname: "web01.example.com", port: 22, username: "deploy", warning: ".ppk"},
		{alias: "db via proxy", hostname: "db.internal", port: 2222, username: "admin", auth: "password", warning: "SOCKS 5 代理 socks.example.com:1080"},
		{alias: "nouser", hostname: "192.0.2.10", port: 22, auth: "password", warning: "未保存用户名"},
	})
	for _, host := range preview.Hosts {
		if len(host.IdentityFiles) != 0 {
			t.Errorf("host %q imports identity files %q; PPK keys must not be referenced", host.Alias, host.IdentityFiles)
		}
	}
	if len(preview.Keys) != 0 {
		t.Fatalf("keys = %+v, want none", preview.Keys)
	}
	requireDiagnostic(t, preview, "session-protocol-skipped", "telnet")
	requireDiagnostic(t, preview, "session-protocol-skipped", "serial")
	requireDiagnostic(t, preview, "template-skipped", "Default Settings")
	requireDiagnostic(t, preview, "password-not-imported", "PuTTY")
}

func TestParsePuTTYRegValues(t *testing.T) {
	t.Parallel()
	text := "REGEDIT4\n\n[HKEY_USERS\\S-1-5-21\\Software\\SimonTatham\\PuTTY\\Sessions\\a%2Fb]\n" +
		"\"HostName\"=\"h\\\"q\\\\x\"\n\"PortNumber\"=dword:0000000a\n\"Blob\"=hex:01,\\\n  02\n" +
		"[-HKEY_CURRENT_USER\\Software\\SimonTatham\\PuTTY\\Sessions\\gone]\n"
	sessions, err := ParsePuTTYReg(text, "x.reg")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Key != "a%2Fb" {
		t.Fatalf("sessions = %+v", sessions)
	}
	if got := sessions[0].Values["hostname"]; got != `h"q\x` {
		t.Fatalf("hostname = %q", got)
	}
	if got := sessions[0].Values["portnumber"]; got != "10" {
		t.Fatalf("port = %q", got)
	}
	result := PuTTYResult(sessions, Limits{})
	if len(result.Hosts) != 1 || result.Hosts[0].Alias != "a/b" {
		t.Fatalf("hosts = %+v, want the URL-decoded session name", result.Hosts)
	}
}

func TestPuTTYRegWithoutSessions(t *testing.T) {
	t.Parallel()
	_, err := ParsePuTTYReg("Windows Registry Editor Version 5.00\n\n[HKEY_CURRENT_USER\\Software\\Other]\n", "x.reg")
	if !errors.Is(err, ErrNoClientSessions) {
		t.Fatalf("err = %v, want ErrNoClientSessions", err)
	}
}

func TestPreviewMobaXtermBookmarks(t *testing.T) {
	t.Parallel()
	preview, err := PreviewMobaXterm(clientFixture("mobaxterm", "MobaXterm.ini"), nil, nil, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	checkClientHosts(t, preview, []wantClientHost{
		{alias: "Jump Box", hostname: "jump.example.com", port: 22, username: "ops", auth: "password"},
		{alias: "Production/Web/web01", hostname: "10.0.1.11", port: 2222, username: "deploy", group: "Production/Web", warning: "SSH 网关 jump.example.com"},
		{alias: "web02", hostname: "10.0.1.12", port: 22, username: "deploy", group: "Production/Web", warning: ".ppk"},
		{alias: "web03", hostname: "10.0.1.13", port: 22, username: "deploy", group: "Production/Web", warning: "路径变量"},
		{alias: "Staging/Web/web01", hostname: "10.1.1.11", port: 22, username: "deploy", group: "Staging/Web", auth: "password"},
	})
	// web01's key resolves below the home directory and does not exist there, so it is reported and dropped.
	if codes := diagnosticCodes(preview); codes["key-unreadable"] != 1 {
		t.Fatalf("diagnostics = %+v, want one unreadable key", preview.Diagnostics)
	}
	requireDiagnostic(t, preview, "session-protocol-skipped", "Office RDP")
	requireDiagnostic(t, preview, "password-not-imported", "MobaXterm")
}

func TestMobaXtermWithoutBookmarks(t *testing.T) {
	t.Parallel()
	_, err := ParseMobaXterm("[Misc]\nA=1\n", "MobaXterm.ini", Limits{})
	if !errors.Is(err, ErrNoClientSessions) {
		t.Fatalf("err = %v, want ErrNoClientSessions", err)
	}
}

func TestPreviewXshellSessions(t *testing.T) {
	t.Parallel()
	preview, err := PreviewXshell(clientFixture("xshell", "Sessions"), nil, nil, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	checkClientHosts(t, preview, []wantClientHost{
		{alias: "跳板机", hostname: "jump.example.com", port: 22, username: "ops", auth: "password", warning: `代理 "corp-proxy"`},
		{alias: "db01", hostname: "db01.example.com", port: 2200, username: "dba", group: "生产/数据库", warning: "id_rsa_2048"},
	})
	requireDiagnostic(t, preview, "session-protocol-skipped", "TELNET")
	requireDiagnostic(t, preview, "password-not-imported", "Xshell")
}

func TestPreviewSecureCRTSessions(t *testing.T) {
	t.Parallel()
	preview, err := PreviewSecureCRT(clientFixture("securecrt", "Sessions"), nil, nil, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	checkClientHosts(t, preview, []wantClientHost{
		{alias: "bastion", hostname: "bastion.example.com", port: 22, username: "ops", auth: "password"},
		// The Windows identity path does not exist here, so the host falls back to agent auth with a diagnostic.
		{alias: "web01", hostname: "web01.example.com", port: 2222, username: "deploy", group: "Production", warning: `"Session:bastion"`},
	})
	requireDiagnostic(t, preview, "key-unreadable", "")
	requireDiagnostic(t, preview, "session-protocol-skipped", "Telnet")
	requireDiagnostic(t, preview, "template-skipped", "Default")
	for _, diagnostic := range preview.Diagnostics {
		if strings.Contains(diagnostic.Source, "__FolderData__") {
			t.Fatalf("folder metadata reported as %+v", diagnostic)
		}
	}
}

// A SecureCRT identity file in OpenSSH format is planned as a key, like an OpenSSH IdentityFile.
func TestPreviewSecureCRTUsableIdentity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pair, err := keys.Generate(keys.Options{Algorithm: keys.AlgorithmEd25519})
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyPath, pair.PrivateKeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	sessions := filepath.Join(dir, "Sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	session := "S:\"Protocol Name\"=SSH2\nS:\"Hostname\"=app.example.com\nS:\"Username\"=app\n" +
		"D:\"[SSH2] Port\"=00000016\nS:\"Identity Filename V2\"=" + keyPath + "::rawkey\n"
	if err := os.WriteFile(filepath.Join(sessions, "app.ini"), []byte(session), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewSecureCRT(sessions, nil, []ExistingKey{}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Hosts) != 1 || preview.Hosts[0].AuthMethod != "key" || !slices.Equal(preview.Hosts[0].IdentityFiles, []string{keyPath}) {
		t.Fatalf("hosts = %+v", preview.Hosts)
	}
	if len(preview.Keys) != 1 || preview.Keys[0].Fingerprint != pair.Fingerprint || preview.Keys[0].Source != SourceSecureCRT {
		t.Fatalf("keys = %+v", preview.Keys)
	}
}

func TestClientFolderWithoutSessions(t *testing.T) {
	t.Parallel()
	if _, err := PreviewXshell(t.TempDir(), nil, nil, Limits{}); !errors.Is(err, ErrNoClientSessions) {
		t.Fatalf("Xshell err = %v, want ErrNoClientSessions", err)
	}
	if _, err := PreviewSecureCRT(clientFixture("xshell", "Sessions"), nil, nil, Limits{}); !errors.Is(err, ErrNoClientSessions) {
		t.Fatalf("SecureCRT err = %v, want ErrNoClientSessions", err)
	}
}

// Client sessions go through the same conflict planning as OpenSSH hosts.
func TestClientPreviewPlansConflicts(t *testing.T) {
	t.Parallel()
	existing := []ExistingAsset{
		{ID: "a1", Name: "bastion", Host: "bastion.example.com", Port: 22, Username: "ops"},
		{ID: "a2", Name: "old-web", Host: "web01.example.com", Port: 2222, Username: "deploy"},
	}
	preview, err := PreviewSecureCRT(clientFixture("securecrt", "Sessions"), existing, nil, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]PlanAction{}
	for _, host := range preview.Hosts {
		actions[host.Alias] = host.Action
	}
	if actions["bastion"] != PlanSkipDuplicate || actions["web01"] != PlanConflictEndpoint {
		t.Fatalf("actions = %v", actions)
	}
}

func TestClientHostLimitTruncates(t *testing.T) {
	t.Parallel()
	preview, err := PreviewMobaXterm(clientFixture("mobaxterm", "MobaXterm.ini"), nil, nil, Limits{MaxHosts: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Hosts) != 2 || !preview.Truncated || diagnosticCodes(preview)["limit-truncated"] != 1 {
		t.Fatalf("hosts %d truncated %v diagnostics %+v", len(preview.Hosts), preview.Truncated, preview.Diagnostics)
	}
}

func TestDecodeClientText(t *testing.T) {
	t.Parallel()
	utf16le := func(s string, bom bool) []byte {
		var out []byte
		if bom {
			out = append(out, 0xFF, 0xFE)
		}
		for _, r := range s {
			out = append(out, byte(r), byte(r>>8))
		}
		return out
	}
	cases := []struct {
		name string
		data []byte
	}{
		{"utf8", []byte("a=1\r\nb=2")},
		{"utf8 bom", append([]byte{0xEF, 0xBB, 0xBF}, "a=1\nb=2"...)},
		{"utf16le bom", utf16le("a=1\r\nb=2", true)},
		{"utf16le no bom", utf16le("a=1\r\nb=2", false)},
		{"utf16be bom", []byte{0xFE, 0xFF, 0, 'a', 0, '=', 0, '1', 0, '\n', 0, 'b', 0, '=', 0, '2'}},
	}
	for _, tc := range cases {
		if got := decodeClientText(tc.data); got != "a=1\nb=2" {
			t.Errorf("%s: %q", tc.name, got)
		}
	}
}

func TestClientWalkSkipsSymlinksAndCapsDepth(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	body, err := os.ReadFile(filepath.Join(clientFixture("xshell", "Sessions"), "跳板机.xsh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "linked.xsh"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "linked.xsh"), filepath.Join(dir, "linked.xsh")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := PreviewXshell(dir, nil, nil, Limits{}); !errors.Is(err, ErrNoClientSessions) {
		t.Fatalf("symlinked session was followed: err = %v", err)
	}
	deep := dir
	for i := 0; i < maxClientDepth+2; i++ {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "deep.xsh"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PreviewXshell(dir, nil, nil, Limits{}); !errors.Is(err, ErrNoClientSessions) {
		t.Fatalf("too-deep session was read: err = %v", err)
	}
}
