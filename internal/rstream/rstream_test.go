package rstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

func randomBytes(rng *rand.Rand, n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(rng.Uint32())
	}
	return data
}

// exchange runs both directions to completion: each side writes its payload, half-closes and reads the peer's
// payload to EOF. It returns what each side read.
func exchange(t *testing.T, client, agent net.Conn, up, down []byte, onClientRead func(total int)) (gotUp, gotDown []byte) {
	t.Helper()
	agentDone := make(chan []byte, 1)
	agentErr := make(chan error, 2)
	go func() {
		go func() {
			if _, err := agent.Write(down); err != nil {
				agentErr <- fmt.Errorf("agent write: %w", err)
				return
			}
			agentErr <- agent.(*Stream).CloseWrite()
		}()
		data, err := io.ReadAll(agent)
		if err != nil {
			agentErr <- fmt.Errorf("agent read: %w", err)
		}
		agentDone <- data
	}()
	// The client half-closes only after it read the download, so the stream cannot finish while the test still
	// switches its carriers.
	clientErr := make(chan error, 1)
	go func() {
		_, err := client.Write(up)
		clientErr <- err
	}()
	buffer := make([]byte, 7000)
	for {
		n, err := client.Read(buffer)
		gotDown = append(gotDown, buffer[:n]...)
		if n > 0 && onClientRead != nil {
			onClientRead(len(gotDown))
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("client read after %d bytes: %v", len(gotDown), err)
		}
	}
	if err := <-clientErr; err != nil {
		t.Fatalf("client write: %v", err)
	}
	if err := client.(*Stream).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case gotUp = <-agentDone:
	case <-time.After(20 * time.Second):
		t.Fatalf("agent did not read the upload to EOF:\nclient %s\nagent %s", client.(*Stream).debugState(), agent.(*Stream).debugState())
	}
	if err := <-agentErr; err != nil {
		t.Fatal(err)
	}
	return gotUp, gotDown
}

func finishBoth(t *testing.T, h *harness, client, agent net.Conn, budgets ...*Budget) {
	t.Helper()
	_ = client.Close()
	_ = agent.Close()
	for _, stream := range []*Stream{client.(*Stream), agent.(*Stream)} {
		select {
		case <-stream.Done():
		case <-time.After(10 * time.Second):
			t.Fatalf("stream did not finish after both sides closed:\nclient %s\nagent %s", client.(*Stream).debugState(), agent.(*Stream).debugState())
		}
	}
	waitFor(t, "server to drop the finished stream", func() bool { return h.server.Len() == 0 })
	for _, budget := range budgets {
		if used := budget.Used(); used != 0 {
			t.Fatalf("budget still holds %d bytes after the streams finished", used)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	budget := NewBudget(DefaultBudgetBytes)
	h := newHarness(t, ServerOptions{Budget: NewBudget(DefaultBudgetBytes)}, "file")
	client, _, agent := h.open(Options{Budget: budget})
	rng := rand.New(rand.NewPCG(1, 2))
	up, down := randomBytes(rng, 300_000), randomBytes(rng, 200_000)
	gotUp, gotDown := exchange(t, client, agent, up, down, nil)
	if !bytes.Equal(gotUp, up) || !bytes.Equal(gotDown, down) {
		t.Fatalf("round trip corrupted: up %d/%d down %d/%d", len(gotUp), len(up), len(gotDown), len(down))
	}
	finishBoth(t, h, client, agent, budget, h.server.opts.Budget)
	if client.Purpose() != "file" {
		t.Fatalf("purpose = %q", client.Purpose())
	}
}

// Property: whatever the switch points and the way each carrier ends (a planned migration, a cut, a half-closed
// carrier) and however new carriers misbehave (duplicated or delayed writes), each direction delivers its bytes
// exactly once and in order, and both ends finish cleanly.
func TestExactlyOnceAnySwitchPoint(t *testing.T) {
	for seed := uint64(1); seed <= 24; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { runSwitchScenario(t, seed) })
	}
}

