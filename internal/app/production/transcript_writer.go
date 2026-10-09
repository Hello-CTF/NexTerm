package production

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	core "github.com/Hello-CTF/NexTerm/internal/app"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

const (
	transcriptQueueMaxItems            = 4096
	transcriptQueueMaxBytes            = 64 << 20
	transcriptBatchMaxItems            = 256
	transcriptBatchMaxBytes            = 4 << 20
	transcriptDefaultMaxSessionBytes   = 64 << 20
	transcriptRetentionSettingKey      = core.TranscriptRetentionSettingKey
	transcriptRetentionInterval        = 6 * time.Hour
	transcriptDefaultRetentionMaxAge   = core.TranscriptDefaultRetentionMaxAge
	transcriptDefaultRetentionMaxCount = core.TranscriptDefaultRetentionMaxCount
)

var transcriptTruncationMarker = []byte("\r\n\x1b[33m[NexTerm] transcript size limit reached; further output not recorded\x1b[0m\r\n")

type transcriptWriterConfig struct {
	Database          *store.Store
	Logger            *slog.Logger
	MaxSessionBytes   int64
	QueueMaxBytes     int64
	RetentionInterval time.Duration
}

type transcriptItemKind int

const (
	transcriptItemStart transcriptItemKind = iota
	transcriptItemChunk
	transcriptItemEnd
)

type transcriptItem struct {
	kind      transcriptItemKind
	ticket    uint64
	sessionID string
	tabID     string
	durableID string
	ts        int64
	chunkKind int
	data      []byte
	info      session.TranscriptInfo
}

type transcriptPeriod struct {
	transcriptID string
	durableBytes map[string]int64
	nextSeq      int64
	bytes        int64
	truncated    bool
	pending      []store.TranscriptChunkRow
}

