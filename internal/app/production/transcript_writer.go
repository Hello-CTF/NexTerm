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

var transcriptTruncationMarker = []byte("\r\n\x1b[33m[NexTerm] transcript size limit reached; further output not recorded\x1b[0m\r\n")

type transcriptStore interface {
	TranscriptStart(context.Context, store.TranscriptRow) error
	TranscriptAppendChunks(context.Context, string, []store.TranscriptChunkRow, ...map[string]int64) error
	TranscriptEnd(context.Context, string, int64, bool) error
}

type transcriptWriterConfig struct {
	Database          transcriptStore
	Logger            *slog.Logger
	MaxSessionBytes   int64
	QueueMaxBytes     int64
	RetentionInterval time.Duration
}

type transcriptItemKind int

const (
	transcriptItemStart transcriptItemKind = iota
	transcriptItemChunk
	transcriptItemDrop
	transcriptItemEnd
)

type transcriptDrop struct {
	tabID      string
	ts         int64
	chunks     int64
	bytes      int64
	durableIDs map[string]struct{}
}

func (d *transcriptDrop) merge(other transcriptDrop) {
	if d.chunks == 0 {
		d.tabID = other.tabID
		d.ts = other.ts
	}
	d.chunks += other.chunks
	d.bytes += other.bytes
	if d.durableIDs == nil {
		d.durableIDs = make(map[string]struct{})
	}
	for durableID := range other.durableIDs {
		d.durableIDs[durableID] = struct{}{}
	}
}

type deferredTranscript struct {
	info      session.TranscriptInfo
	startedAt int64
	drop      transcriptDrop
}

type transcriptItem struct {
	kind      transcriptItemKind
	sessionID string
	tabID     string
	durableID string
	ts        int64
	chunkKind int
	data      []byte
	info      session.TranscriptInfo
	drop      transcriptDrop
}

type transcriptPeriod struct {
	sessionID     string
	transcriptID  string
	durableBytes  map[string]int64
	durableFrozen map[string]bool
	nextSeq       int64
	bytes         int64
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
	return &transcriptWriter{
		database:          config.Database,
		retentionDatabase: retentionDatabase,
		logger:            logger,
		maxSessionBytes:   config.MaxSessionBytes,
		queueMaxBytes:     config.QueueMaxBytes,
		retentionInterval: config.RetentionInterval,
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
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.run()
	}()
	if w.retentionDatabase != nil {
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			w.retentionLoop(runCtx)
		}()
	}
	return nil
}

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
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *transcriptWriter) SessionStarted(ctx context.Context, info session.TranscriptInfo) {
	w.enqueue(ctx, transcriptItem{kind: transcriptItemStart, sessionID: info.SessionID, ts: time.Now().UnixMilli(), info: info})
}

func (w *transcriptWriter) SessionOutput(ctx context.Context, durableID, sessionID, tabID string, data []byte) {
	if len(data) == 0 {
		return
	}
	w.enqueue(ctx, transcriptItem{
		kind: transcriptItemChunk, sessionID: sessionID, tabID: tabID, durableID: durableID,
		ts: time.Now().UnixMilli(), data: bytes.Clone(data),
	})
}

func (w *transcriptWriter) SessionInput(ctx context.Context, sessionID, tabID string, data []byte) {
	if len(data) == 0 {
		return
	}
	w.enqueue(ctx, transcriptItem{
		kind: transcriptItemChunk, chunkKind: store.TranscriptChunkKindInput,
		sessionID: sessionID, tabID: tabID, ts: time.Now().UnixMilli(), data: bytes.Clone(data),
	})
}

func (w *transcriptWriter) SessionResize(ctx context.Context, sessionID, tabID string, cols, rows uint32) {
	w.enqueue(ctx, transcriptItem{
		kind: transcriptItemChunk, chunkKind: store.TranscriptChunkKindResize,
		sessionID: sessionID, tabID: tabID, ts: time.Now().UnixMilli(),
		data: []byte(fmt.Sprintf(`{"cols":%d,"rows":%d}`, cols, rows)),
	})
}

func (w *transcriptWriter) SessionEnded(ctx context.Context, sessionID string) {
	w.enqueue(ctx, transcriptItem{kind: transcriptItemEnd, sessionID: sessionID, ts: time.Now().UnixMilli()})
}

func (w *transcriptWriter) enqueue(_ context.Context, item transcriptItem) {
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
	delete(w.activeDrops, item.sessionID)
	w.queueMu.Unlock()
	w.signal()
}

