//go:build !windows

package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// prefixedConn replays an already consumed request frame before the rest of the stream.
type prefixedConn struct {
	net.Conn
	reader io.Reader
}

func (c prefixedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

// legacyServerDial serves each operation like an agent that predates the
// requests in missing: they get the old server's unexpected-frame error.
func legacyServerDial(t *testing.T, missing ...deviceproto.FrameType) func(context.Context) (net.Conn, error) {
	t.Helper()
	server := NewServer()
	return func(ctx context.Context) (net.Conn, error) {
		client, serverSide := net.Pipe()
		go func() {
			kind, payload, err := deviceproto.ReadFrame(serverSide)
			if err != nil {
				_ = serverSide.Close()
				return
			}
			for _, rejected := range missing {
				if kind == rejected {
					_ = deviceproto.WriteError(serverSide, fmt.Errorf("%w: unexpected request frame %d", deviceproto.ErrProtocol, byte(kind)))
					_ = serverSide.Close()
					return
				}
			}
			var frame bytes.Buffer
			if err := deviceproto.WriteFrame(&frame, kind, payload); err != nil {
				_ = serverSide.Close()
				return
			}
			_ = server.ServeConn(context.Background(), prefixedConn{Conn: serverSide, reader: io.MultiReader(&frame, serverSide)})
		}()
		return client, nil
	}
}

func TestUploadToAgentWithoutStatInfoKeepsTargetMode(t *testing.T) {
	filesystem := NewFileSystem(Options{Dial: legacyServerDial(t, deviceproto.FrameStatInfo), Channel: base.FileChannelDevice})
	dir := t.TempDir()
	target := filepath.Join(dir, "target.csv")
	if err := os.WriteFile(target, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.csv")
	if err := os.WriteFile(source, []byte("a,b\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := filesystem.FileMode(context.Background(), target); !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("FileMode on a legacy agent = %v, want ErrUnsupported", err)
	}
	if _, err := fslocal.Upload(context.Background(), source, filesystem, target, fslocal.TransferOptions{}); err != nil {
		t.Fatalf("Upload to a legacy agent: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "a,b\n1,2\n" {
		t.Fatalf("target = %q, %v", data, err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("target mode = %v, want 0640 kept", info.Mode().Perm())
	}
}
