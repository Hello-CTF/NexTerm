package session

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const commandLogQueueSize = 256

type commandEnvelope struct {
	ctx    context.Context
	record CommandRecord
}

type commandGapKey struct {
	sessionID string
	tabID     string
}

type commandGap struct {
	ctx    context.Context
	record CommandRecord
	count  int64
	first  time.Time
	last   time.Time
}

type asyncCommandSink struct {
	sink       CommandSink
	logger     *slog.Logger
	now        func() time.Time
	queue      chan commandEnvelope
	gapSignals chan struct{}
	closeCh    chan struct{}
	done       chan struct{}

	startOnce  sync.Once
	mu         sync.RWMutex
	closed     bool
	rejected   atomic.Uint64
	failures   atomic.Uint64
	degraded   atomic.Uint64
	gapRecords atomic.Uint64

	gapMu      sync.Mutex
	gapPending map[commandGapKey]*commandGap
	gapQueued  bool
}

func newAsyncCommandSink(sink CommandSink, logger *slog.Logger, queueSize int, now func() time.Time) *asyncCommandSink {
	if queueSize <= 0 {
		queueSize = commandLogQueueSize
	}
	if logger == nil {
		logger = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	return &asyncCommandSink{
		sink: sink, logger: logger, now: now, queue: make(chan commandEnvelope, queueSize),
		gapSignals: make(chan struct{}, 1), closeCh: make(chan struct{}), done: make(chan struct{}),
		gapPending: make(map[commandGapKey]*commandGap),
	}
}

func (s *asyncCommandSink) start() {
	s.startOnce.Do(func() {
		go s.run()
	})
}

func commandItem(ctx context.Context, record CommandRecord) commandEnvelope {
	if ctx == nil {
		ctx = context.Background()
	}
	if record.ExitCode != nil {
		code := *record.ExitCode
		record.ExitCode = &code
	}
	return commandEnvelope{ctx: context.WithoutCancel(ctx), record: record}
}

func (s *asyncCommandSink) reject(record CommandRecord) {
	rejected := s.rejected.Add(1)
	s.logger.Error("command log record rejected after shutdown", "session", record.SessionID, "tab", record.TabID, "rejected", rejected)
}

func (s *asyncCommandSink) RecordCommand(ctx context.Context, record CommandRecord) {
	item := commandItem(ctx, record)
	s.start()
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		s.reject(record)
		return
	}
	select {
	case s.queue <- item:
		s.mu.RUnlock()
		return
	default:
	}
	s.logger.Warn("command log queue full; blocking command source without dropping the record", "session", record.SessionID, "tab", record.TabID, "queue", len(s.queue))
	s.queue <- item
	s.mu.RUnlock()
}

func (s *asyncCommandSink) RecordTerminalCommand(ctx context.Context, record CommandRecord) {
	item := commandItem(ctx, record)
	s.start()
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		s.reject(record)
		return
	}
	select {
	case s.queue <- item:
		s.mu.RUnlock()
		return
	default:
	}
	report, degraded := s.queueGap(item)
	s.mu.RUnlock()
	if report {
		s.logger.Warn("command log queue saturated; terminal records will be represented by an audit gap", "session", record.SessionID, "tab", record.TabID, "degraded", degraded)
	}
}

func (s *asyncCommandSink) queueGap(item commandEnvelope) (bool, uint64) {
	at := item.record.FinishedAt
	if at.IsZero() {
		at = s.now()
	}
	key := commandGapKey{sessionID: item.record.SessionID, tabID: item.record.TabID}
	s.gapMu.Lock()
	gap := s.gapPending[key]
	if gap == nil {
		gap = &commandGap{ctx: item.ctx, record: item.record, first: at}
		s.gapPending[key] = gap
	}
	gap.count++
	gap.last = at
	report := false
	if !s.gapQueued {
		s.gapQueued = true
		s.gapSignals <- struct{}{}
		report = true
	}
	s.gapMu.Unlock()
	return report, s.degraded.Add(1)
}

func (s *asyncCommandSink) flushGaps() {
	s.gapMu.Lock()
	pending := s.gapPending
	s.gapPending = make(map[commandGapKey]*commandGap)
	s.gapQueued = false
	s.gapMu.Unlock()
	for _, gap := range pending {
		record := gap.record
		record.Command = fmt.Sprintf("[NexTerm] command log gap: omitted %d terminal command record(s) because the persistence queue was saturated", gap.count)
		record.Source = CommandSourceTerminalGap
		record.ExitCode = nil
		record.StartedAt = gap.first
		record.FinishedAt = gap.last
		s.gapRecords.Add(1)
		s.write(commandEnvelope{ctx: gap.ctx, record: record})
	}
}

func (s *asyncCommandSink) run() {
	defer close(s.done)
	for {
		select {
		case <-s.gapSignals:
			s.flushGaps()
		default:
		}
		select {
		case item := <-s.queue:
			s.write(item)
		case <-s.gapSignals:
			s.flushGaps()
		case <-s.closeCh:
			s.drain()
			return
		}
	}
}

func (s *asyncCommandSink) drain() {
	for {
		select {
		case item := <-s.queue:
			s.write(item)
			continue
		default:
		}
		select {
		case <-s.gapSignals:
			s.flushGaps()
			continue
		default:
		}
		s.gapMu.Lock()
		pending := len(s.gapPending) > 0
		s.gapMu.Unlock()
		if pending {
			s.flushGaps()
			continue
		}
		return
	}
}

func (s *asyncCommandSink) write(item commandEnvelope) {
	defer func() {
		if recovered := recover(); recovered != nil {
			failures := s.failures.Add(1)
			s.logger.Error("command log persistence panic; record was not confirmed", "session", item.record.SessionID, "tab", item.record.TabID, "failures", failures, "panic", recovered)
		}
	}()
	s.sink.RecordCommand(item.ctx, item.record)
}

func (s *asyncCommandSink) Close() {
	s.start()
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.closeCh)
	}
	s.mu.Unlock()
	<-s.done
}
