package rstream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"testing"
	"time"
)

func isReset(err error, code uint16) bool {
	reset, ok := errors.AsType[*ResetError](err)
	return ok && reset.Code == code
}

func (s *Stream) ackedOffset() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acked
}

// A make-before-break migration whose RESUME is slow to reach the agent, while the application keeps reading on
// the old carrier: by the time the agent sees the RESUME it holds ACKs past the RESUME's offset, and the stream
// still switches and delivers every byte once.
func TestMigrateWhileReadingOnTheOldCarrier(t *testing.T) {
	h := newHarness(t, ServerOptions{}, "file")
	client, _, agent := h.open(Options{})
	rng := rand.New(rand.NewPCG(3, 3))
	down := randomBytes(rng, 300_000)
	if _, err := agent.Write(down); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 0, len(down)+100)
	buffer := make([]byte, 16*1024)
	read := func(until int) {
		t.Helper()
		for len(got) < until {
			n, err := client.Read(buffer[:min(len(buffer), until-len(got))])
			if err != nil {
				t.Fatalf("read after %d bytes: %v", len(got), err)
			}
			got = append(got, buffer[:n]...)
		}
	}
	read(50_000)

	// The RESUME waits in the new link until Accept runs.
	link := newFakeLink()
	migrated := make(chan error, 1)
	go func() { migrated <- client.Migrate(context.Background(), link.client()) }()
	waitFor(t, "the RESUME to be sent", func() bool {
		link.mu.Lock()
		defer link.mu.Unlock()
		return len(link.agent().inbox) > 0
	})
	read(len(down))
	waitFor(t, "the agent to see ACKs past the RESUME", func() bool { return agent.(*Stream).ackedOffset() > 50_000 })

	go func() {
		conn, resumed, err := h.server.Accept(context.Background(), link.agent(), "file")
		h.accepted <- accepted{link: link, conn: conn, resumed: resumed, err: err}
	}()
	if result := h.next(); result.err != nil || !result.resumed {
		t.Fatalf("delayed resume = %+v", result)
	}
	if err := <-migrated; err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := agent.Write([]byte("tail")); err != nil {
		t.Fatal(err)
	}
	read(len(down) + 4)
	if !bytes.Equal(got[:len(down)], down) || string(got[len(down):]) != "tail" {
		t.Fatal("bytes differ after a migration that raced the reader")
	}
	if _, err := client.Write([]byte("up")); err != nil {
		t.Fatal(err)
	}
	up := make([]byte, 2)
	if _, err := io.ReadFull(agent, up); err != nil || string(up) != "up" {
		t.Fatalf("agent read %q, %v", up, err)
	}
}

// A side that closed the stream while the peer keeps writing resets it once its own bytes were delivered, so
// the peer's writer fails promptly instead of after the linger time.
func TestCloseResetsAPeerThatKeepsWriting(t *testing.T) {
	h := newHarness(t, ServerOptions{}, "file")
	client, _, agent := h.open(Options{})
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := client.Write(make([]byte, 1024))
		if err != nil {
			if !isReset(err, ResetClosed) {
				t.Fatalf("write into a closed stream = %v; want a closed reset", err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("writer was not told the peer closed")
		}
		time.Sleep(time.Millisecond)
	}
}

func (s *Stream) finDeliveredNow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finDelivered
}

// A client whose last ACK from the agent was lost resumes a stream the agent already completed; the agent
// remembers the completion, so the client finishes cleanly instead of failing as if the agent had forgotten it.
func TestResumeOfAFinishedStream(t *testing.T) {
	h := newHarness(t, ServerOptions{}, "file")
	client, link, agent := h.open(Options{})
	client.OnCarrierLost(func() {})
	if err := agent.(*Stream).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if rest, err := io.ReadAll(client); err != nil || len(rest) != 0 {
		t.Fatalf("client read to EOF = %d bytes, %v", len(rest), err)
	}
	waitFor(t, "the agent's FIN to be acknowledged", agent.(*Stream).finDeliveredNow)
	// The agent's ACK of the client's FIN goes nowhere, yet its write succeeds, so the agent completes.
	link.agent().configure(func(c *fakeConn) { c.blackhole = true })
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if rest, err := io.ReadAll(agent); err != nil || len(rest) != 0 {
		t.Fatalf("agent read to EOF = %d bytes, %v", len(rest), err)
	}
	select {
	case <-agent.(*Stream).Done():
	case <-time.After(10 * time.Second):
		t.Fatal("agent did not complete the stream")
	}
	err := client.Migrate(context.Background(), h.carrier().client())
	if result := h.next(); !isReset(result.err, ResetFinished) {
		t.Fatalf("accept = %+v; want a finished answer", result)
	}
	if !errors.Is(err, errFinished) {
		t.Fatalf("resume of a finished stream = %v", err)
	}
	select {
	case <-client.Done():
	default:
		t.Fatal("client stream did not end")
	}
}
