package fleetserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/relaymux"
	"github.com/Hello-CTF/NexTerm/internal/version"
)

// relayService holds the multiplexed relay state: the settings, what the config endpoints advertise, the
// agents' live links by device and every live link for shutdown.
type relayService struct {
	mu        sync.Mutex
	settings  RelaySettings
	quicPorts []int
	pin       string
	agents    map[string]*relaymux.Link
	links     map[*relaymux.Link]struct{}
	muxes     []*relaymux.UDPMux
	closed    bool
	wg        sync.WaitGroup
}

func newRelayService() *relayService {
	return &relayService{agents: make(map[string]*relaymux.Link), links: make(map[*relaymux.Link]struct{})}
}

// track registers a live link for shutdown; it returns false once the service is closing.
func (r *relayService) track(link *relaymux.Link) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	r.links[link] = struct{}{}
	return true
}

func (r *relayService) untrack(link *relaymux.Link) {
	r.mu.Lock()
	delete(r.links, link)
	r.mu.Unlock()
}

// registerAgent makes link the device's relay link, closing the one it replaces, like a control channel.
func (r *relayService) registerAgent(deviceID string, link *relaymux.Link) {
	r.mu.Lock()
	previous := r.agents[deviceID]
	r.agents[deviceID] = link
	r.mu.Unlock()
	if previous != nil && previous != link {
		_ = previous.Close()
	}
}

func (r *relayService) unregisterAgent(deviceID string, link *relaymux.Link) {
	r.mu.Lock()
	if r.agents[deviceID] == link {
		delete(r.agents, deviceID)
	}
	r.mu.Unlock()
}

func (r *relayService) agent(deviceID string) *relaymux.Link {
	r.mu.Lock()
	defer r.mu.Unlock()
	link := r.agents[deviceID]
	if link == nil || link.Err() != nil {
		return nil
	}
	return link
}

// kickAgent ends the device's relay link with the revocation reason; every stream on it ends with it.
func (r *relayService) kickAgent(deviceID, reason string) {
	r.mu.Lock()
	link := r.agents[deviceID]
	delete(r.agents, deviceID)
	r.mu.Unlock()
	if link == nil {
		return
	}
	message := "设备已撤销"
	if reason == agent.ErrorCodeOwnerUnavailable {
		message = "设备所属账号暂不可用"
	}
	link.Goaway(relaymux.Reply{Status: http.StatusForbidden, Code: string(ipc.CodeForbidden), Reason: reason, Message: message})
}

// close ends every relay link and QUIC listener (service shutdown).
func (r *relayService) close() {
	r.mu.Lock()
	r.closed = true
	links := make([]*relaymux.Link, 0, len(r.links))
	for link := range r.links {
		links = append(links, link)
	}
	muxes := r.muxes
	r.muxes = nil
	r.agents = make(map[string]*relaymux.Link)
	r.mu.Unlock()
	for _, mux := range muxes {
		_ = mux.Close()
	}
	for _, link := range links {
		_ = link.Close()
	}
	r.wg.Wait()
}

// ServeRelayQUIC accepts QUIC relay sessions on mux until the service closes; the service takes ownership of
// mux. The STUN responder may serve mux.STUNConn() on the same socket.
func (s *Service) ServeRelayQUIC(mux *relaymux.UDPMux) {
	s.relay.mu.Lock()
	if s.relay.closed {
		s.relay.mu.Unlock()
		_ = mux.Close()
		return
	}
	s.relay.muxes = append(s.relay.muxes, mux)
	s.relay.wg.Add(1)
	s.relay.mu.Unlock()
	go func() {
		defer s.relay.wg.Done()
		for {
			session, err := mux.Accept(context.Background())
			if err != nil {
				return
			}
			go s.serveRelaySession(session)
		}
	}()
}

// serveRelayWS handles GET /relay/ws: the websocket carrier of the relay on the HTTPS port. The upgrade
// carries no credential; the session authenticates with its Hello.
func (s *Service) serveRelayWS(w http.ResponseWriter, r *http.Request) {
	session, err := relaymux.AcceptWS(w, r)
	if err != nil {
		return
	}
	s.serveRelaySession(session)
}

// serveRelaySession authenticates a session by its Hello and serves it until it ends.
func (s *Service) serveRelaySession(session relaymux.Session) {
	pending, err := relaymux.AcceptHandshake(context.Background(), session)
	if err != nil {
		return
	}
	if s.authOff {
		pending.Refuse(relaymux.Reply{Status: http.StatusForbidden, Code: string(ipc.CodeForbidden), Message: "当前为共享工作区模式（未启用账号登录），设备管理不可用"})
		return
	}
	if pending.Hello.Version != relaymux.Version {
		pending.Refuse(relaymux.Reply{Status: http.StatusUpgradeRequired, Code: "version_mismatch", Message: "中转协议版本不兼容", ServerVersion: version.Version})
		return
	}
	switch pending.Hello.Role {
	case relaymux.RoleClient:
		s.serveRelayClient(pending)
	case relaymux.RoleAgent:
		s.serveRelayAgent(pending)
	default:
		pending.Refuse(relaymux.Reply{Status: http.StatusBadRequest, Code: string(ipc.CodeBadParam), Message: "未知的中转会话角色"})
	}
}

