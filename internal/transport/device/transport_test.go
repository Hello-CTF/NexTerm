package device

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type deviceTestRig struct {
	digest string
	dial   DialFunc
}

func newDeviceTestRig(t *testing.T) *deviceTestRig {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := supervisor.HelperEndpoint(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	server, err := supervisor.NewServer(instance, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := supervisor.StateDigest(stateDir, strconv.Itoa(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = instance.Close()
	})
	return &deviceTestRig{
		digest: digest,
		dial: func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", endpoint)
		},
	}
}

func TestTransportKindGenerationAndLifecycle(t *testing.T) {
	rig := newDeviceTestRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 7)
	if transport.Kind() != "device" {
		t.Fatalf("Kind() = %q; want device", transport.Kind())
	}
	if transport.Generation() != 7 {
		t.Fatalf("Generation() = %d; want 7", transport.Generation())
	}
	if transport.DeviceID() != "dev-1" {
		t.Fatalf("DeviceID() = %q; want dev-1", transport.DeviceID())
	}
	if !transport.IsAlive() {
		t.Fatal("IsAlive() = false before Close; want true")
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	if transport.IsAlive() {
		t.Fatal("IsAlive() = true after Close; want false")
	}
}

func TestTransportExecUnsupported(t *testing.T) {
	rig := newDeviceTestRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 1)
	if _, err := transport.Exec(context.Background(), "echo hi", base.ExecOptions{}); !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("Exec error = %v; want base.ErrUnsupported", err)
	}
}

func TestTransportCapabilitySurface(t *testing.T) {
	rig := newDeviceTestRig(t)
	var transport base.Transport = New("dev-1", rig.digest, rig.dial, 1)
	if _, ok := transport.(base.PTYTransport); ok {
		t.Fatal("device transport must not expose PTY channels; tabs are supervisor durable sessions")
	}
	if _, ok := transport.(base.FileTransport); ok {
		t.Fatal("device transport must not expose a filesystem; supervisor v2 has no fs frames")
	}
	if _, ok := transport.(base.Dialer); ok {
		t.Fatal("device transport must not expose a dialer; port forwarding stays unsupported")
	}
}

func TestTransportPingRoundTrip(t *testing.T) {
	rig := newDeviceTestRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 1)
	if _, err := transport.Ping(context.Background()); err != nil {
		t.Fatalf("Ping = %v; want a supervisor list round-trip", err)
	}
}

func TestTransportPingDialFailure(t *testing.T) {
	rig := newDeviceTestRig(t)
	transport := New("dev-1", rig.digest, func(context.Context, string) (net.Conn, error) {
		return nil, errors.New("bridge unavailable")
	}, 1)
	if _, err := transport.Ping(context.Background()); err == nil {
		t.Fatal("Ping must fail when the bridge dial fails")
	}
}

func TestTransportProviderCreateAttachRoundTrip(t *testing.T) {
	rig := newDeviceTestRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 1)
	provider := transport.Provider()
	ctx := context.Background()
	tabID := ids.New()
	attachment, err := provider.Create(ctx, base.DurableCreateOptions{
		ID: tabID, Command: []string{"/bin/sh"}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attachment.Write([]byte("echo device-provider-marker\n")); err != nil {
		t.Fatal(err)
	}
	readUntilMarker(t, attachment, "device-provider-marker")
	// detach 保留设备端会话, 重 attach 回放历史输出 (durable 语义)。
	if err := attachment.Close(); err != nil {
		t.Fatal(err)
	}
	reattached, err := provider.Attach(ctx, tabID)
	if err != nil {
		t.Fatal(err)
	}
	readUntilMarker(t, reattached, "device-provider-marker")
	if err := reattached.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	if err := reattached.Close(); err != nil {
		t.Fatal(err)
	}
}

func readUntilMarker(t *testing.T, attachment base.DurableAttachment, marker string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	buffer := make([]byte, 32*1024)
	var received []byte
	for time.Now().Before(deadline) {
		count, err := attachment.Read(buffer)
		received = append(received, buffer[:count]...)
		if strings.Contains(string(received), marker) {
			return
		}
		if err != nil {
			t.Fatalf("read = %v after %q; want marker %q", err, received, marker)
		}
	}
	t.Fatalf("timed out waiting for %q; received %q", marker, received)
}
