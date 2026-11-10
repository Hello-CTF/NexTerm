package local

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sshfs "github.com/Hello-CTF/NexTerm/internal/fs/ssh"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/pkg/sftp"
)

const sftpTestLatency = 20 * time.Millisecond

type delayedChunk struct {
	data []byte
	at   time.Time
	err  error
}

// latencyConn delays every byte the server sends by a fixed one-way latency and counts SFTP
// requests awaiting a response, so a test can see how many requests a transfer overlaps.
type latencyConn struct {
	net.Conn
	chunks  chan delayedChunk
	pending []byte
	readErr error

	mu       sync.Mutex
	inflight int
	peak     int
	wbuf     []byte
	rbuf     []byte
}

func newLatencyConn(conn net.Conn, latency time.Duration) *latencyConn {
	c := &latencyConn{Conn: conn, chunks: make(chan delayedChunk, 1<<14)}
	go func() {
		for {
			buffer := make([]byte, 64<<10)
			n, err := conn.Read(buffer)
			c.chunks <- delayedChunk{data: buffer[:n], at: time.Now().Add(latency), err: err}
			if err != nil {
				return
			}
		}
	}()
	return c
}

func countSFTPFrames(buffer *[]byte, p []byte) (frames int) {
	*buffer = append(*buffer, p...)
	for len(*buffer) >= 4 {
		length := int(binary.BigEndian.Uint32(*buffer))
		if len(*buffer) < 4+length {
			break
		}
		*buffer = (*buffer)[4+length:]
		frames++
	}
	return frames
}

func (c *latencyConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.inflight += countSFTPFrames(&c.wbuf, p)
	c.peak = max(c.peak, c.inflight)
	c.mu.Unlock()
	return c.Conn.Write(p)
}

func (c *latencyConn) Read(p []byte) (int, error) {
	if len(c.pending) == 0 {
		if c.readErr != nil {
			return 0, c.readErr
		}
		chunk := <-c.chunks
		if wait := time.Until(chunk.at); wait > 0 {
			time.Sleep(wait)
		}
		c.pending, c.readErr = chunk.data, chunk.err
		if len(c.pending) == 0 {
			return 0, c.readErr
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	c.mu.Lock()
	c.inflight -= countSFTPFrames(&c.rbuf, p[:n])
	c.mu.Unlock()
	return n, nil
}

func (c *latencyConn) Peak() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peak
}

func (c *latencyConn) ResetPeak() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.peak = c.inflight
}

func newLatencySFTP(tb testing.TB, latency time.Duration) (*sshfs.FS, *latencyConn, string) {
	tb.Helper()
	root := tb.TempDir()
	clientSide, serverSide := net.Pipe()
	server, err := sftp.NewServer(serverSide, sftp.WithServerWorkingDirectory(root))
	if err != nil {
		tb.Fatal(err)
	}
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = server.Serve()
	}()
	conn := newLatencyConn(clientSide, latency)
	client, err := sftp.NewClientPipe(conn, conn, sshfs.ClientOptions()...)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		client.Close()
		server.Close()
		clientSide.Close()
		serverSide.Close()
		<-serveDone
	})
	return sshfs.New(client, nil), conn, root
}

func randomPayload(size int, seed int64) []byte {
	payload := make([]byte, size)
	rand.New(rand.NewSource(seed)).Read(payload)
	return payload
}

type progressLog struct {
	mu     sync.Mutex
	events []Progress
}

func (l *progressLog) add(progress Progress) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, progress)
}

func (l *progressLog) snapshot() []Progress {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Progress(nil), l.events...)
}

func assertFinalProgress(t *testing.T, log *progressLog, total int64) {
	t.Helper()
	events := log.snapshot()
	if len(events) == 0 {
		t.Fatal("no progress reported")
	}
	last := events[len(events)-1]
	if !last.Done || last.Error != "" || last.Transferred != total || last.Total != total {
		t.Fatalf("final progress = %+v, want done at %d", last, total)
	}
	for i := 1; i < len(events); i++ {
		if events[i].Transferred < events[i-1].Transferred {
			t.Fatalf("progress went backwards: %+v", events)
		}
	}
}

