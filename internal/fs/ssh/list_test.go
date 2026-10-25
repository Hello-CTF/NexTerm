package ssh

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/pkg/sftp"
)

func TestListManyEntriesAndSymlinks(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	dir := filepath.Join(root, "big")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const files, subdirs, links = 700, 40, 300
	for i := 0; i < files; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < subdirs; i++ {
		if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("d%03d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{}
	for i := 0; i < links; i++ {
		name := fmt.Sprintf("l%04d", i)
		target := fmt.Sprintf("f%04d", i)
		if i%10 == 0 {
			target = fmt.Sprintf("missing-%d", i) // broken link
		}
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
		want[name] = target
	}
	entries, err := filesystem.List(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != files+subdirs+links {
		t.Fatalf("entries = %d", len(entries))
	}
	if !sort.SliceIsSorted(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if (a.Kind == base.FileDirectory) != (b.Kind == base.FileDirectory) {
			return a.Kind == base.FileDirectory
		}
		return a.Name < b.Name
	}) {
		t.Fatal("entries are not ordered directories first, then by name")
	}
	seenLinks := 0
	for _, e := range entries {
		if e.Path != dir+"/"+e.Name {
			t.Fatalf("path = %q for %q", e.Path, e.Name)
		}
		if e.Kind == base.FileSymlink {
			seenLinks++
			if e.SymlinkTarget != want[e.Name] {
				t.Fatalf("%s -> %q, want %q", e.Name, e.SymlinkTarget, want[e.Name])
			}
		} else if e.SymlinkTarget != "" {
			t.Fatalf("non-link %s has target %q", e.Name, e.SymlinkTarget)
		}
	}
	if seenLinks != links {
		t.Fatalf("links = %d", seenLinks)
	}
}

func TestListErrorsAndCancellation(t *testing.T) {
	filesystem, root := newTestFS(t, nil)
	if _, err := filesystem.List(context.Background(), filepath.Join(root, "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing dir error = %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := os.Symlink("t", filepath.Join(root, fmt.Sprintf("l%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := filesystem.List(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled list error = %v", err)
	}
}

// inflightConn tracks the peak number of SFTP requests awaiting a response by
// counting length-prefixed packets in each direction.
type inflightConn struct {
	net.Conn
	mu       sync.Mutex
	inflight int
	peak     int
	wbuf     []byte
	rbuf     []byte
}

func countFrames(buf *[]byte, p []byte) (n int) {
	*buf = append(*buf, p...)
	for len(*buf) >= 4 {
		l := int(binary.BigEndian.Uint32(*buf))
		if len(*buf) < 4+l {
			break
		}
		*buf = (*buf)[4+l:]
		n++
	}
	return n
}

func (c *inflightConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.inflight += countFrames(&c.wbuf, p)
	c.peak = max(c.peak, c.inflight)
	c.mu.Unlock()
	return c.Conn.Write(p)
}

func (c *inflightConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.mu.Lock()
	c.inflight -= countFrames(&c.rbuf, p[:n])
	c.mu.Unlock()
	return n, err
}

func (c *inflightConn) Peak() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peak
}

func TestListSymlinkLookupsOverlap(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 200; i++ {
		if err := os.Symlink("target", filepath.Join(root, fmt.Sprintf("l%03d", i))); err != nil {
			t.Fatal(err)
		}
	}
	clientSide, serverSide := net.Pipe()
	server, err := sftp.NewServer(serverSide, sftp.WithServerWorkingDirectory(root))
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve()
	conn := &inflightConn{Conn: clientSide}
	client, err := sftp.NewClientPipe(conn, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); server.Close() })

	entries, err := New(client, nil).List(context.Background(), root)
	if err != nil || len(entries) != 200 {
		t.Fatalf("List = %d entries, %v", len(entries), err)
	}
	for _, e := range entries {
		if e.SymlinkTarget != "target" {
			t.Fatalf("%s -> %q", e.Name, e.SymlinkTarget)
		}
	}
	if peak := conn.Peak(); peak < 2 || peak > listLinkConcurrency {
		t.Fatalf("peak in-flight requests = %d, want within [2, %d]", peak, listLinkConcurrency)
	}
}
