package purpose

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
)

type fileHarness struct {
	server *FileServer
	root   string
	ctx    context.Context
}

func newFileHarness(t *testing.T) *fileHarness {
	t.Helper()
	return &fileHarness{server: NewFileServer(nil), root: t.TempDir(), ctx: context.Background()}
}

// client opens a fresh bridge to the server, one operation per bridge.
func (h *fileHarness) client(t *testing.T) (*deviceproto.Client, func()) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	go func() { _ = h.server.ServeConn(h.ctx, serverConn) }()
	return deviceproto.NewClient(clientConn), func() { _ = clientConn.Close() }
}

// run executes one unary operation on a fresh bridge.
func (h *fileHarness) run(t *testing.T, op func(*deviceproto.Client) error) {
	t.Helper()
	client, done := h.client(t)
	defer done()
	if err := op(client); err != nil {
		t.Fatal(err)
	}
}

func TestFileServerWriteReadStatExists(t *testing.T) {
	h := newFileHarness(t)
	ctx := h.ctx
	file := filepath.Join(h.root, "note.txt")
	h.run(t, func(c *deviceproto.Client) error {
		return c.WriteFile(ctx, file, []byte("hello device"), false)
	})
	h.run(t, func(c *deviceproto.Client) error {
		data, err := c.ReadFile(ctx, file, 1<<20)
		if err != nil {
			return err
		}
		if string(data) != "hello device" {
			return fmt.Errorf("ReadFile = %q; want hello device", data)
		}
		return nil
	})
	h.run(t, func(c *deviceproto.Client) error {
		size, err := c.Stat(ctx, file)
		if err != nil {
			return err
		}
		if size != int64(len("hello device")) {
			return fmt.Errorf("Stat = %d; want %d", size, len("hello device"))
		}
		return nil
	})
	h.run(t, func(c *deviceproto.Client) error {
		_, err := c.Stat(ctx, filepath.Join(h.root, "missing.txt"))
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("Stat missing = %v; want fs.ErrNotExist", err)
		}
		return nil
	})
}

func TestFileServerWriteFileBackup(t *testing.T) {
	h := newFileHarness(t)
	ctx := h.ctx
	file := filepath.Join(h.root, "note.txt")
	h.run(t, func(c *deviceproto.Client) error {
		return c.WriteFile(ctx, file, []byte("version one"), false)
	})
	h.run(t, func(c *deviceproto.Client) error {
		return c.WriteFile(ctx, file, []byte("version two"), true)
	})
	h.run(t, func(c *deviceproto.Client) error {
		backup, err := c.ReadFile(ctx, file+".nexterm-bak", 1<<20)
		if err != nil {
			return err
		}
		if string(backup) != "version one" {
			return fmt.Errorf("backup = %q; want version one", backup)
		}
		return nil
	})
	h.run(t, func(c *deviceproto.Client) error {
		current, err := c.ReadFile(ctx, file, 1<<20)
		if err != nil {
			return err
		}
		if string(current) != "version two" {
			return fmt.Errorf("current = %q; want version two", current)
		}
		return nil
	})
}

func TestFileServerMkdirListRenameDelete(t *testing.T) {
	h := newFileHarness(t)
	ctx := h.ctx
	file := filepath.Join(h.root, "note.txt")
	sub := filepath.Join(h.root, "sub")
	h.run(t, func(c *deviceproto.Client) error {
		return c.WriteFile(ctx, file, []byte("x"), false)
	})
	h.run(t, func(c *deviceproto.Client) error {
		return c.Mkdir(ctx, sub)
	})
	h.run(t, func(c *deviceproto.Client) error {
		entries, err := c.List(ctx, h.root)
		if err != nil {
			return err
		}
		if len(entries) != 2 {
			return fmt.Errorf("List returned %d entries; want 2", len(entries))
		}
		// Directories sort first, then by name.
		if entries[0].Name != "sub" || entries[0].Kind != "dir" {
			return fmt.Errorf("entries[0] = %+v; want the sub directory first", entries[0])
		}
		if entries[1].Name != "note.txt" || entries[1].Kind != "file" {
			return fmt.Errorf("entries[1] = %+v; want note.txt", entries[1])
		}
		return nil
	})
	h.run(t, func(c *deviceproto.Client) error {
		_, err := c.List(ctx, filepath.Join(h.root, "missing"))
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("List missing = %v; want fs.ErrNotExist", err)
		}
		return nil
	})
	renamed := filepath.Join(h.root, "renamed.txt")
	h.run(t, func(c *deviceproto.Client) error {
		return c.Rename(ctx, file, renamed)
	})
	h.run(t, func(c *deviceproto.Client) error {
		if _, err := c.Stat(ctx, file); !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("old path = %v; want fs.ErrNotExist", err)
		}
		return nil
	})
	h.run(t, func(c *deviceproto.Client) error {
		if _, err := c.Stat(ctx, renamed); err != nil {
			return fmt.Errorf("new path = %v; want present", err)
		}
		return nil
	})
	h.run(t, func(c *deviceproto.Client) error {
		return c.Delete(ctx, renamed, false)
	})
	h.run(t, func(c *deviceproto.Client) error {
		return c.WriteFile(ctx, filepath.Join(sub, "nested.txt"), []byte("nested"), false)
	})
	h.run(t, func(c *deviceproto.Client) error {
		return c.Delete(ctx, sub, true)
	})
	h.run(t, func(c *deviceproto.Client) error {
		entries, err := c.List(ctx, h.root)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return fmt.Errorf("List after deletes = %d entries; want empty", len(entries))
		}
		return nil
	})
}

