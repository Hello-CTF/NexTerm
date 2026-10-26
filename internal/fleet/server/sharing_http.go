package fleetserver

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/sharing"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/wscompress"
	"github.com/coder/websocket"
)

// maxShareViewerMessage is the read limit of the viewer WS; input frames go through supervisor protocol
// fragmentation (256KiB per frame), so this only catches abnormally large messages.
const maxShareViewerMessage = 1 << 20

// defaultShareRevalidateInterval is the periodic recheck interval for idle share connections: when the data flow stops,
// revocation/expiry/device changes take effect at the latest within this interval.
const defaultShareRevalidateInterval = 10 * time.Second

// shareViewerWriteTimeout is the per-message timeout for writing to the viewer WS: a slow viewer (TCP
// backpressure) must not hang the output pump and the connection teardown indefinitely.
const shareViewerWriteTimeout = 30 * time.Second

// shareTerminateCheckTimeout is the timeout for the forced authorization recheck before termination: the recheck only reads the database,
// and on timeout it is treated as no termination code (as if a pure transport error), never delaying the connection teardown.
const shareTerminateCheckTimeout = 5 * time.Second

type hostShareView struct {
	ID                string `json:"id"`
	OwnerID           string `json:"owner_id"`
	OwnerUsername     string `json:"owner_username"`
	DeviceID          string `json:"device_id"`
	RecipientID       string `json:"recipient_id"`
	RecipientUsername string `json:"recipient_username"`
	Permission        string `json:"permission"`
	CreatedAt         int64  `json:"created_at"`
	ExpiresAt         int64  `json:"expires_at"`
	RevokedAt         int64  `json:"revoked_at,omitempty"`
}

func newHostShareView(share *sharing.HostShare) hostShareView {
	return hostShareView{
		ID: share.ID, OwnerID: share.OwnerID, OwnerUsername: share.OwnerUsername,
		DeviceID: share.DeviceID, RecipientID: share.RecipientID, RecipientUsername: share.RecipientUsername,
		Permission: string(share.Permission), CreatedAt: share.CreatedAt, ExpiresAt: share.ExpiresAt, RevokedAt: share.RevokedAt,
	}
}

type hostShareCreateRequest struct {
	DeviceID          string `json:"device_id"`
	RecipientUsername string `json:"recipient_username"`
	Write             bool   `json:"write"`
	TTLMS             int64  `json:"ttl_ms"`
}

func (s *Service) serveHostShareCreate(w http.ResponseWriter, r *http.Request) {
	var request hostShareCreateRequest
	if err := decodeFleetJSON(w, r, &request); err != nil {
		writeFleetFailure(w, err)
		return
	}
	// Device authorization and deny auditing happen inside sharing.CreateHostShare before username resolution (unauthorized requests always get
	// 403+audit, never revealing whether the recipient exists); the recipient is resolved by exact username (case-insensitive),
	// and creation does not require the frontend to hold a user directory (/admin/users is superadmin-only), only the device owner
	// signs the same contract. Neither error path touches any password or key material.
	share, err := s.sharing.CreateHostShare(r.Context(), identityFrom(r), request.DeviceID, request.RecipientUsername, request.Write, time.Duration(request.TTLMS)*time.Millisecond)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, newHostShareView(share))
}

func (s *Service) serveHostShareList(w http.ResponseWriter, r *http.Request) {
	shares, err := s.sharing.ListHostShares(r.Context(), identityFrom(r))
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	views := make([]hostShareView, 0, len(shares))
	for _, share := range shares {
		views = append(views, newHostShareView(share))
	}
	writeFleetJSON(w, http.StatusOK, map[string][]hostShareView{"shares": views})
}

func (s *Service) serveHostShareRevoke(w http.ResponseWriter, r *http.Request) {
	if err := s.sharing.RevokeHostShare(r.Context(), identityFrom(r), r.PathValue("id")); err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// dialShareSupervisor returns the supervisor client dedicated to the device managed-share data path: each dial
// requests an outbound bridge via the registry (supervisor protocol bytes pass through transparently), and the hello digest comes from
// the state_digest reported by the device control channel. It refuses when the device is offline or has not reported one.
func (s *Service) dialShareSupervisor(deviceID string) (*supervisor.Client, error) {
	digest, ok := s.registry.StateDigest(deviceID)
	if !ok {
		return nil, ipc.NewError(ipc.CodeDisconnected, "设备代理未上报状态摘要")
	}
	return supervisor.NewRemoteClient(func(ctx context.Context) (net.Conn, error) {
		return s.registry.RequestBridge(ctx, deviceID, agent.PurposeTerminal)
	}, digest), nil
}

type shareReadyFrame struct {
	Type       string `json:"type"`
	SessionID  string `json:"session_id"`
	Permission string `json:"permission"`
	ExpiresAt  int64  `json:"expires_at"`
}

type shareErrorFrame struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// shareTerminalWS serializes all writes to the viewer WS (binary output and text control frames share
// one connection, and coder/websocket allows only one concurrent writer); reads go in a single direction and need no lock.
type shareTerminalWS struct {
	conn *websocket.Conn

	writeMu      sync.Mutex
	writeTimeout time.Duration
}

func (v *shareTerminalWS) writeMessage(ctx context.Context, kind websocket.MessageType, payload []byte) error {
	v.writeMu.Lock()
	defer v.writeMu.Unlock()
	timeout := v.writeTimeout
	if timeout <= 0 {
		timeout = shareViewerWriteTimeout
	}
	writeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return v.conn.Write(writeCtx, kind, payload)
}

func (v *shareTerminalWS) writeJSON(ctx context.Context, frame any) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	v.writeMu.Lock()
	defer v.writeMu.Unlock()
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return v.conn.Write(writeCtx, websocket.MessageText, payload)
}

