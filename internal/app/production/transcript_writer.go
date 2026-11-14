package production

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	core "github.com/Hello-CTF/NexTerm/internal/app"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

const (
	transcriptQueueMaxItems            = 4096
	transcriptQueueMaxBytes            = 64 << 20
	transcriptControlQueueMaxItems     = 1024
	transcriptBatchMaxItems            = 256
	transcriptBatchMaxBytes            = 4 << 20
	transcriptDefaultMaxSessionBytes   = 64 << 20
	transcriptRetentionSettingKey      = core.TranscriptRetentionSettingKey
	transcriptRetentionInterval        = 6 * time.Hour
	transcriptDefaultRetentionMaxAge   = core.TranscriptDefaultRetentionMaxAge
	transcriptDefaultRetentionMaxCount = core.TranscriptDefaultRetentionMaxCount
)

// transcriptBatchLinger lets a busy terminal's output chunks share one append transaction instead of
// committing each chunk on its own; a full batch or shutdown flushes without waiting.
const transcriptBatchLinger = 250 * time.Millisecond

var transcriptTruncationMarker = []byte("\r\n\x1b[33m[NexTerm] 录制已达大小上限，之后的输出不再录制\x1b[0m\r\n")

type transcriptStore interface {
	TranscriptStart(context.Context, store.TranscriptRow) error
	TranscriptAppendChunks(context.Context, string, []store.TranscriptChunkRow) error
	TranscriptEnd(context.Context, string, int64, bool) error
}

type transcriptWriterConfig struct {
	Database          transcriptStore
	Logger            *slog.Logger
	MaxSessionBytes   int64
	QueueMaxBytes     int64
	RetentionInterval time.Duration
	// RequireOwner rejects ownerless transcript writes: non-open multi-user
	// servers fail closed instead of recording rows every account can read.
	// Desktop and open-access deployments leave it false, so ownerless
	// sessions keep the legacy shared-store behavior.
	RequireOwner bool
	// BatchLinger is how long the writer waits after the first queued item for more to join its batch.
	// Zero flushes as soon as an item is queued.
	BatchLinger time.Duration
	// KeyframeIntervalBytes overrides the output between keyframes; tests
	// use it to build keyframes without recording 8 MiB.
	KeyframeIntervalBytes int64
}

type transcriptItemKind int

const (
	transcriptItemStart transcriptItemKind = iota
	transcriptItemChunk
	transcriptItemDrop
	transcriptItemEnd
)

type transcriptDrop struct {
	ts     int64
	chunks int64
	bytes  int64
	// output is set when a dropped chunk was session output, not only the
	// starting screen or a resize.
	output bool
}

type transcriptItem struct {
	kind      transcriptItemKind
	sessionID string
	tabID     string
	ownerID   string
	ts        int64
	chunkKind int
	data      []byte
	// screen marks the screen a recording starts with or redraws: it is
	// state, not output the session produced while recorded.
	screen bool
	// cols and rows are the start item's terminal size.
	cols, rows uint32
	info       session.TranscriptInfo
	drop       transcriptDrop
}

// transcriptPeriod is one tab's recording. Its row is written once the
// recording holds session output, so a recording nothing happened in never
// reaches the database, not even after a crash.
type transcriptPeriod struct {
	info         session.TranscriptInfo
	startedAt    int64
	transcriptID string
	ownerID      string
	writeCtx     context.Context
	created      bool
	failed       bool
	nextSeq      int64
	bytes        int64
	// hasOutput reports that the recording holds session output, not only
	// its starting screen, geometry or notices.
	hasOutput     bool
	truncated     bool
	capped        bool
	pending       []store.TranscriptChunkRow
	batches       int64
	flushedChunks int64
	droppedChunks int64
	droppedBytes  int64
}

type transcriptWriterStats struct {
	droppedChunks  uint64
	droppedBytes   uint64
	batches        uint64
	flushedChunks  uint64
	sessionFlushes uint64
	flushErrors    uint64
}

