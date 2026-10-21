package chunked_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/fs/chunked"
	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
)

func mustNewUpload(t *testing.T, uploadID, remote string, total int64, chunkSize int) *chunked.Upload {
	t.Helper()
	upload, err := chunked.New(uploadID, remote, total, chunkSize)
	if err != nil {
		t.Fatal(err)
	}
	return upload
}

func TestScanStagingGroupsOnlyMatchingPath(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.bin")
	other := filepath.Join(dir, "other.bin")
	destination := fslocal.New()
	first := mustNewUpload(t, "00000000000000000000000001", remote, 8, 4)
	writeChunk(t, first, destination, 0, "abcd")
	second := mustNewUpload(t, "00000000000000000000000002", other, 4, 4)
	writeChunk(t, second, destination, 0, "wxyz")
	for _, name := range []string{".remote.bin.nexterm-not-an-id.part-0", "plain.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	staged, err := chunked.ScanStaging(context.Background(), destination, remote)
	if err != nil {
		t.Fatalf("ScanStaging: %v", err)
	}
	if len(staged) != 1 {
		t.Fatalf("staged groups = %+v, want exactly one", staged)
	}
	group := staged[0]
	if group.UploadID != "00000000000000000000000001" || !group.HasManifest || !group.HasParams || group.HasCommit {
		t.Fatalf("group = %+v", group)
	}
	if group.Total != 8 || group.ChunkSize != 4 {
		t.Fatalf("group params = %+v", group)
	}
	if len(group.Parts) != 1 || group.Parts[0] != 4+32 {
		t.Fatalf("group parts = %+v", group.Parts)
	}
}

func TestScanStagingSortsMultipleGroupsAndSurvivesMissingDir(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.bin")
	destination := fslocal.New()
	writeChunk(t, mustNewUpload(t, "00000000000000000000000002", remote, 12, 4), destination, 0, "abcd")
	writeChunk(t, mustNewUpload(t, "00000000000000000000000001", remote, 8, 4), destination, 0, "wxyz")

	staged, err := chunked.ScanStaging(context.Background(), destination, remote)
	if err != nil {
		t.Fatalf("ScanStaging: %v", err)
	}
	if len(staged) != 2 || staged[0].UploadID != "00000000000000000000000001" || staged[1].UploadID != "00000000000000000000000002" {
		t.Fatalf("staged order = %+v", staged)
	}

	missing, err := chunked.ScanStaging(context.Background(), destination, filepath.Join(dir, "absent", "remote.bin"))
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing dir scan = %+v, %v", missing, err)
	}
}

func TestScanStagingFindsRelativePathStaging(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	destination := fslocal.New()
	writeChunk(t, mustNewUpload(t, "00000000000000000000000001", "remote.bin", 4, 4), destination, 0, "abcd")

	staged, err := chunked.ScanStaging(context.Background(), destination, "remote.bin")
	if err != nil {
		t.Fatalf("ScanStaging: %v", err)
	}
	if len(staged) != 1 || !staged[0].HasParams || staged[0].Total != 4 {
		t.Fatalf("relative staged = %+v", staged)
	}
	if dir := chunked.StagingDir("remote.bin"); dir != "." {
		t.Fatalf("StagingDir(relative) = %q, want .", dir)
	}
	nested := filepath.Join(dir, "nested", "remote.bin")
	if got := chunked.StagingDir(nested); got != filepath.Dir(nested) {
		t.Fatalf("StagingDir(absolute) = %q, want %q", got, filepath.Dir(nested))
	}
}