type shareWSReader struct {
	conn *websocket.Conn
	buf  []byte
}

func (r *shareWSReader) Read(p []byte) (int, error) {
	for {
		if len(r.buf) > 0 {
			count := copy(p, r.buf)
			r.buf = r.buf[count:]
			return count, nil
		}
		kind, payload, err := r.conn.Read(context.Background())
		if err != nil {
			return 0, err
		}
		// Viewer input accepts only binary frames; text control frames never enter the terminal byte stream.
		if kind != websocket.MessageBinary {
			continue
		}
		r.buf = payload
	}
}

type shareWSWriter struct {
	viewer *shareTerminalWS
}

func (w shareWSWriter) Write(p []byte) (int, error) {
	if err := w.viewer.writeMessage(context.Background(), websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// discardInput keeps reading and discarding viewer messages after the input direction is rejected by the Gate: after read-only or
// permission shrinkage the session stays alive (output continues), and control frames (ping/close) also need continuous reads to be answered.
// Reads use a background ctx - coder/websocket tears the connection down when the read ctx is canceled, which would
// kill the viewer connection before the termination error frame is written.
func (v *shareTerminalWS) discardInput() error {
	for {
		if _, _, err := v.conn.Read(context.Background()); err != nil {
			return err
		}
	}
}

// serveSharePublicGone answers every request to the removed legacy /share/public/{token} route, including WS
// upgrades, with 410 and no-store; it never consults tokens, storage or the SPA entry.
func serveSharePublicGone(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeFleetJSON(w, http.StatusGone, ipc.Failure(ipc.NewError(ipc.CodeNotFound, "公开分享链接已不再支持")))
}

// serveShareTerminalOpen handles GET /share/devices/{id}/terminal: a registered user
// (AuthorizeTerminalOpen already audited) opens a new terminal via the host agent, with bytes flowing both ways through the
// sharing Gate, and permission shrinkage taking effect immediately.
func (s *Service) serveShareTerminalOpen(w http.ResponseWriter, r *http.Request) {
	grant, err := s.sharing.AuthorizeTerminalOpen(r.Context(), identityFrom(r), r.PathValue("id"))
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	s.serveShareTerminal(w, r, &grant.Grant, s.sharing.NewHostGate(grant))
}

// serveShareTerminal upgrades the viewer WS and establishes the gated data path: it creates a terminal via the
// host agent (registered shares) and pumps it through the Gate.
func (s *Service) serveShareTerminal(w http.ResponseWriter, r *http.Request, grant *sharing.Grant, gate *sharing.Gate) {
	conn, err := websocket.Accept(w, r, wscompress.Accept(wscompress.Public, nil))
	if err != nil {
		return
	}
	conn.SetReadLimit(maxShareViewerMessage)
	viewer := &shareTerminalWS{conn: conn}
	s.serveShareTerminalWithViewer(r, viewer, grant, gate)
}

func (s *Service) serveShareTerminalWithViewer(r *http.Request, viewer *shareTerminalWS, grant *sharing.Grant, gate *sharing.Gate) string {
	client, err := s.dialShareSupervisor(grant.DeviceID)
	if err != nil {
		s.failShareTerminal(viewer, err)
		return ""
	}
	sessionID, stream, cleanup, err := s.openShareStream(r, client)
	if err != nil {
		s.failShareTerminal(viewer, err)
		return ""
	}
	if cleanup != nil {
		defer cleanup()
	}
	if err := viewer.writeJSON(r.Context(), shareReadyFrame{
		Type: "ready", SessionID: sessionID, Permission: string(grant.Permission), ExpiresAt: grant.ExpiresAt,
	}); err != nil {
		_ = stream.Close()
		_ = viewer.conn.Close(websocket.StatusInternalError, "ready failed")
		return sessionID
	}
	s.pumpShareTerminal(viewer, stream, gate, sessionID)
	return sessionID
}

// openShareStream creates the shared terminal on the device and attaches to it by its identity. Host grants never
// name an existing session, so there is no path that attaches without an identity.
func (s *Service) openShareStream(r *http.Request, client *supervisor.Client) (string, *supervisor.Stream, func(), error) {
	info, err := client.Create(r.Context(), supervisor.CreateOptions{})
	if err != nil {
		return "", nil, nil, err
	}
	expect := &supervisor.Identity{CreatedAt: info.CreatedAt, Incarnation: info.Incarnation}
	cleanup := func() {
		killCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Kill(killCtx, info.ID, expect)
	}
	stream, err := client.Attach(r.Context(), info.ID, expect)
	if err != nil {
		// When attach fails, reclaim the just-created session so no orphan terminal is left on the host.
		cleanup()
		return "", nil, nil, err
	}
	return info.ID, stream, cleanup, nil
}

// pumpShareTerminal pumps bytes both ways until either side ends or the grant becomes invalid: output via Gate.PipeOutput
// (terminal to viewer), input via Gate.PipeInput (viewer to terminal), plus a periodic recheck
// goroutine to cover idle connections (revocation/expiry takes effect even with no data flow). Once the grant becomes invalid, any
// goroutine returns with an error, followed by a best-effort error frame and close; blocked reads
// are woken by stream.Close and the viewer close.
func (s *Service) pumpShareTerminal(viewer *shareTerminalWS, stream *supervisor.Stream, gate *sharing.Gate, sessionID string) {
	pumpCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 3)
	go func() {
		done <- gate.PipeOutput(pumpCtx, shareWSWriter{viewer: viewer}, stream)
	}()
	go func() {
		err := gate.PipeInput(pumpCtx, stream, &shareWSReader{conn: viewer.conn})
		if errors.Is(err, sharing.ErrInputNotAllowed) {
			err = viewer.discardInput()
		}
		done <- err
	}()
	lastReady := gate.Grant()
	go func() {
		ticker := time.NewTicker(s.shareRevalidateInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pumpCtx.Done():
				done <- pumpCtx.Err()
				return
			case <-ticker.C:
				if err := gate.Check(pumpCtx); err != nil {
					done <- err
					return
				}
				current := gate.Grant()
				if current != nil && (lastReady == nil || lastReady.Permission != current.Permission || lastReady.ExpiresAt != current.ExpiresAt) {
					if err := viewer.writeJSON(pumpCtx, shareReadyFrame{
						Type: "ready", SessionID: sessionID, Permission: string(current.Permission), ExpiresAt: current.ExpiresAt,
					}); err != nil {
						done <- err
						return
					}
					lastReady = current
				}
			}
		}
	}()
	err := <-done
	cancel()
	_ = stream.Close()
	code, message, ok := shareTermination(err)
	if !ok && err != nil {
		// A transport-layer error (bridge disconnect, etc.) arriving before the authorization recheck maps to no termination code; before terminating,
		// force one authorization recheck: expiry/revocation still delivers a recognizable error frame, and the transport error never masks
		// the real termination reason.
		checkCtx, checkCancel := context.WithTimeout(context.Background(), shareTerminateCheckTimeout)
		defer checkCancel()
		if checkErr := gate.CheckFresh(checkCtx); checkErr != nil {
			code, message, ok = shareTermination(checkErr)
		}
	}
	if ok {
		_ = viewer.writeJSON(context.Background(), shareErrorFrame{Type: "error", Code: code, Message: message})
	}
	status := websocket.StatusNormalClosure
	reason := "share ended"
	if err != nil {
		status = websocket.StatusPolicyViolation
		reason = "share terminated"
	}
	_ = viewer.conn.Close(status, reason)
	<-done
	<-done
}