func runSwitchScenario(t *testing.T, seed uint64) {
	rng := rand.New(rand.NewPCG(seed, 0x5eed))
	window := []int{minWindow, 16 * 1024, 64 * 1024}[rng.IntN(3)]
	up := randomBytes(rng, 1+rng.IntN(200_000))
	down := randomBytes(rng, 1+rng.IntN(200_000))
	type event struct {
		at   int
		kind int
	}
	const (
		migrate = iota
		cut
		halfClose
		kinds
	)
	events := make([]event, 1+rng.IntN(4))
	for i := range events {
		events[i] = event{at: rng.IntN(len(down)), kind: rng.IntN(kinds)}
	}
	for i := 1; i < len(events); i++ {
		for j := i; j > 0 && events[j].at < events[j-1].at; j-- {
			events[j], events[j-1] = events[j-1], events[j]
		}
	}
	misbehave := make([]int, 16)
	for i := range misbehave {
		misbehave[i] = rng.IntN(3)
	}

	h := newHarness(t, ServerOptions{}, "forward")
	budget := NewBudget(DefaultBudgetBytes)
	client, first, agent := h.open(Options{BufferSize: window, Budget: budget})

	var mu sync.Mutex
	current, carriers := first, 0
	newCarrier := func() *fakeLink {
		link := h.carrier()
		mu.Lock()
		mode := misbehave[carriers%len(misbehave)]
		carriers++
		current = link
		mu.Unlock()
		for _, end := range []*fakeConn{link.client(), link.agent()} {
			end.configure(func(c *fakeConn) {
				c.duplicate = mode == 1
				if mode == 2 {
					c.delay = 100 * time.Microsecond
				}
			})
		}
		return link
	}
	client.ResumeWith(func(context.Context) (net.Conn, error) { return newCarrier().client(), nil })

	resumes := make(chan error, 64)
	stopDrain := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case result := <-h.accepted:
				switch {
				case result.err != nil && result.link.damaged():
					// The test broke this carrier before its RESUME arrived; the client retries on another.
				case result.err != nil:
					resumes <- result.err
				case !result.resumed || result.conn != agent:
					resumes <- fmt.Errorf("carrier accepted as a new stream: %+v", result)
				}
			case <-stopDrain:
				return
			}
		}
	}()

	next := 0
	gotUp, gotDown := exchange(t, client, agent, up, down, func(total int) {
		for next < len(events) && total > events[next].at {
			mu.Lock()
			link := current
			mu.Unlock()
			switch events[next].kind {
			case migrate:
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				if err := client.Migrate(ctx, newCarrier().client()); err != nil {
					t.Errorf("seed %d: migrate at %d: %v", seed, total, err)
				}
				cancel()
			case cut:
				link.Cut()
			case halfClose:
				link.agent().HalfClose()
			}
			next++
		}
	})
	if !bytes.Equal(gotDown, down) {
		t.Fatalf("seed %d: download corrupted: %d bytes, want %d", seed, len(gotDown), len(down))
	}
	if !bytes.Equal(gotUp, up) {
		t.Fatalf("seed %d: upload corrupted: %d bytes, want %d", seed, len(gotUp), len(up))
	}
	finishBoth(t, h, client, agent, budget, h.server.opts.Budget)
	close(stopDrain)
	<-drained
	close(resumes)
	for err := range resumes {
		t.Errorf("seed %d (window %d, up %d, down %d, events %v): %v", seed, window, len(up), len(down), events, err)
	}
}

// A writer blocks once its buffer holds a window of unacknowledged bytes and continues when the peer reads.
func TestWriteBackpressure(t *testing.T) {
	h := newHarness(t, ServerOptions{}, "forward")
	client, _, agent := h.open(Options{BufferSize: 16 * 1024})
	payload := make([]byte, 64*1024)
	_ = client.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	n, err := client.Write(payload)
	if !errors.Is(err, os.ErrDeadlineExceeded) || n != 16*1024 {
		t.Fatalf("write into a full window = %d, %v; want 16384 bytes then a deadline error", n, err)
	}
	_ = client.SetWriteDeadline(time.Time{})
	done := make(chan error, 1)
	go func() {
		_, err := client.Write(payload[n:])
		done <- err
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(agent, got); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("write after the peer read: %v", err)
	}
}