// A 256 KiB Read loop keeps at most 8 SFTP reads in flight; the pipelined path must overlap far more.
func TestDownloadOverSFTPKeepsManyReadsInFlight(t *testing.T) {
	filesystem, conn, root := newLatencySFTP(t, sftpTestLatency)
	payload := randomPayload(6<<20, 1)
	if err := os.WriteFile(filepath.Join(root, "big.bin"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "big.bin")
	log := &progressLog{}
	conn.ResetPeak()
	transferred, err := Download(context.Background(), filesystem, filepath.Join(root, "big.bin"), target, TransferOptions{
		TaskID: "download", ProgressInterval: time.Millisecond, Progress: log.add,
	})
	if err != nil || transferred != int64(len(payload)) {
		t.Fatalf("Download = %d, %v", transferred, err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("downloaded content differs (%d bytes, %v)", len(got), err)
	}
	if peak := conn.Peak(); peak <= 16 {
		t.Fatalf("peak in-flight requests = %d, want more than 16", peak)
	}
	assertFinalProgress(t, log, int64(len(payload)))
}

func TestUploadOverSFTPKeepsManyWritesInFlight(t *testing.T) {
	filesystem, conn, root := newLatencySFTP(t, sftpTestLatency)
	payload := randomPayload(6<<20, 2)
	source := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(source, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	log := &progressLog{}
	conn.ResetPeak()
	transferred, err := Upload(context.Background(), source, filesystem, filepath.Join(root, "big.bin"), TransferOptions{
		TaskID: "upload", ProgressInterval: time.Millisecond, Progress: log.add,
	})
	if err != nil || transferred != int64(len(payload)) {
		t.Fatalf("Upload = %d, %v", transferred, err)
	}
	got, err := os.ReadFile(filepath.Join(root, "big.bin"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("uploaded content differs (%d bytes, %v)", len(got), err)
	}
	if peak := conn.Peak(); peak <= 16 {
		t.Fatalf("peak in-flight requests = %d, want more than 16", peak)
	}
	assertFinalProgress(t, log, int64(len(payload)))
}

func TestResumedDownloadOverSFTPVerifiesTheWholeFile(t *testing.T) {
	filesystem, _, root := newLatencySFTP(t, time.Millisecond)
	payload := randomPayload(3<<20+123, 3)
	if err := os.WriteFile(filepath.Join(root, "big.bin"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(target, payload[:1<<20+7], 0o600); err != nil {
		t.Fatal(err)
	}
	transferred, err := Download(context.Background(), filesystem, filepath.Join(root, "big.bin"), target, TransferOptions{Resume: true})
	if err != nil || transferred != int64(len(payload)) {
		t.Fatalf("resumed Download = %d, %v", transferred, err)
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, payload) {
		t.Fatal("resumed download content differs")
	}
}

func TestDownloadOverSFTPStopsWhenCancelled(t *testing.T) {
	filesystem, _, root := newLatencySFTP(t, sftpTestLatency)
	payload := randomPayload(8<<20, 4)
	if err := os.WriteFile(filepath.Join(root, "big.bin"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	target := filepath.Join(t.TempDir(), "big.bin")
	transferred, err := Download(ctx, filesystem, filepath.Join(root, "big.bin"), target, TransferOptions{
		ProgressInterval: time.Nanosecond,
		Progress: func(progress Progress) {
			if progress.Transferred > 0 {
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Download error = %v", err)
	}
	if transferred >= int64(len(payload)) {
		t.Fatalf("cancelled Download transferred everything (%d bytes)", transferred)
	}
}

type streamingSource struct {
	io.Reader
	data []byte
}

func (s *streamingSource) PipelinedWriteTo(w io.Writer) (int64, error) {
	var written int64
	for start := 0; start < len(s.data); start += 1000 {
		end := min(start+1000, len(s.data))
		n, err := w.Write(s.data[start:end])
		written += int64(n)
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

type bufferWriter struct {
	bytes.Buffer
	closed bool
}

func (w *bufferWriter) Sync() error  { return nil }
func (w *bufferWriter) Close() error { w.closed = true; return nil }

var _ base.RemoteWriter = (*bufferWriter)(nil)

func TestPipelinedSourceStopsAtTheReportedSize(t *testing.T) {
	data := randomPayload(10_500, 5)
	target := &bufferWriter{}
	transferred, err := finishTransfer(context.Background(), &streamingSource{data: data}, target, 0, 4321, TransferOptions{})
	if err != nil || transferred != 4321 {
		t.Fatalf("finishTransfer = %d, %v", transferred, err)
	}
	if !bytes.Equal(target.Bytes(), data[:4321]) || !target.closed {
		t.Fatalf("target holds %d bytes (closed=%v), want exactly the reported size", target.Len(), target.closed)
	}
}

func TestPipelinedSourceShorterThanReportedFails(t *testing.T) {
	target := &bufferWriter{}
	transferred, err := finishTransfer(context.Background(), &streamingSource{data: make([]byte, 2000)}, target, 0, 5000, TransferOptions{})
	if !errors.Is(err, io.ErrUnexpectedEOF) || transferred != 2000 {
		t.Fatalf("short source = %d, %v", transferred, err)
	}
}

type drainingTarget struct {
	bufferWriter
}

func (d *drainingTarget) PipelinedReadFrom(r io.Reader) (int64, error) {
	return d.Buffer.ReadFrom(r)
}

func TestPipelinedTargetStopsAtTheReportedSize(t *testing.T) {
	data := randomPayload(9000, 6)
	target := &drainingTarget{}
	transferred, err := finishTransfer(context.Background(), bytes.NewReader(data), target, 0, 7000, TransferOptions{})
	if err != nil || transferred != 7000 || !bytes.Equal(target.Bytes(), data[:7000]) {
		t.Fatalf("finishTransfer = %d, %v, %d bytes", transferred, err, target.Len())
	}
}

// BenchmarkSFTPTransferWithLatency reports pipelined throughput over a link with 20 ms of latency.
func BenchmarkSFTPTransferWithLatency(b *testing.B) {
	filesystem, _, root := newLatencySFTP(b, sftpTestLatency)
	payload := randomPayload(16<<20, 7)
	remote := filepath.Join(root, "bench.bin")
	if err := os.WriteFile(remote, payload, 0o600); err != nil {
		b.Fatal(err)
	}
	target := filepath.Join(b.TempDir(), "bench.bin")
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for range b.N {
		if _, err := Download(context.Background(), filesystem, remote, target, TransferOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}
