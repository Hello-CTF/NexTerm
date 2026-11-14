package production

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/fs/conditional"
	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func expectedVersion(content string) (*int64, string) {
	size := int64(len(content))
	sum := sha256.Sum256([]byte(content))
	return &size, hex.EncodeToString(sum[:])
}

// A save from an editor replaces the file only while it is still the version
// the editor read; a file changed meanwhile is reported as a conflict.
func TestWriteFileExpectedRefusesAChangedFile(t *testing.T) {
	ctx := context.Background()
	target := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(target, []byte("opened"), 0o600); err != nil {
		t.Fatal(err)
	}
	size, sum := expectedVersion("opened")
	filesystem := fslocal.New()
	if err := writeFileExpected(ctx, filesystem, fsRequest{Path: target, ExpectedSize: size, ExpectedSHA256: sum}, []byte("saved")); err != nil {
		t.Fatalf("save over the opened version = %v", err)
	}
	if data, _ := os.ReadFile(target); string(data) != "saved" {
		t.Fatalf("content = %q", data)
	}
	err := writeFileExpected(ctx, filesystem, fsRequest{Path: target, ExpectedSize: size, ExpectedSHA256: sum}, []byte("stale"))
	if !errors.Is(err, conditional.ErrVersionMismatch) {
		t.Fatalf("save over a changed file = %v, want a version mismatch", err)
	}
	var appErr *ipc.Error
	if !errors.As(fsIPCError("ssh", err), &appErr) || appErr.Code != ipc.Code("conflict") {
		t.Fatalf("IPC error = %v, want a conflict", fsIPCError("ssh", err))
	}
	if data, _ := os.ReadFile(target); string(data) != "saved" {
		t.Fatalf("a refused save changed the file: %q", data)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := writeFileExpected(ctx, filesystem, fsRequest{Path: target, ExpectedSize: size, ExpectedSHA256: sum}, []byte("x")); !errors.Is(err, conditional.ErrVersionMismatch) {
		t.Fatalf("save over a deleted file = %v, want a version mismatch", err)
	}
}