type transcriptWriterCounters struct {
	droppedChunks  atomic.Uint64
	droppedBytes   atomic.Uint64
	batches        atomic.Uint64
	flushedChunks  atomic.Uint64
	sessionFlushes atomic.Uint64
	flushErrors    atomic.Uint64
}

type transcriptWriter struct {
	database          transcriptStore
	retentionDatabase *store.Store
	logger            *slog.Logger
	maxSessionBytes   int64
	queueMaxBytes     int64
	retentionInterval time.Duration
	requireOwner      bool
	batchLinger       time.Duration

	queueMu        sync.Mutex
	queue          []transcriptItem
	queuedChunks   int
	queuedBytes    int
	queuedControls int
	activeDrops    map[string]int
	closed         bool

	signalCh chan struct{}
	spaceCh  chan struct{}
	stats    transcriptWriterCounters

	lifecycleMu sync.Mutex
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	retentionWg sync.WaitGroup

	// keyframer builds seek keyframes from the stored chunks; nil when the
	// database cannot store them.
	keyframer *transcriptKeyframer
}

var _ session.TranscriptSink = (*transcriptWriter)(nil)

func newTranscriptWriter(config transcriptWriterConfig) *transcriptWriter {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if config.MaxSessionBytes <= 0 {
		config.MaxSessionBytes = transcriptDefaultMaxSessionBytes
	}
	if config.QueueMaxBytes <= 0 {
		config.QueueMaxBytes = transcriptQueueMaxBytes
	}
	if config.RetentionInterval <= 0 {
		config.RetentionInterval = transcriptRetentionInterval
	}
	retentionDatabase, _ := config.Database.(*store.Store)
	var keyframer *transcriptKeyframer
	if keyframes, ok := config.Database.(keyframeStore); ok {
		keyframer = newTranscriptKeyframer(keyframes, logger)
		if config.KeyframeIntervalBytes > 0 {
			keyframer.intervalBytes = config.KeyframeIntervalBytes
		}
	}
	return &transcriptWriter{
		keyframer:         keyframer,
		database:          config.Database,
		retentionDatabase: retentionDatabase,
		logger:            logger,
		maxSessionBytes:   config.MaxSessionBytes,
		queueMaxBytes:     config.QueueMaxBytes,
		retentionInterval: config.RetentionInterval,
		requireOwner:      config.RequireOwner,
		batchLinger:       config.BatchLinger,
		activeDrops:       make(map[string]int),
		signalCh:          make(chan struct{}, 1),
		spaceCh:           make(chan struct{}),
	}
}

func (w *transcriptWriter) statsSnapshot() transcriptWriterStats {
	return transcriptWriterStats{
		droppedChunks:  w.stats.droppedChunks.Load(),
		droppedBytes:   w.stats.droppedBytes.Load(),
		batches:        w.stats.batches.Load(),
		flushedChunks:  w.stats.flushedChunks.Load(),
		sessionFlushes: w.stats.sessionFlushes.Load(),
		flushErrors:    w.stats.flushErrors.Load(),
	}
}

