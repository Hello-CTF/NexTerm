package fleetserver

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/p2p/grant"
)

const (
	maxP2PAuditGrants  = 16
	maxP2PAuditRecords = 100
	maxP2PAuditReason  = 128
	maxP2PAuditBytes   = int64(1) << 50
	// p2pAuditWindow is how late an agent may report a stream, counted from the grant's expiry.
	p2pAuditWindow = 24 * time.Hour
	// p2pAuditFuture tolerates agent clocks slightly ahead of the server.
	p2pAuditFuture = 5 * time.Minute

	// One upload per device every 6 s on average, bursts of 10: an agent batches its stream records.
	p2pAuditRate  = 1.0 / 6
	p2pAuditBurst = 10

	pathDirect = "direct"
)

// p2pStreamAudit is the stored form of an agent-reported direct stream: the relay audit fields
// (action/device_id/requester/outcome/reason) plus path=direct and the stream details.
type p2pStreamAudit struct {
	Action    string `json:"action"`
	DeviceID  string `json:"device_id"`
	Requester string `json:"requester"`
	Outcome   string `json:"outcome"`
	Reason    string `json:"reason,omitempty"`
	Path      string `json:"path"`
	GrantID   string `json:"grant_id"`
	Purpose   string `json:"purpose"`
	StartedAt int64  `json:"started_at"`
	EndedAt   int64  `json:"ended_at"`
	BytesIn   int64  `json:"bytes_in"`
	BytesOut  int64  `json:"bytes_out"`
}

// p2pAuditAction maps a purpose to the relay's audit action, so direct and relayed streams share one vocabulary.
func p2pAuditAction(purpose string) (string, bool) {
	switch purpose {
	case "", agent.PurposeTerminal:
		return "terminal_open", true
	case deviceproto.PurposeFile, deviceproto.PurposeFileMux:
		return "file_open", true
	case deviceproto.PurposeForward, deviceproto.PurposeListen, deviceproto.PurposeAccept:
		return "forward_open", true
	}
	return "", false
}

// RecordP2PAudit stores the agent's after-the-fact records of direct streams. Each record must name a grant the
// request carries, signed by this server for this device and owner; records are attributed to the device owner.
// Rejected records are reported back, never dropped silently.
func (s *Service) RecordP2PAudit(ctx context.Context, deviceID, ownerID string, request agent.P2PAuditRequest) (agent.P2PAuditResponse, error) {
	if len(request.Grants) > maxP2PAuditGrants || len(request.Records) > maxP2PAuditRecords {
		return agent.P2PAuditResponse{}, ipc.BadParam(fmt.Errorf("一次最多上报 %d 个授权和 %d 条记录", maxP2PAuditGrants, maxP2PAuditRecords))
	}
	keys, err := s.p2p.keys.all(ctx, s)
	if err != nil {
		return agent.P2PAuditResponse{}, err
	}
	now := s.now()
	grants := make(map[string]*grant.Claims, len(request.Grants))
	for _, token := range request.Grants {
		claims, err := grant.Parse(token, keys)
		if err != nil || claims.DeviceID != deviceID || claims.OwnerID != ownerID || now-claims.ExpiresAt > p2pAuditWindow.Milliseconds() {
			continue
		}
		grants[claims.GrantID] = claims
	}
	ctx = ipc.WithUserID(ctx, ownerID)
	response := agent.P2PAuditResponse{Rejected: []agent.P2PAuditRejection{}}
	for index, record := range request.Records {
		payload, reason := validateP2PAuditRecord(record, grants, now)
		if reason != "" {
			response.Rejected = append(response.Rejected, agent.P2PAuditRejection{Index: index, Reason: reason})
			continue
		}
		payload.DeviceID, payload.Requester = deviceID, ownerID
		if err := s.auditP2P(ctx, auditKindDeviceTerminal, deviceID, payload); err != nil {
			return response, err
		}
		response.Accepted++
	}
	return response, nil
}

func validateP2PAuditRecord(record agent.P2PAuditRecord, grants map[string]*grant.Claims, now int64) (p2pStreamAudit, string) {
	claims := grants[record.GrantID]
	if claims == nil {
		return p2pStreamAudit{}, "unknown_grant"
	}
	action, ok := p2pAuditAction(record.Purpose)
	if !ok {
		return p2pStreamAudit{}, "unknown_purpose"
	}
	purpose := record.Purpose
	if purpose == "" {
		purpose = agent.PurposeTerminal
	}
	outcome := record.Outcome
	if outcome == "" {
		outcome = "allow"
	}
	grace := p2pGrantGrace.Milliseconds()
	skew := grant.ClockSkew.Milliseconds()
	switch {
	case outcome != "allow" && outcome != "deny":
		return p2pStreamAudit{}, "bad_outcome"
	case outcome == "allow" && !slices.Contains(claims.Scope, purpose):
		return p2pStreamAudit{}, "out_of_scope"
	case len(record.Reason) > maxP2PAuditReason:
		return p2pStreamAudit{}, "reason_too_long"
	case record.StartedAt <= 0 || record.EndedAt < record.StartedAt:
		return p2pStreamAudit{}, "bad_time"
	case record.StartedAt < claims.IssuedAt-skew || record.StartedAt > claims.ExpiresAt+grace:
		return p2pStreamAudit{}, "outside_grant"
	case record.EndedAt > now+p2pAuditFuture.Milliseconds():
		return p2pStreamAudit{}, "bad_time"
	case record.BytesIn < 0 || record.BytesOut < 0 || record.BytesIn > maxP2PAuditBytes || record.BytesOut > maxP2PAuditBytes:
		return p2pStreamAudit{}, "bad_bytes"
	}
	return p2pStreamAudit{
		Action: action, Outcome: outcome, Reason: record.Reason, Path: pathDirect,
		GrantID: record.GrantID, Purpose: purpose, StartedAt: record.StartedAt, EndedAt: record.EndedAt,
		BytesIn: record.BytesIn, BytesOut: record.BytesOut,
	}, ""
}

// keyedLimiter is a token bucket per key on the service clock (milliseconds).
type keyedLimiter struct {
	rate  float64
	burst float64

	mu      sync.Mutex
	buckets map[string]*keyedBucket
}

type keyedBucket struct {
	tokens float64
	last   int64
}

func newKeyedLimiter(rate float64, burst int) *keyedLimiter {
	return &keyedLimiter{rate: rate, burst: float64(burst), buckets: make(map[string]*keyedBucket)}
}

func (l *keyedLimiter) allow(key string, now int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.buckets[key]
	if entry == nil {
		if len(l.buckets) >= 4096 {
			for candidate, bucket := range l.buckets {
				if bucket.tokens+float64(now-bucket.last)/1000*l.rate >= l.burst {
					delete(l.buckets, candidate)
				}
			}
		}
		entry = &keyedBucket{tokens: l.burst, last: now}
		l.buckets[key] = entry
	}
	if elapsed := now - entry.last; elapsed > 0 {
		entry.tokens = min(l.burst, entry.tokens+float64(elapsed)/1000*l.rate)
		entry.last = now
	}
	if entry.tokens < 1 {
		return false
	}
	entry.tokens--
	return true
}
