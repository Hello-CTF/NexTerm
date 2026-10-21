//go:build windows

package sshconfig

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// A one-shot identity-key read holds the key open across its stat and bounded
// read. With a plain os.Open — the mode readKeyFileBounded used before the
// shared-delete fix — the handle shares read and write but not delete, so a
// concurrent key removal or rename-replace rotation fails with a sharing
// violation for as long as the read is in flight. This pins that conflict at
// the identity-key level with throwaway key material: both operations must
// fail while the plain-open handle is alive and succeed once it closes, so
// the failures are attributable to the handle's sharing mode, not the
// filesystem.
func TestOneShotKeyReadPlainOpenBlocksRemovalAndRotation(t *testing.T) {
	dir := t.TempDir()
	path, _ := writeTestKey(t, dir, "id_ed25519", false)

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("plain open key: %v", err)
	}
	if err := os.Remove(path); err == nil {
		file.Close()
		t.Fatal("remove succeeded while a plain-open key read handle was open; expected a sharing violation")
	}
	rotated, _ := writeTestKey(t, dir, "id_ed25519.new", false)
	if err := os.Rename(rotated, path); err == nil {
		file.Close()
		t.Fatal("rotation rename succeeded while a plain-open key read handle was open; expected a sharing violation")
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close plain-open handle: %v", err)
	}
	if err := os.Rename(rotated, path); err != nil {
		t.Fatalf("rotation rename after the read handle closed: %v", err)
	}
}

// The fixed one-shot read opens keys through openSharedDelete. A concurrent
// removal then succeeds immediately instead of failing with a sharing
// violation, and the in-flight read keeps reading the pre-removal snapshot
// through its handle — the Unix unlink-while-open semantics the fingerprint
// path relies on.
func TestOneShotKeyReadSharedDeleteAllowsRemoval(t *testing.T) {
	dir := t.TempDir()
	path, fingerprint := writeTestKey(t, dir, "id_ed25519", false)

	file, err := openSharedDelete(path)
	if err != nil {
		t.Fatalf("openSharedDelete key: %v", err)
	}
	defer file.Close()
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove while one-shot key read handle open: %v", err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read through open handle after removal: %v", err)
	}
	gotFingerprint, keyType, err := FingerprintPrivateKey(data)
	if err != nil {
		t.Fatalf("fingerprint snapshot after removal: %v", err)
	}
	if gotFingerprint != fingerprint || keyType != "ssh-ed25519" {
		t.Fatalf("snapshot fingerprint = %q (%s), want %q (ssh-ed25519)", gotFingerprint, keyType, fingerprint)
	}
}

// A rename-replace rotation (write the new key, rename it over the old path)
// racing the one-shot read must likewise succeed once the read shares delete.
// The in-flight read keeps the pre-rotation snapshot pinned by its handle,
// while the next one-shot read through InspectKeyFile observes the rotated
// key.
func TestOneShotKeyReadSharedDeleteAllowsRotation(t *testing.T) {
	dir := t.TempDir()
	path, oldFingerprint := writeTestKey(t, dir, "id_ed25519", false)
	rotated, newFingerprint := writeTestKey(t, dir, "id_ed25519.new", false)

	file, err := openSharedDelete(path)
	if err != nil {
		t.Fatalf("openSharedDelete key: %v", err)
	}
	if err := os.Rename(rotated, path); err != nil {
		file.Close()
		t.Fatalf("rotation rename while one-shot key read handle open: %v", err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		file.Close()
		t.Fatalf("read through open handle after rotation: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close read handle: %v", err)
	}
	gotFingerprint, _, err := FingerprintPrivateKey(data)
	if err != nil {
		t.Fatalf("fingerprint snapshot after rotation: %v", err)
	}
	if gotFingerprint != oldFingerprint {
		t.Fatalf("in-flight read fingerprint = %q, want pre-rotation snapshot %q", gotFingerprint, oldFingerprint)
	}
	info, err := InspectKeyFile(filepath.Join(dir, "id_ed25519"))
	if err != nil {
		t.Fatalf("InspectKeyFile after rotation: %v", err)
	}
	if info.Fingerprint != newFingerprint {
		t.Fatalf("post-rotation InspectKeyFile fingerprint = %q, want %q", info.Fingerprint, newFingerprint)
	}
}
