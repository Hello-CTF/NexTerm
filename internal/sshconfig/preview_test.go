package sshconfig

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func writeTestKey(t *testing.T, dir, name string, encrypted bool) (string, string) {
	t.Helper()
	path, fingerprint, authorizedKey := generateTestKey(t, dir, name, encrypted)
	if authorizedKey != "" {
		if err := os.WriteFile(path+".pub", []byte(authorizedKey), 0o644); err != nil {
			t.Fatalf("write pub key: %v", err)
		}
	}
	return path, fingerprint
}

func generateTestKey(t *testing.T, dir, name string, encrypted bool) (string, string, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	var block *pem.Block
	if encrypted {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("test-passphrase"))
	} else {
		block, err = ssh.MarshalPrivateKey(priv, "")
	}
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return path, ssh.FingerprintSHA256(sshPub), string(ssh.MarshalAuthorizedKey(sshPub))
}

func TestPreviewConflicts(t *testing.T) {
	result, err := Parse(filepath.Join("..", "..", "testdata", "sshconfig", "conflicts"), DefaultLimits)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !hasDiagnostic(result.Diagnostics, "duplicate-alias") {
		t.Errorf("missing duplicate-alias diagnostic: %+v", result.Diagnostics)
	}
	existing := []ExistingAsset{
		{Name: "web", Host: "web.example.com", Port: 22, Username: "deploy"},
		{Name: "db", Host: "old-db.example.com", Port: 3306, Username: "old"},
		{Name: "api-old", Host: "api.example.com", Port: 8080, Username: "svc"},
	}
	preview := PreviewSSHConfig(result, existing, nil, DefaultLimits)
	actions := map[string]PlanAction{}
	for _, host := range preview.Hosts {
		actions[host.Alias] = host.Action
	}
	if actions["web"] != PlanSkipDuplicate {
		t.Errorf("web action = %q, want skip-duplicate", actions["web"])
	}
	if actions["db"] != PlanConflictAlias {
		t.Errorf("db action = %q, want conflict-alias", actions["db"])
	}
	if actions["api"] != PlanConflictEndpoint {
		t.Errorf("api action = %q, want conflict-endpoint", actions["api"])
	}
	if actions["dupendpoint"] != PlanConflictEndpoint {
		t.Errorf("dupendpoint action = %q, want conflict-endpoint", actions["dupendpoint"])
	}
}

func TestPreviewProxyJump(t *testing.T) {
	result, err := Parse(filepath.Join("..", "..", "testdata", "sshconfig", "proxyjump"), DefaultLimits)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	existing := []ExistingAsset{{Name: "asset1", Host: "asset1.example.com", Port: 22}}
	preview := PreviewSSHConfig(result, existing, nil, DefaultLimits)
	warnings := map[string][]string{}
	actions := map[string]PlanAction{}
	for _, host := range preview.Hosts {
		warnings[host.Alias] = host.Warnings
		actions[host.Alias] = host.Action
	}
	for _, alias := range []string{"bastion", "app", "db", "nonejump", "external"} {
		if len(warnings[alias]) != 0 {
			t.Errorf("%s unexpected warnings: %v", alias, warnings[alias])
		}
		if actions[alias] != PlanAdd {
			t.Errorf("%s action = %q, want add", alias, actions[alias])
		}
	}
	if !hasWarning(warnings["loop1"], "cycle") || !hasWarning(warnings["loop2"], "cycle") {
		t.Errorf("loop cycle not detected: %v %v", warnings["loop1"], warnings["loop2"])
	}
	if !hasWarning(warnings["selfy"], "itself") {
		t.Errorf("self jump not detected: %v", warnings["selfy"])
	}
	if !hasWarning(warnings["dangling"], "ghost") {
		t.Errorf("dangling jump not detected: %v", warnings["dangling"])
	}
	if !hasWarning(warnings["ipv6"], "2001:db8::1") {
		t.Errorf("ipv6 dangling jump not detected: %v", warnings["ipv6"])
	}
	for _, alias := range []string{"loop1", "loop2", "selfy", "dangling", "ipv6"} {
		if actions[alias] != PlanBlockedJump {
			t.Errorf("%s action = %q, want blocked-jump", alias, actions[alias])
		}
	}
}

func TestPreviewBatchConflicts(t *testing.T) {
	result, err := Parse(filepath.Join("..", "..", "testdata", "sshconfig", "conflicts"), DefaultLimits)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	preview := PreviewSSHConfig(result, nil, nil, DefaultLimits)
	actions := map[string]PlanAction{}
	for _, host := range preview.Hosts {
		actions[host.Alias] = host.Action
	}
	if actions["api"] != PlanAdd {
		t.Errorf("api action = %q, want add", actions["api"])
	}
	if actions["dupendpoint"] != PlanConflictEndpoint {
		t.Errorf("dupendpoint action = %q, want conflict-endpoint", actions["dupendpoint"])
	}
	if actions["web"] != PlanAdd || actions["db"] != PlanAdd {
		t.Errorf("unexpected batch actions: %v", actions)
	}
}

