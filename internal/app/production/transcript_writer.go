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
	sessionID string
	tabID     string
	ts        int64
	data      []byte
	info      session.TranscriptInfo
}

type transcriptSessionState struct {
	transcriptID string
	nextSeq      int64
	bytes        int64
	truncated    bool
}

type transcriptEnd struct {
	sessionID string
	state     *transcriptSessionState
	ts        int64
}

type transcriptWriter struct {
	database          *store.Store
	logger            *slog.Logger
	maxSessionBytes   int64
	queueMaxBytes     int64
	retentionInterval time.Duration

	mu          sync.Mutex
	cond        *sync.Cond
	queue       []transcriptItem
	queuedBytes int
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
	writer := &transcriptWriter{
		database:          config.Database,
		logger:            logger,
		maxSessionBytes:   config.MaxSessionBytes,
		queueMaxBytes:     config.QueueMaxBytes,
		retentionInterval: config.RetentionInterval,
		signalCh:          make(chan struct{}, 1),
	}
	writer.cond = sync.NewCond(&writer.mu)
	return writer
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
	w.mu.Lock()
	w.closed = true
	w.cond.Broadcast()
	w.mu.Unlock()
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

func (w *transcriptWriter) SessionStarted(_ context.Context, info session.TranscriptInfo) {
	w.enqueue(transcriptItem{kind: transcriptItemStart, sessionID: info.SessionID, ts: time.Now().UnixMilli(), info: info})
}

func (w *transcriptWriter) SessionOutput(sessionID, tabID string, data []byte) {
	if len(data) == 0 {
		return
	}
	w.enqueue(transcriptItem{
		kind: transcriptItemChunk, sessionID: sessionID, tabID: tabID,
		ts: time.Now().UnixMilli(), data: bytes.Clone(data),
	})
}

func (w *transcriptWriter) SessionEnded(sessionID string) {
	w.enqueue(transcriptItem{kind: transcriptItemEnd, sessionID: sessionID, ts: time.Now().UnixMilli()})
}

func (w *transcriptWriter) enqueue(item transcriptItem) {
	w.mu.Lock()
	for !w.closed && int64(w.queuedBytes)+int64(len(item.data)) > w.queueMaxBytes {
		w.cond.Wait()
	}
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.queue = append(w.queue, item)
	w.queuedBytes += len(item.data)
	w.mu.Unlock()
	w.signal()
}

func (w *transcriptWriter) signal() {
	select {
	case w.signalCh <- struct{}{}:
	default:
	}
}

func (w *transcriptWriter) run() {
	sessions := make(map[string]*transcriptSessionState)
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
		w.mu.Lock()
		if len(w.queue) > 0 {
			batch := w.queue
			if len(batch) > transcriptBatchMaxItems {
				batch = batch[:transcriptBatchMaxItems]
			}
			batchBytes := 0
			for index, item := range batch {
				if index > 0 && batchBytes+len(item.data) > transcriptBatchMaxBytes {
					batch = batch[:index]
					break
				}
				batchBytes += len(item.data)
			}
			w.queue = w.queue[len(batch):]
			for _, item := range batch {
				w.queuedBytes -= len(item.data)
			}
			w.cond.Broadcast()
			w.mu.Unlock()
			return batch, true
		}
		closed := w.closed
		w.mu.Unlock()
		if closed {
			return nil, false
		}
		<-w.signalCh
	}
}

func (w *transcriptWriter) flushBatch(sessions map[string]*transcriptSessionState, batch []transcriptItem) {
	pending := make(map[string][]store.TranscriptChunkRow)
	order := make([]string, 0, 4)
	ended := make([]transcriptEnd, 0, 2)
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
			sessions[item.sessionID] = &transcriptSessionState{transcriptID: transcriptID}
		case transcriptItemChunk:
			state := sessions[item.sessionID]
			if state == nil || state.truncated {
				continue
			}
			if state.bytes+int64(len(item.data)) > w.maxSessionBytes {
				marker := store.TranscriptChunkRow{
					Seq: state.nextSeq, TabID: item.tabID, TS: item.ts,
					Data: transcriptTruncationMarker,
				}
				state.nextSeq++
				state.bytes += int64(len(transcriptTruncationMarker))
				state.truncated = true
				if _, ok := pending[item.sessionID]; !ok {
					order = append(order, item.sessionID)
				}
				pending[item.sessionID] = append(pending[item.sessionID], marker)
				continue
			}
			if _, ok := pending[item.sessionID]; !ok {
				order = append(order, item.sessionID)
			}
			pending[item.sessionID] = append(pending[item.sessionID], store.TranscriptChunkRow{
				Seq: state.nextSeq, TabID: item.tabID, TS: item.ts, Data: item.data,
			})
			state.nextSeq++
			state.bytes += int64(len(item.data))
		case transcriptItemEnd:
			state := sessions[item.sessionID]
			if state == nil {
				continue
			}
			ended = append(ended, transcriptEnd{sessionID: item.sessionID, state: state, ts: item.ts})
			delete(sessions, item.sessionID)
		}
	}
	for _, sessionID := range order {
		state := sessions[sessionID]
		if state == nil {
			continue
		}
		if err := w.database.TranscriptAppendChunks(context.Background(), state.transcriptID, pending[sessionID]); err != nil {
			w.logger.Error("transcript chunk flush failed", "transcriptId", state.transcriptID, "error", err)
		}
	}
	for _, end := range ended {
		if chunks := pending[end.sessionID]; len(chunks) > 0 {
			if err := w.database.TranscriptAppendChunks(context.Background(), end.state.transcriptID, chunks); err != nil {
				w.logger.Error("transcript chunk flush failed", "transcriptId", end.state.transcriptID, "error", err)
			}
		}
		if err := w.database.TranscriptEnd(context.Background(), end.state.transcriptID, end.ts, end.state.truncated); err != nil {
			w.logger.Error("transcript end failed", "transcriptId", end.state.transcriptID, "error", err)
		}
	}
}

func (w *transcriptWriter) endRemainingSessions(sessions map[string]*transcriptSessionState) {
	now := time.Now().UnixMilli()
	for sessionID, state := range sessions {
		if err := w.database.TranscriptEnd(context.Background(), state.transcriptID, now, state.truncated); err != nil {
			w.logger.Error("transcript end failed during shutdown", "transcriptId", state.transcriptID, "error", err)
		}
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
