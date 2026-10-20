package logtail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type fakeExecChannel struct {
	events    chan base.OutputEvent
	done      chan struct{}
	closeOnce sync.Once
	onClose   func()
}

func newFakeExecChannel(onClose func()) *fakeExecChannel {
	return &fakeExecChannel{events: make(chan base.OutputEvent, 64), done: make(chan struct{}), onClose: onClose}
}

func (c *fakeExecChannel) NextOutput(ctx context.Context) (base.OutputEvent, error) {
	select {
	case event := <-c.events:
		return event, nil
	default:
	}
	select {
	case event := <-c.events:
		return event, nil
	case <-c.done:
		return base.OutputEvent{}, io.EOF
	case <-ctx.Done():
		return base.OutputEvent{}, ctx.Err()
	}
}

func (c *fakeExecChannel) send(text string, stderr bool) {
	c.events <- base.OutputEvent{Data: []byte(text), Stderr: stderr}
}

func (c *fakeExecChannel) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.onClose != nil {
			c.onClose()
		}
	})
	return nil
}

func (c *fakeExecChannel) Wait(ctx context.Context) error {
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *fakeExecChannel) Read([]byte) (int, error)                     { return 0, io.EOF }
func (c *fakeExecChannel) Write(data []byte) (int, error)               { return len(data), nil }
func (c *fakeExecChannel) Stderr() io.Reader                            { return nil }
func (c *fakeExecChannel) Resize(context.Context, uint32, uint32) error { return nil }
func (c *fakeExecChannel) CloseWrite() error                            { return nil }
func (c *fakeExecChannel) ID() string                                   { return "fake-exec" }
func (c *fakeExecChannel) Generation() uint64                           { return 7 }

type fakeStreamTransport struct {
	opened       chan *fakeExecChannel
	commands     chan string
	active       atomic.Int32
	maxActive    atomic.Int32
	ptyCalls     atomic.Int32
	bufferedExec atomic.Int32
}

func newFakeStreamTransport() *fakeStreamTransport {
	return &fakeStreamTransport{opened: make(chan *fakeExecChannel, 16), commands: make(chan string, 16)}
}

func (t *fakeStreamTransport) Kind() string       { return "ssh" }
func (t *fakeStreamTransport) Generation() uint64 { return 7 }
func (t *fakeStreamTransport) IsAlive() bool      { return true }
func (t *fakeStreamTransport) Close() error       { return nil }
func (t *fakeStreamTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}
func (t *fakeStreamTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	t.bufferedExec.Add(1)
	return base.ExecResult{}, errors.New("buffered exec is not allowed for log tail")
}
func (t *fakeStreamTransport) OpenPTY(context.Context, base.PTYOptions) (base.Channel, error) {
	t.ptyCalls.Add(1)
	return nil, errors.New("PTY is not allowed for log tail")
}
func (t *fakeStreamTransport) OpenExec(ctx context.Context, command string, options base.ExecOptions) (base.Channel, error) {
	if options.Timeout != 0 || options.ExpectedGeneration != 7 {
		return nil, fmt.Errorf("unexpected exec options: %+v", options)
	}
	active := t.active.Add(1)
	for {
		maximum := t.maxActive.Load()
		if active <= maximum || t.maxActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	channel := newFakeExecChannel(func() { t.active.Add(-1) })
	context.AfterFunc(ctx, func() { _ = channel.Close() })
	t.commands <- command
	t.opened <- channel
	return channel, nil
}

type recordingSink struct {
	mu       sync.Mutex
	events   []Event
	notify   chan struct{}
	closed   atomic.Bool
	graceful atomic.Bool
}

func newRecordingSink() *recordingSink {
	return &recordingSink{notify: make(chan struct{}, 1)}
}

func (s *recordingSink) Send(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
	return nil
}

func (s *recordingSink) Close() error {
	s.closed.Store(true)
	return nil
}

func (s *recordingSink) CloseGracefully() error {
	s.graceful.Store(true)
	return s.Close()
}

func (s *recordingSink) snapshot() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...)
}

func (s *recordingSink) waitFor(t *testing.T, predicate func([]Event) bool) []Event {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		events := s.snapshot()
		if predicate(events) {
			return events
		}
		select {
		case <-s.notify:
		case <-deadline:
			t.Fatalf("timed out waiting for events: %+v", events)
		}
	}
}

