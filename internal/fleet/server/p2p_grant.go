package fleetserver

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/p2p/grant"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"github.com/coder/websocket"
)

const (
	// p2pGrantGrace mirrors the agent's grace for streams opened under a grant that just expired; a renewal
	// inside it keeps the grant id, so it is neither audited again nor mistaken for a new session.
	p2pGrantGrace = 60 * time.Second
	// p2pRenewAfter is when clients should renew a grant.
	p2pRenewAfter = 2 * time.Minute

	auditActionP2POpen = "p2p_open"

	p2pRevokeSession  = "session_revoked"
	p2pRevokeTerminal = "terminal_disabled"
	p2pRevokeDevice   = "device_revoked"
	p2pRevokeOwner    = "owner_unavailable"
	p2pRevokeKey      = "device_key_changed"
)

// p2pScopePurposes are the bridge purposes a grant may cover, in a stable order.
var p2pScopePurposes = []string{
	agent.PurposeTerminal, deviceproto.PurposeFile, deviceproto.PurposeFileMux,
	deviceproto.PurposeForward, deviceproto.PurposeListen, deviceproto.PurposeAccept,
}

// activeGrant is one direct session the server granted: the same login session, device and client key keep one
// grant id across renewals.
type activeGrant struct {
	id            string
	userID        string
	sessionID     string
	loginDeviceID string
	deviceID      string
	clientPub     string
	expiresAt     int64
}

type grantKey struct {
	sessionID string
	deviceID  string
	clientPub string
}

// p2pGrants is this instance's table of live grants, consulted to keep grant ids stable across renewals and to
// send p2p_revoke when the basis of a grant ends.
type p2pGrants struct {
	mu     sync.Mutex
	byID   map[string]*activeGrant
	byKey  map[grantKey]*activeGrant
	pushes sync.WaitGroup
}

func newP2PGrants() *p2pGrants {
	return &p2pGrants{byID: make(map[string]*activeGrant), byKey: make(map[grantKey]*activeGrant)}
}

// upsert returns the live grant for key, creating one when none is live; created reports a new session.
func (g *p2pGrants) upsert(now int64, key grantKey, identity *account.Identity, expiresAt int64) (*activeGrant, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneLocked(now)
	if entry := g.byKey[key]; entry != nil {
		entry.expiresAt = expiresAt
		return entry, false
	}
	entry := &activeGrant{
		id: ids.New(), userID: identity.UserID, sessionID: identity.SessionID, loginDeviceID: identity.DeviceID,
		deviceID: key.deviceID, clientPub: key.clientPub, expiresAt: expiresAt,
	}
	g.byID[entry.id] = entry
	g.byKey[key] = entry
	return entry, true
}

func (g *p2pGrants) pruneLocked(now int64) {
	for id, entry := range g.byID {
		if now >= entry.expiresAt+p2pGrantGrace.Milliseconds() {
			delete(g.byID, id)
			delete(g.byKey, grantKey{sessionID: entry.sessionID, deviceID: entry.deviceID, clientPub: entry.clientPub})
		}
	}
}

// take removes and returns the grants match selects.
func (g *p2pGrants) take(match func(*activeGrant) bool) []*activeGrant {
	g.mu.Lock()
	defer g.mu.Unlock()
	var taken []*activeGrant
	for id, entry := range g.byID {
		if match(entry) {
			taken = append(taken, entry)
			delete(g.byID, id)
			delete(g.byKey, grantKey{sessionID: entry.sessionID, deviceID: entry.deviceID, clientPub: entry.clientPub})
		}
	}
	return taken
}

// revokeP2PGrants drops the matching grants and tells their agents to close the direct sessions. push=false only
// drops them, for a device whose control channel is being torn down anyway. Frames go out asynchronously so a
// logout never waits on a slow agent.
func (s *Service) revokeP2PGrants(match func(*activeGrant) bool, reason string, push bool) {
	taken := s.p2p.grants.take(match)
	if !push {
		return
	}
	for _, entry := range taken {
		s.p2p.grants.pushes.Add(1)
		go func() {
			defer s.p2p.grants.pushes.Done()
			s.registry.pushP2PRevoke(entry.deviceID, entry.id, reason)
		}()
	}
}

// revokeP2PGrantsNow drops the matching grants and sends their p2p_revoke frames before it returns, for a caller
// that kicks the control channel next: an asynchronous push would find the channel already gone. The frames go
// out in parallel, each bounded by pushP2PRevoke's write timeout.
func (s *Service) revokeP2PGrantsNow(match func(*activeGrant) bool, reason string) {
	var pushes sync.WaitGroup
	for _, entry := range s.p2p.grants.take(match) {
		pushes.Go(func() { s.registry.pushP2PRevoke(entry.deviceID, entry.id, reason) })
	}
	pushes.Wait()
}