func (w *transcriptWriter) dropChunkLocked(item transcriptItem) bool {
	w.stats.droppedChunks.Add(1)
	w.stats.droppedBytes.Add(uint64(len(item.data)))
	if index, ok := w.activeDrops[item.sessionID]; ok && index >= 0 && index < len(w.queue) {
		marker := &w.queue[index]
		if marker.kind == transcriptItemDrop && marker.sessionID == item.sessionID {
			marker.drop.chunks++
			marker.drop.bytes += int64(len(item.data))
			if item.durableID != "" {
				marker.drop.durableIDs[item.durableID] = struct{}{}
			}
			return false
		}
	}
	drop := transcriptDrop{
		tabID: item.tabID, ts: item.ts, chunks: 1, bytes: int64(len(item.data)), durableIDs: make(map[string]struct{}),
	}
	if item.durableID != "" {
		drop.durableIDs[item.durableID] = struct{}{}
	}
	w.queue = append(w.queue, transcriptItem{kind: transcriptItemDrop, sessionID: item.sessionID, drop: drop})
	w.activeDrops[item.sessionID] = len(w.queue) - 1
	return true
}

func (w *transcriptWriter) signal() {
	select {
	case w.signalCh <- struct{}{}:
	default:
	}
}

func (w *transcriptWriter) notifySpaceLocked() {
	close(w.spaceCh)
	w.spaceCh = make(chan struct{})
}

func (w *transcriptWriter) run() {
	sessions := make(map[string]*transcriptPeriod)
	deferredDrops := make(map[string]*deferredTranscript)
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
			w.endRemainingSessions(sessions)
			w.endDeferredSessions(deferredDrops)
			return
		}
		w.flushBatch(sessions, deferredDrops, batch)
	}
}

