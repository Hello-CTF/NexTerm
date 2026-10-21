package chunked_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/fs/chunked"
	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const testUploadID = "00000000000000000000000001"

func chunkFor(index int64, data string) chunked.Chunk {
	digest := sha256.Sum256([]byte(data))
	return chunked.Chunk{Index: index, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Data: []byte(data)}
}

func writeChunk(t *testing.T, upload *chunked.Upload, destination base.FileSystem, index int64, data string) int64 {
	t.Helper()
	received, err := upload.WriteChunk(context.Background(), destination, chunkFor(index, data))
	if err != nil {
		t.Fatal(err)
	}
	return received
}

func chunkArtifactGlob(remote, pattern string) string {
	return filepath.Join(filepath.Dir(remote), "."+filepath.Base(remote)+".nexterm-"+pattern)
}

func assertNoUploadArtifacts(t *testing.T, remote string) {
	t.Helper()
	matches, err := filepath.Glob(chunkArtifactGlob(remote, "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("upload artifacts remain: %v", matches)
	}
}

func TestChunkBoundariesAndChecksumValidation(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	upload, err := chunked.New(testUploadID, remote, 10, 4)
	if err != nil {
		t.Fatal(err)
	}
	destination := fslocal.New()
	for _, chunk := range []chunked.Chunk{
		chunkFor(-1, "abcd"),
		chunkFor(3, "abcd"),
		chunkFor(0, "abc"),
		{Index: 0, Size: 4, SHA256: "not-a-digest", Data: []byte("abcd")},
	} {
		if _, err := upload.WriteChunk(context.Background(), destination, chunk); err == nil {
			t.Fatalf("WriteChunk(%+v) unexpectedly succeeded", chunk)
		}
	}
	badChecksum := chunkFor(0, "abcd")
	badChecksum.SHA256 = strings.Repeat("0", 64)
	if _, err := upload.WriteChunk(context.Background(), destination, badChecksum); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("checksum mismatch = %v", err)
	}
	if _, err := upload.Commit(context.Background(), destination); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete commit = %v", err)
	}
	if _, err := os.Stat(remote); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target visible after rejected chunks: %v", err)
	}
	if err := upload.Abort(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	assertNoUploadArtifacts(t, remote)
}