// The shared budget caps unacknowledged bytes across streams: a second stream's writer waits while the first
// stream holds the budget and continues once the first stream's peer reads.
func TestBudgetCapAcrossStreams(t *testing.T) {
	budget := NewBudget(32 * 1024)
	h := newHarness(t, ServerOptions{}, "forward")
	first, _, firstAgent := h.open(Options{BufferSize: 64 * 1024, Budget: budget})
	second, _, secondAgent := h.open(Options{BufferSize: 64 * 1024, Budget: budget})

	_ = first.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	if n, err := first.Write(make([]byte, 48*1024)); n != 32*1024 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("first write = %d, %v; want the 32 KiB budget then a deadline error", n, err)
	}
	_ = second.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	if n, err := second.Write([]byte("x")); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("second write with the budget spent = %d, %v; want a deadline error", n, err)
	}
	_ = second.SetWriteDeadline(time.Time{})
	written := make(chan error, 1)
	go func() {
		_, err := second.Write([]byte("after release"))
		written <- err
	}()
	if _, err := io.ReadFull(firstAgent, make([]byte, 32*1024)); err != nil {
		t.Fatal(err)
	}
	if err := <-written; err != nil {
		t.Fatalf("second write after the first stream's bytes were acknowledged: %v", err)
	}
	got := make([]byte, len("after release"))
	if _, err := io.ReadFull(secondAgent, got); err != nil || string(got) != "after release" {
		t.Fatalf("second stream read %q, %v", got, err)
	}
}

