//go:build windows

package sshconfig

import (
	"io"
	"os"
	"testing"
)

// A one-shot identity-key read holds the key open across its stat and bounded
// read. With a plain os.Open — the mode readKeyFileBounded used before the
// shared-delete fix — the handle shares read and write but not delete, so a
// concurrent key removal or rename-away (the first step of a Windows key
// rotation) fails for as long as the read is in flight. This pins that
// conflict at the identity-key level with throwaway key material: both
// operations must fail while the plain-open handle is alive and succeed once
// it closes, so the failures are attributable to the handle's sharing mode,
// not the filesystem. The rotation leg deliberately uses rename-away rather
// than a single-step rename-replace: Windows refuses the latter over any open
// handle regardless of sharing mode (see
// TestOneShotKeyReadSharedDeleteSingleStepReplaceBlocked).
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
	rotatedAway := path + ".old"
	if err := os.Rename(path, rotatedAway); err == nil {
		file.Close()
		t.Fatal("rename-away succeeded while a plain-open key read handle was open; expected a sharing violation")
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close plain-open handle: %v", err)
	}
	if err := os.Rename(path, rotatedAway); err != nil {
		t.Fatalf("rename-away after the read handle closed: %v", err)
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

// A Windows key rotation renames the old key aside and then moves the new key
// into the vacated name; both steps must succeed while the one-shot read is
// in flight once the handle shares delete. The in-flight read keeps the
// pre-rotation snapshot pinned by its handle, while the next one-shot read
// through InspectKeyFile observes the rotated key.
func TestOneShotKeyReadSharedDeleteAllowsRotation(t *testing.T) {
	dir := t.TempDir()
	path, oldFingerprint := writeTestKey(t, dir, "id_ed25519", false)
	rotated, newFingerprint := writeTestKey(t, dir, "id_ed25519.new", false)

	file, err := openSharedDelete(path)
	if err != nil {
		t.Fatalf("openSharedDelete key: %v", err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		file.Close()
		t.Fatalf("rename old key aside while one-shot read handle open: %v", err)
	}
	if err := os.Rename(rotated, path); err != nil {
		file.Close()
		t.Fatalf("rename new key into place while one-shot read handle open: %v", err)
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
	info, err := InspectKeyFile(path)
	if err != nil {
		t.Fatalf("InspectKeyFile after rotation: %v", err)
	}
	if info.Fingerprint != newFingerprint {
		t.Fatalf("post-rotation InspectKeyFile fingerprint = %q, want %q", info.Fingerprint, newFingerprint)
	}
}

// Windows refuses MoveFileEx with MOVEFILE_REPLACE_EXISTING over a file that
// has any open handle, even when every handle shares delete — a platform
// limitation no reader-side sharing mode can lift, confirmed by a native
// stdlib probe (removal, rename-away, and the two-step rotation above all
// succeed; the single-step replace fails with ERROR_ACCESS_DENIED). Rotation
// writers on Windows must therefore move the old name aside before renaming
// the new key in. This deterministic negative control pins that boundary so
// the two-step rotation contract is not read as covering single-step replace;
// it must never be skipped or weakened into a success assertion.
func TestOneShotKeyReadSharedDeleteSingleStepReplaceBlocked(t *testing.T) {
	dir := t.TempDir()
	path, _ := writeTestKey(t, dir, "id_ed25519", false)
	rotated, _ := writeTestKey(t, dir, "id_ed25519.new", false)

	file, err := openSharedDelete(path)
	if err != nil {
		t.Fatalf("openSharedDelete key: %v", err)
	}
	if err := os.Rename(rotated, path); err == nil {
		file.Close()
		t.Fatal("single-step replace succeeded over an open key handle; Windows refuses MoveFileEx REPLACE_EXISTING on open files regardless of sharing mode")
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close read handle: %v", err)
	}
	if err := os.Rename(rotated, path); err != nil {
		t.Fatalf("single-step replace after the read handle closed: %v", err)
	}
}