type transcriptWriter struct {
	database          *store.Store
	logger            *slog.Logger
	maxSessionBytes   int64
	queueMaxBytes     int64
	retentionInterval time.Duration

	queueMu     sync.Mutex
	queue       []transcriptItem
	queuedBytes int
	lifecycle   map[uint64]transcriptItem
	cancelled   map[uint64]bool
	nextTicket  uint64
	serving     uint64
	process     uint64
	spaceCh     chan struct{}
	closed      bool

	signalCh chan struct{}

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
	return &transcriptWriter{
		database:          config.Database,
		logger:            logger,
		maxSessionBytes:   config.MaxSessionBytes,
		queueMaxBytes:     config.QueueMaxBytes,
		retentionInterval: config.RetentionInterval,
		lifecycle:         make(map[uint64]transcriptItem),
		cancelled:         make(map[uint64]bool),
		spaceCh:           make(chan struct{}),
		signalCh:          make(chan struct{}, 1),
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
	w.wg.Add(2)
	go func() {
		defer w.wg.Done()
		w.run()
	}()
	go func() {
		defer w.wg.Done()
		w.retentionLoop(runCtx)
	}()
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
	close(w.spaceCh)
	w.spaceCh = make(chan struct{})
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

func (w *transcriptWriter) enqueue(ctx context.Context, item transcriptItem) {
	if ctx == nil {
		ctx = context.Background()
	}
	w.queueMu.Lock()
	if w.closed {
		w.queueMu.Unlock()
		w.logger.Warn("transcript item dropped: writer already shut down", "sessionId", item.sessionID, "tabId", item.tabID, "itemKind", item.kind)
		return
	}
	ticket := w.nextTicket
	w.nextTicket++
	if item.kind != transcriptItemChunk {
		w.lifecycle[ticket] = item
		w.advanceServingLocked()
		w.queueMu.Unlock()
		w.signal()
		return
	}
	for !w.closed && (ticket != w.serving || len(w.queue) >= transcriptQueueMaxItems || int64(w.queuedBytes)+int64(len(item.data)) > w.queueMaxBytes) {
		space := w.spaceCh
		w.queueMu.Unlock()
		select {
		case <-space:
		case <-ctx.Done():
			w.queueMu.Lock()
			w.cancelled[ticket] = true
			w.advanceServingLocked()
			w.queueMu.Unlock()
			w.signal()
			return
		}
		w.queueMu.Lock()
	}
	item.ticket = ticket
	w.queue = append(w.queue, item)
	w.queuedBytes += len(item.data)
	if !w.closed {
		w.serving++
		w.advanceServingLocked()
	}
	w.queueMu.Unlock()
	w.signal()
}

func (w *transcriptWriter) advanceServingLocked() {
	for {
		if w.cancelled[w.serving] {
			w.serving++
			continue
		}
		if _, ok := w.lifecycle[w.serving]; ok {
			w.serving++
			continue
		}
		return
	}
}

func (w *transcriptWriter) queueIndexOfTicketLocked(ticket uint64) int {
	for index, item := range w.queue {
		if item.ticket == ticket {
			return index
		}
	}
	return -1
}

func (w *transcriptWriter) signal() {
	select {
	case w.signalCh <- struct{}{}:
	default:
	}
}

func (w *transcriptWriter) run() {
	sessions := make(map[string]*transcriptPeriod)
	for {
		batch, open := w.takeBatch()
		if !open {
			w.endRemainingSessions(sessions)
			return
		}
		w.flushBatch(sessions, batch)
	}
}

func (w *transcriptWriter) takeBatch() ([]transcriptItem, bool) {
	for {
		w.queueMu.Lock()
		if w.closed && w.process >= w.nextTicket {
			w.queueMu.Unlock()
			return nil, false
		}
		batch := make([]transcriptItem, 0, transcriptBatchMaxItems)
		batchBytes := 0
		for len(batch) < transcriptBatchMaxItems {
			ticket := w.process
			if item, ok := w.lifecycle[ticket]; ok {
				delete(w.lifecycle, ticket)
				w.process++
				batch = append(batch, item)
				continue
			}
			if w.cancelled[ticket] {
				delete(w.cancelled, ticket)
				w.process++
				continue
			}
			if index := w.queueIndexOfTicketLocked(ticket); index >= 0 {
				item := w.queue[index]
				w.queue = append(w.queue[:index], w.queue[index+1:]...)
				w.queuedBytes -= len(item.data)
				close(w.spaceCh)
				w.spaceCh = make(chan struct{})
				w.process++
				batch = append(batch, item)
				batchBytes += len(item.data)
				if batchBytes >= transcriptBatchMaxBytes {
					break
				}
				continue
			}
			break
		}
		if len(batch) > 0 {
			w.queueMu.Unlock()
			return batch, true
		}
		w.queueMu.Unlock()
		<-w.signalCh
	}
}

func (w *transcriptWriter) flushBatch(sessions map[string]*transcriptPeriod, batch []transcriptItem) {
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
				w.logger.Error("transcript start failed; session output will not be recorded", "sessionId", item.sessionID, "error", err)
				continue
			}
			sessions[item.sessionID] = &transcriptPeriod{transcriptID: transcriptID}
		case transcriptItemChunk:
			period := sessions[item.sessionID]
			if period == nil || period.truncated {
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
				continue
			}
			if item.durableID != "" {
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
		case transcriptItemEnd:
			period := sessions[item.sessionID]
			if period == nil {
				continue
			}
			w.flushPeriod(period, item.ts)
			delete(sessions, item.sessionID)
		}
	}
	for _, period := range sessions {
		if len(period.pending) > 0 {
			if err := w.database.TranscriptAppendChunks(context.Background(), period.transcriptID, period.pending, period.durableBytes); err != nil {
				w.logger.Error("transcript chunk flush failed", "transcriptId", period.transcriptID, "error", err)
			}
			period.pending = nil
			period.durableBytes = nil
		}
	}
}

func (w *transcriptWriter) flushPeriod(period *transcriptPeriod, endedAt int64) {
	if len(period.pending) > 0 {
		if err := w.database.TranscriptAppendChunks(context.Background(), period.transcriptID, period.pending, period.durableBytes); err != nil {
			w.logger.Error("transcript chunk flush failed", "transcriptId", period.transcriptID, "error", err)
		}
		period.pending = nil
		period.durableBytes = nil
	}
	if err := w.database.TranscriptEnd(context.Background(), period.transcriptID, endedAt, period.truncated); err != nil {
		w.logger.Error("transcript end failed", "transcriptId", period.transcriptID, "error", err)
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
	w.enforceRetention(ctx)
	ticker := time.NewTicker(w.retentionInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.enforceRetention(ctx)
		}
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
	if _, err := w.database.EnforceTranscriptRetention(ctx, policy); err != nil && !errors.Is(err, context.Canceled) {
		w.logger.Warn("transcript retention cleanup failed", "error", err)
	}
}

func (w *transcriptWriter) retentionPolicy(ctx context.Context) (store.TranscriptRetentionPolicy, error) {
	return core.TranscriptRetentionPolicy(ctx, w.database)
}