func hasDone(events []Event) bool {
	return len(events) > 0 && events[len(events)-1].Kind == EventDone
}

func sourceFor(id string) Source {
	return Source{SourceID: id, SessionID: id, Kind: SourceFile, Value: "/var/log/" + id + ".log"}
}

func TestServiceStreamsAttributedLinesWithoutPTY(t *testing.T) {
	first := newFakeStreamTransport()
	second := newFakeStreamTransport()
	service := New(Config{Resolver: ResolverFunc(func(_ context.Context, id string) (Target, error) {
		transport := first
		if id == "second" {
			transport = second
		}
		return Target{SessionID: id, SessionName: "host-" + id, Transport: transport, Generation: 7}, nil
	})})
	sink := newRecordingSink()
	_, err := service.Start(t.Context(), "owner", Request{RunID: "merge", Sources: []Source{sourceFor("first"), sourceFor("second")}, MaxConcurrent: 2}, sink)
	if err != nil {
		t.Fatal(err)
	}
	firstChannel := <-first.opened
	secondChannel := <-second.opened
	firstChannel.send("one\npart", false)
	secondChannel.send("two\n", false)
	firstChannel.send("ial\n", false)
	firstChannel.send("problem\n", true)
	_ = firstChannel.Close()
	_ = secondChannel.Close()
	events := sink.waitFor(t, hasDone)

	var sequence uint64
	perSource := map[string][]string{}
	for _, event := range events {
		if event.Sequence <= sequence {
			t.Fatalf("event sequence regressed: %+v", events)
		}
		sequence = event.Sequence
		if event.Kind != EventLine {
			continue
		}
		perSource[event.SourceID] = append(perSource[event.SourceID], event.Text)
		if event.SessionName != "host-"+event.SourceID {
			t.Fatalf("line attribution = %+v", event)
		}
	}
	if got := fmt.Sprint(perSource["first"]); got != "[one partial problem]" {
		t.Fatalf("first source lines = %s", got)
	}
	if got := fmt.Sprint(perSource["second"]); got != "[two]" {
		t.Fatalf("second source lines = %s", got)
	}
	var stderrSeen bool
	for _, event := range events {
		stderrSeen = stderrSeen || event.Kind == EventLine && event.Text == "problem" && event.Stderr
	}
	if !stderrSeen {
		t.Fatalf("stderr line was not preserved: %+v", events)
	}
	if first.ptyCalls.Load()+second.ptyCalls.Load() != 0 || first.bufferedExec.Load()+second.bufferedExec.Load() != 0 {
		t.Fatal("log tail consumed PTY or buffered exec")
	}
	if !sink.closed.Load() {
		t.Fatal("event sink was not closed at run completion")
	}
}

