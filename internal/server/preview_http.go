package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/sharing"
	"github.com/coder/websocket"
)

const (
	// previewViewerReadLimit is the read limit for the preview viewer WS: upstream only carries read_write
	// link input frames (human keystroke volume); it only catches abnormally large messages.
	previewViewerReadLimit = 1 << 20
	// defaultPreviewRevalidateInterval is the authorization recheck interval for preview connections:
	// revocation/expiry takes effect at the latest within this interval, regardless of output.
	defaultPreviewRevalidateInterval = 10 * time.Second
	// previewGuestLease is the preview guest control lease duration: every admitted write renews it,
	// ReleaseGuest explicitly releases it at WS end, and the ready frame tells the viewer via lease_ms.
	previewGuestLease = 3 * time.Second
)

// PreviewSpectator is the preview's minimal dependency on the session Manager: subscribing to tab output, ownership lookup,
// disconnect cleanup, and guest write arbitration for read_write links (WriteGuest/ReleaseGuest).
type PreviewSpectator interface {
	AttachSpectator(context.Context, string, session.SpectateOptions) (session.TabInfo, error)
	TabOwner(string) (string, error)
	DetachChannel(string) error
	WriteGuest(context.Context, string, string, []byte, time.Duration) error
	ReleaseGuest(string, string) error
}

type previewLinkView struct {
	ID             string `json:"id"`
	OwnerID        string `json:"owner_id"`
	SessionID      string `json:"session_id"`
	Permission     string `json:"permission"`
	CreatedAt      int64  `json:"created_at"`
	ExpiresAt      int64  `json:"expires_at"`
	RevokedAt      int64  `json:"revoked_at,omitempty"`
	LastAccessedAt int64  `json:"last_accessed_at,omitempty"`
}

func newPreviewLinkView(link *sharing.PreviewLink) previewLinkView {
	return previewLinkView{
		ID: link.ID, OwnerID: link.OwnerID, SessionID: link.SessionID,
		Permission: string(link.Permission),
		CreatedAt:  link.CreatedAt, ExpiresAt: link.ExpiresAt,
		RevokedAt: link.RevokedAt, LastAccessedAt: link.LastAccessedAt,
	}
}

type previewLinkCreateRequest struct {
	SessionID string `json:"session_id"`
	TTLMS     int64  `json:"ttl_ms"`
	Write     bool   `json:"write"`
}

// servePreviewLinkCreate handles POST /share/previews: create a preview link for a server-hosted terminal tab,
// read-only by default, granting read-write only when write is explicitly true. Only the session owner may create;
// the create response is the only entry carrying the one-time token.
func (s *Server) servePreviewLinkCreate(w http.ResponseWriter, r *http.Request) {
	var request previewLinkCreateRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	identity := accountIdentityFrom(r.Context())
	owner, err := s.spectator.TabOwner(request.SessionID)
	switch {
	case errors.Is(err, session.ErrTabNotFound):
		writeAccountError(w, http.StatusNotFound, ipc.NewError(ipc.CodeNotFound, "终端标签页不存在或已关闭"))
		return
	case err != nil:
		writeAccountFailure(w, err)
		return
	}
	if owner != identity.UserID {
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "终端不属于该用户"))
		return
	}
	link, token, err := s.previews.CreatePreviewLink(r.Context(), identity, request.SessionID, request.Write, time.Duration(request.TTLMS)*time.Millisecond)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, struct {
		previewLinkView
		Token string `json:"token"`
	}{previewLinkView: newPreviewLinkView(link), Token: token})
}

func (s *Server) servePreviewLinkList(w http.ResponseWriter, r *http.Request) {
	links, err := s.previews.ListPreviewLinks(r.Context(), accountIdentityFrom(r.Context()))
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	views := make([]previewLinkView, 0, len(links))
	for _, link := range links {
		views = append(views, newPreviewLinkView(link))
	}
	writeAccountJSON(w, http.StatusOK, map[string][]previewLinkView{"links": views})
}