func TestFileServerListChunks(t *testing.T) {
	h := newFileHarness(t)
	ctx := h.ctx
	total := listChunkEntries + 20
	for i := 0; i < total; i++ {
		name := filepath.Join(h.root, fmt.Sprintf("f%04d", i))
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h.run(t, func(c *deviceproto.Client) error {
		entries, err := c.List(ctx, h.root)
		if err != nil {
			return err
		}
		if len(entries) != total {
			return fmt.Errorf("List returned %d entries; want %d across chunks", len(entries), total)
		}
		return nil
	})
}

func TestFileServerChmod(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod unsupported on windows")
	}
	h := newFileHarness(t)
	ctx := h.ctx
	file := filepath.Join(h.root, "note.txt")
	h.run(t, func(c *deviceproto.Client) error {
		return c.WriteFile(ctx, file, []byte("x"), false)
	})
	h.run(t, func(c *deviceproto.Client) error {
		return c.Chmod(ctx, file, 0o640)
	})
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o; want 640", info.Mode().Perm())
	}
}

func TestFileServerChecksum(t *testing.T) {
	h := newFileHarness(t)
	ctx := h.ctx
	file := filepath.Join(h.root, "note.txt")
	content := []byte("checksum me")
	h.run(t, func(c *deviceproto.Client) error {
		return c.WriteFile(ctx, file, content, false)
	})
	md5sum := md5.Sum(content)
	sha256sum := sha256.Sum256(content)
	h.run(t, func(c *deviceproto.Client) error {
		sum, err := c.Checksum(ctx, file, "md5")
		if err != nil {
			return err
		}
		if sum != hex.EncodeToString(md5sum[:]) {
			return fmt.Errorf("md5 = %q; want %q", sum, hex.EncodeToString(md5sum[:]))
		}
		return nil
	})
	h.run(t, func(c *deviceproto.Client) error {
		sum, err := c.Checksum(ctx, file, "sha256")
		if err != nil {
			return err
		}
		if sum != hex.EncodeToString(sha256sum[:]) {
			return fmt.Errorf("sha256 = %q; want %q", sum, hex.EncodeToString(sha256sum[:]))
		}
		return nil
	})
	h.run(t, func(c *deviceproto.Client) error {
		if _, err := c.Checksum(ctx, file, "sha512"); err == nil {
			return errors.New("unsupported algorithm must fail")
		}
		return nil
	})
}

func TestFileServerReadRange(t *testing.T) {
	h := newFileHarness(t)
	ctx := h.ctx
	file := filepath.Join(h.root, "data.bin")
	content := make([]byte, 100)
	for i := range content {
		content[i] = byte(i)
	}
	h.run(t, func(c *deviceproto.Client) error {
		return c.WriteFile(ctx, file, content, false)
	})
	h.run(t, func(c *deviceproto.Client) error {
		data, size, err := c.ReadRange(ctx, file, 10, 20)
		if err != nil {
			return err
		}
		if size != 100 {
			return fmt.Errorf("size = %d; want 100", size)
		}
		if len(data) != 20 || data[0] != 10 || data[19] != 29 {
			return fmt.Errorf("window = %v; want bytes 10..29", data)
		}
		return nil
	})
	h.run(t, func(c *deviceproto.Client) error {
		data, size, err := c.ReadRange(ctx, file, 200, 20)
		if err != nil {
			return err
		}
		if size != 100 || len(data) != 0 {
			return fmt.Errorf("past-EOF = %d bytes size %d; want empty window size 100", len(data), size)
		}
		return nil
	})
	h.run(t, func(c *deviceproto.Client) error {
		if _, _, err := c.ReadRange(ctx, file, -1, 20); !errors.Is(err, deviceproto.ErrInvalid) {
			return fmt.Errorf("negative offset = %v; want ErrInvalid", err)
		}
		return nil
	})
	h.run(t, func(c *deviceproto.Client) error {
		if _, _, err := c.ReadRange(ctx, file, 0, 0); !errors.Is(err, deviceproto.ErrInvalid) {
			return fmt.Errorf("zero maxBytes = %v; want ErrInvalid", err)
		}
		return nil
	})
}

