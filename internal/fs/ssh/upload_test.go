package ssh

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

func newConcurrentTestFS(t *testing.T) (*FS, string) {
	t.Helper()
	return newTestFSWithOptions(t, nil, ClientOptions()...)
}

func randomSource(t *testing.T, size int) (string, []byte) {
	t.Helper()
	data := make([]byte, size)
	rand.New(rand.NewSource(int64(size) + 1)).Read(data)
	path := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, data
}

func assertRemoteDigest(t *testing.T, root, name string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(got) != sha256.Sum256(want) || len(got) != len(want) {
		t.Fatalf("remote %s differs from source (got %d bytes, want %d)", name, len(got), len(want))
	}
}

func TestConcurrentUploadAndDownloadKeepContentIntact(t *testing.T) {
	buffers := []int{0, 100, 32 << 10, 256<<10 + 7}
	sizes := []int{0, 1, sftpPacketSize - 1, sftpPacketSize + 1, 256<<10 + 7, uploadWriteSize, uploadWriteSize + 13, 9<<20 + 5}
	for _, size := range sizes {
		for _, buffer := range buffers {
			if size >= 1<<20 && buffer != 0 && buffer != 256<<10+7 {
				continue
			}
			filesystem, root := newConcurrentTestFS(t)
			source, data := randomSource(t, size)
			transferred, err := filesystem.Upload(context.Background(), source, "out.bin", TransferOptions{BufferSize: buffer})
			if err != nil || transferred != int64(size) {
				t.Fatalf("size=%d buffer=%d upload = %d, %v", size, buffer, transferred, err)
			}
			assertRemoteDigest(t, root, "out.bin", data)
			local := filepath.Join(t.TempDir(), "down.bin")
			if _, err := filesystem.Download(context.Background(), "out.bin", local, TransferOptions{BufferSize: buffer}); err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(local)
			if !bytes.Equal(got, data) {
				t.Fatalf("size=%d buffer=%d download differs", size, buffer)
			}
		}
	}
}

func TestUploadReportsMonotonicProgressEndingDone(t *testing.T) {
	filesystem, _ := newConcurrentTestFS(t)
	source, _ := randomSource(t, 3<<20)
	var events []Progress
	_, err := filesystem.Upload(context.Background(), source, "p.bin", TransferOptions{
		BufferSize: 512 << 10, ProgressInterval: time.Nanosecond,
		Progress: func(p Progress) { events = append(events, p) },
	})
	if err != nil {
		t.Fatal(err)
	}
	var last int64
	for _, e := range events {
		if e.Transferred < last || e.Total != 3<<20 {
			t.Fatalf("progress went backwards or wrong total: %+v after %d", e, last)
		}
		last = e.Transferred
	}
	if len(events) < 3 {
		t.Fatalf("progress granularity lost: %d events", len(events))
	}
	assertFinalProgress(t, events, 3<<20)
}

func TestCanceledUploadResumesToIdenticalContent(t *testing.T) {
	filesystem, root := newConcurrentTestFS(t)
	source, data := randomSource(t, 5<<20+123)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transferred, err := filesystem.Upload(ctx, source, "out.bin", TransferOptions{
		BufferSize: 512 << 10, ProgressInterval: time.Nanosecond,
		Progress: func(p Progress) {
			if p.Transferred >= 2<<20 {
				cancel()
			}
		},
	})
	if err == nil {
		t.Fatal("canceled upload reported success")
	}
	if transferred <= 0 || transferred >= int64(len(data)) {
		t.Fatalf("canceled upload transferred = %d", transferred)
	}
	resumed, err := filesystem.Upload(context.Background(), source, "out.bin", TransferOptions{Resume: true})
	if err != nil || resumed != int64(len(data)) {
		t.Fatalf("resume = %d, %v", resumed, err)
	}
	assertRemoteDigest(t, root, "out.bin", data)
}

