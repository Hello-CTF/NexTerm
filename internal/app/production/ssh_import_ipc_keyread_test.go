package production

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

// writeSSHImportTestKey writes throwaway in-test key material and returns the
// path, PEM bytes and SHA256 fingerprint. No real private key is ever read.
func writeSSHImportTestKey(t *testing.T, dir, name string) (string, []byte, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	data := pem.EncodeToMemory(block)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return path, data, gossh.FingerprintSHA256(sshPub)
}

// The production import fingerprint reader keeps its bounded-read contract on
// the shared-delete open: a regular key file reads back exactly, while an
// oversized file, a non-regular path and a missing path are rejected with the
// existing production errors.
func TestProductionReadKeyFileBounded(t *testing.T) {
	dir := t.TempDir()
	path, data, fingerprint := writeSSHImportTestKey(t, dir, "id_ed25519")

	got, err := productionReadKeyFileBounded(path)
	if err != nil {
		t.Fatalf("productionReadKeyFileBounded: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("read %d bytes, want the %d written key bytes", len(got), len(data))
	}
	if fp, err := productionPEMFingerprint(got, nil); err != nil || fp != fingerprint {
		t.Fatalf("fingerprint of read key = %q, %v; want %q", fp, err, fingerprint)
	}

	oversized := filepath.Join(dir, "oversized_key")
	if err := os.WriteFile(oversized, bytes.Repeat([]byte("x"), maxImportKeyFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := productionReadKeyFileBounded(oversized); err == nil || !strings.Contains(err.Error(), "exceeds size limit") {
		t.Fatalf("oversized read error = %v, want size-limit rejection", err)
	}

	if _, err := productionReadKeyFileBounded(dir); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("directory read error = %v, want non-regular rejection", err)
	}

	missing := filepath.Join(dir, "missing_key")
	if _, err := productionReadKeyFileBounded(missing); err == nil {
		t.Fatal("missing key read succeeded, want lstat error")
	}
}