func TestPreviewSSHConfigMaxKeys(t *testing.T) {
	dir := t.TempDir()
	keyPath1, _ := writeTestKey(t, dir, "key_one", false)
	keyPath2, _ := writeTestKey(t, dir, "key_two", false)
	configPath := filepath.Join(dir, "config")
	config := "Host a\n    HostName a.example.com\n    IdentityFile " + keyPath1 + "\n\nHost b\n    HostName b.example.com\n    IdentityFile " + keyPath2 + "\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	result, err := Parse(configPath, DefaultLimits)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	preview := PreviewSSHConfig(result, nil, nil, Limits{MaxKeys: 1})
	if len(preview.Keys) != 1 || !preview.Truncated {
		t.Errorf("got %d keys truncated=%v, want 1 true: %+v", len(preview.Keys), preview.Truncated, preview.Keys)
	}
	if !hasDiagnostic(preview.Diagnostics, "limit-truncated") {
		t.Errorf("missing limit-truncated diagnostic: %+v", preview.Diagnostics)
	}
}

func TestInspectKeyFileOversized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge_key")
	if err := os.WriteFile(path, make([]byte, maxKeyFileBytes+1), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if _, err := InspectKeyFile(path); err == nil {
		t.Fatal("expected error for oversized key file")
	}
	if _, err := InspectKeyFile(dir); err == nil {
		t.Fatal("expected error for non-regular key path")
	}
}

func TestInspectKeyFile(t *testing.T) {
	dir := t.TempDir()
	path, fingerprint := writeTestKey(t, dir, "id_ed25519", false)
	info, err := InspectKeyFile(path)
	if err != nil {
		t.Fatalf("InspectKeyFile: %v", err)
	}
	if info.Fingerprint != fingerprint || info.KeyType != "ssh-ed25519" || info.Name != "id_ed25519" {
		t.Errorf("unexpected info: %+v", info)
	}
}

func TestInspectEncryptedKeyViaPub(t *testing.T) {
	dir := t.TempDir()
	path, fingerprint, authorizedKey := generateTestKey(t, dir, "id_encrypted", true)
	if _, err := InspectKeyFile(path); err == nil {
		t.Fatal("expected error for encrypted key without .pub")
	}
	if err := os.WriteFile(path+".pub", []byte(authorizedKey), 0o644); err != nil {
		t.Fatalf("write pub key: %v", err)
	}
	info, err := InspectKeyFile(path)
	if err != nil {
		t.Fatalf("InspectKeyFile with .pub fallback: %v", err)
	}
	if info.Fingerprint != fingerprint {
		t.Errorf("fingerprint = %q, want %q", info.Fingerprint, fingerprint)
	}
}

func TestPlanConfigKeys(t *testing.T) {
	dir := t.TempDir()
	keyPath, fingerprint := writeTestKey(t, dir, "shared_key", false)
	configPath := filepath.Join(dir, "config")
	config := "Host a\n    HostName a.example.com\n    IdentityFile " + keyPath + "\n\nHost b\n    HostName b.example.com\n    IdentityFile " + keyPath + "\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	result, err := Parse(configPath, DefaultLimits)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	preview := PreviewSSHConfig(result, nil, nil, DefaultLimits)
	if len(preview.Keys) != 1 || preview.Keys[0].Fingerprint != fingerprint {
		t.Fatalf("unexpected keys: %+v", preview.Keys)
	}
	if preview.Keys[0].Action != PlanAdd {
		t.Errorf("action = %q, want add", preview.Keys[0].Action)
	}

	preview = PreviewSSHConfig(result, nil, []ExistingKey{{Name: "other", Fingerprint: fingerprint}}, DefaultLimits)
	if len(preview.Keys) != 1 || preview.Keys[0].Action != PlanSkipDuplicate {
		t.Errorf("existing fingerprint not deduped: %+v", preview.Keys)
	}

	preview = PreviewSSHConfig(result, nil, []ExistingKey{{Name: "shared_key", Fingerprint: "SHA256:other"}}, DefaultLimits)
	if len(preview.Keys) != 1 || preview.Keys[0].Action != PlanConflictAlias {
		t.Errorf("name conflict not planned: %+v", preview.Keys)
	}
}

func TestPreviewMissingKeys(t *testing.T) {
	result, err := Parse(filepath.Join("..", "..", "testdata", "sshconfig", "missing_keys"), DefaultLimits)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	preview := PreviewSSHConfig(result, nil, nil, DefaultLimits)
	if len(preview.Keys) != 0 {
		t.Errorf("unexpected keys: %+v", preview.Keys)
	}
	count := 0
	for _, d := range preview.Diagnostics {
		if d.Code == "key-unreadable" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("got %d key-unreadable diagnostics, want 2: %+v", count, preview.Diagnostics)
	}
}

func TestDedupeIdentityFiles(t *testing.T) {
	got := DedupeIdentityFiles([]string{"/a/b", "/a/./b", "/a/b", "", "/c"})
	if strings.Join(got, ",") != "/a/b,/c" {
		t.Errorf("DedupeIdentityFiles = %v", got)
	}
}

func hasWarning(warnings []string, substring string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, substring) {
			return true
		}
	}
	return false
}