func (s *Server) servePreviewLinkRevoke(w http.ResponseWriter, r *http.Request) {
	if err := s.previews.RevokePreviewLink(r.Context(), accountIdentityFrom(r.Context()), r.PathValue("id")); err != nil {
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type previewReadyFrame struct {
	Type       string `json:"type"`
	SessionID  string `json:"session_id"`
	Permission string `json:"permission"`
	ExpiresAt  int64  `json:"expires_at"`
	LeaseMS    int64  `json:"lease_ms"`
}

type previewErrorFrame struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// previewControlFrame is the server-to-viewer control frame: it reports the arbitration result of one guest write.
// guest carries the lease expiry (epoch ms); owner/busy carry nothing.
type previewControlFrame struct {
	Type       string `json:"type"`
	State      string `json:"state"`
	LeaseUntil int64  `json:"lease_until,omitempty"`
}

// serveSharePreview handles GET /share/preview/{token}: plain browser navigation is served the existing SPA
// directly by the static entry (no token check, always no-store); only the WS upgrade is the data plane -
// an anonymous viewer exchanges the public token for a one-time authorization (ResolvePreview is audited), then
// AttachSpectator subscribes to the tab: the first frame is the current screen snapshot, afterwards only live output.
// Read-only links install no input handling; the viewer's upstream bytes are discarded by the read loop; read_write
// link viewer input is binary frames, each re-checked against the current row via RevalidatePreviewInput (revocation/
// expiry/permission shrink take effect immediately) before entering WriteGuest lease arbitration, with arbitration
// results fed back as control frames; authorization rejections deliver non-terminal error frames per semantics
// (degraded read_only / revoked / expired); read_write connection end means ReleaseGuest frees control and lease.
// Output-side authorization is rechecked on an interval (regardless of output), and revocation/expiry terminates.
func (s *Server) serveSharePreview(page http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isPreviewWSUpgrade(r) {
			servePreviewPage(page, w, r)
			return
		}
		key := "preview-resolve|" + clientIP(r)
		if !s.previewResolve.Allow(key) {
			writeAccountJSON(w, http.StatusTooManyRequests, ipc.Failure(ipc.NewError(ipc.CodeForbidden, "尝试过于频繁，请稍后再试")))
			return
		}
		grant, err := s.previews.ResolvePreview(r.Context(), r.PathValue("token"))
		if err == nil {
			s.previewResolve.RecordSuccess(key)
		} else if errors.Is(err, sharing.ErrTokenNotFound) {
			// Only token-not-found counts toward throttle backoff (random token floods); rejections of real tokens
			// such as revoked/expired do not punish the holder.
			s.previewResolve.RecordFailure(key)
		}
		if err != nil {
			writeAccountFailure(w, err)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
		if err != nil {
			return
		}
		conn.SetReadLimit(previewViewerReadLimit)
		ctx, release, err := s.sockets.track(r.Context())
		if err != nil {
			_ = conn.CloseNow()
			return
		}
		defer release()
		channelID := ids.New()
		if _, err := s.spectator.AttachSpectator(ctx, grant.SessionID, session.SpectateOptions{
			ChannelID: channelID,
			ClientID:  "preview-" + grant.ShareID,
		}); err != nil {
			s.failSharePreview(conn, err)
			return
		}
		receiver, err := s.channels.BindChannel(channelID)
		if err != nil {
			_ = s.spectator.DetachChannel(channelID)
			s.failSharePreview(conn, err)
			return
		}
		defer receiver.Close()
		queue := &previewSendQueue{}
		var onRead func(context.Context, websocket.MessageType, []byte)
		if grant.Permission.AllowsWrite() {
			input := &previewInputHandler{s: s, grant: grant, guest: "preview-" + grant.ShareID, queue: queue}
			onRead = input.handle
			defer func() { _ = s.spectator.ReleaseGuest(grant.SessionID, input.guest) }()
		}
		if err := writePreviewJSON(ctx, conn, previewReadyFrame{
			Type: "ready", SessionID: grant.SessionID,
			Permission: string(grant.Permission), ExpiresAt: grant.ExpiresAt,
			LeaseMS: int64(previewGuestLease / time.Millisecond),
		}); err != nil {
			return
		}
		s.pumpSharePreview(ctx, conn, receiver, grant, queue, onRead)
	}
}

// pumpSharePreview pumps subscription frames to the viewer until either side ends or authorization lapses. Rechecks fire both
// before each frame write and on the idle timeout, so revocation/expiry does not depend on traffic. Before terminating, an
// identifiable error frame is delivered (as a normal frame through pumpSocket's serialized write path, not competing with keepalive pings).
// control/read_only frames in the queue take the same write path, interleaving with output frames in arrival order;
// onRead is passed through to pumpSocket's read loop (nil for read-only links).
func (s *Server) pumpSharePreview(ctx context.Context, conn *websocket.Conn, receiver ChannelReceiver, grant *sharing.PreviewGrant, queue *previewSendQueue, onRead func(context.Context, websocket.MessageType, []byte)) {
	interval := s.previewRevalidateInterval
	if interval <= 0 {
		interval = defaultPreviewRevalidateInterval
	}
	lastCheck := time.Now()
	var terminalErr error
	s.pumpSocketWithRead(ctx, conn, func(ctx context.Context) (websocket.MessageType, []byte, func(), error) {
		if terminalErr != nil {
			return 0, nil, nil, terminalErr
		}
		for {
			if payload := queue.pop(); payload != nil {
				return websocket.MessageText, payload, nil, nil
			}
			if time.Since(lastCheck) >= interval {
				refreshed, err := s.previews.RevalidatePreview(ctx, grant)
				if err != nil {
					terminalErr = err
					code, message := previewTermination(err)
					payload, marshalErr := json.Marshal(previewErrorFrame{Type: "error", Code: code, Message: message})
					if marshalErr != nil {
						return 0, nil, nil, terminalErr
					}
					return websocket.MessageText, payload, nil, nil
				}
				grant = refreshed
				lastCheck = time.Now()
			}
			frameCtx, cancel := context.WithTimeout(ctx, interval)
			queue.arm(cancel)
			frame, err := receiver.Next(frameCtx)
			queue.disarm()
			cancel()
			if errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			if errors.Is(err, context.Canceled) && ctx.Err() == nil {
				// Woken by a queued frame to write; back to the loop top to take a frame.
				continue
			}
			if err != nil {
				code, message := previewTermination(err)
				terminalErr = err
				payload, marshalErr := json.Marshal(previewErrorFrame{Type: "error", Code: code, Message: message})
				if marshalErr != nil {
					return 0, nil, nil, terminalErr
				}
				return websocket.MessageText, payload, nil, nil
			}
			ack := func() { _ = receiver.Ack(frame.Sequence) }
			switch frame.Kind {
			case FrameBinary:
				return websocket.MessageBinary, frame.Data, ack, nil
			case FrameJSON:
				return websocket.MessageText, frame.Data, ack, nil
			default:
				return 0, nil, nil, errors.New("unknown channel frame kind")
			}
		}
	}, onRead)
}

// previewSendQueue is the queue by which the viewer upstream handler (read goroutine) delivers text frames to the pump write loop:
// both control and read_only frames go through pumpSocket's serialized write path via it. push wakes a receiver.Next that is
// idle-waiting (the registered cancel), so control feedback does not sleep through the whole recheck interval.
type previewSendQueue struct {
	mu     sync.Mutex
	frames [][]byte
	wake   context.CancelFunc
}

func (q *previewSendQueue) pushJSON(frame any) {
	payload, err := json.Marshal(frame)
	if err != nil {
		return
	}
	q.mu.Lock()
	q.frames = append(q.frames, payload)
	wake := q.wake
	q.mu.Unlock()
	if wake != nil {
		wake()
	}
}

func (q *previewSendQueue) pop() []byte {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.frames) == 0 {
		return nil
	}
	payload := q.frames[0]
	q.frames = q.frames[1:]
	return payload
}

func (q *previewSendQueue) arm(cancel context.CancelFunc) {
	q.mu.Lock()
	q.wake = cancel
	pending := len(q.frames) > 0
	q.mu.Unlock()
	if pending {
		cancel()
	}
}

func (q *previewSendQueue) disarm() {
	q.mu.Lock()
	q.wake = nil
	q.mu.Unlock()
}

// previewInputHandler is the input path for read_write preview connections: it accepts only binary frames (text
// and unknown frames are silently discarded), re-checks authorization against the current row via RevalidatePreviewInput
// for each input, and on admission enters WriteGuest lease arbitration with the result fed back as a control frame;
// authorization rejections deliver non-terminal error frames per semantics (see rejectInput), and connection termination
// for revoked/expired connections is handled by the output-side periodic recheck. handle only runs inside pumpSocket's
// read goroutine, so the grant snapshot needs no locking.
type previewInputHandler struct {
	s     *Server
	grant *sharing.PreviewGrant
	guest string
	queue *previewSendQueue
}

func (h *previewInputHandler) handle(ctx context.Context, messageType websocket.MessageType, data []byte) {
	if messageType != websocket.MessageBinary {
		return
	}
	grant, err := h.s.previews.RevalidatePreviewInput(ctx, h.grant)
	if err != nil {
		h.rejectInput(err)
		return
	}
	h.grant = grant
	switch err := h.s.spectator.WriteGuest(ctx, grant.SessionID, h.guest, data, previewGuestLease); {
	case err == nil:
		h.queue.pushJSON(previewControlFrame{Type: "control", State: "guest", LeaseUntil: time.Now().Add(previewGuestLease).UnixMilli()})
	case errors.Is(err, session.ErrGuestControlOwner):
		h.queue.pushJSON(previewControlFrame{Type: "control", State: "owner"})
	case errors.Is(err, session.ErrGuestControlBusy):
		h.queue.pushJSON(previewControlFrame{Type: "control", State: "busy"})
	}
	// Other write failures (tab closed/disconnected/guest control unsupported) are silently discarded: the output channel
	// will soon give a termination frame or recover via reconnect; there is no extra feedback frame type.
}

// rejectInput maps input-side authorization rejections to non-terminal error frames (connection stays open): revoked/expired share
// the termination frame's code and text as the output side, read-only degradation yields read_only; invalid links and DB/internal
// errors stay silent, leaking no internal details and never misreporting read_only. CodeForbidden outside the sentinels means
// read-only degradation - the only non-sentinel forbidden error from RevalidatePreviewInput.
func (h *previewInputHandler) rejectInput(err error) {
	var frame previewErrorFrame
	switch {
	case errors.Is(err, sharing.ErrPreviewRevoked):
		frame = previewErrorFrame{Type: "error", Code: "revoked", Message: "分享链接已吊销"}
	case errors.Is(err, sharing.ErrPreviewExpired):
		frame = previewErrorFrame{Type: "error", Code: "expired", Message: "分享链接已过期"}
	case errors.Is(err, sharing.ErrPreviewInvalid):
		return
	case ipc.NormalizeError(err).Code == ipc.CodeForbidden:
		frame = previewErrorFrame{Type: "error", Code: "read_only", Message: "预览当前为只读"}
	default:
		return
	}
	h.queue.pushJSON(frame)
}

func (s *Server) failSharePreview(conn *websocket.Conn, err error) {
	code, message := previewTermination(err)
	_ = writePreviewJSON(context.Background(), conn, previewErrorFrame{Type: "error", Code: code, Message: message})
	_ = conn.Close(websocket.StatusPolicyViolation, "preview failed")
}

func writePreviewJSON(ctx context.Context, conn *websocket.Conn, frame any) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, payload)
}