func (w *transcriptWriter) Start(ctx context.Context) error {
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()
	if w.cancel != nil {
		return fmt.Errorf("transcript writer already started")
	}
	if w.database == nil {
		return fmt.Errorf("transcript writer requires a database")
	}
	if w.retentionDatabase != nil {
		// Nothing records yet: whatever is still open was cut off by an
		// earlier run.
		if ended, deleted, err := w.retentionDatabase.TranscriptCloseAbandoned(context.WithoutCancel(ctx)); err != nil {
			w.logger.Warn("settling abandoned transcripts failed", "error", err)
		} else if ended > 0 || deleted > 0 {
			w.logger.Info("settled abandoned transcripts", "ended", ended, "deleted", deleted)
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	if w.keyframer != nil {
		w.keyframer.start()
	}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.run()
	}()
	if w.retentionDatabase != nil {
		w.retentionWg.Add(1)
		go func() {
			defer w.retentionWg.Done()
			w.retentionLoop(runCtx)
		}()
	}
	return nil
}

// Shutdown closes the queue, cancels the run loop's context, joins the
// retention loop unconditionally, then waits for the run loop to drain and
// exit until ctx expires.
//
// Driver limitation (M91/M96): a flush already inside the modernc busy
// handler cannot be interrupted — the durable write context deliberately
// outlives the run loop, the busy handler keeps waiting even when the
// statement context is cancelled, and database/sql Close neither interrupts
// nor joins in-flight connections. While the external lock stays held, the
// wait ends only when the store's busy_timeout expires and the flush fails;
// if the lock clears earlier, the same wait completes and the flush commits.
// When the failure outlasts ctx, Shutdown reports ctx.Err() while the run
// loop finishes the failed flush and exits, and the unflushed tail stays
// suppressed. The bound is per write, not per drain: with the store still
// open, the drain's later statements (such as TranscriptEnd) may each
// busy-wait again; after Store.Close they fail fast on the closed pool.
// Callers that must keep the join deterministic under sustained external
// lock contention bound the wait below their shutdown budget with
// store.OpenOptions.BusyTimeout; the 30s production default is unchanged.
func (w *transcriptWriter) Shutdown(ctx context.Context) error {
	w.lifecycleMu.Lock()
	cancel := w.cancel
	w.cancel = nil
	w.lifecycleMu.Unlock()
	if cancel == nil {
		return nil
	}
	w.queueMu.Lock()
	w.closed = true
	w.notifySpaceLocked()
	w.queueMu.Unlock()
	w.signal()
	cancel()
	// The retention loop is joined unconditionally: every retention write and
	// log must happen before Shutdown returns, never after the caller moved on
	// and possibly closed the store or the test's logger. The run loop keeps
	// its context escape so a stalled database drain cannot wedge shutdown.
	w.retentionWg.Wait()
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		if w.keyframer != nil {
			w.keyframer.close(ctx)
		}
		return nil
	case <-ctx.Done():
		if w.keyframer != nil {
			w.keyframer.close(ctx)
		}
		return ctx.Err()
	}
}

func (w *transcriptWriter) RecordingStarted(ctx context.Context, info session.TranscriptInfo, cols, rows uint32, screen []byte) {
	w.enqueue(ctx, transcriptItem{
		kind: transcriptItemStart, sessionID: info.SessionID, tabID: info.TabID, ts: time.Now().UnixMilli(),
		info: info, cols: cols, rows: rows, data: bytes.Clone(screen),
	})
}

func (w *transcriptWriter) RecordingScreen(ctx context.Context, sessionID, tabID string, cols, rows uint32, screen []byte) {
	w.RecordingResize(ctx, sessionID, tabID, cols, rows)
	if len(screen) == 0 {
		return
	}
	w.enqueue(ctx, transcriptItem{
		kind: transcriptItemChunk, sessionID: sessionID, tabID: tabID, ts: time.Now().UnixMilli(),
		data: bytes.Clone(screen), screen: true,
	})
}

func (w *transcriptWriter) RecordingOutput(ctx context.Context, sessionID, tabID string, data []byte) {
	if len(data) == 0 {
		return
	}
	w.enqueue(ctx, transcriptItem{
		kind: transcriptItemChunk, sessionID: sessionID, tabID: tabID,
		ts: time.Now().UnixMilli(), data: bytes.Clone(data),
	})
}

func (w *transcriptWriter) RecordingResize(ctx context.Context, sessionID, tabID string, cols, rows uint32) {
	w.enqueue(ctx, transcriptItem{
		kind: transcriptItemChunk, chunkKind: store.TranscriptChunkKindResize,
		sessionID: sessionID, tabID: tabID, ts: time.Now().UnixMilli(),
		data: resizePayload(cols, rows),
	})
}

