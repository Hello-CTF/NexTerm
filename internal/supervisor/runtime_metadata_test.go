//go:build unix

package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	buildversion "github.com/Hello-CTF/NexTerm/internal/version"
)

func fakeHelloAckServer(t *testing.T, stateDir string, ack helloMsg) string {
	t.Helper()
	runDir, err := os.MkdirTemp("/tmp", "nx-sup-runtime-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	path := filepath.Join(runDir, "supervisor.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	digest, err := stateDigestFor(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer func() { _ = conn.Close() }()
				kind, payload, err := readFrame(conn)
				if err != nil || kind != frameHello {
					return
				}
				var hello helloMsg
				if err := unmarshalFrame(payload, &hello); err != nil {
					return
				}
				if hello.Version != ProtocolVersion || hello.StateDigest != digest {
					_ = writeFrame(conn, frameError, []byte(`{"code":"state_mismatch","message":"invalid hello"}`))
					return
				}
				response, err := marshalFrame(frameHelloAck, ack)
				if err != nil {
					return
				}
				_ = writeFrame(conn, frameHelloAck, response)
			}()
		}
	}()
	return path
}

func TestRuntimeMetadataDescribesRunningExecutable(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	metadata, known, err := testClient(t, server).RuntimeMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !known {
		t.Fatal("running daemon metadata is unknown")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	if want := hex.EncodeToString(digest[:]); metadata.SHA256 != want {
		t.Fatalf("running SHA256 = %q, want executing file digest %q", metadata.SHA256, want)
	}
	if metadata.Version != buildversion.Version || metadata.Commit != buildversion.Commit {
		t.Fatalf("running release/commit = %q/%q, want %q/%q", metadata.Version, metadata.Commit, buildversion.Version, buildversion.Commit)
	}
	if metadata.Protocol != ProtocolVersion {
		t.Fatalf("running protocol = %d, want %d", metadata.Protocol, ProtocolVersion)
	}

	candidate := filepath.Join(t.TempDir(), "candidate-helper")
	if err := os.WriteFile(candidate, []byte("not the running daemon"), 0o700); err != nil {
		t.Fatal(err)
	}
	candidateDigest := sha256.Sum256([]byte("not the running daemon"))
	if metadata.SHA256 == hex.EncodeToString(candidateDigest[:]) {
		t.Fatal("running metadata was taken from the candidate helper")
	}
}

// A helper that could not hash its own executable sends no runtime metadata;
// the client reports that as unknown instead of inventing it.
func TestRuntimeMetadataUnknownWithoutProof(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	client := NewClient(fakeHelloAckServer(t, stateDir, helloMsg{Version: ProtocolVersion, Activity: &ActivitySnapshot{}}), stateDir)
	metadata, known, err := client.RuntimeMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if known || metadata != (RuntimeMetadata{}) {
		t.Fatalf("metadata = %+v, known = %v; want explicit unknown", metadata, known)
	}
}

func TestRuntimeMetadataAllowsCompatibleReleaseDifference(t *testing.T) {
	want := RuntimeMetadata{
		Version:  "different-release",
		Commit:   "different-commit",
		Protocol: ProtocolVersion,
		SHA256:   strings.Repeat("0123456789abcdef", 4),
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	ack := helloMsg{Version: ProtocolVersion, Runtime: &want, Activity: &ActivitySnapshot{}}
	client := NewClient(fakeHelloAckServer(t, stateDir, ack), stateDir)
	got, known, err := client.RuntimeMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !known || got != want {
		t.Fatalf("metadata = %+v, known = %v; want %+v", got, known, want)
	}
}

func TestRuntimeMetadataKeepsHandshakeGuards(t *testing.T) {
	t.Run("protocol", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), "state")
		ack := helloMsg{Version: ProtocolVersion + 1, Activity: &ActivitySnapshot{}}
		client := NewClient(fakeHelloAckServer(t, stateDir, ack), stateDir)
		if _, _, err := client.RuntimeMetadata(context.Background()); !errors.Is(err, ErrClientUpgradeRequired) {
			t.Fatalf("protocol mismatch error = %v, want ErrClientUpgradeRequired", err)
		}
	})
	t.Run("state digest", func(t *testing.T) {
		supervisor := testSupervisor(t)
		server := testServer(t, supervisor)
		dial := func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", server.SocketPath())
		}
		client := NewRemoteClient(dial, "wrong-state-digest")
		if _, _, err := client.RuntimeMetadata(context.Background()); !errors.Is(err, ErrStateMismatch) {
			t.Fatalf("state mismatch error = %v, want ErrStateMismatch", err)
		}
	})
}

// Runtime metadata and daemon activity are part of every v3 hello
// acknowledgement; no client feature has to ask for them.
func TestHelloAcknowledgementCarriesRuntimeAndActivity(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	conn, err := net.DialTimeout("unix", server.SocketPath(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	digest, err := stateDigestFor(supervisor.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	hello, err := marshalFrame(frameHello, helloMsg{Version: ProtocolVersion, StateDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(conn, frameHello, hello); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := readFrame(conn)
	if err != nil || kind != frameHelloAck {
		t.Fatalf("hello ack = %d, %v", kind, err)
	}
	var ack helloMsg
	if err := unmarshalFrame(payload, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Version != ProtocolVersion || ack.Runtime == nil || ack.Runtime.Protocol != ProtocolVersion || ack.Activity == nil {
		t.Fatalf("hello ack = %+v, want version, runtime metadata and activity", ack)
	}
}
