//go:build unix

package supervisor

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// A restarted helper on the same state directory is a new generation, and an
// attach that expects the previous one is rejected instead of silently
// replaying the crash-recovered record of the session.
func TestRestartedHelperRejectsPreviousGeneration(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	first, err := New(Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	session, err := first.Create(context.Background(), CreateOptions{Command: []string{"/bin/sh", "-c", "sleep 30"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	identity := session.Identity()
	previous := first.Generation()
	if previous == "" {
		t.Fatal("supervisor has no generation")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := New(Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if second.Generation() == previous {
		t.Fatal("restarted supervisor kept the previous generation")
	}
	server := testServer(t, second)
	stale := base.DurableIdentity{CreatedAt: identity.CreatedAt, Incarnation: identity.Incarnation, Generation: previous}
	providers := map[string]base.DurableIdentityAttacher{
		"embedded": NewProvider(second),
		"remote":   NewRemoteProvider(testClient(t, server)),
	}
	for name, provider := range providers {
		t.Run(name, func(t *testing.T) {
			attachment, err := provider.AttachWithIdentity(context.Background(), session.ID(), stale)
			if !errors.Is(err, durable.ErrGenerationMismatch) {
				if attachment != nil {
					_ = attachment.Close()
				}
				t.Fatalf("attach with the previous generation = %v, want durable.ErrGenerationMismatch", err)
			}
			current := stale
			current.Generation = second.Generation()
			attachment, err = provider.AttachWithIdentity(context.Background(), session.ID(), current)
			if err != nil {
				t.Fatalf("attach with the current generation = %v", err)
			}
			_ = attachment.Close()
		})
	}
}

func TestStreamCarriesHelperGeneration(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	provider := NewRemoteProvider(testClient(t, server))
	attachment, err := provider.Create(context.Background(), base.DurableCreateOptions{
		Command: []string{"/bin/sh", "-c", "sleep 30"},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Kill(context.Background()) }()
	stream := attachment.(*streamAttachment).Stream
	if stream.Info().Generation != supervisor.Generation() || stream.Identity().Generation != supervisor.Generation() {
		t.Fatalf("stream generation = %q / %q, want %q", stream.Info().Generation, stream.Identity().Generation, supervisor.Generation())
	}
	descriptors, err := provider.ListDurableDescriptors(context.Background())
	if err != nil || len(descriptors) != 1 || descriptors[0].Generation != supervisor.Generation() {
		t.Fatalf("descriptors = %+v, %v, want the helper generation", descriptors, err)
	}
}

// A stream whose expectations no longer match the helper surfaces the typed
// rejection on input and resize; the session layer sees the durable error.
func TestStreamSurfacesGenerationRejection(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	provider := NewRemoteProvider(testClient(t, server))
	attachment, err := provider.Create(context.Background(), base.DurableCreateOptions{
		Command: []string{"/bin/sh", "-c", "stty raw -echo; exec cat >/dev/null"},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Kill(context.Background()) }()
	stream := attachment.(*streamAttachment)
	stream.identity.Generation = ids.New()
	if err := stream.Resize(context.Background(), 100, 30); !errors.Is(err, durable.ErrGenerationMismatch) {
		t.Fatalf("Resize with a stale generation = %v, want durable.ErrGenerationMismatch", err)
	}
	if _, err := stream.Write([]byte("lost")); !errors.Is(err, durable.ErrGenerationMismatch) {
		t.Fatalf("Write with a stale generation = %v, want durable.ErrGenerationMismatch", err)
	}
}

func rawErrorCode(t *testing.T, conn net.Conn, want frameType) string {
	t.Helper()
	for {
		kind, payload := readErrorFrame(t, conn)
		if kind == frameOutput {
			continue
		}
		if kind != want {
			t.Fatalf("response frame = %d, want %d", kind, want)
		}
		if want == frameInputAck {
			var ack inputAckMsg
			if err := unmarshalFrame(payload, &ack); err != nil {
				t.Fatal(err)
			}
			if ack.Error == nil {
				if ack.Written == 0 {
					t.Fatalf("input ack = %+v, want data written", ack)
				}
				return ""
			}
			if ack.Written != 0 {
				t.Fatalf("rejected input ack = %+v, want nothing written", ack)
			}
			return ack.Error.Code
		}
		if kind == frameOK {
			return ""
		}
		var msg errorMsg
		if err := unmarshalFrame(payload, &msg); err != nil {
			t.Fatal(err)
		}
		return msg.Code
	}
}

func TestServerRejectsMismatchedAttachExpectations(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	session := testScriptSession(t, supervisor, "exec sleep 30")
	identity := session.Identity()
	tests := []struct {
		name string
		msg  attachMsg
		want string
	}{
		{"generation", attachMsg{ID: session.ID(), ExpectIncarnation: identity.Incarnation, ExpectCreatedAt: identity.CreatedAt.UnixNano(), ExpectGeneration: ids.New()}, codeGenerationMismatch},
		{"incarnation", attachMsg{ID: session.ID(), ExpectIncarnation: ids.New(), ExpectCreatedAt: identity.CreatedAt.UnixNano(), ExpectGeneration: supervisor.Generation()}, codeIdentity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conn := rawHello(t, supervisor, server, false)
			payload, err := marshalFrame(frameAttach, test.msg)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeFrame(conn, frameAttach, payload); err != nil {
				t.Fatal(err)
			}
			if code := rawErrorCode(t, conn, frameError); code != test.want {
				t.Fatalf("attach rejection = %q, want %q", code, test.want)
			}
		})
	}
}

func TestServerRejectsMismatchedInputAndResizeExpectations(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	session := testScriptSession(t, supervisor, "stty raw -echo; exec cat >/dev/null")
	conn := rawHello(t, supervisor, server, false)
	info := rawAttachSession(t, conn, attachMsg{ID: session.ID(), ExpectGeneration: supervisor.Generation()})
	if info.Generation != supervisor.Generation() {
		t.Fatalf("attached generation = %q, want %q", info.Generation, supervisor.Generation())
	}
	tests := []struct {
		name        string
		generation  string
		incarnation string
		want        string
	}{
		{"stale generation", ids.New(), info.Incarnation, codeGenerationMismatch},
		{"missing generation", "", info.Incarnation, codeProtocol},
		{"other incarnation", info.Generation, ids.New(), codeIdentity},
		{"current", info.Generation, info.Incarnation, ""},
	}
	for _, test := range tests {
		t.Run("input "+test.name, func(t *testing.T) {
			rawInput(t, conn, test.generation, test.incarnation, []byte("abc"))
			if code := rawErrorCode(t, conn, frameInputAck); code != test.want {
				t.Fatalf("input result = %q, want %q", code, test.want)
			}
		})
		t.Run("resize "+test.name, func(t *testing.T) {
			payload, err := marshalFrame(frameResize, resizeMsg{Cols: 100, Rows: 30, ExpectGeneration: test.generation, ExpectIncarnation: test.incarnation})
			if err != nil {
				t.Fatal(err)
			}
			if err := writeFrame(conn, frameResize, payload); err != nil {
				t.Fatal(err)
			}
			want := frameError
			if test.want == "" {
				want = frameOK
			}
			if code := rawErrorCode(t, conn, want); code != test.want {
				t.Fatalf("resize result = %q, want %q", code, test.want)
			}
		})
	}
	if got := session.Info(); got.Cols != 100 || got.Rows != 30 {
		t.Fatalf("session size = %dx%d, want only the matching resize applied", got.Cols, got.Rows)
	}
}

func TestServerRejectsTruncatedInputHeader(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	session := testScriptSession(t, supervisor, "exec sleep 30")
	conn := rawHello(t, supervisor, server, false)
	rawAttachSession(t, conn, attachMsg{ID: session.ID()})
	if err := writeFrame(conn, frameInput, []byte{40, 'x'}); err != nil {
		t.Fatal(err)
	}
	if code := rawErrorCode(t, conn, frameFatal); code != codeProtocol {
		t.Fatalf("truncated input = %q, want %q", code, codeProtocol)
	}
}

func TestInputPayloadRoundTrip(t *testing.T) {
	payload, err := inputPayload("gen", "inc", []byte("data"))
	if err != nil {
		t.Fatal(err)
	}
	generation, incarnation, data, err := parseInput(payload)
	if err != nil || generation != "gen" || incarnation != "inc" || string(data) != "data" {
		t.Fatalf("parseInput = %q, %q, %q, %v", generation, incarnation, data, err)
	}
	for _, truncated := range [][]byte{nil, {3, 'g'}, {0, 4, 'i'}} {
		if _, _, _, err := parseInput(truncated); !errors.Is(err, ErrProtocol) {
			t.Fatalf("parseInput(%v) = %v, want ErrProtocol", truncated, err)
		}
	}
}
