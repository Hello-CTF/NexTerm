package sharing

import (
	"context"
	stdsync "sync"
	"time"
)

const (
	// linkInputAuditWindow is the aggregation window for input audit: same-kind events in one window are only counted,
	// so anonymous read_write connections cannot flood audit_log block by block.
	linkInputAuditWindow = time.Minute
)

// Grant is a snapshot of one share authorization: after server-side verification it is handed to the connection path and the agent-side
// Gate, binding the share ID, device, session, permission, and expiry. OwnerID is the device owner
// (distinct from the grantor owner_id of the share row); anonymous preview grants have an empty Recipient.
type Grant struct {
	ShareIDs   []string   `json:"share_ids"`
	DeviceID   string     `json:"device_id"`
	SessionID  string     `json:"session_id,omitempty"`
	OwnerID    string     `json:"owner_id"`
	Recipient  string     `json:"recipient,omitempty"`
	Permission Permission `json:"permission"`
	ExpiresAt  int64      `json:"expires_at"`
}

// auditInputAggregated and its helpers are shared by the preview-link and host-share input paths.
type linkInputAuditPayload struct {
	auditPayload
	Suppressed int64 `json:"suppressed,omitempty"`
}

type linkInputAuditState struct {
	startedAt  int64
	payload    auditPayload
	suppressed int64
}

var linkInputAudits = struct {
	mu      stdsync.Mutex
	windows map[string]*linkInputAuditState
}{windows: map[string]*linkInputAuditState{}}

// auditInputAggregated aggregates input audits by share kind + session/device + time window: within a window,
// same-kind events are only counted without writing rows; after the window expires, same-kind events fold the count into one row; a kind change (e.g.
// allow -> deny) first folds the old window count into a row, then immediately writes one row for the new event, so denials are never delayed.
// Device links have no session ID and aggregate by device (anonymous read_write connections cannot flood audit_log block by block).
func (s *Service) auditInputAggregated(ctx context.Context, kind string, payload auditPayload) error {
	key := payload.SessionID
	if key == "" {
		key = payload.DeviceID
	}
	if key == "" {
		return s.auditInput(ctx, kind, linkInputAuditPayload{auditPayload: payload})
	}
	key = kind + "|" + key
	now := s.now()
	linkInputAudits.mu.Lock()
	state := linkInputAudits.windows[key]
	switch {
	case state == nil:
		if len(linkInputAudits.windows) > 1024 {
			for existing, entry := range linkInputAudits.windows {
				if now-entry.startedAt >= linkInputAuditWindow.Milliseconds() {
					delete(linkInputAudits.windows, existing)
				}
			}
		}
		linkInputAudits.windows[key] = &linkInputAuditState{startedAt: now, payload: payload}
		linkInputAudits.mu.Unlock()
		return s.auditInput(ctx, kind, linkInputAuditPayload{auditPayload: payload})
	case now-state.startedAt < linkInputAuditWindow.Milliseconds() && state.payload.Outcome == payload.Outcome && state.payload.Reason == payload.Reason:
		state.suppressed++
		linkInputAudits.mu.Unlock()
		return nil
	case state.payload.Outcome == payload.Outcome && state.payload.Reason == payload.Reason:
		count := state.suppressed + 1
		state.startedAt, state.suppressed = now, 0
		linkInputAudits.mu.Unlock()
		return s.auditInput(ctx, kind, linkInputAuditPayload{auditPayload: payload, Suppressed: count})
	default:
		flushed := state.payload
		count := state.suppressed
		state.startedAt, state.payload, state.suppressed = now, payload, 0
		linkInputAudits.mu.Unlock()
		if count > 0 {
			if err := s.auditInput(ctx, kind, linkInputAuditPayload{auditPayload: flushed, Suppressed: count}); err != nil {
				return err
			}
		}
		return s.auditInput(ctx, kind, linkInputAuditPayload{auditPayload: payload})
	}
}

func (s *Service) auditInput(ctx context.Context, kind string, payload linkInputAuditPayload) error {
	return s.writeAudit(ctx, kind, payload)
}
