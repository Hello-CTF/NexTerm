package chunked_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fs/chunked"
	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
)

func stagingGlob(t *testing.T, remote, uploadID string) []string {
	t.Helper()
	matches, err := filepath.Glob(chunkArtifactGlob(remote, uploadID+".*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestResumeContinuesInterruptedUploadAcrossSessions(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	destination := fslocal.New()
	first, err := chunked.New(testUploadID, remote, 10, 4)
	if err != nil {
		t.Fatal(err)
	}
	writeChunk(t, first, destination, 2, "ij")
	writeChunk(t, first, destination, 0, "abcd")

	second, err := chunked.New(testUploadID, remote, 10, 4)
	if err != nil {
		t.Fatal(err)
	}
	status, err := second.Resume(context.Background(), destination)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if status.Received != 6 || len(status.Chunks) != 2 || status.Chunks[0] != 0 || status.Chunks[1] != 2 {
		t.Fatalf("resume status = %+v", status)
	}
	if got := writeChunk(t, second, destination, 1, "efgh"); got != 10 {
		t.Fatalf("received after resumed chunk = %d, want 10", got)
	}
	committed, err := second.Commit(context.Background(), destination)
	if err != nil || committed != 10 {
		t.Fatalf("Commit = %d, %v", committed, err)
	}
	data, err := os.ReadFile(remote)
	if err != nil || string(data) != "abcdefghij" {
		t.Fatalf("committed target = %q, %v", data, err)
	}
	assertNoUploadArtifacts(t, remote)
}

func TestResumeDropsCorruptStagedPart(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	destination := fslocal.New()
	first, err := chunked.New(testUploadID, remote, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	writeChunk(t, first, destination, 0, "abcd")
	parts := stagingGlob(t, remote, testUploadID)
	var partPath string
	for _, match := range parts {
		if filepath.Ext(match) != ".manifest" {
			partPath = match
		}
	}
	if partPath == "" {
		t.Fatalf("staged part not found in %v", parts)
	}
	data, err := os.ReadFile(partPath)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(partPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	second, err := chunked.New(testUploadID, remote, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	status, err := second.Resume(context.Background(), destination)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if status.Received != 0 || len(status.Chunks) != 0 {
		t.Fatalf("resume status = %+v, want empty", status)
	}
	if _, err := os.Stat(partPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt part survived resume: %v", err)
	}
	writeChunk(t, second, destination, 0, "abcd")
	writeChunk(t, second, destination, 1, "efgh")
	committed, err := second.Commit(context.Background(), destination)
	if err != nil || committed != 8 {
		t.Fatalf("Commit = %d, %v", committed, err)
	}
	data, err = os.ReadFile(remote)
	if err != nil || string(data) != "abcdefgh" {
		t.Fatalf("committed target = %q, %v", data, err)
	}
}

func TestResumeAdoptsOrphanPartMissingFromManifest(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	destination := fslocal.New()
	first, err := chunked.New(testUploadID, remote, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	writeChunk(t, first, destination, 0, "abcd")
	for _, match := range stagingGlob(t, remote, testUploadID) {
		if filepath.Ext(match) == ".manifest" {
			if err := os.Remove(match); err != nil {
				t.Fatal(err)
			}
		}
	}

	second, err := chunked.New(testUploadID, remote, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	status, err := second.Resume(context.Background(), destination)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if status.Received != 4 || len(status.Chunks) != 1 || status.Chunks[0] != 0 {
		t.Fatalf("resume status = %+v", status)
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
}

func TestResumeIgnoresTruncatedManifestTail(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	destination := fslocal.New()
	first, err := chunked.New(testUploadID, remote, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	writeChunk(t, first, destination, 0, "abcd")
	for _, match := range stagingGlob(t, remote, testUploadID) {
		if filepath.Ext(match) == ".manifest" {
			manifest, err := os.OpenFile(match, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manifest.Write([]byte{7}); err != nil {
				t.Fatal(err)
			}
			if err := manifest.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	second, err := chunked.New(testUploadID, remote, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	status, err := second.Resume(context.Background(), destination)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if status.Received != 4 || len(status.Chunks) != 1 || status.Chunks[0] != 0 {
		t.Fatalf("resume status = %+v", status)
	}
	committed, err := second.Commit(context.Background(), destination)
	if err != nil || committed != 4 {
		t.Fatalf("Commit = %d, %v", committed, err)
	}
}

func TestResumeWithEmptyStagingIsNoop(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	destination := fslocal.New()
	upload, err := chunked.New(testUploadID, remote, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	status, err := upload.Resume(context.Background(), destination)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if status.Received != 0 || len(status.Chunks) != 0 {
		t.Fatalf("resume status = %+v, want empty", status)
	}
	writeChunk(t, upload, destination, 0, "abcd")
	committed, err := upload.Commit(context.Background(), destination)
	if err != nil || committed != 4 {
		t.Fatalf("Commit = %d, %v", committed, err)
	}
}

func TestResumeRejectsClosedUpload(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	destination := fslocal.New()
	upload, err := chunked.New(testUploadID, remote, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	writeChunk(t, upload, destination, 0, "abcd")
	if _, err := upload.Commit(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	if _, err := upload.Resume(context.Background(), destination); err == nil {
		t.Fatal("committed upload resumed")
	}

	aborted, err := chunked.New("00000000000000000000000002", remote, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := aborted.Abort(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	if _, err := aborted.Resume(context.Background(), destination); err == nil {
		t.Fatal("aborted upload resumed")
	}
}

func TestReapStaleStagingRemovesCrashedGroupsAndKeepsFresh(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.bin")
	destination := fslocal.New()
	crashed, err := chunked.New(testUploadID, remote, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	writeChunk(t, crashed, destination, 0, "abcd")

	freshID := "00000000000000000000000002"
	fresh, err := chunked.New(freshID, remote, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	writeChunk(t, fresh, destination, 0, "wxyz")

	old := time.Now().Add(-2 * time.Hour)
	for _, match := range stagingGlob(t, remote, testUploadID) {
		if err := os.Chtimes(match, old, old); err != nil {
			t.Fatal(err)
		}
	}
	unrelated := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := chunked.ReapStaleStaging(context.Background(), destination, dir, time.Hour)
	if err != nil {
		t.Fatalf("ReapStaleStaging: %v", err)
	}
	if len(report.Removed) == 0 {
		t.Fatal("crashed staging group was not reaped")
	}
	for _, match := range stagingGlob(t, remote, testUploadID) {
		t.Fatalf("crashed staging file survived: %s", match)
	}
	if len(stagingGlob(t, remote, freshID)) == 0 {
		t.Fatal("fresh staging group was reaped")
	}
	if report.Kept == 0 {
		t.Fatal("fresh staging group not counted as kept")
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated file touched: %v", err)
	}
}

func TestReapStaleStagingKeepsGroupWithFreshManifest(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.bin")
	destination := fslocal.New()
	upload, err := chunked.New(testUploadID, remote, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	writeChunk(t, upload, destination, 0, "abcd")

	old := time.Now().Add(-2 * time.Hour)
	for _, match := range stagingGlob(t, remote, testUploadID) {
		if filepath.Ext(match) != ".manifest" {
			if err := os.Chtimes(match, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	report, err := chunked.ReapStaleStaging(context.Background(), destination, dir, time.Hour)
	if err != nil {
		t.Fatalf("ReapStaleStaging: %v", err)
	}
	if len(report.Removed) != 0 {
		t.Fatalf("staging with fresh manifest reaped: %v", report.Removed)
	}
	if len(stagingGlob(t, remote, testUploadID)) == 0 {
		t.Fatal("active staging group was reaped")
	}
}

func TestReapStaleStagingZeroMinAgeRemovesAll(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.bin")
	destination := fslocal.New()
	upload, err := chunked.New(testUploadID, remote, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	writeChunk(t, upload, destination, 0, "abcd")
	report, err := chunked.ReapStaleStaging(context.Background(), destination, dir, 0)
	if err != nil {
		t.Fatalf("ReapStaleStaging: %v", err)
	}
	if len(report.Removed) == 0 {
		t.Fatal("staging survived zero min age")
	}
	if matches := stagingGlob(t, remote, testUploadID); len(matches) != 0 {
		t.Fatalf("staging files remain: %v", matches)
	}
}

func TestParseStagingName(t *testing.T) {
	for _, test := range []struct {
		name     string
		uploadID string
		kind     string
		ok       bool
	}{
		{name: ".remote.bin.nexterm-00000000000000000000000001.manifest", uploadID: "00000000000000000000000001", kind: "manifest", ok: true},
		{name: ".remote.bin.nexterm-00000000000000000000000001.commit", uploadID: "00000000000000000000000001", kind: "commit", ok: true},
		{name: ".remote.bin.nexterm-00000000000000000000000001.part-000000000000", uploadID: "00000000000000000000000001", kind: "part-000000000000", ok: true},
		{name: "remote.bin.nexterm-00000000000000000000000001.manifest", ok: false},
		{name: ".remote.bin.nexterm-0000000000000000000000001.manifest", ok: false},
		{name: ".remote.bin.nexterm-00000000000000000000000001.part-x", ok: false},
		{name: ".remote.bin.nexterm-00000000000000000000000001.tmp", ok: false},
		{name: ".remote.bin.manifest", ok: false},
		{name: ".nexterm-00000000000000000000000001.manifest", ok: false},
		{name: ".remote.bin.nexterm-.manifest", ok: false},
	} {
		uploadID, kind, ok := chunked.ParseStagingName(test.name)
		if ok != test.ok || uploadID != test.uploadID || kind != test.kind {
			t.Fatalf("ParseStagingName(%q) = %q, %q, %v; want %q, %q, %v", test.name, uploadID, kind, ok, test.uploadID, test.kind, test.ok)
		}
	}
}
