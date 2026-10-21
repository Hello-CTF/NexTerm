//go:build windows

package production

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/sshconfig"
)

// The production SSH import fingerprint reader (productionReadKeyFileBounded)
// held keys with a plain os.Open before the shared-delete fix: read/write
// sharing but no delete sharing on Windows, so a concurrent key removal or
// rename-away — the first step of a Windows key rotation — failed for as long
// as the read was in flight, the same conflict the one-shot sshconfig key
// read fixed. This pins that conflict at the production reader level with
// throwaway key material: both operations must fail while the plain-open
// handle is alive and succeed once it closes, so the failures are attributable
// to the handle's sharing mode, not the filesystem. The rotation leg
// deliberately uses rename-away rather than a single-step rename-replace:
// Windows refuses the latter over any open handle regardless of sharing mode
// (see TestProductionKeyReadSharedDeleteSingleStepReplaceBlocked).
func TestProductionKeyReadPlainOpenBlocksRemovalAndRotation(t *testing.T) {
	dir := t.TempDir()
	path, _, _ := writeSSHImportTestKey(t, dir, "id_ed25519")

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("plain open key: %v", err)
	}
	if err := os.Remove(path); err == nil {
		file.Close()
		t.Fatal("remove succeeded while a plain-open production key read handle was open; expected a sharing violation")
	}
	rotatedAway := path + ".old"
	if err := os.Rename(path, rotatedAway); err == nil {
		file.Close()
		t.Fatal("rename-away succeeded while a plain-open production key read handle was open; expected a sharing violation")
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close plain-open handle: %v", err)
	}
	if err := os.Rename(path, rotatedAway); err != nil {
		t.Fatalf("rename-away after the read handle closed: %v", err)
	}
}

// The fixed production reader opens keys through sshconfig.OpenSharedDelete. A
// concurrent removal then succeeds immediately instead of failing with a
// sharing violation, and the in-flight read keeps reading the pre-removal
// snapshot through its handle — the Unix unlink-while-open semantics the
// import fingerprint path relies on.
func TestProductionKeyReadSharedDeleteAllowsRemoval(t *testing.T) {
	dir := t.TempDir()
	path, data, fingerprint := writeSSHImportTestKey(t, dir, "id_ed25519")

	file, err := sshconfig.OpenSharedDelete(path)
	if err != nil {
		t.Fatalf("OpenSharedDelete key: %v", err)
	}
	defer file.Close()
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove while production key read handle open: %v", err)
	}
	snapshot, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read through open handle after removal: %v", err)
	}
	if !bytes.Equal(snapshot, data) {
		t.Fatal("snapshot after removal differs from the written key bytes")
	}
	if gotFingerprint, err := productionPEMFingerprint(snapshot, nil); err != nil || gotFingerprint != fingerprint {
		t.Fatalf("snapshot fingerprint = %q, %v; want %q", gotFingerprint, err, fingerprint)
	}
}

// A Windows key rotation renames the old key aside and then moves the new key
// into the vacated name; both steps must succeed while the production read is
// in flight once the handle shares delete. The in-flight read keeps the
// pre-rotation snapshot pinned by its handle, while the next bounded read
// through productionReadKeyFileBounded observes the rotated key.
func TestProductionKeyReadSharedDeleteAllowsRotation(t *testing.T) {
	dir := t.TempDir()
	path, oldData, oldFingerprint := writeSSHImportTestKey(t, dir, "id_ed25519")
	rotated, newData, newFingerprint := writeSSHImportTestKey(t, dir, "id_ed25519.new")

	file, err := sshconfig.OpenSharedDelete(path)
	if err != nil {
		t.Fatalf("OpenSharedDelete key: %v", err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		file.Close()
		t.Fatalf("rename old key aside while production read handle open: %v", err)
	}
	if err := os.Rename(rotated, path); err != nil {
		file.Close()
		t.Fatalf("rename new key into place while production read handle open: %v", err)
	}
	snapshot, err := io.ReadAll(file)
	if err != nil {
		file.Close()
		t.Fatalf("read through open handle after rotation: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close read handle: %v", err)
	}
	if !bytes.Equal(snapshot, oldData) {
		t.Fatal("in-flight read differs from the pre-rotation key bytes")
	}
	if gotFingerprint, err := productionPEMFingerprint(snapshot, nil); err != nil || gotFingerprint != oldFingerprint {
		t.Fatalf("in-flight fingerprint = %q, %v; want pre-rotation %q", gotFingerprint, err, oldFingerprint)
	}
	data, err := productionReadKeyFileBounded(path)
	if err != nil {
		t.Fatalf("productionReadKeyFileBounded after rotation: %v", err)
	}
	if !bytes.Equal(data, newData) {
		t.Fatal("post-rotation bounded read differs from the rotated key bytes")
	}
	if gotFingerprint, err := productionPEMFingerprint(data, nil); err != nil || gotFingerprint != newFingerprint {
		t.Fatalf("post-rotation fingerprint = %q, %v; want %q", gotFingerprint, err, newFingerprint)
	}
}

// Windows refuses MoveFileEx with MOVEFILE_REPLACE_EXISTING over a file that
// has any open handle, even when every handle shares delete — a platform
// limitation no reader-side sharing mode can lift, confirmed by a native
// stdlib probe (removal, rename-away, and the two-step rotation above all
// succeed; the single-step replace fails with ERROR_ACCESS_DENIED). Rotation
// writers on Windows must therefore move the old name aside before renaming
// the new key in. This deterministic negative control pins that boundary for
// the production reader's open so the two-step rotation contract is not read
// as covering single-step replace; it must never be skipped or weakened into
// a success assertion.
func TestProductionKeyReadSharedDeleteSingleStepReplaceBlocked(t *testing.T) {
	dir := t.TempDir()
	path, _, _ := writeSSHImportTestKey(t, dir, "id_ed25519")
	rotated, _, _ := writeSSHImportTestKey(t, dir, "id_ed25519.new")

	file, err := sshconfig.OpenSharedDelete(path)
	if err != nil {
		t.Fatalf("OpenSharedDelete key: %v", err)
	}
	if err := os.Rename(rotated, path); err == nil {
		file.Close()
		t.Fatal("single-step replace succeeded over an open production key handle; Windows refuses MoveFileEx REPLACE_EXISTING on open files regardless of sharing mode")
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close read handle: %v", err)
	}
	if err := os.Rename(rotated, path); err != nil {
		t.Fatalf("single-step replace after the read handle closed: %v", err)
	}
}
