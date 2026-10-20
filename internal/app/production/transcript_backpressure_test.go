package production

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type delayedTranscriptStore struct {
	mu             sync.Mutex
	started        []store.TranscriptRow
	chunks         []store.TranscriptChunkRow
	ended          []bool
	endedByID      map[string]bool
	durableOffsets map[string]int64
	events         []string
	startFailures  int

	appendStarted      chan struct{}
	appendOnce         sync.Once
	appendFailed       chan struct{}
	appendFailureOnce  sync.Once
	failDurableAppends int
	panicAppend        bool
	panicOnce          sync.Once
	releaseAppend      chan struct{}
	releaseOnce        sync.Once
	endCalled          chan struct{}
	endOnce            sync.Once
}

func newDelayedTranscriptStore() *delayedTranscriptStore {
	return &delayedTranscriptStore{
		durableOffsets: make(map[string]int64), endedByID: make(map[string]bool),
		appendStarted: make(chan struct{}), appendFailed: make(chan struct{}),
		releaseAppend: make(chan struct{}), endCalled: make(chan struct{}),
	}
}

func (s *delayedTranscriptStore) TranscriptStart(_ context.Context, row store.TranscriptRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.startFailures != 0 {
		if s.startFailures > 0 {
			s.startFailures--
		}
		s.events = append(s.events, "start_error")
		return errors.New("injected transcript start failure")
	}
	s.started = append(s.started, row)
	s.events = append(s.events, "start")
	return nil
}

func (s *delayedTranscriptStore) TranscriptAppendChunks(_ context.Context, transcriptID string, chunks []store.TranscriptChunkRow, durableBytes ...map[string]int64) error {
	if s.panicAppend {
		s.panicOnce.Do(func() {
			s.appendOnce.Do(func() { close(s.appendStarted) })
			panic("injected transcript store panic")
		})
	}
	s.appendOnce.Do(func() { close(s.appendStarted) })
	<-s.releaseAppend
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failDurableAppends > 0 && len(durableBytes) > 0 {
		hasDelta := false
		for _, count := range durableBytes[0] {
			hasDelta = hasDelta || count > 0
		}
		if hasDelta {
			s.failDurableAppends--
			s.events = append(s.events, "append_error")
			s.appendFailureOnce.Do(func() { close(s.appendFailed) })
			return errors.New("injected durable append failure")
		}
	}
	for _, chunk := range chunks {
		chunk.TranscriptID = transcriptID
		chunk.Data = bytes.Clone(chunk.Data)
		s.chunks = append(s.chunks, chunk)
	}
	if len(durableBytes) > 0 {
		for id, count := range durableBytes[0] {
			s.durableOffsets[id] += count
		}
	}
	s.events = append(s.events, "append")
	return nil
}

func (s *delayedTranscriptStore) TranscriptEnd(_ context.Context, transcriptID string, _ int64, truncated bool) error {
	s.mu.Lock()
	s.ended = append(s.ended, truncated)
	s.endedByID[transcriptID] = truncated
	s.events = append(s.events, "end")
	s.mu.Unlock()
	s.endOnce.Do(func() { close(s.endCalled) })
	return nil
}

func (s *delayedTranscriptStore) unblock() {
	s.releaseOnce.Do(func() { close(s.releaseAppend) })
}

func (s *delayedTranscriptStore) snapshot() ([]store.TranscriptChunkRow, []bool, map[string]int64, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	chunks := append([]store.TranscriptChunkRow(nil), s.chunks...)
	ended := append([]bool(nil), s.ended...)
	offsets := make(map[string]int64, len(s.durableOffsets))
	for id, value := range s.durableOffsets {
		offsets[id] = value
	}
	events := append([]string(nil), s.events...)
	return chunks, ended, offsets, events
}

func (s *delayedTranscriptStore) startedRows() []store.TranscriptRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.TranscriptRow(nil), s.started...)
}

