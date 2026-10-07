package sshconfig

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func scanHomeKeys(t *testing.T, dir string) []ScannedKey {
	t.Helper()
	keys, _, err := ScanHome(dir, Limits{})
	if err != nil {
		t.Fatalf("ScanHome: %v", err)
	}
	return keys
}

func TestScanHomeFindsKeysAndSkipsNoise(t *testing.T) {
	dir := t.TempDir()
	keyPath, fingerprint := writeTestKey(t, dir, "id_ed25519", false)
	for name, content := range map[string]string{
		"known_hosts":     "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n",
		"authorized_keys": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA demo\n",
		"config":          "Host example\n  HostName example.com\n",
		"notes.txt":       "not a key at all\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "config.d"), 0o700); err != nil {
		t.Fatal(err)
	}

	keys, diagnostics, err := ScanHome(dir, Limits{})
	if err != nil {
		t.Fatalf("ScanHome: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("keys = %+v, want exactly id_ed25519", keys)
	}
	key := keys[0]
	if key.Path != keyPath || key.Name != "id_ed25519" || key.Fingerprint != fingerprint || key.KeyType != "ssh-ed25519" || key.Encrypted {
		t.Fatalf("scanned key = %+v", key)
	}
	foundUnsupported := false
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "unsupported-file" && strings.HasSuffix(diagnostic.Source, "notes.txt") {
			foundUnsupported = true
		}
		if strings.Contains(diagnostic.Message, "PRIVATE KEY") {
			t.Fatalf("diagnostic leaks key material: %+v", diagnostic)
		}
	}
	if !foundUnsupported {
		t.Fatalf("notes.txt should be diagnosed as unsupported-file, got %+v", diagnostics)
	}
	for _, name := range []string{"known_hosts", "authorized_keys", "config"} {
		for _, diagnostic := range diagnostics {
			if strings.HasSuffix(diagnostic.Source, name) {
				t.Fatalf("well-known file %s must be skipped silently: %+v", name, diagnostic)
			}
		}
	}
}

func TestScanHomeEncryptedOpenSSHKeyUsesEmbeddedPublicKey(t *testing.T) {
	dir := t.TempDir()
	keyPath, fingerprint, _ := generateTestKey(t, dir, "id_encrypted", true)
	keys := scanHomeKeys(t, dir)
	if len(keys) != 1 {
		t.Fatalf("keys = %+v, want the encrypted key", keys)
	}
	key := keys[0]
	if key.Path != keyPath || !key.Encrypted || key.Fingerprint != fingerprint {
		t.Fatalf("scanned key = %+v", key)
	}
}

