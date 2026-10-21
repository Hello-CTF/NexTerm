package chunked_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
)

// A resumed upload must not commit staged chunks the current session never
// presented: without a trusted content identity, every chunk has to be
// re-presented (matching chunks deduplicate) before Commit succeeds.
func TestCommitRequiresSessionConfirmedChunks(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	destination := fslocal.New()
	first := mustNewUpload(t, testUploadID, remote, 8, 4)
	writeChunk(t, first, destination, 0, "abcd")
	writeChunk(t, first, destination, 1, "efgh")

	second := mustNewUpload(t, testUploadID, remote, 8, 4)
	status, err := second.Resume(context.Background(), destination)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if status.Received != 8 || len(status.Chunks) != 2 {
		t.Fatalf("resume status = %+v", status)
	}
	if _, err := second.Commit(context.Background(), destination); err == nil {
		t.Fatal("commit succeeded with session-unconfirmed staged chunks")
	}
	writeChunk(t, second, destination, 0, "abcd")
	if _, err := second.Commit(context.Background(), destination); err == nil {
		t.Fatal("commit succeeded with a partially confirmed staged set")
	}
	writeChunk(t, second, destination, 1, "efgh")
	committed, err := second.Commit(context.Background(), destination)
	if err != nil || committed != 8 {
		t.Fatalf("Commit = %d, %v", committed, err)
	}
	data, err := os.ReadFile(remote)
	if err != nil || string(data) != "abcdefgh" {
		t.Fatalf("committed target = %q, %v", data, err)
	}
	assertNoUploadArtifacts(t, remote)
}