func TestOutOfOrderDuplicateChunksCommitAtomically(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	if err := os.WriteFile(remote, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	upload, err := chunked.New(testUploadID, remote, 10, 4)
	if err != nil {
		t.Fatal(err)
	}
	destination := fslocal.New()
	if got := writeChunk(t, upload, destination, 2, "ij"); got != 2 {
		t.Fatalf("received = %d, want 2", got)
	}
	if got := writeChunk(t, upload, destination, 0, "abcd"); got != 6 {
		t.Fatalf("received = %d, want 6", got)
	}
	if got := writeChunk(t, upload, destination, 2, "ij"); got != 6 {
		t.Fatalf("duplicate changed received to %d", got)
	}
	conflict := chunkFor(2, "XY")
	if _, err := upload.WriteChunk(context.Background(), destination, conflict); !errors.Is(err, chunked.ErrChunkConflict) {
		t.Fatalf("conflicting duplicate = %v", err)
	}
	if got := writeChunk(t, upload, destination, 1, "efgh"); got != 10 {
		t.Fatalf("received = %d, want 10", got)
	}
	data, err := os.ReadFile(remote)
	if err != nil || string(data) != "old" {
		t.Fatalf("target changed before commit: %q, %v", data, err)
	}
	committed, err := upload.Commit(context.Background(), destination)
	if err != nil || committed != 10 {
		t.Fatalf("Commit = %d, %v", committed, err)
	}
	data, err = os.ReadFile(remote)
	if err != nil || string(data) != "abcdefghij" {
		t.Fatalf("committed target = %q, %v", data, err)
	}
	assertNoUploadArtifacts(t, remote)
}

func TestAbortCleansEveryAttemptedChunkWithoutTouchingTarget(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	if err := os.WriteFile(remote, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	upload, err := chunked.New(testUploadID, remote, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	destination := fslocal.New()
	writeChunk(t, upload, destination, 1, "efgh")
	if _, err := upload.Commit(context.Background(), destination); err == nil {
		t.Fatal("incomplete commit succeeded")
	}
	if err := upload.Abort(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	assertNoUploadArtifacts(t, remote)
	data, err := os.ReadFile(remote)
	if err != nil || string(data) != "old" {
		t.Fatalf("abort changed target: %q, %v", data, err)
	}
	if _, err := upload.WriteChunk(context.Background(), destination, chunkFor(0, "abcd")); err == nil {
		t.Fatal("aborted upload accepted another chunk")
	}
}

func TestAbortCleansCompleteManifestRecordsAfterTruncatedTail(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	upload, err := chunked.New(testUploadID, remote, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	destination := fslocal.New()
	writeChunk(t, upload, destination, 0, "abcd")
	matches, err := filepath.Glob(chunkArtifactGlob(remote, "*.manifest"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("manifest = %v, %v", matches, err)
	}
	manifest, err := os.OpenFile(matches[0], os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Close(); err != nil {
		t.Fatal(err)
	}
	if err := upload.Abort(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	assertNoUploadArtifacts(t, remote)
}

type cancelOnWriteFS struct {
	*fslocal.FileSystem
	cancel context.CancelFunc
}

func (f *cancelOnWriteFS) OpenWrite(ctx context.Context, path string, appendMode bool) (base.RemoteWriter, error) {
	writer, err := f.FileSystem.OpenWrite(ctx, path, appendMode)
	if err != nil {
		return nil, err
	}
	return &cancelOnWriteWriter{RemoteWriter: writer, cancel: f.cancel}, nil
}

type cancelOnWriteWriter struct {
	base.RemoteWriter
	cancel context.CancelFunc
}

func (w *cancelOnWriteWriter) Write(data []byte) (int, error) {
	n, err := w.RemoteWriter.Write(data)
	w.cancel()
	return n, err
}

func TestCancellationRemovesPartialChunk(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	ctx, cancel := context.WithCancel(context.Background())
	destination := &cancelOnWriteFS{FileSystem: fslocal.New(), cancel: cancel}
	upload, err := chunked.New(testUploadID, remote, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upload.WriteChunk(ctx, destination, chunkFor(0, "abcd")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled chunk = %v", err)
	}
	assertNoUploadArtifacts(t, remote)
	if _, err := os.Stat(remote); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled upload exposed target: %v", err)
	}
}

type failingRenameFS struct {
	*fslocal.FileSystem
	err error
}

func (f *failingRenameFS) AtomicRename(context.Context, string, string) error {
	return f.err
}

func TestCommitFailurePreservesOldTargetAndCleansOnAbort(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	if err := os.WriteFile(remote, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	renameErr := errors.New("rename failed")
	destination := &failingRenameFS{FileSystem: fslocal.New(), err: renameErr}
	upload, err := chunked.New(testUploadID, remote, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	writeChunk(t, upload, destination, 0, "abcd")
	if _, err := upload.Commit(context.Background(), destination); !errors.Is(err, renameErr) {
		t.Fatalf("Commit = %v", err)
	}
	data, err := os.ReadFile(remote)
	if err != nil || string(data) != "old" {
		t.Fatalf("failed commit changed target: %q, %v", data, err)
	}
	if err := upload.Abort(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	assertNoUploadArtifacts(t, remote)
}

func TestCommitRejectsStagedChunkCorruption(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	if err := os.WriteFile(remote, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	upload, err := chunked.New(testUploadID, remote, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	destination := fslocal.New()
	writeChunk(t, upload, destination, 0, "abcd")
	matches, err := filepath.Glob(chunkArtifactGlob(remote, "*.part-*"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("staged chunks = %v, %v", matches, err)
	}
	staged, err := os.ReadFile(matches[0])
	if err != nil || len(staged) != 36 {
		t.Fatalf("read staged chunk: len=%d, %v", len(staged), err)
	}
	staged[len(staged)-1] ^= 0xff
	if err := os.WriteFile(matches[0], staged, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := upload.Commit(context.Background(), destination); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("corrupted staged commit = %v", err)
	}
	data, err := os.ReadFile(remote)
	if err != nil || string(data) != "old" {
		t.Fatalf("corrupted commit changed target: %q, %v", data, err)
	}
	if err := upload.Abort(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	assertNoUploadArtifacts(t, remote)
}

type flakyDeleteFS struct {
	*fslocal.FileSystem
	failOnce bool
}

func (f *flakyDeleteFS) Delete(ctx context.Context, path string, isDir bool) error {
	if f.failOnce {
		f.failOnce = false
		return errors.New("delete blocked")
	}
	return f.FileSystem.Delete(ctx, path, isDir)
}

func TestAbortRetriesFailedCleanup(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	destination := &flakyDeleteFS{FileSystem: fslocal.New(), failOnce: true}
	upload, err := chunked.New(testUploadID, remote, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	writeChunk(t, upload, destination, 0, "abcd")
	if err := upload.Abort(context.Background(), destination); err == nil {
		t.Fatal("first abort unexpectedly succeeded")
	}
	if err := upload.Abort(context.Background(), destination); err != nil {
		t.Fatalf("abort retry = %v", err)
	}
	assertNoUploadArtifacts(t, remote)
}

type countingFS struct {
	*fslocal.FileSystem
	maxRead  int
	maxWrite int
	reads    int
}

func (f *countingFS) OpenRead(ctx context.Context, path string) (base.RemoteReader, error) {
	reader, err := f.FileSystem.OpenRead(ctx, path)
	if err != nil {
		return nil, err
	}
	return &countingReader{RemoteReader: reader, owner: f}, nil
}

func (f *countingFS) OpenWrite(ctx context.Context, path string, appendMode bool) (base.RemoteWriter, error) {
	writer, err := f.FileSystem.OpenWrite(ctx, path, appendMode)
	if err != nil {
		return nil, err
	}
	return &countingWriter{RemoteWriter: writer, owner: f}, nil
}

type countingReader struct {
	base.RemoteReader
	owner *countingFS
}

func (r *countingReader) Read(data []byte) (int, error) {
	r.owner.reads++
	if len(data) > r.owner.maxRead {
		r.owner.maxRead = len(data)
	}
	return r.RemoteReader.Read(data)
}

type countingWriter struct {
	base.RemoteWriter
	owner *countingFS
}

func (w *countingWriter) Write(data []byte) (int, error) {
	if len(data) > w.owner.maxWrite {
		w.owner.maxWrite = len(data)
	}
	return w.RemoteWriter.Write(data)
}

func TestCommitUsesBoundedReadWriteBuffers(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	destination := &countingFS{FileSystem: fslocal.New()}
	upload, err := chunked.New(testUploadID, remote, 16*1024, 1024)
	if err != nil {
		t.Fatal(err)
	}
	data := strings.Repeat("x", 1024)
	for index := int64(0); index < 16; index++ {
		writeChunk(t, upload, destination, index, data)
	}
	destination.reads = 0
	if _, err := upload.Commit(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	const maxCommitBuffer = 256 << 10
	if destination.maxRead == 0 || destination.maxRead > maxCommitBuffer || destination.maxWrite > maxCommitBuffer {
		t.Fatalf("buffer bounds: maxRead=%d maxWrite=%d, want <= %d", destination.maxRead, destination.maxWrite, maxCommitBuffer)
	}
	if destination.reads <= 16 {
		t.Fatalf("commit reads = %d, want streaming reads and EOF checks", destination.reads)
	}
}

func TestCommitPreservesTargetModeAndHiddenStaging(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not available on Windows")
	}
	for _, mode := range []os.FileMode{0o600, 0o755} {
		t.Run(mode.String(), func(t *testing.T) {
			remote := filepath.Join(t.TempDir(), "remote.bin")
			if err := os.WriteFile(remote, []byte("old"), mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(remote, mode); err != nil {
				t.Fatal(err)
			}
			upload, err := chunked.New(testUploadID, remote, 4, 4)
			if err != nil {
				t.Fatal(err)
			}
			destination := fslocal.New()
			writeChunk(t, upload, destination, 0, "abcd")
			for _, path := range []string{
				chunked.StagingPath(remote, testUploadID, "part-000000000000"),
				chunked.StagingPath(remote, testUploadID, "manifest"),
			} {
				if !strings.HasPrefix(filepath.Base(path), ".") {
					t.Fatalf("staging basename is not hidden: %s", path)
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("staging mode for %s = %v, %v", path, info.Mode(), err)
				}
			}
			if _, err := upload.Commit(context.Background(), destination); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(remote)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatalf("committed mode = %v, %v; want %v", info.Mode(), err, mode)
			}
			assertNoUploadArtifacts(t, remote)
		})
	}
}

func TestSameTargetUploadsCommitIndependently(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	first, err := chunked.New(testUploadID, remote, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	second, err := chunked.New("00000000000000000000000002", remote, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	destination := fslocal.New()
	writeChunk(t, first, destination, 0, "aaaa")
	writeChunk(t, second, destination, 0, "bbbb")
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, upload := range []*chunked.Upload{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := upload.Commit(context.Background(), destination)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(remote)
	if err != nil || (string(data) != "aaaa" && string(data) != "bbbb") {
		t.Fatalf("concurrent target = %q, %v", data, err)
	}
	assertNoUploadArtifacts(t, remote)
}

type listOnlyModeFS struct {
	base.FileSystem
	entries []base.FileEntry
}

func (f *listOnlyModeFS) List(context.Context, string) ([]base.FileEntry, error) {
	return f.entries, nil
}

func TestTargetModeFallsBackToListMetadata(t *testing.T) {
	destination := &listOnlyModeFS{entries: []base.FileEntry{{Name: "remote.bin", Mode: 0o755}}}
	mode, err := chunked.TargetMode(context.Background(), destination, "/dir/remote.bin")
	if err != nil || mode != 0o755 {
		t.Fatalf("TargetMode = %v, %v", mode, err)
	}
	destination.entries = nil
	mode, err = chunked.TargetMode(context.Background(), destination, "/dir/remote.bin")
	if err != nil || mode != chunked.DefaultUploadMode {
		t.Fatalf("new target mode = %v, %v", mode, err)
	}
}

func TestEmptyUploadCommitsEmptyFile(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "empty.bin")
	upload, err := chunked.New(testUploadID, remote, 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	destination := fslocal.New()
	if _, err := upload.Commit(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(remote)
	if err != nil || info.Size() != 0 {
		t.Fatalf("empty target = %+v, %v", info, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != chunked.DefaultUploadMode {
		t.Fatalf("new target mode = %v, want %v", info.Mode(), chunked.DefaultUploadMode)
	}
	assertNoUploadArtifacts(t, remote)
}

type chmodUnsupportedFS struct {
	*fslocal.FileSystem
}

func (chmodUnsupportedFS) Chmod(context.Context, string, fs.FileMode) error {
	return fmt.Errorf("chmod: %w", base.ErrUnsupported)
}

// A filesystem without chmod capability (the Windows local FS) still
// commits: mode preservation is best-effort, while content confirmation and
// the atomic replace stay mandatory.
func TestCommitToleratesChmodUnsupported(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.bin")
	if err := os.WriteFile(remote, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	upload, err := chunked.New(testUploadID, remote, 10, 4)
	if err != nil {
		t.Fatal(err)
	}
	destination := chmodUnsupportedFS{FileSystem: fslocal.New()}
	writeChunk(t, upload, destination, 0, "abcd")
	writeChunk(t, upload, destination, 1, "efgh")
	writeChunk(t, upload, destination, 2, "ij")
	if _, err := upload.Commit(context.Background(), destination); err != nil {
		t.Fatalf("commit on a chmod-unsupported filesystem: %v", err)
	}
	if data, err := os.ReadFile(remote); err != nil || string(data) != "abcdefghij" {
		t.Fatalf("committed target = %q, %v", data, err)
	}
	assertNoUploadArtifacts(t, remote)
}
