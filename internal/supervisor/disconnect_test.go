package supervisor

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// attachedPair returns an attached durable stream and the server side of the pipe.
func attachedPair(t *testing.T) (*streamAttachment, net.Conn) {
	t.Helper()
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
	go func() {
		for _, reply := range []struct {
			kind frameType
			body []byte
		}{
			{frameHelloAck, fakeHelloAck()},
			{frameAttached, []byte(`{"id":"terminal","incarnation":"episode","cols":80,"rows":24}`)},
		} {
			if _, _, err := readFrame(peer); err != nil {
				return
			}
			if err := writeFrame(peer, reply.kind, reply.body); err != nil {
				return
			}
		}
	}()
	client := NewRemoteClient(func(context.Context) (net.Conn, error) { return local, nil }, "digest")
	provider := NewRemoteProvider(client)
	att, err := provider.AttachFromWithIdentity(context.Background(), "terminal", base.DurableIdentity{Incarnation: "episode"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return att.(*streamAttachment), peer
}

func assertTransportFailure(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, base.ErrDisconnected) {
		t.Errorf("%s = %v; want an error wrapping base.ErrDisconnected (session layer decides reconnect on it)", what, err)
	}
	for _, bad := range []error{durable.ErrClosed, durable.ErrNotFound, durable.ErrIdentity} {
		if errors.Is(err, bad) {
			t.Errorf("%s = %v maps a network loss to %v", what, err, bad)
		}
	}
}

// Peer vanishes: Read, Wait and Write must all report a disconnect.
func TestPeerDropReportsDisconnected(t *testing.T) {
	att, peer := attachedPair(t)
	_ = peer.Close()
	buf := make([]byte, 16)
	_, rerr := att.Read(buf)
	assertTransportFailure(t, "Read", rerr)
	werr := att.Wait(context.Background())
	assertTransportFailure(t, "Wait", werr)
	_, wrerr := att.Write([]byte("x"))
	assertTransportFailure(t, "Write", wrerr)
}

// A write whose ctx expires while the network stalls (half-open TCP) fails the
// stream with the bare context error, hiding that the transport is broken.
func TestStalledWriteTimeoutReportsDisconnected(t *testing.T) {
	att, _ := attachedPair(t) // server never reads again -> no ack
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := att.Stream.WriteContext(ctx, []byte("x"))
	t.Logf("WriteContext err = %v", err)
	buf := make([]byte, 16)
	_, rerr := att.Read(buf)
	assertTransportFailure(t, "Read after stalled-write timeout", rerr)
	assertTransportFailure(t, "Wait after stalled-write timeout", att.Wait(context.Background()))
}

// Peer disconnects during the hello handshake / before attach reply.
func TestAttachHandshakeDropIsNotIdentityLoss(t *testing.T) {
	for _, stage := range []int{0, 1} {
		local, peer := net.Pipe()
		go func(stage int) {
			defer peer.Close()
			if _, _, err := readFrame(peer); err != nil {
				return
			}
			if stage == 1 {
				_ = writeFrame(peer, frameHelloAck, fakeHelloAck())
				_, _, _ = readFrame(peer)
			}
		}(stage)
		client := NewRemoteClient(func(context.Context) (net.Conn, error) { return local, nil }, "digest")
		_, err := NewRemoteProvider(client).AttachFromWithIdentity(context.Background(), "terminal", base.DurableIdentity{Incarnation: "episode"}, 0)
		if err == nil {
			t.Fatal("expected error")
		}
		for _, bad := range []error{durable.ErrClosed, durable.ErrNotFound, durable.ErrIdentity, durable.ErrGenerationMismatch} {
			if errors.Is(err, bad) {
				t.Errorf("stage %d: %v maps to terminal loss %v", stage, err, bad)
			}
		}
		if !errors.Is(err, base.ErrDisconnected) && !errors.Is(err, durable.ErrUnavailable) {
			t.Errorf("stage %d: %v is neither disconnected nor unavailable", stage, err)
		}
	}
}

// Dial failure while the network is down.
func TestAttachDialFailureIsTransient(t *testing.T) {
	client := NewRemoteClient(func(context.Context) (net.Conn, error) { return nil, errors.New("network is unreachable") }, "digest")
	_, err := NewRemoteProvider(client).AttachFromWithIdentity(context.Background(), "terminal", base.DurableIdentity{Incarnation: "episode"}, 0)
	if !errors.Is(err, durable.ErrUnavailable) {
		t.Fatalf("dial failure = %v, want durable.ErrUnavailable", err)
	}
}

// Both list and attach after a server-side frameError that is not a not-found:
// a hung list must not look like an empty list.
func TestListTimeoutNotNotFound(t *testing.T) {
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
	go func() {
		_, _, _ = readFrame(peer)
		_ = writeFrame(peer, frameHelloAck, fakeHelloAck())
		_, _, _ = readFrame(peer)
	}()
	client := NewRemoteClient(func(context.Context) (net.Conn, error) { return local, nil }, "digest")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := NewRemoteProvider(client).ListDurableDescriptors(ctx)
	t.Logf("list err = %v", err)
	if err == nil || errors.Is(err, durable.ErrNotFound) {
		t.Fatalf("list err = %v", err)
	}
}