func (w *transcriptWriter) RecordingEnded(ctx context.Context, sessionID, tabID string) {
	w.enqueue(ctx, transcriptItem{kind: transcriptItemEnd, sessionID: sessionID, tabID: tabID, ts: time.Now().UnixMilli()})
}

// isOutput reports a chunk of session output, not the starting screen or a resize.
func (item transcriptItem) isOutput() bool {
	return item.kind == transcriptItemChunk && item.chunkKind == store.TranscriptChunkKindOutput && !item.screen
}

func resizePayload(cols, rows uint32) []byte {
	return []byte(fmt.Sprintf(`{"cols":%d,"rows":%d}`, cols, rows))
}

func (w *transcriptWriter) enqueue(ctx context.Context, item transcriptItem) {
	item.ownerID = ipc.OwnerID(ctx)
	w.queueMu.Lock()
	reported := false
	for !w.closed && item.kind != transcriptItemChunk && w.queuedControls >= transcriptControlQueueMaxItems {
		space := w.spaceCh
		w.queueMu.Unlock()
		if !reported {
			w.logger.Warn("transcript lifecycle queue full; blocking lifecycle source", "sessionId", item.sessionID, "itemKind", item.kind, "queue", transcriptControlQueueMaxItems)
			reported = true
		}
		<-space
		w.queueMu.Lock()
	}
	if w.closed {
		if item.kind == transcriptItemChunk {
			w.stats.droppedChunks.Add(1)
			w.stats.droppedBytes.Add(uint64(len(item.data)))
		}
		droppedChunks := w.stats.droppedChunks.Load()
		droppedBytes := w.stats.droppedBytes.Load()
		w.queueMu.Unlock()
		w.logger.Warn("transcript item dropped: writer already shut down", "sessionId", item.sessionID, "tabId", item.tabID, "itemKind", item.kind, "droppedChunks", droppedChunks, "droppedBytes", droppedBytes)
		return
	}
	if item.kind == transcriptItemChunk && (w.queuedChunks >= transcriptQueueMaxItems || int64(w.queuedBytes)+int64(len(item.data)) > w.queueMaxBytes) {
		report := w.dropChunkLocked(item)
		droppedChunks := w.stats.droppedChunks.Load()
		droppedBytes := w.stats.droppedBytes.Load()
		w.queueMu.Unlock()
		w.signal()
		if report {
			w.logger.Warn("transcript queue full; dropping reconstructable data", "sessionId", item.sessionID, "tabId", item.tabID, "droppedChunks", droppedChunks, "droppedBytes", droppedBytes)
		}
		return
	}
	w.queue = append(w.queue, item)
	if item.kind == transcriptItemChunk {
		w.queuedChunks++
		w.queuedBytes += len(item.data)
	} else {
		w.queuedControls++
	}
	delete(w.activeDrops, item.tabID)
	w.queueMu.Unlock()
	w.signal()
}

func (w *transcriptWriter) dropChunkLocked(item transcriptItem) bool {
	w.stats.droppedChunks.Add(1)
	w.stats.droppedBytes.Add(uint64(len(item.data)))
	if index, ok := w.activeDrops[item.tabID]; ok && index >= 0 && index < len(w.queue) {
		marker := &w.queue[index]
		if marker.kind == transcriptItemDrop && marker.tabID == item.tabID {
			marker.drop.chunks++
			marker.drop.bytes += int64(len(item.data))
			marker.drop.output = marker.drop.output || item.isOutput()
			return false
		}
	}
	w.queue = append(w.queue, transcriptItem{
		kind: transcriptItemDrop, sessionID: item.sessionID, tabID: item.tabID, ownerID: item.ownerID,
		drop: transcriptDrop{ts: item.ts, chunks: 1, bytes: int64(len(item.data)), output: item.isOutput()},
	})
	w.activeDrops[item.tabID] = len(w.queue) - 1
	return true
}

func (w *transcriptWriter) signal() {
	select {
	case w.signalCh <- struct{}{}:
	default:
	}
}