func writeLegacyEncryptedRSA(t *testing.T, dir, name string) (string, ssh.PublicKey) {
	t.Helper()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	block, err := x509.EncryptPEMBlock(rand.Reader, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(rsaKey), []byte("pw"), x509.PEMCipherAES256)
	if err != nil {
		t.Fatalf("encrypt pem: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(&rsaKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return path, pub
}

func TestScanHomeEncryptedPEMKeyNeedsPubSibling(t *testing.T) {
	dir := t.TempDir()
	path, pub := writeLegacyEncryptedRSA(t, dir, "id_rsa_legacy")
	keys, diagnostics, err := ScanHome(dir, Limits{})
	if err != nil {
		t.Fatalf("ScanHome: %v", err)
	}
	if len(keys) != 0 || !hasDiagnostic(diagnostics, "encrypted-key-unreadable") {
		t.Fatalf("keys = %+v, diagnostics = %+v", keys, diagnostics)
	}

	if err := os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(pub), 0o644); err != nil {
		t.Fatal(err)
	}
	keys, _, err = ScanHome(dir, Limits{})
	if err != nil {
		t.Fatalf("ScanHome: %v", err)
	}
	if len(keys) != 1 || !keys[0].Encrypted || keys[0].Fingerprint != ssh.FingerprintSHA256(pub) {
		t.Fatalf("keys = %+v", keys)
	}
}

func TestInspectKeyFileLegacyPEMEncrypted(t *testing.T) {
	dir := t.TempDir()
	path, pub := writeLegacyEncryptedRSA(t, dir, "id_rsa_legacy")
	if _, err := InspectKeyFile(path); err == nil {
		t.Fatal("legacy encrypted key without .pub must fail inspection")
	}
	if err := os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(pub), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := InspectKeyFile(path)
	if err != nil {
		t.Fatalf("InspectKeyFile with .pub fallback: %v", err)
	}
	if info.Fingerprint != ssh.FingerprintSHA256(pub) || !info.Encrypted {
		t.Fatalf("info = %+v", info)
	}
}

func TestScanHomeOversizedFileSkipped(t *testing.T) {
	dir := t.TempDir()
	big := make([]byte, maxKeyFileBytes+1)
	copy(big, "-----BEGIN OPENSSH PRIVATE KEY-----\n")
	if err := os.WriteFile(filepath.Join(dir, "id_big"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	keys, diagnostics, err := ScanHome(dir, Limits{})
	if err != nil {
		t.Fatalf("ScanHome: %v", err)
	}
	if len(keys) != 0 || !hasDiagnostic(diagnostics, "key-unreadable") {
		t.Fatalf("keys = %+v, diagnostics = %+v", keys, diagnostics)
	}
}

func TestPreviewHomeMapsConfigAndPlansKeys(t *testing.T) {
	dir := t.TempDir()
	keyPath, keyFingerprint := writeTestKey(t, dir, "id_ed25519", false)
	config := "Host web\n  HostName web.example.com\n  User deploy\n  IdentityFile " + keyPath + "\n\n" +
		"Host db\n  HostName db.example.com\n  User postgres\n"
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	preview, err := PreviewHome(dir, nil, nil, Limits{})
	if err != nil {
		t.Fatalf("PreviewHome: %v", err)
	}
	if preview.Source != "ssh-home" {
		t.Fatalf("source = %q", preview.Source)
	}
	if len(preview.Hosts) != 2 {
		t.Fatalf("hosts = %+v", preview.Hosts)
	}
	web := preview.Hosts[0]
	if web.Alias != "web" || web.Action != PlanAdd || web.AuthMethod != "key" || len(web.IdentityFiles) != 1 || web.IdentityFiles[0] != keyPath {
		t.Fatalf("web host = %+v", web)
	}
	if preview.Hosts[1].Action != PlanAdd || preview.Hosts[1].AuthMethod != "" {
		t.Fatalf("db host = %+v", preview.Hosts[1])
	}
	if len(preview.Keys) != 1 {
		t.Fatalf("keys = %+v", preview.Keys)
	}
	key := preview.Keys[0]
	if key.Action != PlanAdd || key.Fingerprint != keyFingerprint || key.Path != keyPath || key.Source != "ssh-home" || len(key.Aliases) != 1 || key.Aliases[0] != "id_ed25519" {
		t.Fatalf("key preview = %+v", key)
	}
}

func TestPreviewHomeDuplicateAndConflictPlanning(t *testing.T) {
	dir := t.TempDir()
	_, fingerprint, _ := generateTestKey(t, dir, "id_ed25519", false)
	data, err := os.ReadFile(filepath.Join(dir, "id_ed25519"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "id_ed25519_copy"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	preview, err := PreviewHome(dir, nil, nil, Limits{})
	if err != nil {
		t.Fatalf("PreviewHome: %v", err)
	}
	if len(preview.Keys) != 2 {
		t.Fatalf("keys = %+v", preview.Keys)
	}
	if preview.Keys[0].Action != PlanAdd || preview.Keys[0].Fingerprint != fingerprint {
		t.Fatalf("first key = %+v", preview.Keys[0])
	}
	if preview.Keys[1].Action != PlanSkipDuplicate {
		t.Fatalf("same-fingerprint copy = %+v, want skip-duplicate", preview.Keys[1])
	}

	existingKeys := []ExistingKey{{Name: "id_ed25519", Fingerprint: fingerprint}}
	preview, err = PreviewHome(dir, nil, existingKeys, Limits{})
	if err != nil {
		t.Fatalf("PreviewHome: %v", err)
	}
	if preview.Keys[0].Action != PlanSkipDuplicate {
		t.Fatalf("existing fingerprint key = %+v", preview.Keys[0])
	}

	existingKeys = []ExistingKey{{Name: "id_ed25519", Fingerprint: "SHA256:different"}}
	preview, err = PreviewHome(dir, nil, existingKeys, Limits{})
	if err != nil {
		t.Fatalf("PreviewHome: %v", err)
	}
	if preview.Keys[0].Action != PlanConflictAlias {
		t.Fatalf("name conflict key = %+v, want conflict-alias", preview.Keys[0])
	}
}

func TestPreviewHomeMissingDir(t *testing.T) {
	preview, err := PreviewHome(filepath.Join(t.TempDir(), "missing"), nil, nil, Limits{})
	if err != nil {
		t.Fatalf("PreviewHome: %v", err)
	}
	if len(preview.Hosts) != 0 || len(preview.Keys) != 0 || !hasDiagnostic(preview.Diagnostics, "home-missing") {
		t.Fatalf("preview = %+v", preview)
	}
}

func TestPreviewHomeNeverContainsKeyMaterial(t *testing.T) {
	dir := t.TempDir()
	keyPath, _ := writeTestKey(t, dir, "id_ed25519", false)
	encPath, _, _ := generateTestKey(t, dir, "id_enc", true)
	config := "Host web\n  HostName web.example.com\n  IdentityFile " + keyPath + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewHome(dir, nil, nil, Limits{})
	if err != nil {
		t.Fatalf("PreviewHome: %v", err)
	}
	raw, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(raw)
	if strings.Contains(serialized, "PRIVATE KEY") {
		t.Fatal("preview JSON contains PEM armor")
	}
	for _, path := range []string{keyPath, encPath} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if len(trimmed) >= 24 && !strings.HasPrefix(trimmed, "-----") {
				if strings.Contains(serialized, trimmed[:24]) {
					t.Fatalf("preview JSON leaks key body fragment %q", trimmed[:24])
				}
			}
		}
	}
}