func TestServiceBoundsConcurrencyAndCancelWaitsForChannels(t *testing.T) {
	transport := newFakeStreamTransport()
	service := New(Config{Resolver: ResolverFunc(func(_ context.Context, id string) (Target, error) {
		return Target{SessionID: id, SessionName: id, Transport: transport, Generation: 7}, nil
	})})
	sink := newRecordingSink()
	_, err := service.Start(t.Context(), "owner", Request{
		RunID: "bounded", Sources: []Source{sourceFor("a"), sourceFor("b"), sourceFor("c")}, MaxConcurrent: 2,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	channels := []*fakeExecChannel{<-transport.opened, <-transport.opened}
	select {
	case extra := <-transport.opened:
		t.Fatalf("third exec exceeded concurrency: %+v", extra)
	default:
	}
	_ = channels[0].Close()
	channels = append(channels, <-transport.opened)
	if _, err := service.Cancel(t.Context(), "other-owner", "bounded"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-owner cancel = %v, want ErrForbidden", err)
	}
	if _, err := service.Cancel(t.Context(), "owner", "bounded"); err != nil {
		t.Fatal(err)
	}
	for index, channel := range channels {
		select {
		case <-channel.done:
		default:
			t.Fatalf("channel %d still open after Cancel returned", index)
		}
	}
	sink.waitFor(t, hasDone)
	if got := transport.active.Load(); got != 0 || transport.maxActive.Load() > 2 {
		t.Fatalf("active = %d, max active = %d", got, transport.maxActive.Load())
	}
}

func TestServiceReportsSourceStartFailureAndCompletesOthers(t *testing.T) {
	transport := newFakeStreamTransport()
	service := New(Config{Resolver: ResolverFunc(func(_ context.Context, id string) (Target, error) {
		if id == "bad" {
			return Target{}, errors.New("permission denied")
		}
		return Target{SessionID: id, SessionName: "good-host", Transport: transport, Generation: 7}, nil
	})})
	sink := newRecordingSink()
	if _, err := service.Start(t.Context(), "owner", Request{RunID: "failure", Sources: []Source{sourceFor("bad"), sourceFor("good")}}, sink); err != nil {
		t.Fatal(err)
	}
	channel := <-transport.opened
	channel.send("still visible\n", false)
	_ = channel.Close()
	events := sink.waitFor(t, hasDone)
	var failed, goodLine bool
	for _, event := range events {
		if event.Kind == EventStatus && event.SourceID == "bad" && event.Status == StatusFailed && event.Error == "permission denied" {
			failed = true
		}
		goodLine = goodLine || event.Kind == EventLine && event.SourceID == "good" && event.Text == "still visible"
	}
	if !failed || !goodLine {
		t.Fatalf("failure visibility = %v, good line = %v; events: %+v", failed, goodLine, events)
	}
}

func TestServiceCancelBeforeStartPreventsRemoteExec(t *testing.T) {
	transport := newFakeStreamTransport()
	now := time.Unix(100, 0)
	service := New(Config{
		Now: func() time.Time { return now },
		Resolver: ResolverFunc(func(_ context.Context, id string) (Target, error) {
			return Target{SessionID: id, Transport: transport, Generation: 7}, nil
		}),
	})
	if _, err := service.Cancel(t.Context(), "owner", "early"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	sink := newRecordingSink()
	if _, err := service.Start(t.Context(), "owner", Request{RunID: "early", Sources: []Source{sourceFor("a")}}, sink); err != nil {
		t.Fatal(err)
	}
	events := sink.waitFor(t, hasDone)
	select {
	case command := <-transport.commands:
		t.Fatalf("early-canceled command started after 24h: %q", command)
	default:
	}
	var canceled bool
	for _, event := range events {
		canceled = canceled || event.Kind == EventStatus && event.Status == StatusCanceled
	}
	if !canceled {
		t.Fatalf("early cancellation was not visible: %+v", events)
	}
	sink.waitFor(t, func([]Event) bool { return sink.graceful.Load() })
	if !sink.graceful.Load() {
		t.Fatal("service did not use graceful sink close")
	}
}

func TestServiceCompensatingCancelDoesNotRecreatePending(t *testing.T) {
	transport := newFakeStreamTransport()
	service := New(Config{Resolver: ResolverFunc(func(_ context.Context, id string) (Target, error) {
		return Target{SessionID: id, Transport: transport, Generation: 7}, nil
	})})
	if _, err := service.Cancel(t.Context(), "owner", "early"); err != nil {
		t.Fatal(err)
	}
	sink := newRecordingSink()
	if _, err := service.Start(t.Context(), "owner", Request{RunID: "early", Sources: []Source{sourceFor("a")}}, sink); err != nil {
		t.Fatal(err)
	}
	sink.waitFor(t, hasDone)

	for range 2 * maxPendingCancelsPerOwner {
		if _, err := service.Cancel(t.Context(), "owner", "early"); err != nil {
			t.Fatalf("compensating cancel = %v", err)
		}
	}
	service.mu.Lock()
	pending := len(service.pending)
	service.mu.Unlock()
	if pending != 0 {
		t.Fatalf("compensating cancels recreated %d pending entries", pending)
	}

	if _, err := service.Cancel(t.Context(), "owner", "next-early"); err != nil {
		t.Fatalf("next early cancel was rejected after compensating cancels: %v", err)
	}
	nextSink := newRecordingSink()
	if _, err := service.Start(t.Context(), "owner", Request{RunID: "next-early", Sources: []Source{sourceFor("b")}}, nextSink); err != nil {
		t.Fatal(err)
	}
	nextSink.waitFor(t, hasDone)
	select {
	case command := <-transport.commands:
		t.Fatalf("next early-canceled command reached OpenExec: %q", command)
	default:
	}
}