// transcriptOwnerContext resolves the database context carrying the trusted
// session owner into which a transcript period writes. The second return is
// false when a non-open deployment must fail closed: an ownerless row would
// be readable by every account.
func (w *transcriptWriter) transcriptOwnerContext(ownerID string) (context.Context, bool) {
	if ownerID == "" {
		if w.requireOwner {
			return nil, false
		}
		return context.Background(), true
	}
	return ipc.WithUserID(context.Background(), ownerID), true
}

func (w *transcriptWriter) failClosedOwnerless(sessionID, operation string) {
	w.logger.Warn("transcript write without an owner on a non-open server; failing closed instead of ownerless",
		"sessionId", sessionID, "operation", operation)
}

func (w *transcriptWriter) notifySpaceLocked() {
	close(w.spaceCh)
	w.spaceCh = make(chan struct{})
}

func (w *transcriptWriter) run() {
	periods := make(map[string]*transcriptPeriod)
	defer func() {
		if recovered := recover(); recovered != nil {
			w.queueMu.Lock()
			w.closed = true
			w.stats.droppedChunks.Add(uint64(w.queuedChunks))
			w.stats.droppedBytes.Add(uint64(w.queuedBytes))
			w.queue = nil
			w.queuedChunks = 0
			w.queuedBytes = 0
			w.queuedControls = 0
			w.notifySpaceLocked()
			w.activeDrops = make(map[string]int)
			w.queueMu.Unlock()
			w.logger.Error("transcript writer panic; writer closed without blocking terminal I/O", "panic", recovered)
		}
	}()
	for {
		batch, open := w.takeBatch()
		if !open {
			w.endRemainingPeriods(periods)
			return
		}
		w.flushBatch(periods, batch)
	}
}

func (w *transcriptWriter) takeBatch() ([]transcriptItem, bool) {
	var lingerUntil time.Time
	for {
		w.queueMu.Lock()
		if len(w.queue) == 0 {
			if w.closed {
				w.queueMu.Unlock()
				return nil, false
			}
			w.queueMu.Unlock()
			<-w.signalCh
			continue
		}
		if w.batchLinger > 0 && !w.closed && len(w.queue) < transcriptBatchMaxItems && w.queuedBytes < transcriptBatchMaxBytes {
			if lingerUntil.IsZero() {
				lingerUntil = time.Now().Add(w.batchLinger)
			}
			if wait := time.Until(lingerUntil); wait > 0 {
				w.queueMu.Unlock()
				timer := time.NewTimer(wait)
				select {
				case <-w.signalCh:
				case <-timer.C:
				}
				timer.Stop()
				continue
			}
		}
		count := 0
		batchBytes := 0
		for count < len(w.queue) && count < transcriptBatchMaxItems {
			item := &w.queue[count]
			if item.kind == transcriptItemChunk {
				batchBytes += len(item.data)
			}
			count++
			if batchBytes >= transcriptBatchMaxBytes {
				break
			}
		}
		batch := append(make([]transcriptItem, 0, count), w.queue[:count]...)
		for _, item := range batch {
			switch item.kind {
			case transcriptItemChunk:
				w.queuedChunks--
				w.queuedBytes -= len(item.data)
			case transcriptItemStart, transcriptItemEnd:
				w.queuedControls--
			}
		}
		for tabID, index := range w.activeDrops {
			if index < count {
				delete(w.activeDrops, tabID)
			} else {
				w.activeDrops[tabID] = index - count
			}
		}
		remaining := len(w.queue) - count
		copy(w.queue, w.queue[count:])
		for index := remaining; index < len(w.queue); index++ {
			w.queue[index] = transcriptItem{}
		}
		w.queue = w.queue[:remaining]
		w.notifySpaceLocked()
		w.queueMu.Unlock()
		return batch, true
	}
}