// pushP2PRevoke sends p2p_revoke to the device control channel, only when the agent advertised CapabilityP2P:
// an agent that predates the frame treats unknown control messages as fatal and would drop its channel.
func (r *Registry) pushP2PRevoke(deviceID, grantID, reason string) bool {
	r.mu.Lock()
	channel := r.controls[deviceID]
	r.mu.Unlock()
	if channel == nil || !slices.Contains(channel.capabilities, agent.CapabilityP2P) {
		return false
	}
	payload, err := json.Marshal(struct {
		Type    string `json:"type"`
		GrantID string `json:"grant_id"`
		Reason  string `json:"reason,omitempty"`
	}{Type: "p2p_revoke", GrantID: grantID, Reason: reason})
	if err != nil {
		return false
	}
	writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return channel.conn.Write(writeCtx, websocket.MessageText, payload) == nil
}

// p2pGrantView is the POST /fleet/devices/{id}/p2p/grant response.
type p2pGrantView struct {
	Grant        string    `json:"grant"`
	GrantID      string    `json:"grant_id"`
	KID          string    `json:"kid"`
	DeviceID     string    `json:"device_id"`
	AgentPub     grant.Key `json:"agent_pub"`
	Scope        []string  `json:"scope"`
	IssuedAt     int64     `json:"issued_at"`
	ExpiresAt    int64     `json:"expires_at"`
	RenewAfterMS int64     `json:"renew_after_ms"`
}

type p2pGrantDenied struct {
	reason string
	err    error
}

// p2pAuditedDenials are the denials written to the audit log: like the relay, the ownership decisions, plus a
// device key awaiting confirmation. Switch, presence and capability refusals are not, because clients retry
// them in the background and would fill the log.
var p2pAuditedDenials = map[string]bool{"not_found": true, "device_revoked": true, "not_owner": true, "device_key_pending": true}

// IssueP2PGrant runs the relay checks (owner, revocation, terminal switch, online, capability) plus the P2P
// policy (server switch, per-device relay choice, trusted device key) and signs a grant for clientPub. The first
// grant of a session is audited as p2p_open; renewals are not. token is the login session cookie, rechecked after
// the grant is registered so a logout that races the request either fails it or reaches the registered grant.
func (s *Service) IssueP2PGrant(ctx context.Context, identity *account.Identity, deviceID string, clientPub grant.Key, token string) (*p2pGrantView, error) {
	view, denied := s.issueP2PGrant(ctx, identity, deviceID, clientPub, token)
	if denied != nil {
		if !p2pAuditedDenials[denied.reason] {
			return nil, denied.err
		}
		_ = s.audit(ctx, auditKindDeviceTerminal, auditPayload{
			Action: auditActionP2POpen, DeviceID: deviceID, Requester: identity.UserID, Outcome: "deny", Reason: denied.reason,
		})
		return nil, denied.err
	}
	return view, nil
}