// previewTermination maps pump errors to termination codes safely deliverable to the viewer; transport disconnects
// and internal errors carry no details (avoiding leaking internal state).
func previewTermination(err error) (string, string) {
	switch {
	case errors.Is(err, sharing.ErrPreviewExpired):
		return "expired", "分享链接已过期"
	case errors.Is(err, sharing.ErrPreviewRevoked):
		return "revoked", "分享链接已吊销"
	case errors.Is(err, sharing.ErrPreviewInvalid), errors.Is(err, sharing.ErrTokenNotFound):
		return "not_found", "分享链接无效"
	case errors.Is(err, session.ErrTabNotFound), errors.Is(err, session.ErrTabClosed):
		return "not_found", "会话不存在或已关闭"
	case errors.Is(err, hub.ErrClosed), errors.Is(err, hub.ErrDetached):
		return "ended", "分享已结束"
	}
	return "internal", "分享暂时不可用"
}

// servePreviewPage serves the existing SPA with the static entry (same index.html as "/", no redirect
// and no separate shell); token URLs are always no-store, and the path is pinned to "/" before file lookup.
func servePreviewPage(page http.Handler, w http.ResponseWriter, r *http.Request) {
	if page == nil {
		http.NotFound(w, r)
		return
	}
	cloned := r.Clone(r.Context())
	cloned.URL.Path = "/"
	cloned.URL.RawPath = ""
	cloned.RequestURI = "/"
	if cloned.URL.RawQuery != "" {
		cloned.RequestURI += "?" + cloned.URL.RawQuery
	}
	page.ServeHTTP(previewNoStoreWriter{w}, cloned)
}

// previewNoStoreWriter forces Cache-Control: no-store before writing, overriding the static entry's default no-cache
// for text/html - token URLs allow no caching at all.
type previewNoStoreWriter struct{ http.ResponseWriter }

func (w previewNoStoreWriter) WriteHeader(status int) {
	w.Header().Set("Cache-Control", "no-store")
	w.ResponseWriter.WriteHeader(status)
}

func (w previewNoStoreWriter) Write(payload []byte) (int, error) {
	w.Header().Set("Cache-Control", "no-store")
	return w.ResponseWriter.Write(payload)
}

// isPreviewWSUpgrade decides whether a request is a WebSocket upgrade; the conditions match
// websocket.Accept's handshake checks (Connection/Upgrade header tokens, case-insensitive).
func isPreviewWSUpgrade(r *http.Request) bool {
	return previewHeaderHasToken(r.Header, "Connection", "upgrade") && previewHeaderHasToken(r.Header, "Upgrade", "websocket")
}

func previewHeaderHasToken(header http.Header, key, token string) bool {
	for _, value := range header.Values(key) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}