// relayRefusal turns a fleet error into a refusal with the status writeFleetFailure would answer.
func relayRefusal(err error) relaymux.Reply {
	status, normalized := fleetFailureStatus(err)
	return relaymux.Reply{Status: status, Code: string(normalized.Code), Message: normalized.Message}
}

func relayReply(status int, code ipc.Code, message string) relaymux.Reply {
	return relaymux.Reply{Status: status, Code: string(code), Message: message}
}

// reloginReply is the refusal and goaway of a session whose login ended, the bridge relay's 401.
var reloginReply = relayReply(http.StatusUnauthorized, ipc.CodeForbidden, bridgeSessionReloginMessage)

// authenticateRelayClient applies the checks of the fleet session middleware (fleetGuard and
// requireFleetSession) to a Hello's token: a valid login session, no pending password reset, no pending
// enforced TOTP enrollment.
func (s *Service) authenticateRelayClient(ctx context.Context, token string) (*account.Identity, *relaymux.Reply) {
	if token == "" {
		return nil, &reloginReply
	}
	identity, err := s.accounts.ValidateSession(ctx, token)
	if err != nil {
		return nil, &reloginReply
	}
	if identity.State == account.StateResetRequired {
		refusal := relayReply(http.StatusForbidden, ipc.CodeForbidden, "必须先完成密码重置")
		return nil, &refusal
	}
	if !identity.MFAEnabled {
		required, err := s.accounts.MFARequired(ctx)
		if err != nil {
			refusal := relayRefusal(err)
			return nil, &refusal
		}
		if required {
			refusal := relayReply(http.StatusForbidden, ipc.CodeMFAEnrollmentRequired, "超级管理员已要求启用两步验证：完成 TOTP 绑定前，该账号只能使用绑定相关功能")
			return nil, &refusal
		}
	}
	return identity, nil
}

// serveRelayClient serves a client session: every stream is a device bridge request authorized like
// GET /fleet/devices/{id}/bridge. The session is registered under its login session, so a revocation or a
// failed periodic recheck ends it with a 401 goaway; every stream is registered too and checks the login
// session again before it opens.
func (s *Service) serveRelayClient(pending *relaymux.PendingLink) {
	token := pending.Hello.Token
	identity, refusal := s.authenticateRelayClient(context.Background(), token)
	if refusal != nil {
		pending.Refuse(*refusal)
		return
	}
	sessionCtx, release := s.bridges.track(context.Background(), identity, token)
	defer release()
	if s.bridges.sessionInvalid(sessionCtx, identity, token) || errors.Is(context.Cause(sessionCtx), errBridgeSessionRevoked) {
		pending.Refuse(reloginReply)
		return
	}
	link, err := pending.Accept(relaymux.Reply{ServerVersion: version.Version})
	if err != nil {
		return
	}
	if !s.relay.track(link) {
		_ = link.Close()
		return
	}
	defer s.relay.untrack(link)
	stop := context.AfterFunc(sessionCtx, func() {
		if errors.Is(context.Cause(sessionCtx), errBridgeSessionRevoked) {
			link.Goaway(reloginReply)
			return
		}
		_ = link.Close()
	})
	defer stop()
	var streams sync.WaitGroup
	defer streams.Wait()
	for {
		incoming, err := link.AcceptHeader(sessionCtx)
		if err != nil {
			return
		}
		streams.Add(1)
		go func() {
			defer streams.Done()
			s.serveRelayClientStream(sessionCtx, identity, token, incoming)
		}()
	}
}

// serveRelayClientStream authorizes one client stream like the bridge endpoint, opens the device side and
// pipes the two.
func (s *Service) serveRelayClientStream(ctx context.Context, identity *account.Identity, token string, incoming *relaymux.IncomingStream) {
	header := incoming.Header
	plan, refusal := s.checkRelayStream(ctx, identity, header)
	if refusal != nil {
		incoming.Refuse(*refusal)
		return
	}
	// As in the bridge relay: register under the login session before rechecking it, so a revocation landing
	// after the check reaches this stream.
	streamCtx, release := s.bridges.track(ctx, identity, token)
	defer release()
	if s.bridges.sessionInvalid(streamCtx, identity, token) || errors.Is(context.Cause(streamCtx), errBridgeSessionRevoked) {
		incoming.Refuse(reloginReply)
		return
	}
	requestID := header.RequestID
	if requestID == "" {
		requestID = ids.New()
	}
	device, refusal := s.openRelayDeviceStream(streamCtx, header.DeviceID, plan, requestID)
	if refusal != nil {
		if errors.Is(context.Cause(streamCtx), errBridgeSessionRevoked) {
			refusal = &reloginReply
		}
		incoming.Refuse(*refusal)
		return
	}
	var params map[string]string
	if plan.stream {
		params = map[string]string{relaymux.ParamStream: "1"}
	}
	client, err := incoming.Accept(params)
	if err != nil {
		_ = device.Close()
		return
	}
	pipeRelayStream(streamCtx, client, device)
}

