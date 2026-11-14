package production

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
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
	mu            sync.Mutex
	started       []store.TranscriptRow
	chunks        []store.TranscriptChunkRow
	ended         []bool
	endedByID     map[string]bool
	events        []string
	startFailures int

	appendStarted     chan struct{}
	appendOnce        sync.Once
	appendFailed      chan struct{}
	appendFailureOnce sync.Once
	failAppends       int
	panicAppend       bool
	panicOnce         sync.Once
	releaseAppend     chan struct{}
	releaseOnce       sync.Once
	endCalled         chan struct{}
	endOnce           sync.Once
}

func newDelayedTranscriptStore() *delayedTranscriptStore {
	return &delayedTranscriptStore{
		endedByID:     make(map[string]bool),
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

func (s *delayedTranscriptStore) TranscriptAppendChunks(_ context.Context, transcriptID string, chunks []store.TranscriptChunkRow) error {
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
	if s.failAppends > 0 {
		s.failAppends--
		s.events = append(s.events, "append_error")
		s.appendFailureOnce.Do(func() { close(s.appendFailed) })
		return errors.New("injected append failure")
	}
	for _, chunk := range chunks {
		chunk.TranscriptID = transcriptID
		chunk.Data = bytes.Clone(chunk.Data)
		s.chunks = append(s.chunks, chunk)
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

func (s *delayedTranscriptStore) snapshot() ([]store.TranscriptChunkRow, []bool, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	chunks := append([]store.TranscriptChunkRow(nil), s.chunks...)
	ended := append([]bool(nil), s.ended...)
	events := append([]string(nil), s.events...)
	return chunks, ended, events
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
	startTestRecording(ctx, writer, "session-p99", "tab-1")
	payload := bytes.Repeat([]byte("p"), 16)
	writer.RecordingOutput(ctx, "session-p99", "tab-1", payload)
	select {
	case <-database.appendStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not enter the delayed store")
	}

	const calls = 256
	durations := make([]time.Duration, 0, calls)
	for index := 0; index < calls; index++ {
		started := time.Now()
		writer.RecordingOutput(ctx, "session-p99", "tab-1", payload)
		durations = append(durations, time.Since(started))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p99 := durations[(len(durations)*99)/100]
	if p99 >= 100*time.Millisecond {
		t.Fatalf("interactive output enqueue P99 = %s while the store is blocked", p99)
	}
	t.Logf("interactive output enqueue P99 with a blocked store: %s", p99)
	writer.RecordingEnded(ctx, "session-p99", "tab-1")
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
	if stats.droppedChunks != 192 || stats.droppedBytes != uint64(192*len(payload)) || stats.batches != 2 || stats.flushedChunks != 67 || stats.sessionFlushes != 1 || stats.flushErrors != 0 {
		t.Fatalf("writer counters = %+v", stats)
	}
	// The first append holds the start resize and the first output; the
	// second the 64 queued outputs and the marker for the 192 dropped.
	chunks, ended, _ := database.snapshot()
	if len(chunks) != 67 || len(ended) != 1 || !ended[0] {
		t.Fatalf("persisted chunks=%d ended=%+v", len(chunks), ended)
	}
	for index, chunk := range chunks {
		if chunk.Seq != int64(index) {
			t.Fatalf("chunk %d has seq %d", index, chunk.Seq)
		}
	}
	if !bytes.Contains(chunks[len(chunks)-1].Data, []byte("省略了 192 段输出")) {
		t.Fatalf("last chunk must describe the degraded tail: %+v", chunks[len(chunks)-1])
	}
}

func TestTranscriptWriterRecordingEndFlushesTailWithoutShutdown(t *testing.T) {
	ctx := context.Background()
	database := newDelayedTranscriptStore()
	database.unblock()
	writer := newTranscriptWriter(transcriptWriterConfig{Database: database, Logger: discardTranscriptLogger()})
	cleanupDelayedTranscriptWriter(t, writer, database)
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	startTestRecording(ctx, writer, "session-end-flush", "tab-1")
	writer.RecordingOutput(ctx, "session-end-flush", "tab-1", []byte("first\r\n"))
	writer.RecordingOutput(ctx, "session-end-flush", "tab-1", []byte("tail-output\r\n"))
	writer.RecordingEnded(ctx, "session-end-flush", "tab-1")
	select {
	case <-database.endCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("RecordingEnded did not flush the critical tail")
	}
	chunks, ended, events := database.snapshot()
	if len(chunks) != 3 || string(chunks[2].Data) != "tail-output\r\n" || len(ended) != 1 {
		t.Fatalf("tail was not persisted before End: chunks=%+v ended=%+v", chunks, ended)
	}
	if len(events) == 0 || events[len(events)-1] != "end" {
		t.Fatalf("End must follow the tail flush: %+v", events)
	}
	// The background batch may persist the chunks before RecordingEnded runs,
	// leaving the recording-end flush nothing to do: which path wrote them is
	// incidental, only that nothing was dropped or failed.
	if stats := writer.statsSnapshot(); stats.sessionFlushes > 1 || stats.droppedChunks != 0 || stats.flushErrors != 0 {
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
	_, ended, _ := database.snapshot()
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
	startTestRecording(ctx, writer, "session-item-bound", "tab-1")
	for index := 0; index < transcriptQueueMaxItems+8; index++ {
		writer.RecordingOutput(ctx, "session-item-bound", "tab-1", []byte("x"))
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
	writer.RecordingEnded(ctx, "session-item-bound", "tab-1")
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	// The start resize, every queued output and the drop marker.
	chunks, ended, _ := database.snapshot()
	if len(chunks) != transcriptQueueMaxItems+2 || len(ended) != 1 || !ended[0] {
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
	startTestRecording(ctx, writer, "session-panic", "tab-1")
	writer.RecordingOutput(ctx, "session-panic", "tab-1", []byte("before-panic"))
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
		writer.RecordingOutput(ctx, "session-panic", "tab-1", []byte("after-panic"))
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
	startTestRecording(ctx, writer, "session-control-bound", "tab-1")
	writer.RecordingOutput(ctx, "session-control-bound", "tab-1", []byte("block-store"))
	select {
	case <-database.appendStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not enter the delayed store")
	}
	for index := 0; index < transcriptControlQueueMaxItems; index++ {
		writer.RecordingEnded(ctx, "unknown-control-session", "unknown-tab")
	}
	enqueued := make(chan struct{})
	go func() {
		writer.RecordingEnded(ctx, "unknown-control-session", "unknown-tab")
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

// A failed append degrades only that recording: its later chunks still
// persist and it ends truncated, and the tab's next recording is complete.
func TestTranscriptWriterFailedAppendEndsRecordingTruncated(t *testing.T) {
	ctx := context.Background()
	database := newDelayedTranscriptStore()
	database.failAppends = 1
	database.unblock()
	writer := newTranscriptWriter(transcriptWriterConfig{Database: database, Logger: discardTranscriptLogger()})
	cleanupDelayedTranscriptWriter(t, writer, database)
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	a := bytes.Repeat([]byte("a"), 100)
	b := []byte("b")
	startTestRecording(ctx, writer, "session-append-failure", "tab-1")
	writer.RecordingOutput(ctx, "session-append-failure", "tab-1", a)
	select {
	case <-database.appendFailed:
	case <-time.After(5 * time.Second):
		t.Fatal("append failure was not injected")
	}
	writer.RecordingOutput(ctx, "session-append-failure", "tab-1", b)
	writer.RecordingEnded(ctx, "session-append-failure", "tab-1")
	select {
	case <-database.endCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("first recording did not end")
	}
	started := database.startedRows()
	if len(started) != 1 {
		t.Fatalf("first recording starts = %+v", started)
	}
	oldChunks := database.chunksForTranscript(started[0].ID)
	if len(oldChunks) != 1 || !bytes.Equal(oldChunks[0].Data, b) || oldChunks[0].Seq != 2 {
		t.Fatalf("only the later successful block should remain in the degraded recording: %+v", oldChunks)
	}
	if truncated, ended := database.endedTruncated(started[0].ID); !ended || !truncated {
		t.Fatalf("failed recording must end degraded: ended=%v truncated=%v", ended, truncated)
	}

	startTestRecording(ctx, writer, "session-append-failure", "tab-1")
	writer.RecordingOutput(ctx, "session-append-failure", "tab-1", a)
	writer.RecordingEnded(ctx, "session-append-failure", "tab-1")
	shutdownTestWriter(t, writer, 10*time.Second)
	started = database.startedRows()
	if len(started) != 2 {
		t.Fatalf("the next recording was not created: %+v", started)
	}
	nextChunks := database.chunksForTranscript(started[1].ID)
	if len(nextChunks) != 2 || nextChunks[0].Kind != store.TranscriptChunkKindResize || !bytes.Equal(nextChunks[1].Data, a) {
		t.Fatalf("the next recording must be complete: %+v", nextChunks)
	}
	if truncated, ended := database.endedTruncated(started[1].ID); !ended || truncated {
		t.Fatalf("the next recording must end complete: ended=%v truncated=%v", ended, truncated)
	}
	if stats := writer.statsSnapshot(); stats.flushErrors != 1 {
		t.Fatalf("flush errors = %+v, want the one failed append", stats)
	}
}

// Drop markers land between the accepted chunks at the position of the loss.
func TestTranscriptWriterDropMarkersKeepCausalOrder(t *testing.T) {
	ctx := context.Background()
	database := newDelayedTranscriptStore()
	database.unblock()
	writer := newTranscriptWriter(transcriptWriterConfig{
		Database: database, Logger: discardTranscriptLogger(), QueueMaxBytes: 1,
	})
	startTestRecording(ctx, writer, "session-drop-order", "tab-1")
	writer.RecordingOutput(ctx, "session-drop-order", "tab-1", []byte("a"))
	writer.RecordingOutput(ctx, "session-drop-order", "tab-1", []byte("dropped-1"))
	first, open := writer.takeBatch()
	if !open {
		t.Fatal("first dropped segment did not dequeue")
	}
	writer.RecordingOutput(ctx, "session-drop-order", "tab-1", []byte("b"))
	writer.RecordingOutput(ctx, "session-drop-order", "tab-1", []byte("dropped-2"))
	writer.RecordingEnded(ctx, "session-drop-order", "tab-1")
	second, open := writer.takeBatch()
	if !open {
		t.Fatal("second dropped segment did not dequeue")
	}
	batch := append(first, second...)
	periods := make(map[string]*transcriptPeriod)
	end := len(batch)
	for index, item := range batch {
		if item.kind == transcriptItemEnd {
			end = index
			break
		}
	}
	writer.flushBatch(periods, batch[:end])
	period := periods["tab-1"]
	if period == nil || period.droppedChunks != 2 || !period.truncated {
		t.Fatalf("drop state = %+v", period)
	}
	writer.flushBatch(periods, batch[end:])
	started := database.startedRows()
	if len(started) != 1 {
		t.Fatalf("drop recording starts = %+v", started)
	}
	chunks := database.chunksForTranscript(started[0].ID)
	if len(chunks) != 5 || chunks[0].Kind != store.TranscriptChunkKindResize || string(chunks[1].Data) != "a" || string(chunks[3].Data) != "b" ||
		!bytes.Contains(chunks[2].Data, []byte("省略了 1 段输出")) || !bytes.Contains(chunks[4].Data, []byte("省略了 1 段输出")) {
		t.Fatalf("accepted output and drop markers lost causal order: %+v", chunks)
	}
	if truncated, ended := database.endedTruncated(started[0].ID); !ended || !truncated {
		t.Fatalf("drop recording must end degraded: ended=%v truncated=%v", ended, truncated)
	}
}

// Queue drops are kept per tab: a drop in one tab marks only that tab's
// recording, also between tabs of one session that share a batch.
func TestTranscriptWriterDropMarkersStayWithTheirTab(t *testing.T) {
	ctx := context.Background()
	database := newDelayedTranscriptStore()
	database.unblock()
	writer := newTranscriptWriter(transcriptWriterConfig{
		Database: database, Logger: discardTranscriptLogger(), QueueMaxBytes: 1,
	})
	startTestRecording(ctx, writer, "session-a", "tab-a")
	writer.RecordingOutput(ctx, "session-a", "tab-a", []byte("a"))
	writer.RecordingOutput(ctx, "session-a", "tab-a", []byte("dropped-a"))
	startTestRecording(ctx, writer, "session-a", "tab-b")
	writer.RecordingOutput(ctx, "session-a", "tab-b", []byte("dropped-b"))
	startTestRecording(ctx, writer, "session-a", "tab-c")
	writer.RecordingEnded(ctx, "session-a", "tab-b")
	writer.RecordingEnded(ctx, "session-a", "tab-a")
	writer.RecordingEnded(ctx, "session-a", "tab-c")
	batch, open := writer.takeBatch()
	if !open {
		t.Fatal("cross-tab batch did not dequeue")
	}
	periods := make(map[string]*transcriptPeriod)
	end := len(batch)
	for index, item := range batch {
		if item.kind == transcriptItemEnd {
			end = index
			break
		}
	}
	writer.flushBatch(periods, batch[:end])
	for _, tabID := range []string{"tab-a", "tab-b"} {
		period := periods[tabID]
		if period == nil || period.droppedChunks != 1 || !period.truncated {
			t.Fatalf("%s drop summary = %+v", tabID, period)
		}
	}
	if period := periods["tab-c"]; period == nil || period.droppedChunks != 0 || period.truncated {
		t.Fatalf("tab-c must not inherit another tab's drops: %+v", period)
	}
	writer.flushBatch(periods, batch[end:])
	started := database.startedRows()
	if len(started) != 2 {
		t.Fatalf("only the tabs with output or drops are kept: %+v", started)
	}
	for _, row := range started {
		if row.TabID != "tab-a" && row.TabID != "tab-b" {
			t.Fatalf("unexpected recording %+v", row)
		}
		chunks := database.chunksForTranscript(row.ID)
		if len(chunks) == 0 || !bytes.Contains(chunks[len(chunks)-1].Data, []byte("省略了 1 段输出")) {
			t.Fatalf("%s is missing its own drop marker: %+v", row.TabID, chunks)
		}
		for _, chunk := range chunks {
			if chunk.TabID != row.TabID {
				t.Fatalf("%s holds a chunk of %s", row.TabID, chunk.TabID)
			}
		}
		if truncated, ended := database.endedTruncated(row.ID); !ended || !truncated {
			t.Fatalf("%s did not end degraded: ended=%v truncated=%v", row.TabID, ended, truncated)
		}
	}
}

// A failed TranscriptStart loses only that recording, visibly: its later
// chunks and its end are skipped, and the tab's next recording is kept.
func TestTranscriptWriterStartFailureDropsOnlyThatRecording(t *testing.T) {
	database := newDelayedTranscriptStore()
	database.startFailures = 1
	database.unblock()
	var logs syncedLogBuffer
	writer := newTranscriptWriter(transcriptWriterConfig{Database: database, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	periods := make(map[string]*transcriptPeriod)
	item := func(kind transcriptItemKind, data string) transcriptItem {
		return transcriptItem{kind: kind, sessionID: "session-start-failure", tabID: "tab-1", ts: 1, data: []byte(data), info: transcriptIdentity("session-start-failure", "tab-1")}
	}
	writer.flushBatch(periods, []transcriptItem{item(transcriptItemStart, ""), item(transcriptItemChunk, "lost")})
	writer.flushBatch(periods, []transcriptItem{item(transcriptItemChunk, "also-lost")})
	writer.flushBatch(periods, []transcriptItem{item(transcriptItemEnd, "")})
	if len(periods) != 0 || len(database.startedRows()) != 0 {
		t.Fatalf("a failed start must not keep the recording: periods=%+v started=%+v", periods, database.startedRows())
	}
	if _, ended, _ := database.snapshot(); len(ended) != 0 {
		t.Fatalf("a recording without a row must not be ended: %+v", ended)
	}
	if got := writer.statsSnapshot().flushErrors; got != 1 {
		t.Fatalf("flush errors = %d, want the failed start", got)
	}
	if !strings.Contains(logs.String(), "transcript start failed") {
		t.Fatalf("the lost recording must be logged: %s", logs.String())
	}

	writer.flushBatch(periods, []transcriptItem{item(transcriptItemStart, ""), item(transcriptItemChunk, "kept"), item(transcriptItemEnd, "")})
	started := database.startedRows()
	if len(started) != 1 {
		t.Fatalf("the next recording must be kept: %+v", started)
	}
	chunks := database.chunksForTranscript(started[0].ID)
	if len(chunks) != 2 || string(chunks[1].Data) != "kept" {
		t.Fatalf("next recording chunks = %+v", chunks)
	}
	if truncated, ended := database.endedTruncated(started[0].ID); !ended || truncated {
		t.Fatalf("next recording must end complete: ended=%v truncated=%v", ended, truncated)
	}
}

var _ transcriptStore = (*delayedTranscriptStore)(nil)