func TestFileServerOpenStreams(t *testing.T) {
	h := newFileHarness(t)
	ctx := h.ctx
	file := filepath.Join(h.root, "stream.bin")
	content := make([]byte, 70*1024)
	for i := range content {
		content[i] = byte(i % 251)
	}
	client, done := h.client(t)
	writer, err := client.OpenWrite(ctx, file, false)
	if err != nil {
		done()
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	done()

	client, done = h.client(t)
	reader, err := client.OpenRead(ctx, file)
	if err != nil {
		done()
		t.Fatal(err)
	}
	if reader.Size() != int64(len(content)) {
		t.Fatalf("OpenRead size = %d; want %d", reader.Size(), len(content))
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	done()
	if len(data) != len(content) {
		t.Fatalf("OpenRead returned %d bytes; want %d", len(data), len(content))
	}
	for i := range content {
		if data[i] != content[i] {
			t.Fatalf("byte %d = %d; want %d", i, data[i], content[i])
		}
	}

	client, done = h.client(t)
	appender, err := client.OpenWrite(ctx, file, true)
	if err != nil {
		done()
		t.Fatal(err)
	}
	if _, err := appender.Write([]byte("tail")); err != nil {
		t.Fatal(err)
	}
	if err := appender.Close(); err != nil {
		t.Fatal(err)
	}
	done()
	h.run(t, func(c *deviceproto.Client) error {
		data, err := c.ReadFile(ctx, file, 1<<20)
		if err != nil {
			return err
		}
		if string(data[len(data)-4:]) != "tail" {
			return fmt.Errorf("appended file ends with %q; want tail", data[len(data)-4:])
		}
		return nil
	})
}

func TestFileServerNotFoundMapping(t *testing.T) {
	h := newFileHarness(t)
	ctx := h.ctx
	missing := filepath.Join(h.root, "missing.txt")
	h.run(t, func(c *deviceproto.Client) error {
		if _, err := c.ReadFile(ctx, missing, 1<<20); !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("ReadFile = %v; want fs.ErrNotExist", err)
		}
		return nil
	})
	client, done := h.client(t)
	if _, err := client.OpenRead(ctx, missing); !errors.Is(err, fs.ErrNotExist) {
		done()
		t.Fatalf("OpenRead = %v; want fs.ErrNotExist", err)
	}
	done()
	client, done = h.client(t)
	defer done()
	if _, err := client.OpenWrite(ctx, filepath.Join(h.root, "missing-dir", "x"), false); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("OpenWrite = %v; want fs.ErrNotExist", err)
	}
}

