package ssh

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

type gzipDeleteFailure struct {
	err error
}

func (f gzipDeleteFailure) Delete(context.Context, string, bool) error {
	return f.err
}

func TestGzipCleanupFailuresAreJoinedAndVisible(t *testing.T) {
	local := t.TempDir()
	if err := os.WriteFile(filepath.Join(local, "not-empty"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	remoteErr := errors.New("remote gzip cleanup failed")
	err := cleanupGzipUpload(gzipDeleteFailure{err: remoteErr}, local, "/tmp/helper.gz")
	if !errors.Is(err, remoteErr) {
		t.Fatalf("gzip cleanup error = %v, want remote cause", err)
	}
	if !hasTransferStage(err, TransferStageCleanupGzip) {
		t.Fatalf("gzip cleanup error lacks stage: %v", err)
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("gzip cleanup error lacks local removal failure: %v", err)
	}
}

func TestGzipCleanupIgnoresAlreadyRemovedArtifacts(t *testing.T) {
	if err := cleanupGzipUpload(gzipDeleteFailure{err: fs.ErrNotExist}, filepath.Join(t.TempDir(), "missing"), "/tmp/missing.gz"); err != nil {
		t.Fatalf("already removed gzip artifacts produced an error: %v", err)
	}
}
