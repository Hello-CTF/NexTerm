//go:build unix

package supervisor

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type replayCountingConn struct {
	net.Conn
	bytes *atomic.Int64
}

func (c *replayCountingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.bytes.Add(int64(n))
	return n, err
}

func TestRemoteCompressedReplayResumeAndLiveOutput(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	session := testScriptSession(t, supervisor, `i=0; while [ "$i" -lt 4096 ]; do printf 'terminal history repeated content %06d\n' "$i"; i=$((i+1)); done; printf 'history-ready\n'; read line; printf 'live:%s\n' "$line"`)
	probe, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, probe, "history-ready")
	_ = probe.Close()
	recording, err := readRecording(supervisor.StateDir(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	digest, err := stateDigestFor(supervisor.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	var transferred atomic.Int64
	client := NewRemoteClient(func(ctx context.Context) (net.Conn, error) {
		conn, err := dialSocket(ctx, server.SocketPath())
		if err != nil {
			return nil, err
		}
		return &replayCountingConn{Conn: conn, bytes: &transferred}, nil
	}, digest)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const offset = 111
	stream, err := client.AttachFrom(ctx, session.ID(), nil, offset)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	got := make([]byte, len(recording)-offset)
	if _, err := io.ReadFull(stream, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, recording[offset:]) {
		t.Fatal("compressed replay differs from the recording at the requested offset")
	}
	if wire := transferred.Load(); wire >= int64(len(got))/4 {
		t.Fatalf("history used %d wire bytes for %d original bytes", wire, len(got))
	} else {
		t.Logf("history: raw=%d wire=%d", len(got), wire)
	}
	if _, err := stream.Write([]byte("after-replay\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, stream, "live:after-replay")
}

func TestReplayCompressionRequiresClientCapability(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	session := testScriptSession(t, supervisor, `i=0; while [ "$i" -lt 1024 ]; do printf 'legacy replay repeated content\n'; i=$((i+1)); done`)
	if err := session.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	recording, err := readRecording(supervisor.StateDir(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	// Local clients omit the network compression capability, as old clients do.
	stream, err := testClient(t, server).Attach(context.Background(), session.ID(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if got := readToEOF(t, stream); !bytes.Equal(got, recording) {
		t.Fatal("legacy replay differs from recording")
	}
}