func (s *delayedTranscriptStore) chunksForTranscript(transcriptID string) []store.TranscriptChunkRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	var chunks []store.TranscriptChunkRow
	for _, chunk := range s.chunks {
		if chunk.TranscriptID == transcriptID {
			chunks = append(chunks, chunk)
		}
	}
	return chunks
}

func (s *delayedTranscriptStore) endedTruncated(transcriptID string) (bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	truncated, ended := s.endedByID[transcriptID]
	return truncated, ended
}

func cleanupDelayedTranscriptWriter(t *testing.T, writer *transcriptWriter, database *delayedTranscriptStore) {
	t.Helper()
	t.Cleanup(func() {
		database.unblock()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = writer.Shutdown(ctx)
	})
}

func TestTranscriptWriterSlowStoreInteractiveOutputP99(t *testing.T) {
	ctx := context.Background()
	database := newDelayedTranscriptStore()
	writer := newTranscriptWriter(transcriptWriterConfig{
		Database: database, Logger: discardTranscriptLogger(), QueueMaxBytes: 1024,
	})
	cleanupDelayedTranscriptWriter(t, writer, database)
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	writer.SessionStarted(ctx, transcriptIdentity("session-p99"))
	payload := bytes.Repeat([]byte("p"), 16)
	writer.SessionOutput(ctx, "tab-1", "session-p99", "tab-1", payload)
	select {
	case <-database.appendStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not enter the delayed store")
	}

	const calls = 256
	durations := make([]time.Duration, 0, calls)
	for index := 0; index < calls; index++ {
		started := time.Now()
		writer.SessionOutput(ctx, "tab-1", "session-p99", "tab-1", payload)
		durations = append(durations, time.Since(started))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p99 := durations[(len(durations)*99)/100]
	if p99 >= 100*time.Millisecond {
		t.Fatalf("interactive output enqueue P99 = %s while the store is blocked", p99)
	}
	t.Logf("interactive output enqueue P99 with a blocked store: %s", p99)
	writer.SessionEnded(ctx, "session-p99")
	if stats := writer.statsSnapshot(); stats.droppedChunks != 192 {
		t.Fatalf("drops before drain = %+v, want 192", stats)
	}
	database.unblock()
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}

	stats := writer.statsSnapshot()
	if stats.droppedChunks != 192 || stats.droppedBytes != uint64(192*len(payload)) || stats.batches != 2 || stats.flushedChunks != 66 || stats.sessionFlushes != 1 || stats.flushErrors != 0 {
		t.Fatalf("writer counters = %+v", stats)
	}
	chunks, ended, offsets, _ := database.snapshot()
	if len(chunks) != 66 || len(ended) != 1 || !ended[0] {
		t.Fatalf("persisted chunks=%d ended=%+v", len(chunks), ended)
	}
	for index, chunk := range chunks {
		if chunk.Seq != int64(index) {
			t.Fatalf("chunk %d has seq %d", index, chunk.Seq)
		}
	}
	if !bytes.Contains(chunks[len(chunks)-1].Data, []byte("omitted 192")) {
		t.Fatalf("last chunk must describe the degraded tail: %+v", chunks[len(chunks)-1])
	}
	if got, want := offsets["tab-1"], int64(65*len(payload)); got != want {
		t.Fatalf("durable offset = %d, want only confirmed output bytes %d", got, want)
	}
}

func TestTranscriptWriterSessionEndFlushesTailWithoutShutdown(t *testing.T) {
	ctx := context.Background()
	database := newDelayedTranscriptStore()
	database.unblock()
	writer := newTranscriptWriter(transcriptWriterConfig{Database: database, Logger: discardTranscriptLogger()})
	cleanupDelayedTranscriptWriter(t, writer, database)
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	writer.SessionStarted(ctx, transcriptIdentity("session-end-flush"))
	writer.SessionOutput(ctx, "", "session-end-flush", "tab-1", []byte("first\r\n"))
	writer.SessionInput(ctx, "session-end-flush", "tab-1", []byte("tail-input\r"))
	writer.SessionEnded(ctx, "session-end-flush")
	select {
	case <-database.endCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("SessionEnded did not flush the critical tail")
	}
	chunks, ended, _, events := database.snapshot()
	if len(chunks) != 2 || string(chunks[1].Data) != "tail-input\r" || len(ended) != 1 {
		t.Fatalf("tail was not persisted before End: chunks=%+v ended=%+v", chunks, ended)
	}
	if len(events) == 0 || events[len(events)-1] != "end" {
		t.Fatalf("End must follow the tail flush: %+v", events)
	}
	if stats := writer.statsSnapshot(); stats.sessionFlushes != 1 || stats.droppedChunks != 0 {
		t.Fatalf("session flush counters = %+v", stats)
	}
}