func (w *transcriptWriter) flushBatch(periods map[string]*transcriptPeriod, batch []transcriptItem) {
	for _, item := range batch {
		switch item.kind {
		case transcriptItemStart:
			if previous := periods[item.tabID]; previous != nil {
				w.logger.Warn("transcript start for a tab with an open recording; ending the previous one", "sessionId", item.sessionID, "tabId", item.tabID)
				w.flushPeriod(previous, item.ts)
			}
			writeCtx, ok := w.transcriptOwnerContext(item.ownerID)
			if !ok {
				w.failClosedOwnerless(item.sessionID, "start")
				delete(periods, item.tabID)
				continue
			}
			period := &transcriptPeriod{info: item.info, startedAt: item.ts, ownerID: item.ownerID, writeCtx: writeCtx}
			periods[item.tabID] = period
			w.appendChunk(period, store.TranscriptChunkRow{TabID: item.tabID, TS: item.ts, Kind: store.TranscriptChunkKindResize, Data: resizePayload(item.cols, item.rows)})
			if len(item.data) > 0 {
				w.appendChunk(period, store.TranscriptChunkRow{TabID: item.tabID, TS: item.ts, Data: item.data})
			}
		case transcriptItemChunk:
			period := periods[item.tabID]
			if period == nil {
				continue
			}
			if item.isOutput() {
				period.hasOutput = true
			}
			w.appendChunk(period, store.TranscriptChunkRow{TabID: item.tabID, TS: item.ts, Kind: item.chunkKind, Data: item.data})
		case transcriptItemDrop:
			period := periods[item.tabID]
			if period == nil {
				continue
			}
			w.applyTranscriptDrop(period, item.tabID, item.drop)
		case transcriptItemEnd:
			period := periods[item.tabID]
			if period == nil {
				continue
			}
			w.flushPeriod(period, item.ts)
			delete(periods, item.tabID)
		}
	}
	for _, period := range periods {
		w.flushPending(period)
	}
}

// appendChunk queues a chunk for the period's next flush, ending the
// recording with a marker once it reaches the size cap.
func (w *transcriptWriter) appendChunk(period *transcriptPeriod, chunk store.TranscriptChunkRow) {
	if period.capped || period.failed {
		return
	}
	if period.bytes+int64(len(chunk.Data)) > w.maxSessionBytes {
		chunk.Kind = store.TranscriptChunkKindOutput
		chunk.Data = transcriptTruncationMarker
		period.truncated = true
		period.capped = true
	}
	chunk.Seq = period.nextSeq
	period.pending = append(period.pending, chunk)
	period.nextSeq++
	period.bytes += int64(len(chunk.Data))
}

func (w *transcriptWriter) applyTranscriptDrop(period *transcriptPeriod, tabID string, drop transcriptDrop) {
	if !drop.output {
		// Only the starting screen or a resize was lost: nothing the session
		// produced is missing from the recording.
		return
	}
	period.hasOutput = true
	period.truncated = true
	period.droppedChunks += drop.chunks
	period.droppedBytes += drop.bytes
	marker := []byte(fmt.Sprintf("\r\n\x1b[33m[NexTerm] 录制队列已满，此处省略了 %d 段输出（%d 字节）\x1b[0m\r\n", drop.chunks, drop.bytes))
	w.appendChunk(period, store.TranscriptChunkRow{TabID: tabID, TS: drop.ts, Data: marker})
}

