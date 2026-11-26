package relaymux

import (
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

func TestReadReplyWithinWaitsForItsOwnBound(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = WriteFrame(server, Reply{Status: 200})
	}()
	reply, err := ReadReplyWithin(client, 2*time.Second)
	if err != nil || reply.Status != 200 {
		t.Fatalf("reply = %+v, %v", reply, err)
	}

	quiet, peer := net.Pipe()
	defer quiet.Close()
	defer peer.Close()
	if _, err := ReadReplyWithin(quiet, 50*time.Millisecond); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a silent peer must hit the bound, got %v", err)
	}
}