func (s *Service) issueP2PGrant(ctx context.Context, identity *account.Identity, deviceID string, clientPub grant.Key, token string) (*p2pGrantView, *p2pGrantDenied) {
	if !s.P2PEnabled() {
		return nil, &p2pGrantDenied{"p2p_disabled", ipc.NewError(ipc.CodeForbidden, "服务器已关闭直连")}
	}
	device, err := s.loadGrant(ctx, deviceID)
	switch {
	case err != nil:
		return nil, &p2pGrantDenied{"not_found", err}
	case device.revoked:
		return nil, &p2pGrantDenied{"device_revoked", ipc.NewError(ipc.CodeForbidden, "设备已撤销")}
	case device.userID != identity.UserID:
		// Only the owner's own sessions connect directly; host shares and other accounts always use the relay.
		return nil, &p2pGrantDenied{"not_owner", ipc.NewError(ipc.CodeForbidden, "设备不属于该用户")}
	}
	desired, err := s.AgentDesiredConfig(ctx, deviceID)
	if err != nil {
		return nil, &p2pGrantDenied{"not_found", err}
	}
	if !desired.TerminalEnabled {
		return nil, &p2pGrantDenied{"terminal_disabled", ipc.NewError(ipc.CodeForbidden, "设备已关闭远程访问")}
	}
	s.p2p.mu.Lock()
	forceRelay := s.p2p.forceRelay
	s.p2p.mu.Unlock()
	if forceRelay != nil {
		relay, err := forceRelay(ctx, identity.UserID, deviceID)
		if err != nil {
			return nil, &p2pGrantDenied{"db_error", err}
		}
		if relay {
			return nil, &p2pGrantDenied{"force_relay", ipc.NewError(ipc.CodeForbidden, "该设备已设置为始终使用中转")}
		}
	}
	if !s.registry.AgentOnline(deviceID) {
		return nil, &p2pGrantDenied{"offline", ipc.NewError(ipc.CodeDisconnected, errDeviceOffline.Error())}
	}
	capabilities, _ := s.registry.Capabilities(deviceID)
	if !slices.Contains(capabilities, agent.CapabilityP2P) {
		return nil, &p2pGrantDenied{"capability_missing", ipc.NewError(ipc.CodeForbidden, purposeUnsupportedMessage(agent.PurposeP2PSignal))}
	}
	key, err := s.deviceP2PKey(ctx, deviceID)
	if err != nil {
		return nil, &p2pGrantDenied{"db_error", err}
	}
	switch {
	case key == nil || key.PublicKey == "":
		return nil, &p2pGrantDenied{"device_key_missing", ipc.NewError(ipc.CodeForbidden, "设备尚未登记直连密钥")}
	case key.PendingKey != "":
		return nil, &p2pGrantDenied{"device_key_pending", ipc.NewError(ipc.CodeForbidden, "设备的直连密钥已变化，需要设备所有者确认后才能直连")}
	}
	agentPub, err := grant.ParseKey(key.PublicKey)
	if err != nil {
		return nil, &p2pGrantDenied{"device_key_invalid", ipc.NewError(ipc.CodeForbidden, "设备的直连密钥无效")}
	}
	scope := make([]string, 0, len(p2pScopePurposes))
	for _, purpose := range p2pScopePurposes {
		if slices.Contains(capabilities, purpose) {
			scope = append(scope, purpose)
		}
	}
	signing, err := s.p2p.keys.signing(ctx, s)
	if err != nil {
		return nil, &p2pGrantDenied{"db_error", err}
	}
	now := s.now()
	expiresAt := now + grant.DefaultTTL.Milliseconds()
	entry, created := s.p2p.grants.upsert(now, grantKey{sessionID: identity.SessionID, deviceID: deviceID, clientPub: clientPub.String()}, identity, expiresAt)
	if s.bridges.sessionInvalid(ctx, identity, token) {
		s.p2p.grants.take(func(candidate *activeGrant) bool { return candidate == entry })
		return nil, &p2pGrantDenied{"session_revoked", errP2PSessionRevoked}
	}
	signed, err := grant.Issue(signing, grant.Claims{
		GrantID: entry.id, DeviceID: deviceID, OwnerID: identity.UserID,
		ClientPub: clientPub, AgentPub: agentPub, Scope: scope,
		IssuedAt: now, ExpiresAt: expiresAt,
	})
	if err != nil {
		return nil, &p2pGrantDenied{"sign_failed", ipc.WrapError(ipc.CodeCrypto, "加密错误: 直连授权签名失败", err)}
	}
	if created {
		if err := s.auditP2P(ctx, auditKindDeviceTerminal, deviceID, p2pOpenAudit{
			Action: auditActionP2POpen, DeviceID: deviceID, Requester: identity.UserID, Outcome: "allow",
			GrantID: entry.id, Scope: scope,
		}); err != nil {
			s.p2p.grants.take(func(candidate *activeGrant) bool { return candidate == entry })
			return nil, &p2pGrantDenied{"db_error", err}
		}
	}
	return &p2pGrantView{
		Grant: signed, GrantID: entry.id, KID: signing.KID, DeviceID: deviceID, AgentPub: agentPub,
		Scope: scope, IssuedAt: now, ExpiresAt: expiresAt, RenewAfterMS: p2pRenewAfter.Milliseconds(),
	}, nil
}

// p2pOpenAudit is the p2p_open payload: the relay audit fields plus the grant id and scope.
type p2pOpenAudit struct {
	Action    string   `json:"action"`
	DeviceID  string   `json:"device_id"`
	Requester string   `json:"requester"`
	Outcome   string   `json:"outcome"`
	Reason    string   `json:"reason,omitempty"`
	GrantID   string   `json:"grant_id"`
	Scope     []string `json:"scope"`
}

// auditP2P writes a fleet audit row with an arbitrary payload for deviceID, under the owner set in ctx.
func (s *Service) auditP2P(ctx context.Context, kind, deviceID string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "数据编码失败: "+err.Error(), err)
	}
	row := store.AuditRow{TS: s.now(), Source: auditSourceFleet, Kind: kind, PayloadJSON: string(encoded), OwnerID: ipc.OwnerID(ctx), AssetID: &deviceID}
	if err := query.Use(s.db).AuditRow.WithContext(ctx).Create(&row); err != nil {
		return dbError(err)
	}
	return nil
}