func TestFileServerRejectsOversizeDeclaredWrite(t *testing.T) {
	h := newFileHarness(t)
	clientConn, serverConn := net.Pipe()
	go func() { _ = h.server.ServeConn(h.ctx, serverConn) }()
	defer clientConn.Close()
	err := deviceproto.WriteMessage(clientConn, deviceproto.FrameWriteFile, deviceproto.WriteMsg{
		Path: filepath.Join(h.root, "x"), Size: deviceproto.MaxWriteFileSize + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	kind, payload, err := deviceproto.ReadFrame(clientConn)
	if err != nil {
		t.Fatal(err)
	}
	if kind != deviceproto.FrameError {
		t.Fatalf("kind = %d; want FrameError", kind)
	}
	if !errors.Is(deviceproto.ReadError(payload), deviceproto.ErrInvalid) {
		t.Fatalf("error = %v; want ErrInvalid", deviceproto.ReadError(payload))
	}
}

func TestFileServerRejectsUnknownFrame(t *testing.T) {
	h := newFileHarness(t)
	clientConn, serverConn := net.Pipe()
	go func() { _ = h.server.ServeConn(h.ctx, serverConn) }()
	defer clientConn.Close()
	if err := deviceproto.WriteMessage(clientConn, deviceproto.FrameDial, deviceproto.DialMsg{Network: "tcp", Address: "x:1"}); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := deviceproto.ReadFrame(clientConn)
	if err != nil {
		t.Fatal(err)
	}
	if kind != deviceproto.FrameError {
		t.Fatalf("kind = %d; want FrameError", kind)
	}
	if !errors.Is(deviceproto.ReadError(payload), deviceproto.ErrProtocol) {
		t.Fatalf("error = %v; want ErrProtocol", deviceproto.ReadError(payload))
	}
}

func TestFileServerServeClaimsBridge(t *testing.T) {
	ends := make(chan net.Conn, 1)
	var gotBridge, gotPurpose string
	server := NewFileServer(BridgeDialerFunc(func(ctx context.Context, bridgeID, purpose string) (net.Conn, error) {
		gotBridge, gotPurpose = bridgeID, purpose
		serverSide, clientSide := net.Pipe()
		ends <- clientSide
		return serverSide, nil
	}))
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), "bridge-9") }()
	clientSide := <-ends
	dir := filepath.Join(t.TempDir(), "served")
	if err := deviceproto.NewClient(clientSide).Mkdir(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve = %v; want nil after the operation completes", err)
	}
	_ = clientSide.Close()
	if gotBridge != "bridge-9" || gotPurpose != deviceproto.PurposeFile {
		t.Fatalf("Serve dialed bridge %q purpose %q; want bridge-9/file", gotBridge, gotPurpose)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("served mkdir = %v; want the directory on disk", err)
	}
}

func startEchoListener(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go echoConn(conn)
		}
	}()
	return listener.Addr().String()
}

// echoConn mirrors bytes and propagates the half-close so the other side
// observes EOF after its CloseWrite.
func echoConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	buffer := make([]byte, 64*1024)
	for {
		count, err := conn.Read(buffer)
		if count > 0 {
			if _, writeErr := conn.Write(buffer[:count]); writeErr != nil {
				return
			}
		}
		if err != nil {
			if tcpConn, ok := conn.(*net.TCPConn); ok {
				_ = tcpConn.CloseWrite()
			}
			return
		}
	}
}

func dialHarness(t *testing.T) (*deviceproto.Client, func()) {
	t.Helper()
	server := NewDialServer(nil)
	clientConn, serverConn := net.Pipe()
	go func() { _ = server.ServeConn(context.Background(), serverConn) }()
	return deviceproto.NewClient(clientConn), func() { _ = clientConn.Close() }
}

func TestDialServerEchoRoundTrip(t *testing.T) {
	echo := startEchoListener(t)
	client, done := dialHarness(t)
	defer done()
	ctx := context.Background()
	stream, err := client.Dial(ctx, "tcp", echo)
	if err != nil {
		t.Fatal(err)
	}
	message := []byte("forward through the device")
	if _, err := stream.Write(message); err != nil {
		t.Fatal(err)
	}
	echoed := make([]byte, len(message))
	if _, err := io.ReadFull(stream, echoed); err != nil {
		t.Fatal(err)
	}
	if string(echoed) != string(message) {
		t.Fatalf("echo = %q; want %q", echoed, message)
	}
	if err := stream.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("Read after half-close = %v; want io.EOF", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDialServerConnectRefused(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	client, done := dialHarness(t)
	defer done()
	if _, err := client.Dial(context.Background(), "tcp", address); err == nil {
		t.Fatal("Dial to a closed port must fail")
	}
}

func TestDialServerRejectsBadNetwork(t *testing.T) {
	client, done := dialHarness(t)
	defer done()
	_, err := client.Dial(context.Background(), "udp", "127.0.0.1:53")
	if !errors.Is(err, deviceproto.ErrInvalid) {
		t.Fatalf("Dial udp = %v; want ErrInvalid", err)
	}
}

func TestDialServerRejectsNonDialFirstFrame(t *testing.T) {
	server := NewDialServer(nil)
	clientConn, serverConn := net.Pipe()
	go func() { _ = server.ServeConn(context.Background(), serverConn) }()
	defer clientConn.Close()
	if err := deviceproto.WriteMessage(clientConn, deviceproto.FrameList, deviceproto.PathMsg{Path: "/"}); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := deviceproto.ReadFrame(clientConn)
	if err != nil {
		t.Fatal(err)
	}
	if kind != deviceproto.FrameError {
		t.Fatalf("kind = %d; want FrameError", kind)
	}
	if !errors.Is(deviceproto.ReadError(payload), deviceproto.ErrProtocol) {
		t.Fatalf("error = %v; want ErrProtocol", deviceproto.ReadError(payload))
	}
}
