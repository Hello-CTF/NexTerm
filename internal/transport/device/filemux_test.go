package device

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/fs/remote"
)

type countingDial struct {
	mu      sync.Mutex
	counts  map[string]int
	muxable bool
	serve   context.Context
}

func (d *countingDial) dial(_ context.Context, purpose string) (net.Conn, error) {
	d.mu.Lock()
	d.counts[purpose]++
	d.mu.Unlock()
	client, server := net.Pipe()
	switch purpose {
	case deviceproto.PurposeFileMux:
		if !d.muxable {
			_ = client.Close()
			_ = server.Close()
			return nil, errors.New("该设备代理版本过旧，不支持此通道")
		}
		go func() { _ = remote.ServeMux(d.serve, server) }()
	case deviceproto.PurposeFile:
		go func() { _ = remote.NewServer().ServeConn(d.serve, server) }()
	default:
		return nil, errors.New("unexpected purpose " + purpose)
	}
	return client, nil
}

func (d *countingDial) count(purpose string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.counts[purpose]
}

func listTimes(t *testing.T, transport *Transport, times int) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	filesystem, err := transport.FileSystem(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for range times {
		entries, err := filesystem.List(context.Background(), dir)
		if err != nil || len(entries) != 1 {
			t.Fatalf("list = %+v, %v", entries, err)
		}
	}
}

// File operations share one bridge instead of pairing a bridge each.
func TestFileOperationsShareOneBridge(t *testing.T) {
	serve, stop := context.WithCancel(context.Background())
	defer stop()
	dial := &countingDial{counts: map[string]int{}, muxable: true, serve: serve}
	transport := New("dev-1", "digest", dial.dial, 1)
	defer transport.Close()
	listTimes(t, transport, 5)
	if mux, single := dial.count(deviceproto.PurposeFileMux), dial.count(deviceproto.PurposeFile); mux != 1 || single != 0 {
		t.Fatalf("bridges: file-mux %d, file %d; want one shared bridge", mux, single)
	}
}

// An agent that cannot multiplex still serves files, a bridge per operation,
// and is not asked to multiplex again for a while.
func TestFileOperationsFallBackForAnOlderAgent(t *testing.T) {
	serve, stop := context.WithCancel(context.Background())
	defer stop()
	dial := &countingDial{counts: map[string]int{}, serve: serve}
	transport := New("dev-1", "digest", dial.dial, 1)
	defer transport.Close()
	listTimes(t, transport, 3)
	if mux, single := dial.count(deviceproto.PurposeFileMux), dial.count(deviceproto.PurposeFile); mux != 1 || single != 3 {
		t.Fatalf("bridges: file-mux %d, file %d; want one refused mux attempt and a bridge per operation", mux, single)
	}
}
