package fleetserver

import (
	"bytes"
	"context"
	"testing"

	"github.com/coder/websocket"
)

func TestWSConnReadSplitsMessagesAcrossReads(t *testing.T) {
	peer, serverConn := benchmarkWSPair(t)
	conn := newWSConn(serverConn)
	big := bytes.Repeat([]byte("0123456789"), 10)
	for _, message := range [][]byte{big, []byte("short"), {}, []byte("tail")} {
		if err := peer.Write(context.Background(), websocket.MessageBinary, message); err != nil {
			t.Fatal(err)
		}
	}
	want := append(append(append([]byte(nil), big...), "short"...), "tail"...)
	got := make([]byte, 0, len(want))
	chunk := make([]byte, 30)
	for len(got) < len(want) {
		count, err := conn.Read(chunk)
		if err != nil {
			t.Fatalf("read after %d bytes: %v", len(got), err)
		}
		got = append(got, chunk[:count]...)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("stream mismatch: %q", got)
	}
}