type transcriptFeedChannel struct {
	generation uint64
	reads      chan []byte
	closed     chan struct{}
	closeOnce  sync.Once
}

func newTranscriptFeedChannel(generation uint64) *transcriptFeedChannel {
	return &transcriptFeedChannel{generation: generation, reads: make(chan []byte, 512), closed: make(chan struct{})}
}

func (c *transcriptFeedChannel) Read(buffer []byte) (int, error) {
	select {
	case data := <-c.reads:
		return copy(buffer, data), nil
	case <-c.closed:
		return 0, io.EOF
	}
}

func (c *transcriptFeedChannel) Write(data []byte) (int, error) { return len(data), nil }
func (c *transcriptFeedChannel) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}
func (c *transcriptFeedChannel) Stderr() io.Reader { return nil }
func (c *transcriptFeedChannel) Resize(context.Context, uint32, uint32) error {
	return nil
}
func (c *transcriptFeedChannel) Wait(ctx context.Context) error {
	select {
	case <-c.closed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *transcriptFeedChannel) CloseWrite() error  { return nil }
func (c *transcriptFeedChannel) ID() string         { return "transcript-feed" }
func (c *transcriptFeedChannel) Generation() uint64 { return c.generation }
func (c *transcriptFeedChannel) send(data []byte) error {
	select {
	case c.reads <- bytes.Clone(data):
		return nil
	case <-c.closed:
		return base.ErrClosed
	}
}

type transcriptFeedTransport struct {
	kind       string
	generation uint64
	channel    *transcriptFeedChannel
	alive      atomic.Bool
}

func newTranscriptFeedTransport(kind string, generation uint64) *transcriptFeedTransport {
	transport := &transcriptFeedTransport{kind: kind, generation: generation}
	transport.channel = newTranscriptFeedChannel(generation)
	transport.alive.Store(true)
	return transport
}

func (t *transcriptFeedTransport) Kind() string       { return t.kind }
func (t *transcriptFeedTransport) Generation() uint64 { return t.generation }
func (t *transcriptFeedTransport) IsAlive() bool      { return t.alive.Load() }
func (t *transcriptFeedTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}
func (t *transcriptFeedTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{}, nil
}
func (t *transcriptFeedTransport) Close() error {
	t.alive.Store(false)
	return t.channel.Close()
}
func (t *transcriptFeedTransport) OpenPTY(context.Context, base.PTYOptions) (base.Channel, error) {
	return t.channel, nil
}

func TestTranscriptWriterSlowStoreTerminalOutputP99(t *testing.T) {
	ctx := context.Background()
	database := newDelayedTranscriptStore()
	writer := newTranscriptWriter(transcriptWriterConfig{
		Database: database, Logger: discardTranscriptLogger(), QueueMaxBytes: 1024,
	})
	cleanupDelayedTranscriptWriter(t, writer, database)
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var transport *transcriptFeedTransport
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(_ context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
			transport = newTranscriptFeedTransport(asset.Kind, generation)
			return transport, nil
		}),
		Transcripts: writer,
		Logger:      discardTranscriptLogger(),
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(ctx, session.Asset{ID: "asset-p99", Name: "web-01", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.OpenTab(ctx, session.OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "transcript-p99", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatal(err)
	}
	receiver, err := manager.Bind("transcript-p99")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receiver.Close() })
	payload := bytes.Repeat([]byte("p"), 16)
	roundTrip := func() {
		t.Helper()
		if err := transport.channel.send(payload); err != nil {
			t.Fatal(err)
		}
		for {
			nextCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			frame, err := receiver.Next(nextCtx)
			cancel()
			if err != nil {
				t.Fatalf("terminal output stopped while the transcript store was blocked: %v", err)
			}
			if err := receiver.Ack(frame.Sequence); err != nil {
				t.Fatal(err)
			}
			if frame.Kind != hub.FrameBinary {
				continue
			}
			if !bytes.Equal(frame.Data, payload) {
				t.Fatalf("terminal output changed during transcript degradation: %q", frame.Data)
			}
			break
		}
	}
	roundTrip()
	select {
	case <-database.appendStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not enter the delayed store")
	}

	const calls = 256
	durations := make([]time.Duration, 0, calls)
	for index := 0; index < calls; index++ {
		started := time.Now()
		roundTrip()
		durations = append(durations, time.Since(started))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p99 := durations[(len(durations)*99)/100]
	if p99 >= 100*time.Millisecond {
		t.Fatalf("terminal output P99 = %s while the transcript store is blocked", p99)
	}
	t.Logf("terminal output P99 with a blocked transcript store: %s", p99)
	if stats := writer.statsSnapshot(); stats.droppedChunks == 0 {
		t.Fatal("test did not exercise transcript queue degradation")
	}
	closed := make(chan error, 1)
	go func() { closed <- manager.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session close blocked on the transcript store")
	}
	database.unblock()
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	_, ended, _, _ := database.snapshot()
	if len(ended) != 1 || !ended[0] {
		t.Fatalf("degraded session must flush and end with an explicit truncation marker: %+v", ended)
	}
}

func TestTranscriptWriterItemQueueBound(t *testing.T) {
	ctx := context.Background()
	database := newDelayedTranscriptStore()
	database.unblock()
	writer := newTranscriptWriter(transcriptWriterConfig{
		Database: database, Logger: discardTranscriptLogger(), QueueMaxBytes: 1 << 30,
	})
	cleanupDelayedTranscriptWriter(t, writer, database)
	writer.SessionStarted(ctx, transcriptIdentity("session-item-bound"))
	for index := 0; index < transcriptQueueMaxItems+8; index++ {
		writer.SessionOutput(ctx, "", "session-item-bound", "tab-1", []byte("x"))
	}
	writer.queueMu.Lock()
	queued := writer.queuedChunks
	writer.queueMu.Unlock()
	if queued != transcriptQueueMaxItems {
		t.Fatalf("queued chunks = %d, want fixed bound %d", queued, transcriptQueueMaxItems)
	}
	if stats := writer.statsSnapshot(); stats.droppedChunks != 8 || stats.droppedBytes != 8 {
		t.Fatalf("item-bound drop counters = %+v", stats)
	}
	writer.SessionEnded(ctx, "session-item-bound")
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	chunks, ended, _, _ := database.snapshot()
	if len(chunks) != transcriptQueueMaxItems+1 || len(ended) != 1 || !ended[0] {
		t.Fatalf("item-bound drain: chunks=%d ended=%+v", len(chunks), ended)
	}
}

func TestTranscriptWriterStorePanicDoesNotBlockInteractiveOutput(t *testing.T) {
	ctx := context.Background()
	database := newDelayedTranscriptStore()
	database.panicAppend = true
	database.unblock()
	writer := newTranscriptWriter(transcriptWriterConfig{Database: database, Logger: discardTranscriptLogger()})
	cleanupDelayedTranscriptWriter(t, writer, database)
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	writer.SessionStarted(ctx, transcriptIdentity("session-panic"))
	writer.SessionOutput(ctx, "", "session-panic", "tab-1", []byte("before-panic"))
	select {
	case <-database.appendStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not enter the panic-injected store")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		writer.queueMu.Lock()
		closed := writer.closed
		writer.queueMu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("writer did not close after recovering the store panic")
		}
		time.Sleep(time.Millisecond)
	}
	outputDone := make(chan struct{})
	go func() {
		writer.SessionOutput(ctx, "", "session-panic", "tab-1", []byte("after-panic"))
		close(outputDone)
	}()
	select {
	case <-outputDone:
	case <-time.After(2 * time.Second):
		t.Fatal("interactive output blocked after a transcript store panic")
	}
	if stats := writer.statsSnapshot(); stats.droppedChunks != 1 || stats.droppedBytes != uint64(len("after-panic")) {
		t.Fatalf("post-panic drop counters = %+v", stats)
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptWriterControlQueueBound(t *testing.T) {
	ctx := context.Background()
	database := newDelayedTranscriptStore()
	writer := newTranscriptWriter(transcriptWriterConfig{Database: database, Logger: discardTranscriptLogger()})
	cleanupDelayedTranscriptWriter(t, writer, database)
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	writer.SessionStarted(ctx, transcriptIdentity("session-control-bound"))
	writer.SessionOutput(ctx, "", "session-control-bound", "tab-1", []byte("block-store"))
	select {
	case <-database.appendStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not enter the delayed store")
	}
	for index := 0; index < transcriptControlQueueMaxItems; index++ {
		writer.SessionEnded(ctx, "unknown-control-session")
	}
	enqueued := make(chan struct{})
	go func() {
		writer.SessionEnded(ctx, "unknown-control-session")
		close(enqueued)
	}()
	select {
	case <-enqueued:
		t.Fatal("lifecycle enqueue exceeded its bound without backpressure")
	case <-time.After(100 * time.Millisecond):
	}
	writer.queueMu.Lock()
	controls := writer.queuedControls
	writer.queueMu.Unlock()
	if controls != transcriptControlQueueMaxItems {
		t.Fatalf("queued controls = %d, want %d", controls, transcriptControlQueueMaxItems)
	}
	database.unblock()
	select {
	case <-enqueued:
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycle enqueue did not resume after a batch drained")
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptWriterDurableOffsetStopsAtFailedAppendUntilReplay(t *testing.T) {
	ctx := context.Background()
	database := newDelayedTranscriptStore()
	database.failDurableAppends = 1
	database.unblock()
	writer := newTranscriptWriter(transcriptWriterConfig{Database: database, Logger: discardTranscriptLogger()})
	cleanupDelayedTranscriptWriter(t, writer, database)
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	a := bytes.Repeat([]byte("a"), 100)
	b := []byte("b")
	writer.SessionStarted(ctx, transcriptIdentity("session-offset-failure"))
	writer.SessionOutput(ctx, "tab-1", "session-offset-failure", "tab-1", a)
	select {
	case <-database.appendFailed:
	case <-time.After(5 * time.Second):
		t.Fatal("durable append failure was not injected")
	}
	writer.SessionOutput(ctx, "tab-1", "session-offset-failure", "tab-1", b)
	writer.SessionEnded(ctx, "session-offset-failure")
	select {
	case <-database.endCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("first period did not end")
	}
	started := database.startedRows()
	if len(started) != 1 {
		t.Fatalf("first period starts = %+v", started)
	}
	oldChunks := database.chunksForTranscript(started[0].ID)
	if len(oldChunks) != 1 || !bytes.Equal(oldChunks[0].Data, b) {
		t.Fatalf("only the later successful block should remain in the degraded old period: %+v", oldChunks)
	}
	_, _, offsets, _ := database.snapshot()
	if offsets["tab-1"] != 0 {
		t.Fatalf("offset crossed a failed block: %d", offsets["tab-1"])
	}
	if truncated, ended := database.endedTruncated(started[0].ID); !ended || !truncated {
		t.Fatalf("failed period must end degraded: ended=%v truncated=%v", ended, truncated)
	}

	writer.SessionStarted(ctx, transcriptIdentity("session-offset-failure"))
	writer.SessionOutput(ctx, "tab-1", "session-offset-failure", "tab-1", a)
	writer.SessionOutput(ctx, "tab-1", "session-offset-failure", "tab-1", b)
	writer.SessionEnded(ctx, "session-offset-failure")
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	started = database.startedRows()
	if len(started) != 2 {
		t.Fatalf("replay period was not created: %+v", started)
	}
	replayChunks := database.chunksForTranscript(started[1].ID)
	if len(replayChunks) != 2 || !bytes.Equal(replayChunks[0].Data, a) || !bytes.Equal(replayChunks[1].Data, b) {
		t.Fatalf("recovery must replay the complete unrecorded suffix once: %+v", replayChunks)
	}
	_, _, offsets, _ = database.snapshot()
	if got, want := offsets["tab-1"], int64(len(a)+len(b)); got != want {
		t.Fatalf("offset after continuous replay = %d, want %d", got, want)
	}
	if truncated, ended := database.endedTruncated(started[1].ID); !ended || truncated {
		t.Fatalf("continuous replay period must be complete: ended=%v truncated=%v", ended, truncated)
	}
}

func TestTranscriptWriterDurableOffsetStopsAtDroppedChunk(t *testing.T) {
	ctx := context.Background()
	database := newDelayedTranscriptStore()
	database.unblock()
	writer := newTranscriptWriter(transcriptWriterConfig{
		Database: database, Logger: discardTranscriptLogger(), QueueMaxBytes: 1,
	})
	writer.SessionStarted(ctx, transcriptIdentity("session-offset-drop"))
	writer.SessionOutput(ctx, "tab-1", "session-offset-drop", "tab-1", []byte("a"))
	writer.SessionOutput(ctx, "tab-1", "session-offset-drop", "tab-1", []byte("dropped-1"))
	first, open := writer.takeBatch()
	if !open {
		t.Fatal("first dropped segment did not dequeue")
	}
	writer.SessionOutput(ctx, "tab-1", "session-offset-drop", "tab-1", []byte("b"))
	writer.SessionOutput(ctx, "tab-1", "session-offset-drop", "tab-1", []byte("dropped-2"))
	writer.SessionEnded(ctx, "session-offset-drop")
	second, open := writer.takeBatch()
	if !open {
		t.Fatal("second dropped segment did not dequeue")
	}
	batch := append(first, second...)
	sessions := make(map[string]*transcriptPeriod)
	deferred := make(map[string]*deferredTranscript)
	end := len(batch)
	for index, item := range batch {
		if item.kind == transcriptItemEnd {
			end = index
			break
		}
	}
	writer.flushBatch(sessions, deferred, batch[:end])
	period := sessions["session-offset-drop"]
	if period == nil || period.droppedChunks != 2 || !period.durableFrozen["tab-1"] {
		t.Fatalf("drop continuity state = %+v", period)
	}
	writer.flushBatch(sessions, deferred, batch[end:])
	started := database.startedRows()
	if len(started) != 1 {
		t.Fatalf("drop period starts = %+v", started)
	}
	chunks := database.chunksForTranscript(started[0].ID)
	if len(chunks) != 4 || string(chunks[0].Data) != "a" || string(chunks[2].Data) != "b" ||
		!bytes.Contains(chunks[1].Data, []byte("omitted 1 chunk(s)")) || !bytes.Contains(chunks[3].Data, []byte("omitted 1 chunk(s)")) {
		t.Fatalf("accepted output and drop markers lost causal order: %+v", chunks)
	}
	_, _, offsets, _ := database.snapshot()
	if offsets["tab-1"] != 1 {
		t.Fatalf("offset crossed a dropped durable block: %d", offsets["tab-1"])
	}
	if truncated, ended := database.endedTruncated(started[0].ID); !ended || !truncated {
		t.Fatalf("drop period must end degraded: ended=%v truncated=%v", ended, truncated)
	}
}

func TestTranscriptWriterDropMarkersPreserveSessionCausality(t *testing.T) {
	ctx := context.Background()
	database := newDelayedTranscriptStore()
	database.unblock()
	writer := newTranscriptWriter(transcriptWriterConfig{
		Database: database, Logger: discardTranscriptLogger(), QueueMaxBytes: 1,
	})
	writer.SessionStarted(ctx, transcriptIdentity("session-a"))
	writer.SessionOutput(ctx, "", "session-a", "tab-a", []byte("a"))
	writer.SessionOutput(ctx, "", "session-a", "tab-a", []byte("dropped-a"))
	writer.SessionStarted(ctx, transcriptIdentity("session-b"))
	writer.SessionOutput(ctx, "", "session-b", "tab-b", []byte("dropped-b"))
	writer.SessionEnded(ctx, "session-b")
	writer.SessionEnded(ctx, "session-a")
	batch, open := writer.takeBatch()
	if !open {
		t.Fatal("cross-session batch did not dequeue")
	}
	sessions := make(map[string]*transcriptPeriod)
	deferred := make(map[string]*deferredTranscript)
	end := len(batch)
	for index, item := range batch {
		if item.kind == transcriptItemEnd {
			end = index
			break
		}
	}
	writer.flushBatch(sessions, deferred, batch[:end])
	if len(deferred) != 0 {
		t.Fatalf("drops were not applied after their session Start: %+v", deferred)
	}
	for _, sessionID := range []string{"session-a", "session-b"} {
		period := sessions[sessionID]
		if period == nil || period.droppedChunks != 1 || !period.truncated {
			t.Fatalf("%s drop summary = %+v", sessionID, period)
		}
	}
	writer.flushBatch(sessions, deferred, batch[end:])
	started := database.startedRows()
	if len(started) != 2 {
		t.Fatalf("cross-session starts = %+v", started)
	}
	for _, row := range started {
		chunks := database.chunksForTranscript(row.ID)
		if len(chunks) == 0 || !bytes.Contains(chunks[len(chunks)-1].Data, []byte("omitted 1 chunk(s)")) {
			t.Fatalf("%s is missing its own drop marker: %+v", row.SessionID, chunks)
		}
		if truncated, ended := database.endedTruncated(row.ID); !ended || !truncated {
			t.Fatalf("%s did not end degraded: ended=%v truncated=%v", row.SessionID, ended, truncated)
		}
	}
}

func failedStartBatch(sessionID string, round int) []transcriptItem {
	drop := func(tabID string) transcriptItem {
		return transcriptItem{kind: transcriptItemDrop, sessionID: sessionID, drop: transcriptDrop{
			tabID: tabID, ts: int64(round), chunks: 1, bytes: 1, durableIDs: map[string]struct{}{tabID: {}},
		}}
	}
	return []transcriptItem{
		{kind: transcriptItemStart, sessionID: sessionID, ts: int64(round), info: transcriptIdentity(sessionID)},
		drop("tab-a"),
		{kind: transcriptItemChunk, sessionID: sessionID, tabID: "tab-a", ts: int64(round), data: []byte("accepted")},
		drop("tab-b"),
	}
}

func TestTranscriptWriterStartFailureKeepsDeferredBounded(t *testing.T) {
	database := newDelayedTranscriptStore()
	database.startFailures = 3
	database.unblock()
	writer := newTranscriptWriter(transcriptWriterConfig{Database: database, Logger: discardTranscriptLogger()})
	sessions := make(map[string]*transcriptPeriod)
	deferred := make(map[string]*deferredTranscript)
	const sessionID = "session-start-retry"
	for round := 1; round <= 3; round++ {
		writer.flushBatch(sessions, deferred, failedStartBatch(sessionID, round))
		if len(deferred) != 1 {
			t.Fatalf("deferred sessions after round %d = %d", round, len(deferred))
		}
		drop := deferred[sessionID].drop
		if drop.chunks != int64(2*round) || drop.bytes != int64(2*round) || len(drop.durableIDs) != 2 {
			t.Fatalf("deferred aggregate after round %d = %+v", round, drop)
		}
	}
	writer.flushBatch(sessions, deferred, []transcriptItem{{kind: transcriptItemStart, sessionID: sessionID, ts: 4, info: transcriptIdentity(sessionID)}})
	if len(deferred) != 0 {
		t.Fatalf("successful retry did not release deferred state: %+v", deferred)
	}
	period := sessions[sessionID]
	if period == nil || period.droppedChunks != 6 || period.droppedBytes != 6 || period.flushedChunks != 1 || len(period.durableFrozen) != 2 {
		t.Fatalf("retry must apply one bounded aggregate: %+v", period)
	}
	writer.flushBatch(sessions, deferred, []transcriptItem{{kind: transcriptItemEnd, sessionID: sessionID, ts: 5}})
	started := database.startedRows()
	if len(started) != 1 {
		t.Fatalf("successful retry rows = %+v", started)
	}
	chunks := database.chunksForTranscript(started[0].ID)
	if len(chunks) != 1 || !bytes.Contains(chunks[0].Data, []byte("omitted 6 chunk(s) (6 bytes)")) {
		t.Fatalf("retry must persist one aggregate marker: %+v", chunks)
	}
	if truncated, ended := database.endedTruncated(started[0].ID); !ended || !truncated {
		t.Fatalf("retry period must end degraded: ended=%v truncated=%v", ended, truncated)
	}
}

func TestTranscriptWriterShutdownFlushesAggregatedDeferred(t *testing.T) {
	database := newDelayedTranscriptStore()
	database.startFailures = 1
	database.unblock()
	writer := newTranscriptWriter(transcriptWriterConfig{Database: database, Logger: discardTranscriptLogger()})
	sessions := make(map[string]*transcriptPeriod)
	deferred := make(map[string]*deferredTranscript)
	writer.flushBatch(sessions, deferred, failedStartBatch("session-deferred-shutdown", 1))
	writer.endDeferredSessions(deferred)
	if len(deferred) != 0 {
		t.Fatalf("Shutdown did not release deferred state: %+v", deferred)
	}
	started := database.startedRows()
	if len(started) != 1 {
		t.Fatalf("Shutdown aggregate rows = %+v", started)
	}
	chunks := database.chunksForTranscript(started[0].ID)
	if len(chunks) != 1 || !bytes.Contains(chunks[0].Data, []byte("omitted 2 chunk(s) (2 bytes)")) {
		t.Fatalf("Shutdown must persist one aggregate marker: %+v", chunks)
	}
	if truncated, ended := database.endedTruncated(started[0].ID); !ended || !truncated {
		t.Fatalf("Shutdown aggregate must end degraded: ended=%v truncated=%v", ended, truncated)
	}
}

func TestTranscriptWriterFailedStartEndAndShutdownReleaseDeferred(t *testing.T) {
	for _, mode := range []string{"end", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			database := newDelayedTranscriptStore()
			database.startFailures = -1
			database.unblock()
			var logBuffer bytes.Buffer
			writer := newTranscriptWriter(transcriptWriterConfig{
				Database: database, Logger: slog.New(slog.NewTextHandler(&logBuffer, nil)),
			})
			sessions := make(map[string]*transcriptPeriod)
			deferred := make(map[string]*deferredTranscript)
			const sessionID = "session-permanent-start-failure"
			writer.flushBatch(sessions, deferred, failedStartBatch(sessionID, 1))
			writer.flushBatch(sessions, deferred, failedStartBatch(sessionID, 2))
			if mode == "end" {
				writer.flushBatch(sessions, deferred, []transcriptItem{{kind: transcriptItemEnd, sessionID: sessionID, ts: 3}})
			} else {
				writer.endDeferredSessions(deferred)
			}
			if len(deferred) != 0 {
				t.Fatalf("permanent failure retained deferred state: %+v", deferred)
			}
			if len(database.startedRows()) != 0 {
				t.Fatal("permanent Start failure unexpectedly created a transcript")
			}
			if !bytes.Contains(logBuffer.Bytes(), []byte("releasing gap state")) || !bytes.Contains(logBuffer.Bytes(), []byte("droppedChunks=4")) {
				t.Fatalf("permanent failure must expose aggregate loss: %s", logBuffer.String())
			}
			if got := writer.statsSnapshot().flushErrors; got != 3 {
				t.Fatalf("Start attempts and final flush errors = %d, want 3", got)
			}
		})
	}
}

var _ transcriptStore = (*delayedTranscriptStore)(nil)