// A stream opened for one purpose on a carrier authorized for another is refused with a RESET the client sees.
func TestOpenPurposeMismatchRejected(t *testing.T) {
	h := newHarness(t, ServerOptions{}, "file")
	link := h.carrierFor(PurposeTerminal)
	client, err := Open(context.Background(), link.client(), "file", Options{})
	if err != nil {
		t.Fatal(err)
	}
	result := h.next()
	if reset, ok := errors.AsType[*ResetError](result.err); !ok || reset.Code != ResetPurposeMismatch || result.conn != nil {
		t.Fatalf("accept = %+v; want a purpose mismatch", result)
	}
	_, err = client.Read(make([]byte, 1))
	if reset, ok := errors.AsType[*ResetError](err); !ok || reset.Code != ResetPurposeMismatch {
		t.Fatalf("client read = %v; want the purpose mismatch reset", err)
	}
	if h.server.Len() != 0 {
		t.Fatal("refused stream was registered")
	}

	// "" and "terminal" are the same purpose on both ends.
	link = h.carrierFor("")
	terminal, err := Open(context.Background(), link.client(), PurposeTerminal, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result := h.next(); result.err != nil {
		t.Fatalf("terminal stream on an empty-purpose carrier: %v", result.err)
	}
	_ = terminal.Close()
}

// resumeWith sends a hand-built RESUME on a new carrier of purpose and returns the agent's verdict.
func resumeWith(t *testing.T, h *harness, purpose string, frame resumeFrame) error {
	t.Helper()
	link := h.carrierFor(purpose)
	_, _, err := handshakeResume(context.Background(), link.client(), frame)
	if result := h.next(); result.err == nil {
		t.Fatalf("forged RESUME accepted: %+v", result)
	}
	return err
}

// A RESUME with a wrong MAC, a replayed generation or another purpose is refused and leaves the stream working.
func TestResumeRejections(t *testing.T) {
	h := newHarness(t, ServerOptions{}, "file")
	client, _, agent := h.open(Options{})

	frame := resumeFrame{sid: client.sid, gen: 1}
	var wrongKey [keySize]byte
	frame.mac = resumeMAC(wrongKey, client.sid, frame.gen, frame.recv)
	if reset, ok := errors.AsType[*ResetError](resumeWith(t, h, "file", frame)); !ok || reset.Code != ResetBadMAC {
		t.Fatalf("bad MAC answer = %v", reset)
	}

	frame.mac = resumeMAC(client.key, client.sid, frame.gen, frame.recv)
	if reset, ok := errors.AsType[*ResetError](resumeWith(t, h, "forward", frame)); !ok || reset.Code != ResetPurposeMismatch {
		t.Fatalf("cross-purpose resume answer = %v", reset)
	}

	if err := client.Migrate(context.Background(), h.carrier().client()); err != nil {
		t.Fatal(err)
	}
	if result := h.next(); result.err != nil || !result.resumed {
		t.Fatalf("genuine resume = %+v", result)
	}
	// Replaying generation 1, which the genuine migration used, is refused.
	if reset, ok := errors.AsType[*ResetError](resumeWith(t, h, "file", frame)); !ok || reset.Code != ResetStaleResume {
		t.Fatalf("replayed resume answer = %v", reset)
	}

	if _, err := client.Write([]byte("still here")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("still here"))
	if _, err := io.ReadFull(agent, got); err != nil || string(got) != "still here" {
		t.Fatalf("stream after rejected resumes read %q, %v", got, err)
	}
}

// The agent keeps a stream that lost its carrier for the retention time and drops it after.
func TestAgentRetention(t *testing.T) {
	clock := &fakeClock{}
	h := newHarness(t, ServerOptions{Clock: clock}, "file")

	t.Run("resume within the retention", func(t *testing.T) {
		client, link, agent := h.open(Options{Clock: clock, ResumeTimeout: time.Hour})
		client.ResumeWith(func(context.Context) (net.Conn, error) { return nil, errors.New("not yet") })
		if _, err := client.Write([]byte("before")); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, 6)
		if _, err := io.ReadFull(agent, got); err != nil {
			t.Fatal(err)
		}
		link.Cut()
		waitFor(t, "both sides to notice the cut", func() bool { return clock.pending() >= 2 })
		clock.Advance(DefaultRetain - time.Second)
		if h.server.Len() != 1 {
			t.Fatal("agent dropped the stream before the retention ended")
		}
		if err := client.Migrate(context.Background(), h.carrier().client()); err != nil {
			t.Fatalf("resume within the retention: %v", err)
		}
		if result := h.next(); !result.resumed {
			t.Fatalf("accept = %+v", result)
		}
		clock.Advance(2 * time.Second)
		if _, err := client.Write([]byte("after")); err != nil {
			t.Fatal(err)
		}
		got = make([]byte, 5)
		if _, err := io.ReadFull(agent, got); err != nil || string(got) != "after" {
			t.Fatalf("read after resume %q, %v", got, err)
		}
		_ = client.Close()
		_ = agent.Close()
		waitFor(t, "stream to finish", func() bool { return h.server.Len() == 0 })
	})

	t.Run("dropped after the retention", func(t *testing.T) {
		client, link, agent := h.open(Options{Clock: clock, ResumeTimeout: time.Hour})
		client.ResumeWith(func(context.Context) (net.Conn, error) { return nil, errors.New("unreachable") })
		link.Cut()
		waitFor(t, "agent to notice the cut", func() bool {
			agent := agent.(*Stream)
			agent.mu.Lock()
			defer agent.mu.Unlock()
			return agent.lostPending
		})
		clock.Advance(DefaultRetain - time.Millisecond)
		if h.server.Len() != 1 {
			t.Fatal("agent dropped the stream early")
		}
		clock.Advance(time.Millisecond)
		if h.server.Len() != 0 {
			t.Fatal("agent kept the stream past the retention")
		}
		if _, err := agent.Read(make([]byte, 1)); !errors.Is(err, ErrCarrierLost) {
			t.Fatalf("agent read after the retention = %v; want ErrCarrierLost", err)
		}
		err := client.Migrate(context.Background(), h.carrier().client())
		if !errors.Is(err, ErrUnknownStream) {
			t.Fatalf("late resume = %v; want ErrUnknownStream", err)
		}
		h.next()
		select {
		case <-client.Done():
		default:
			t.Fatal("client stream survived an agent that dropped it")
		}
	})
}

// A client stream whose carrier is lost without a replacement in time ends with ErrCarrierLost.
func TestClientResumeTimeout(t *testing.T) {
	clock := &fakeClock{}
	h := newHarness(t, ServerOptions{}, "file")
	client, link, _ := h.open(Options{Clock: clock, ResumeTimeout: 5 * time.Second})
	client.OnCarrierLost(func() {})
	link.Cut()
	waitFor(t, "client to notice the cut", func() bool { return clock.pending() > 0 })
	clock.Advance(5 * time.Second)
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, ErrCarrierLost) {
		t.Fatalf("read = %v; want ErrCarrierLost", err)
	}

	// Without a hook nobody can resume, so a loss ends the stream at once.
	bare, link, _ := h.open(Options{})
	link.Cut()
	if _, err := bare.Read(make([]byte, 1)); !errors.Is(err, ErrCarrierLost) {
		t.Fatalf("read without a hook = %v; want ErrCarrierLost", err)
	}
}