// flushPending writes the period's queued chunks, creating its row with the
// first chunks that hold session output.
func (w *transcriptWriter) flushPending(period *transcriptPeriod) {
	if len(period.pending) == 0 || period.failed {
		period.pending = nil
		return
	}
	if !period.created {
		if !period.hasOutput && !period.truncated {
			return
		}
		transcriptID := ids.New()
		if err := w.database.TranscriptStart(period.writeCtx, store.TranscriptRow{
			ID:        transcriptID,
			SessionID: period.info.SessionID,
			TabID:     period.info.TabID,
			TabTitle:  period.info.TabTitle,
			AssetID:   period.info.AssetID,
			AssetName: period.info.AssetName,
			AssetKind: period.info.AssetKind,
			StartedAt: period.startedAt,
		}); err != nil {
			period.failed = true
			period.pending = nil
			w.stats.flushErrors.Add(1)
			w.logger.Error("transcript start failed; this recording is not kept", "sessionId", period.info.SessionID, "tabId", period.info.TabID, "error", err)
			return
		}
		period.transcriptID = transcriptID
		period.created = true
	}
	chunks := len(period.pending)
	if err := w.database.TranscriptAppendChunks(period.writeCtx, period.transcriptID, period.pending); err != nil {
		if w.keyframer != nil {
			w.keyframer.control(period.transcriptID, false)
		}
		period.truncated = true
		w.stats.flushErrors.Add(1)
		w.logger.Error("transcript chunk flush failed", "sessionId", period.info.SessionID, "transcriptId", period.transcriptID, "chunks", chunks, "error", err)
	} else {
		if w.keyframer != nil {
			w.keyframer.chunks(period.writeCtx, period.transcriptID, period.pending)
		}
		period.batches++
		period.flushedChunks += int64(chunks)
		batches := w.stats.batches.Add(1)
		flushedChunks := w.stats.flushedChunks.Add(uint64(chunks))
		w.logger.Debug("transcript batch flushed", "sessionId", period.info.SessionID, "transcriptId", period.transcriptID, "chunks", chunks, "batches", batches, "flushedChunks", flushedChunks)
	}
	period.pending = nil
}

// flushPeriod ends a recording. One that never held session output was never
// written and leaves nothing behind.
func (w *transcriptWriter) flushPeriod(period *transcriptPeriod, endedAt int64) {
	w.flushPending(period)
	if !period.created {
		return
	}
	if w.keyframer != nil {
		defer w.keyframer.control(period.transcriptID, true)
	}
	if err := w.database.TranscriptEnd(period.writeCtx, period.transcriptID, endedAt, period.truncated); err != nil {
		w.stats.flushErrors.Add(1)
		w.logger.Error("transcript end failed", "sessionId", period.info.SessionID, "transcriptId", period.transcriptID, "error", err)
		return
	}
	w.stats.sessionFlushes.Add(1)
	w.logger.Info("transcript recording flushed", "sessionId", period.info.SessionID, "tabId", period.info.TabID, "transcriptId", period.transcriptID, "batches", period.batches, "flushedChunks", period.flushedChunks, "droppedChunks", period.droppedChunks, "droppedBytes", period.droppedBytes)
}

func (w *transcriptWriter) endRemainingPeriods(periods map[string]*transcriptPeriod) {
	now := time.Now().UnixMilli()
	for tabID, period := range periods {
		w.flushPeriod(period, now)
		delete(periods, tabID)
	}
}

func (w *transcriptWriter) retentionLoop(ctx context.Context) {
	if err := w.retentionDatabase.RunRetentionSchedule(ctx, store.RetentionSchedule{
		Name: "transcripts", Interval: w.retentionInterval, Run: w.enforceRetention,
	}); err != nil {
		w.logger.Error("transcript retention scheduler failed", "error", err)
	}
}

func (w *transcriptWriter) enforceRetention(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		return
	}
	policy, err := w.retentionPolicy(ctx)
	if err != nil {
		w.logger.Warn("transcript retention policy invalid; applying defaults", "error", err)
		policy = store.TranscriptRetentionPolicy{
			MaxAge:   transcriptDefaultRetentionMaxAge,
			MaxCount: transcriptDefaultRetentionMaxCount,
		}
	}
	if _, err := w.retentionDatabase.EnforceTranscriptRetention(ctx, policy); err != nil && !errors.Is(err, context.Canceled) {
		w.logger.Warn("transcript retention cleanup failed", "error", err)
	}
}

func (w *transcriptWriter) retentionPolicy(ctx context.Context) (store.TranscriptRetentionPolicy, error) {
	return core.TranscriptRetentionPolicy(ctx, w.retentionDatabase)
}
