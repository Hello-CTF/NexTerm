package agentside

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/p2p/clock"
)

// Limits of POST /agent/p2p/audit (internal/fleet/server/p2p_audit.go): records and grants per request, and one
// request every six seconds on average.
const (
	AuditMaxRecords = 100
	AuditMaxGrants  = 16
	// AuditFlushInterval paces uploads, above the server's average rate.
	AuditFlushInterval = 10 * time.Second
	// AuditMaxPending bounds the records kept while the server is unreachable; older ones are dropped and logged.
	AuditMaxPending = 2000
	auditTimeout    = 15 * time.Second
)

type pendingRecord struct {
	id     uint64
	token  string
	record agent.P2PAuditRecord
}

// auditor batches direct stream records and uploads them. A request carries one grant token per grant id (the
// server keys grants by id), so records made under an older token of the same grant wait for the next batch.
type auditor struct {
	host     Host
	clock    clock.Clock
	interval time.Duration
	logger   *slog.Logger

	mu      sync.Mutex
	pending []pendingRecord
	nextID  uint64
	timer   clock.Timer
	ctx     context.Context
	stopped bool
	wg      sync.WaitGroup
	// flushing serializes uploads.
	flushing sync.Mutex
}

func newAuditor(host Host, clk clock.Clock, interval time.Duration, logger *slog.Logger) *auditor {
	return &auditor{host: host, clock: clk, interval: interval, logger: logger}
}

func (a *auditor) start(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ctx = ctx
	a.scheduleLocked()
}

func (a *auditor) scheduleLocked() {
	if a.stopped || a.ctx == nil || a.timer != nil {
		return
	}
	a.timer = a.clock.AfterFunc(a.interval, func() {
		a.mu.Lock()
		a.timer = nil
		a.mu.Unlock()
		a.spawnFlush()
	})
}

func (a *auditor) spawnFlush() {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	a.wg.Add(1)
	a.mu.Unlock()
	go func() {
		defer a.wg.Done()
		a.flush()
		a.mu.Lock()
		if len(a.pending) > 0 {
			a.scheduleLocked()
		}
		a.mu.Unlock()
	}()
}

func (a *auditor) add(token string, record agent.P2PAuditRecord) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped {
		return
	}
	if len(a.pending) >= AuditMaxPending {
		a.logger.Warn("direct stream audit records dropped: the server has not accepted them", "dropped", 1, "grant_id", a.pending[0].record.GrantID)
		a.pending = a.pending[1:]
	}
	a.nextID++
	a.pending = append(a.pending, pendingRecord{id: a.nextID, token: token, record: record})
	a.scheduleLocked()
}

// batch selects up to AuditMaxRecords pending records under at most AuditMaxGrants grant ids, one token each.
func (a *auditor) batch() ([]uint64, []string, []agent.P2PAuditRecord) {
	a.mu.Lock()
	defer a.mu.Unlock()
	tokens := make(map[string]string)
	var grants []string
	var indexes []uint64
	var records []agent.P2PAuditRecord
	for _, p := range a.pending {
		if len(records) == AuditMaxRecords {
			break
		}
		token, seen := tokens[p.record.GrantID]
		if seen && token != p.token {
			continue
		}
		if !seen {
			if len(grants) == AuditMaxGrants {
				continue
			}
			tokens[p.record.GrantID] = p.token
			grants = append(grants, p.token)
		}
		indexes = append(indexes, p.id)
		records = append(records, p.record)
	}
	return indexes, grants, records
}

func (a *auditor) flush() {
	a.flushing.Lock()
	defer a.flushing.Unlock()
	indexes, grants, records := a.batch()
	if len(records) == 0 {
		return
	}
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, auditTimeout)
	defer cancel()
	response, err := a.host.UploadP2PAudit(ctx, grants, records)
	if err != nil {
		// Kept for the next round; the server rate limit and outages both land here.
		a.logger.Debug("direct stream audit upload failed; retrying later", "records", len(records), "error", err)
		return
	}
	for _, rejected := range response.Rejected {
		if rejected.Index >= 0 && rejected.Index < len(records) {
			a.logger.Warn("server rejected a direct stream audit record", "grant_id", records[rejected.Index].GrantID, "purpose", records[rejected.Index].Purpose, "reason", rejected.Reason)
		}
	}
	a.mu.Lock()
	sent := make(map[uint64]bool, len(indexes))
	for _, i := range indexes {
		sent[i] = true
	}
	kept := a.pending[:0]
	for _, p := range a.pending {
		if !sent[p.id] {
			kept = append(kept, p)
		}
	}
	a.pending = kept
	a.mu.Unlock()
}

// stop uploads what is pending once, best effort, and stops the timer.
func (a *auditor) stop() {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	a.stopped = true
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
	ctx := a.ctx
	a.mu.Unlock()
	if ctx != nil {
		a.mu.Lock()
		a.ctx = context.WithoutCancel(ctx)
		a.mu.Unlock()
		a.flush()
	}
}

func (a *auditor) wait() { a.wg.Wait() }

// pendingCount reports the records not yet accepted (tests).
func (a *auditor) pendingCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.pending)
}