func (s *Service) failShareTerminal(viewer *shareTerminalWS, err error) {
	code, message, ok := shareTermination(err)
	if !ok {
		code, message = "internal", "分享数据通路建立失败"
	}
	_ = viewer.writeJSON(context.Background(), shareErrorFrame{Type: "error", Code: code, Message: message})
	_ = viewer.conn.Close(websocket.StatusPolicyViolation, "share failed")
}

// shareTermination maps pump errors to termination codes that are safe to deliver to the viewer; transport-layer
// disconnects and internal errors carry no details (avoiding leaking database/internal state).
func shareTermination(err error) (string, string, bool) {
	switch {
	case err == nil:
		return "", "", false
	case errors.Is(err, sharing.ErrGrantExpired):
		return "expired", "分享授权已过期", true
	case errors.Is(err, sharing.ErrInputNotAllowed):
		return "read_only", "分享授权为只读", true
	case errors.Is(err, supervisor.ErrNotFound):
		return "not_found", "分享会话不存在", true
	case errors.Is(err, supervisor.ErrHelperUpgradeRequired):
		return "helper_upgrade_required", "设备上的 nexterm-agent 版本过旧，请升级后重试", true
	case errors.Is(err, supervisor.ErrUnavailable):
		return "disconnected", "设备代理离线", true
	}
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		switch appErr.Code {
		case ipc.CodeForbidden, ipc.CodeDisconnected, ipc.CodeNotFound:
			return string(appErr.Code), appErr.Message, true
		}
	}
	return "", "", false
}