func TestResumeDistrustsHolesInTheLastWindow(t *testing.T) {
	const size = 3*uploadWriteSize + 1000
	cases := map[string]struct{ remoteSize, holeAt, holeLen int }{
		// Interrupted: the file extends past a window that was only partly written.
		"hole before the end of a partial upload": {remoteSize: 2*uploadWriteSize + 500, holeAt: uploadWriteSize + 4096, holeLen: 64 << 10},
		// Looks complete by size but a chunk in the tail never arrived.
		"hole in a file that already has full size": {remoteSize: size, holeAt: size - 100<<10, holeLen: 32 << 10},
		"zero tail chunk": {remoteSize: uploadWriteSize + 10, holeAt: uploadWriteSize, holeLen: 10},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			filesystem, root := newConcurrentTestFS(t)
			source, data := randomSource(t, size)
			damaged := append([]byte(nil), data[:tc.remoteSize]...)
			clear(damaged[tc.holeAt : tc.holeAt+tc.holeLen])
			if err := os.WriteFile(filepath.Join(root, "out.bin"), damaged, 0o600); err != nil {
				t.Fatal(err)
			}
			var events []Progress
			transferred, err := filesystem.Upload(context.Background(), source, "out.bin", TransferOptions{
				Resume: true, Progress: func(p Progress) { events = append(events, p) },
			})
			if err != nil || transferred != size {
				t.Fatalf("resume = %d, %v", transferred, err)
			}
			assertRemoteDigest(t, root, "out.bin", data)
			assertFinalProgress(t, events, size)
		})
	}
}