// openRelayDeviceStream reaches the device: over the agent's relay link when it keeps one, otherwise through
// a per-operation bridge exactly as the bridge relay requests it.
func (s *Service) openRelayDeviceStream(ctx context.Context, deviceID string, plan relayStreamPlan, requestID string) (net.Conn, *relaymux.Reply) {
	if link := s.relay.agent(deviceID); link != nil {
		params := map[string]string{}
		if plan.stream {
			params[relaymux.ParamStream] = "1"
		}
		if plan.resume {
			params[relaymux.ParamResume] = "1"
		}
		conn, _, err := link.OpenDeviceStream(ctx, relaymux.StreamHeader{Purpose: plan.purpose, DeviceID: deviceID, Params: params, RequestID: requestID})
		if err == nil {
			return conn, nil
		}
		if reply, refused := relaymux.Refused(err); refused && link.Err() == nil {
			return nil, &reply
		}
		slog.Debug("relay agent stream failed", "device", deviceID, "request", requestID, "error", err)
		refusal := relayReply(http.StatusServiceUnavailable, ipc.CodeDisconnected, errDeviceOffline.Error())
		return nil, &refusal
	}
	bridge, err := s.registry.RequestStreamBridge(ctx, deviceID, plan.purpose, plan.stream)
	if err != nil {
		refusal := relayReply(http.StatusServiceUnavailable, ipc.CodeDisconnected, errDeviceOffline.Error())
		if errors.Is(err, errPurposeUnsupported) {
			refusal = relayReply(http.StatusForbidden, ipc.CodeForbidden, purposeUnsupportedMessage(plan.purpose))
		}
		return nil, &refusal
	}
	relayed := bridge.(*wsConn)
	if plan.stream {
		// A stream bridge carries rstream frames; no create-frame rewrite may touch the bytes.
		relayed.createEnv = nil
	}
	return relayed, nil
}

// serveRelayAgent serves an agent session: it authenticates like /ws/device, becomes the device's relay
// link and carries the streams the server opens to the device. A revocation ends it with a goaway.
func (s *Service) serveRelayAgent(pending *relaymux.PendingLink) {
	hello := pending.Hello
	if hello.Protocol != agent.ProtocolVersion {
		pending.Refuse(relaymux.Reply{Status: http.StatusUpgradeRequired, Code: "version_mismatch", Message: "控制通道协议版本不兼容", ServerVersion: version.Version})
		return
	}
	if hello.DeviceID == "" || hello.Secret == "" {
		pending.Refuse(relaymux.Reply{Status: http.StatusForbidden, Code: string(ipc.CodeForbidden), Message: "设备凭据无效"})
		return
	}
	if _, err := s.AuthenticateAgent(context.Background(), hello.DeviceID, hello.Secret); err != nil {
		pending.Refuse(agentForbiddenReply(err))
		return
	}
	link, err := pending.Accept(relaymux.Reply{ServerVersion: version.Version})
	if err != nil {
		return
	}
	if !s.relay.track(link) {
		_ = link.Close()
		return
	}
	defer s.relay.untrack(link)
	s.relay.registerAgent(hello.DeviceID, link)
	defer s.relay.unregisterAgent(hello.DeviceID, link)
	// A revoke or account disable can land between authentication and registration; re-check once registered.
	if err := s.recheckDevice(context.Background(), hello.DeviceID); err != nil {
		link.Goaway(agentForbiddenReply(err))
		<-link.Done()
		return
	}
	// The server opens streams to the agent; an agent-opened stream has no meaning and is dropped.
	for {
		conn, err := link.AcceptStream(context.Background())
		if err != nil {
			return
		}
		_ = conn.Close()
	}
}

// agentForbiddenReply mirrors the control channel's forbidden frame: code forbidden for every cause, with
// reason owner_unavailable when the owner account is temporarily unavailable.
func agentForbiddenReply(err error) relaymux.Reply {
	frame := forbiddenFrame(err)
	return relaymux.Reply{Status: http.StatusForbidden, Code: frame.Code, Reason: frame.Reason, Message: frame.Message}
}

// pipeRelayStream copies both directions with blocking writes, so each side's flow control reaches the other:
// a reader that stops stalls the writer at the stream windows. An end of input half-closes the other side; an
// error or ctx ending (revocation) resets both.
func pipeRelayStream(ctx context.Context, client, device net.Conn) {
	type result struct{ err error }
	results := make(chan result, 2)
	shuttle := func(dst, src net.Conn) {
		buffer := make([]byte, 32<<10)
		_, err := io.CopyBuffer(dst, src, buffer)
		if err == nil {
			err = relaymux.CloseWrite(dst)
		}
		results <- result{err}
	}
	go shuttle(device, client)
	go shuttle(client, device)
	abort := func() {
		relaymux.Abort(client)
		relaymux.Abort(device)
	}
	stop := context.AfterFunc(ctx, abort)
	defer stop()
	for range 2 {
		if r := <-results; r.err != nil {
			abort()
		}
	}
	_ = client.Close()
	_ = device.Close()
}
