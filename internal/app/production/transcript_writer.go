package production

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

const (
	transcriptQueueMaxItems            = 4096
	transcriptQueueMaxBytes            = 64 << 20
	transcriptBatchMaxItems            = 256
	transcriptBatchMaxBytes            = 4 << 20
	transcriptDefaultMaxSessionBytes   = 64 << 20
	transcriptRetentionSettingKey      = "transcript.retention"
	transcriptRetentionInterval        = 6 * time.Hour
	transcriptDefaultRetentionMaxAge   = 30 * 24 * time.Hour
	transcriptDefaultRetentionMaxCount = 2000
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
	ts        int64
	data      []byte
	info      session.TranscriptInfo
}

type transcriptPeriod struct {
	transcriptID string
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

func (w *transcriptWriter) SessionOutput(ctx context.Context, sessionID, tabID string, data []byte) {
	if len(data) == 0 {
		return
	}
	w.enqueue(ctx, transcriptItem{
		kind: transcriptItemChunk, sessionID: sessionID, tabID: tabID,
		ts: time.Now().UnixMilli(), data: bytes.Clone(data),
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
	ticket := w.nextTicket
	w.nextTicket++
	if item.kind != transcriptItemChunk {
		if !w.closed {
			w.lifecycle[ticket] = item
			w.advanceServingLocked()
		}
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
	if w.closed {
		w.queueMu.Unlock()
		return
	}
	item.ticket = ticket
	w.queue = append(w.queue, item)
	w.queuedBytes += len(item.data)
	w.serving++
	w.advanceServingLocked()
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
		if w.closed && len(w.queue) == 0 && len(w.lifecycle) == 0 {
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
			if len(w.queue) > 0 && (w.queue[0].ticket == ticket || w.closed) {
				item := w.queue[0]
				w.queue = w.queue[1:]
				w.queuedBytes -= len(item.data)
				close(w.spaceCh)
				w.spaceCh = make(chan struct{})
				if item.ticket >= w.process {
					w.process = item.ticket + 1
				}
				batch = append(batch, item)
				batchBytes += len(item.data)
				if batchBytes >= transcriptBatchMaxBytes {
					break
				}
				continue
			}
			if w.closed {
				w.process = w.nextTicket
				break
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
			period.pending = append(period.pending, store.TranscriptChunkRow{
				Seq: period.nextSeq, TabID: item.tabID, TS: item.ts, Data: item.data,
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
			if err := w.database.TranscriptAppendChunks(context.Background(), period.transcriptID, period.pending); err != nil {
				w.logger.Error("transcript chunk flush failed", "transcriptId", period.transcriptID, "error", err)
			}
			period.pending = nil
		}
	}
}

func (w *transcriptWriter) flushPeriod(period *transcriptPeriod, endedAt int64) {
	if len(period.pending) > 0 {
		if err := w.database.TranscriptAppendChunks(context.Background(), period.transcriptID, period.pending); err != nil {
			w.logger.Error("transcript chunk flush failed", "transcriptId", period.transcriptID, "error", err)
		}
		period.pending = nil
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
	policy := store.TranscriptRetentionPolicy{
		MaxAge:   transcriptDefaultRetentionMaxAge,
		MaxCount: transcriptDefaultRetentionMaxCount,
	}
	raw, found, err := w.database.SettingGet(ctx, transcriptRetentionSettingKey)
	if err != nil {
		return policy, err
	}
	if !found || strings.TrimSpace(raw) == "" {
		return policy, nil
	}
	var settings struct {
		MaxAgeMS int64 `json:"maxAgeMs"`
		MaxCount int64 `json:"maxCount"`
	}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return policy, fmt.Errorf("decode %s: %w", transcriptRetentionSettingKey, err)
	}
	if settings.MaxAgeMS < 0 || settings.MaxCount < 0 {
		return policy, fmt.Errorf("%s must not be negative", transcriptRetentionSettingKey)
	}
	if settings.MaxAgeMS > math.MaxInt64/int64(time.Millisecond) {
		return policy, fmt.Errorf("%s maxAgeMs is too large", transcriptRetentionSettingKey)
	}
	if settings.MaxAgeMS > 0 {
		policy.MaxAge = time.Duration(settings.MaxAgeMS) * time.Millisecond
	}
	if settings.MaxCount > 0 {
		policy.MaxCount = settings.MaxCount
	}
	return policy, nil
}