func (w *transcriptWriter) takeBatch() ([]transcriptItem, bool) {
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
		for sessionID, index := range w.activeDrops {
			if index < count {
				delete(w.activeDrops, sessionID)
			} else {
				w.activeDrops[sessionID] = index - count
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

func (w *transcriptWriter) flushBatch(sessions map[string]*transcriptPeriod, deferredDrops map[string]*deferredTranscript, batch []transcriptItem) {
	for _, item := range batch {
		switch item.kind {
		case transcriptItemStart:
			if _, exists := sessions[item.sessionID]; exists {
				w.logger.Warn("transcript start for session with an open transcript", "sessionId", item.sessionID)
				continue
			}
			transcriptID := ids.New()
			if err := w.database.TranscriptStart(context.Background(), store.TranscriptRow{
				ID:        transcriptID,
				SessionID: item.info.SessionID,
				AssetID:   item.info.AssetID,
				AssetName: item.info.AssetName,
				AssetKind: item.info.AssetKind,
				StartedAt: item.ts,
			}); err != nil {
				if deferredDrops[item.sessionID] == nil {
					deferredDrops[item.sessionID] = &deferredTranscript{info: item.info, startedAt: item.ts}
				}
				w.stats.flushErrors.Add(1)
				w.logger.Error("transcript start failed; session output will not be recorded", "sessionId", item.sessionID, "error", err)
				continue
			}
			period := &transcriptPeriod{sessionID: item.sessionID, transcriptID: transcriptID}
			sessions[item.sessionID] = period
			if deferred := deferredDrops[item.sessionID]; deferred != nil && deferred.drop.chunks > 0 {
				w.applyTranscriptDrop(period, deferred.drop)
			}
			delete(deferredDrops, item.sessionID)
		case transcriptItemChunk:
			period := sessions[item.sessionID]
			if period == nil || period.capped {
				continue
			}
			if period.bytes+int64(len(item.data)) > w.maxSessionBytes {
				period.pending = append(period.pending, store.TranscriptChunkRow{
					Seq: period.nextSeq, TabID: item.tabID, TS: item.ts,
					Data: transcriptTruncationMarker,
				})
				period.nextSeq++
				period.bytes += int64(len(transcriptTruncationMarker))
				period.truncated = true
				period.capped = true
				continue
			}
			if item.durableID != "" && !period.durableFrozen[item.durableID] {
				if period.durableBytes == nil {
					period.durableBytes = make(map[string]int64)
				}
				period.durableBytes[item.durableID] += int64(len(item.data))
			}
			period.pending = append(period.pending, store.TranscriptChunkRow{
				Seq: period.nextSeq, TabID: item.tabID, TS: item.ts, Kind: item.chunkKind, Data: item.data,
			})
			period.nextSeq++
			period.bytes += int64(len(item.data))
		case transcriptItemDrop:
			period := sessions[item.sessionID]
			if period == nil {
				deferred := deferredDrops[item.sessionID]
				if deferred == nil {
					deferred = &deferredTranscript{info: session.TranscriptInfo{SessionID: item.sessionID}, startedAt: item.drop.ts}
					deferredDrops[item.sessionID] = deferred
				}
				deferred.drop.merge(item.drop)
				continue
			}
			w.applyTranscriptDrop(period, item.drop)
		case transcriptItemEnd:
			period := sessions[item.sessionID]
			if period == nil {
				w.flushDeferred(deferredDrops[item.sessionID], item.ts)
				delete(deferredDrops, item.sessionID)
				continue
			}
			w.flushPeriod(period, item.ts)
			delete(sessions, item.sessionID)
		}
	}
	for _, period := range sessions {
		w.flushPending(period)
	}
}

func (w *transcriptWriter) applyTranscriptDrop(period *transcriptPeriod, drop transcriptDrop) {
	for durableID := range drop.durableIDs {
		period.freezeDurable(durableID)
	}
	period.truncated = true
	period.droppedChunks += drop.chunks
	period.droppedBytes += drop.bytes
	if period.capped {
		return
	}
	marker := []byte(fmt.Sprintf("\r\n\x1b[33m[NexTerm] transcript queue full; omitted %d chunk(s) (%d bytes)\x1b[0m\r\n", drop.chunks, drop.bytes))
	period.pending = append(period.pending, store.TranscriptChunkRow{
		Seq: period.nextSeq, TabID: drop.tabID, TS: drop.ts, Data: marker,
	})
	period.nextSeq++
	period.bytes += int64(len(marker))
}

func (p *transcriptPeriod) freezeDurable(durableID string) {
	if durableID == "" {
		return
	}
	if p.durableFrozen == nil {
		p.durableFrozen = make(map[string]bool)
	}
	p.durableFrozen[durableID] = true
}

func (w *transcriptWriter) flushPending(period *transcriptPeriod) {
	if len(period.pending) == 0 {
		return
	}
	chunks := len(period.pending)
	if err := w.database.TranscriptAppendChunks(context.Background(), period.transcriptID, period.pending, period.durableBytes); err != nil {
		period.truncated = true
		for durableID := range period.durableBytes {
			period.freezeDurable(durableID)
		}
		w.stats.flushErrors.Add(1)
		w.logger.Error("transcript chunk flush failed", "sessionId", period.sessionID, "transcriptId", period.transcriptID, "chunks", chunks, "error", err)
	} else {
		period.batches++
		period.flushedChunks += int64(chunks)
		batches := w.stats.batches.Add(1)
		flushedChunks := w.stats.flushedChunks.Add(uint64(chunks))
		w.logger.Debug("transcript batch flushed", "sessionId", period.sessionID, "transcriptId", period.transcriptID, "chunks", chunks, "batches", batches, "flushedChunks", flushedChunks)
	}
	period.pending = nil
	period.durableBytes = nil
}

func (w *transcriptWriter) flushPeriod(period *transcriptPeriod, endedAt int64) {
	w.flushPending(period)
	if err := w.database.TranscriptEnd(context.Background(), period.transcriptID, endedAt, period.truncated); err != nil {
		w.stats.flushErrors.Add(1)
		w.logger.Error("transcript end failed", "sessionId", period.sessionID, "transcriptId", period.transcriptID, "error", err)
		return
	}
	w.stats.sessionFlushes.Add(1)
	w.logger.Info("transcript session flushed", "sessionId", period.sessionID, "transcriptId", period.transcriptID, "batches", period.batches, "flushedChunks", period.flushedChunks, "droppedChunks", period.droppedChunks, "droppedBytes", period.droppedBytes)
}

func (w *transcriptWriter) flushDeferred(deferred *deferredTranscript, endedAt int64) {
	if deferred == nil || deferred.drop.chunks == 0 {
		return
	}
	transcriptID := ids.New()
	if err := w.database.TranscriptStart(context.Background(), store.TranscriptRow{
		ID:        transcriptID,
		SessionID: deferred.info.SessionID,
		AssetID:   deferred.info.AssetID,
		AssetName: deferred.info.AssetName,
		AssetKind: deferred.info.AssetKind,
		StartedAt: deferred.startedAt,
	}); err != nil {
		w.stats.flushErrors.Add(1)
		w.logger.Error("deferred transcript flush failed; releasing gap state", "sessionId", deferred.info.SessionID, "droppedChunks", deferred.drop.chunks, "droppedBytes", deferred.drop.bytes, "error", err)
		return
	}
	period := &transcriptPeriod{sessionID: deferred.info.SessionID, transcriptID: transcriptID}
	w.applyTranscriptDrop(period, deferred.drop)
	w.flushPeriod(period, endedAt)
}

func (w *transcriptWriter) endDeferredSessions(deferredDrops map[string]*deferredTranscript) {
	now := time.Now().UnixMilli()
	for sessionID, deferred := range deferredDrops {
		w.flushDeferred(deferred, now)
		delete(deferredDrops, sessionID)
	}
}

func (w *transcriptWriter) endRemainingSessions(sessions map[string]*transcriptPeriod) {
	now := time.Now().UnixMilli()
	for sessionID, period := range sessions {
		w.flushPeriod(period, now)
		delete(sessions, sessionID)
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