func TestMatchesStagedRequiresExactParamsAndIntactParts(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	destination := fslocal.New()
	writeChunk(t, mustNewUpload(t, "00000000000000000000000001", remote, 8, 4), destination, 0, "abcd")
	staged, err := chunked.ScanStaging(context.Background(), destination, remote)
	if err != nil || len(staged) != 1 {
		t.Fatalf("ScanStaging = %+v, %v", staged, err)
	}

	if !mustNewUpload(t, "00000000000000000000000001", remote, 8, 4).MatchesStaged(staged[0]) {
		t.Fatal("identical params did not match")
	}
	if mustNewUpload(t, "00000000000000000000000001", remote, 12, 4).MatchesStaged(staged[0]) {
		t.Fatal("different total matched")
	}
	if mustNewUpload(t, "00000000000000000000000001", remote, 8, 8).MatchesStaged(staged[0]) {
		t.Fatal("different chunk size matched")
	}
	tampered := staged[0]
	tampered.Parts[0] = 999
	if mustNewUpload(t, "00000000000000000000000001", remote, 8, 4).MatchesStaged(tampered) {
		t.Fatal("tampered part size matched")
	}
	legacy := staged[0]
	legacy.HasParams = false
	if mustNewUpload(t, "00000000000000000000000001", remote, 8, 4).MatchesStaged(legacy) {
		t.Fatal("params-less legacy staging matched")
	}
}

func TestFindStagedResumesCrashedUploadAndCommits(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	destination := fslocal.New()
	first := mustNewUpload(t, testUploadID, remote, 10, 4)
	writeChunk(t, first, destination, 0, "abcd")
	writeChunk(t, first, destination, 2, "ij")

	adopted, hit, err := chunked.FindStaged(context.Background(), destination, remote, 10, 4)
	if err != nil || !hit {
		t.Fatalf("FindStaged = %v, %v", hit, err)
	}
	if adopted.ID() != testUploadID {
		t.Fatalf("adopted ID = %q, want %q", adopted.ID(), testUploadID)
	}
	status, err := adopted.Resume(context.Background(), destination)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if status.Received != 6 || len(status.Chunks) != 2 || status.Chunks[0] != 0 || status.Chunks[1] != 2 {
		t.Fatalf("resume status = %+v", status)
	}
	if got := writeChunk(t, adopted, destination, 1, "efgh"); got != 10 {
		t.Fatalf("received after resumed chunk = %d, want 10", got)
	}
	committed, err := adopted.Commit(context.Background(), destination)
	if err != nil || committed != 10 {
		t.Fatalf("Commit = %d, %v", committed, err)
	}
	data, err := os.ReadFile(remote)
	if err != nil || string(data) != "abcdefghij" {
		t.Fatalf("committed target = %q, %v", data, err)
	}
	assertNoUploadArtifacts(t, remote)
}

func TestFindStagedMissesOnParamMismatchForeignPathAndAfterReap(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.bin")
	other := filepath.Join(dir, "other.bin")
	destination := fslocal.New()
	writeChunk(t, mustNewUpload(t, testUploadID, remote, 10, 4), destination, 0, "abcd")

	if _, hit, err := chunked.FindStaged(context.Background(), destination, remote, 12, 4); err != nil || hit {
		t.Fatalf("param mismatch = %v, %v", hit, err)
	}
	if _, hit, err := chunked.FindStaged(context.Background(), destination, remote, 10, 8); err != nil || hit {
		t.Fatalf("chunk size mismatch = %v, %v", hit, err)
	}
	if _, hit, err := chunked.FindStaged(context.Background(), destination, other, 10, 4); err != nil || hit {
		t.Fatalf("foreign path = %v, %v", hit, err)
	}
	if matches := stagingGlob(t, remote, testUploadID); len(matches) != 3 {
		t.Fatalf("mismatched probes disturbed staging: %v", matches)
	}

	if _, err := chunked.ReapStaleStaging(context.Background(), destination, dir, 0); err != nil {
		t.Fatalf("ReapStaleStaging: %v", err)
	}
	if _, hit, err := chunked.FindStaged(context.Background(), destination, remote, 10, 4); err != nil || hit {
		t.Fatalf("reaped staging = %v, %v", hit, err)
	}
}

func TestParseStagingNameAcceptsParamsKind(t *testing.T) {
	uploadID, kind, ok := chunked.ParseStagingName(".remote.bin.nexterm-00000000000000000000000001.params")
	if !ok || uploadID != "00000000000000000000000001" || kind != "params" {
		t.Fatalf("ParseStagingName = %q, %q, %v", uploadID, kind, ok)
	}
	if _, _, ok := chunked.ParseStagingName(".remote.bin.nexterm-00000000000000000000000001.unknown"); ok {
		t.Fatal("unknown kind accepted")
	}
}