// A permanent redial failure ends the stream at once instead of retrying.
func TestResumeWithPermanentFailure(t *testing.T) {
	h := newHarness(t, ServerOptions{}, "file")
	client, link, _ := h.open(Options{})
	refused := errors.New("forbidden")
	client.ResumeWith(func(context.Context) (net.Conn, error) { return nil, Permanent(refused) })
	link.Cut()
	select {
	case <-client.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("stream kept retrying a permanent failure")
	}
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, ErrCarrierLost) || !errors.Is(err, refused) {
		t.Fatalf("read = %v; want ErrCarrierLost wrapping the refusal", err)
	}
}

// A handler that writes its reply and closes at once still delivers the reply when the carrier dies before
// the bytes left: Close keeps the bytes and the FIN for the next carrier.
func TestCloseFlushesAcrossCut(t *testing.T) {
	h := newHarness(t, ServerOptions{}, "file")
	client, link, agent := h.open(Options{})
	client.ResumeWith(func(context.Context) (net.Conn, error) { return h.carrier().client(), nil })
	link.agent().configure(func(c *fakeConn) { c.blackhole = true })
	if _, err := agent.Write([]byte("reply")); err != nil {
		t.Fatal(err)
	}
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the reply to be written into the void", func() bool {
		s := agent.(*Stream)
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.finSent
	})
	link.Cut()
	got, err := io.ReadAll(client)
	if err != nil || string(got) != "reply" {
		t.Fatalf("client read %q, %v; want the reply then EOF", got, err)
	}
	if result := h.next(); !result.resumed {
		t.Fatalf("accept = %+v", result)
	}
	_ = client.Close()
	for _, stream := range []*Stream{client, agent.(*Stream)} {
		select {
		case <-stream.Done():
		case <-time.After(10 * time.Second):
			t.Fatal("stream did not finish")
		}
	}
}

// A closed stream whose peer never finishes is reset after the linger time.
func TestCloseLinger(t *testing.T) {
	clock := &fakeClock{}
	h := newHarness(t, ServerOptions{Clock: clock}, "file")
	client, _, agent := h.open(Options{})
	_ = agent.Close()
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("client read after the agent closed = %v", err)
	}
	clock.Advance(DefaultLinger)
	select {
	case <-client.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("client stream was not reset after the agent's linger")
	}
	if _, err := client.Write([]byte("x")); err == nil {
		t.Fatal("write on a reset stream succeeded")
	}
}

// Duplicate DATA on one carrier is dropped and a gap is a protocol error.
func TestReceiveRules(t *testing.T) {
	h := newHarness(t, ServerOptions{}, "file")
	client, link, agent := h.open(Options{})
	link.client().configure(func(c *fakeConn) { c.duplicate = true })
	if _, err := client.Write([]byte("once")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(agent, got); err != nil || string(got) != "once" {
		t.Fatalf("read %q, %v", got, err)
	}
	_ = agent.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if n, err := agent.Read(got); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("duplicate delivered: %d bytes, %v", n, err)
	}
	_ = agent.SetReadDeadline(time.Time{})

	// A DATA frame past the receive position is a protocol error that resets the stream.
	if _, err := link.client().Write(append(appendDataHeader(nil, 1000, 1), 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Read(got); !errors.Is(err, errProtocol) {
		t.Fatalf("agent read after a gap = %v; want a protocol error", err)
	}
	_, err := client.Read(got)
	if reset, ok := errors.AsType[*ResetError](err); !ok || reset.Code != ResetProtocol {
		t.Fatalf("client read = %v; want a protocol reset", err)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	open := openFrame{window: 65536, purpose: "file-mux"}
	open.sid[0], open.key[31] = 1, 2
	encoded := appendOpen(nil, open)
	decoded, err := parseOpen(encoded[headerSize:])
	if err != nil || decoded != open {
		t.Fatalf("open = %+v, %v", decoded, err)
	}
	resume := resumeFrame{sid: open.sid, gen: 3, recv: 1 << 40}
	resume.mac = resumeMAC(open.key, resume.sid, resume.gen, resume.recv)
	parsed, err := parseResume(appendResume(nil, resume)[headerSize:])
	if err != nil || parsed != resume {
		t.Fatalf("resume = %+v, %v", parsed, err)
	}
	if _, err := parseOpen(append([]byte{protocolVersion + 1}, encoded[headerSize+1:]...)); err == nil {
		t.Fatal("open of another protocol version parsed")
	}
}
