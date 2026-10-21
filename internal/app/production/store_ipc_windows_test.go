//go:build windows

package production

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/sshconfig"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// assetReadKeyFileDesktopRig registers the store commands the way the desktop
// assembly does (desktop=true), so dispatches exercise the production
// asset_read_key_file authorization gate and reader. The server-assembly
// rejection contract stays pinned by TestAssetReadKeyFileServerAssemblyRejected.
func assetReadKeyFileDesktopRig(t *testing.T) *ipc.Dispatcher {
	t.Helper()
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerStoreCommands(dispatcher, database, nil, t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

// The asset_read_key_file handler held keys with a plain os.Open before the
// shared-delete fix: read/write sharing but no delete sharing on Windows, so a
// concurrent key removal or rename-away — the first step of a Windows key
// rotation — failed for as long as the read was in flight, the same conflict
// the production SSH import key read fixed. This pins that conflict for the
// asset key read path with throwaway key material: both operations must fail
// while the plain-open handle is alive and succeed once it closes, so the
// failures are attributable to the handle's sharing mode, not the filesystem.
// The rotation leg deliberately uses rename-away rather than a single-step
// rename-replace: Windows refuses the latter over any open handle regardless
// of sharing mode (see TestAssetReadKeyFileSharedDeleteSingleStepReplaceBlocked).
func TestAssetReadKeyFilePlainOpenBlocksRemovalAndRotation(t *testing.T) {
	dir := t.TempDir()
	path, _, _ := writeSSHImportTestKey(t, dir, "id_ed25519")

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("plain open key: %v", err)
	}
	if err := os.Remove(path); err == nil {
		file.Close()
		t.Fatal("remove succeeded while a plain-open asset key read handle was open; expected a sharing violation")
	}
	rotatedAway := path + ".old"
	if err := os.Rename(path, rotatedAway); err == nil {
		file.Close()
		t.Fatal("rename-away succeeded while a plain-open asset key read handle was open; expected a sharing violation")
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close plain-open handle: %v", err)
	}
	if err := os.Rename(path, rotatedAway); err != nil {
		t.Fatalf("rename-away after the read handle closed: %v", err)
	}
}

// The fixed handler opens keys through sshconfig.OpenSharedDelete. A
// concurrent removal then succeeds immediately instead of failing with a
// sharing violation, and the in-flight read keeps reading the pre-removal
// snapshot through its handle — the Unix unlink-while-open semantics the key
// picker's read relies on. A later dispatch of asset_read_key_file on the
// removed path fails instead of serving stale content.
func TestAssetReadKeyFileSharedDeleteAllowsRemoval(t *testing.T) {
	dir := t.TempDir()
	path, data, _ := writeSSHImportTestKey(t, dir, "id_ed25519")
	dispatcher := assetReadKeyFileDesktopRig(t)

	file, err := sshconfig.OpenSharedDelete(path)
	if err != nil {
		t.Fatalf("OpenSharedDelete key: %v", err)
	}
	defer file.Close()
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove while asset key read handle open: %v", err)
	}
	snapshot, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read through open handle after removal: %v", err)
	}
	if !bytes.Equal(snapshot, data) {
		t.Fatal("snapshot after removal differs from the written key bytes")
	}
	response := dispatchStoreTest(dispatcher, "asset_read_key_file", `{"path":`+strconvQuote(path)+`}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeInternal {
		t.Fatalf("asset_read_key_file of removed key = %+v, want internal error", response)
	}
}

// A Windows key rotation renames the old key aside and then moves the new key
// into the vacated name; both steps must succeed while an asset key read is in
// flight once the handle shares delete. The in-flight read keeps the
// pre-rotation snapshot pinned by its handle, while the next
// asset_read_key_file dispatch observes the rotated key bytes exactly.
func TestAssetReadKeyFileSharedDeleteAllowsRotation(t *testing.T) {
	dir := t.TempDir()
	path, oldData, _ := writeSSHImportTestKey(t, dir, "id_ed25519")
	rotated, newData, _ := writeSSHImportTestKey(t, dir, "id_ed25519.new")
	dispatcher := assetReadKeyFileDesktopRig(t)

	file, err := sshconfig.OpenSharedDelete(path)
	if err != nil {
		t.Fatalf("OpenSharedDelete key: %v", err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		file.Close()
		t.Fatalf("rename old key aside while asset key read handle open: %v", err)
	}
	if err := os.Rename(rotated, path); err != nil {
		file.Close()
		t.Fatalf("rename new key into place while asset key read handle open: %v", err)
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
	response := dispatchStoreTest(dispatcher, "asset_read_key_file", `{"path":`+strconvQuote(path)+`}`)
	var content string
	requireStoreTestResponse(t, response, &content)
	if content != string(newData) {
		t.Fatal("post-rotation asset_read_key_file differs from the rotated key bytes")
	}
}

// Windows refuses MoveFileEx with MOVEFILE_REPLACE_EXISTING over a file that
// has any open handle, even when every handle shares delete — a platform
// limitation no reader-side sharing mode can lift, confirmed by a native
// stdlib probe (removal, rename-away, and the two-step rotation above all
// succeed; the single-step replace fails with ERROR_ACCESS_DENIED). Rotation
// writers on Windows must therefore move the old name aside before renaming
// the new key in. This deterministic negative control pins that boundary for
// the asset key read's open so the two-step rotation contract is not read as
// covering single-step replace; it must never be skipped or weakened into a
// success assertion.
func TestAssetReadKeyFileSharedDeleteSingleStepReplaceBlocked(t *testing.T) {
	dir := t.TempDir()
	path, _, _ := writeSSHImportTestKey(t, dir, "id_ed25519")
	rotated, _, _ := writeSSHImportTestKey(t, dir, "id_ed25519.new")

	file, err := sshconfig.OpenSharedDelete(path)
	if err != nil {
		t.Fatalf("OpenSharedDelete key: %v", err)
	}
	if err := os.Rename(rotated, path); err == nil {
		file.Close()
		t.Fatal("single-step replace succeeded over an open asset key handle; Windows refuses MoveFileEx REPLACE_EXISTING on open files regardless of sharing mode")
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close read handle: %v", err)
	}
	if err := os.Rename(rotated, path); err != nil {
		t.Fatalf("single-step replace after the read handle closed: %v", err)
	}
}