func TestResumeKeepsVerifiedPrefix(t *testing.T) {
	filesystem, root := newConcurrentTestFS(t)
	const size = 2*uploadWriteSize + 77
	source, data := randomSource(t, size)
	if err := os.WriteFile(filepath.Join(root, "out.bin"), data[:uploadWriteSize+1000], 0o600); err != nil {
		t.Fatal(err)
	}
	var first *Progress
	if _, err := filesystem.Upload(context.Background(), source, "out.bin", TransferOptions{
		Resume: true, Progress: func(p Progress) {
			if first == nil {
				first = &p
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	if first == nil || first.Transferred != uploadWriteSize+1000 {
		t.Fatalf("a contiguous correct prefix must be kept, first progress = %+v", first)
	}
	assertRemoteDigest(t, root, "out.bin", data)
}

func TestUploadRateCapStillLimitsThroughput(t *testing.T) {
	filesystem, root := newConcurrentTestFS(t)
	source, data := randomSource(t, 384<<10)
	start := time.Now()
	if _, err := filesystem.Upload(context.Background(), source, "out.bin", TransferOptions{MaxBytesPerSecond: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("384 KiB at a 1 MiB/s cap finished in %v", elapsed)
	}
	assertRemoteDigest(t, root, "out.bin", data)
}

func TestUploadBufferSizeIsBoundedAndPacedWritesStayFinegrained(t *testing.T) {
	if got := uploadBufferSize(TransferOptions{BufferSize: 64 << 20}); got != uploadWriteSize {
		t.Fatalf("oversized buffer not clamped: %d", got)
	}
	if got := uploadBufferSize(TransferOptions{}); got != uploadWriteSize {
		t.Fatalf("default buffer = %d", got)
	}
	if got := uploadBufferSize(TransferOptions{MaxBytesPerSecond: 1000}); got != pacedUploadWriteSize {
		t.Fatalf("slow cap buffer = %d", got)
	}
}

type fakeWriteFile struct {
	writes        []int
	offset        int64
	seeks         []int64
	rejectLargeAs error
	data          map[int64]byte
}

func (f *fakeWriteFile) Write(p []byte) (int, error) {
	f.writes = append(f.writes, len(p))
	if len(p) > sftpPacketSize && f.rejectLargeAs != nil {
		// A server refusing the burst may still have stored part of it.
		for i := 0; i < sftpPacketSize; i++ {
			f.data[f.offset+int64(i)] = p[i]
		}
		f.offset += sftpPacketSize
		return sftpPacketSize, f.rejectLargeAs
	}
	for i, b := range p {
		f.data[f.offset+int64(i)] = b
	}
	f.offset += int64(len(p))
	return len(p), nil
}
func (f *fakeWriteFile) Seek(offset int64, _ int) (int64, error) {
	f.seeks = append(f.seeks, offset)
	f.offset = offset
	return offset, nil
}
func (f *fakeWriteFile) Sync() error  { return nil }
func (f *fakeWriteFile) Close() error { return nil }

func TestWriterFallsBackToSequentialRequestsWhenServerRejectsBursts(t *testing.T) {
	file := &fakeWriteFile{data: map[int64]byte{}, rejectLargeAs: &sftp.StatusError{Code: 4}}
	writer := &remoteWriter{ctx: context.Background(), file: file}
	payload := make([]byte, 3*sftpPacketSize+10)
	rand.New(rand.NewSource(1)).Read(payload)
	for round := 0; round < 2; round++ {
		n, err := writer.Write(payload)
		if err != nil || n != len(payload) {
			t.Fatalf("round %d write = %d, %v", round, n, err)
		}
	}
	for i, b := range append(append([]byte(nil), payload...), payload...) {
		if file.data[int64(i)] != b {
			t.Fatalf("byte %d differs after fallback", i)
		}
	}
	bursts := 0
	for _, size := range file.writes {
		if size > sftpPacketSize {
			bursts++
		}
	}
	if bursts != 1 || !writer.sequential {
		t.Fatalf("the burst must be tried once and then abandoned, bursts=%d sequential=%v", bursts, writer.sequential)
	}
}

func TestWriterDoesNotRetryNonServerErrors(t *testing.T) {
	file := &fakeWriteFile{data: map[int64]byte{}, rejectLargeAs: io.ErrClosedPipe}
	writer := &remoteWriter{ctx: context.Background(), file: file}
	if _, err := writer.Write(make([]byte, 2*sftpPacketSize)); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("transport failure was masked: %v", err)
	}
	if writer.sequential {
		t.Fatal("a broken connection must not trigger the sequential retry")
	}
}

func TestAppendWritersNeverIssueConcurrentRequests(t *testing.T) {
	filesystem, root := newConcurrentTestFS(t)
	data := make([]byte, 700<<10)
	rand.New(rand.NewSource(2)).Read(data)
	if err := os.WriteFile(filepath.Join(root, "log.bin"), []byte("head"), 0o600); err != nil {
		t.Fatal(err)
	}
	writer, err := filesystem.OpenWrite(context.Background(), "log.bin", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	assertRemoteDigest(t, root, "log.bin", append([]byte("head"), data...))
}

// A resume whose verified offset equals the remote size must not need setstat/truncate, and a remote file that
// shrank below the verified offset must be refused instead of silently recreated.
func TestOpenWriteAtSkipsTruncateAtTheVerifiedOffsetAndRefusesAShrunkFile(t *testing.T) {
	filesystem, root := newConcurrentTestFS(t)
	if err := os.WriteFile(filepath.Join(root, "part.bin"), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	writer, err := filesystem.openWriteAt(context.Background(), "part.bin", 10)
	if err != nil {
		t.Fatalf("resume at the exact remote size failed: %v", err)
	}
	if _, err := writer.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "part.bin")); string(got) != "0123456789abc" {
		t.Fatalf("remote content = %q, want the prefix kept and the new bytes appended", got)
	}

	if _, err := filesystem.openWriteAt(context.Background(), "part.bin", 100); err == nil {
		t.Fatal("a remote file shorter than the verified offset must not be resumed")
	}
	if _, err := filesystem.openWriteAt(context.Background(), "missing.bin", 5); err == nil {
		t.Fatal("a recreated empty file must not be resumed at a non-zero offset")
	}
	if _, statErr := os.Stat(filepath.Join(root, "missing.bin")); statErr == nil {
		t.Fatal("a refused resume must not leave a recreated file behind with default permissions")
	}
}
