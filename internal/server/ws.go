package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ai/watches"
	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/wscompress"
	"github.com/coder/websocket"
)

const (
	DefaultWebSocketKeepAlive    = 25 * time.Second
	DefaultWebSocketPingTimeout  = 10 * time.Second
	DefaultWebSocketWriteTimeout = 10 * time.Second
)

type WebSocketConfig struct {
	KeepAlive    time.Duration
	PingTimeout  time.Duration
	WriteTimeout time.Duration
}

func (c WebSocketConfig) withDefaults() WebSocketConfig {
	if c.KeepAlive == 0 {
		c.KeepAlive = DefaultWebSocketKeepAlive
	}
	if c.PingTimeout <= 0 {
		c.PingTimeout = DefaultWebSocketPingTimeout
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = DefaultWebSocketWriteTimeout
	}
	return c
}

func ParseWebSocketEnv(getenv func(string) string) (WebSocketConfig, error) {
	var config WebSocketConfig
	for _, entry := range []struct {
		key    string
		target *time.Duration
	}{
		{"NEXTERM_WS_KEEPALIVE", &config.KeepAlive},
		{"NEXTERM_WS_PING_TIMEOUT", &config.PingTimeout},
		{"NEXTERM_WS_WRITE_TIMEOUT", &config.WriteTimeout},
	} {
		raw := getenv(entry.key)
		if raw == "" {
			continue
		}
		value, err := time.ParseDuration(raw)
		if err != nil {
			return WebSocketConfig{}, fmt.Errorf("%s must be a duration: %w", entry.key, err)
		}
		*entry.target = value
	}
	return config, nil
}

const defaultAccountSocketRevalidateInterval = 30 * time.Second

func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.admitWebSocket(w, r)
	if !ok {
		return
	}
	connection, err := websocket.Accept(w, r, wscompress.Accept(wscompress.Events, &websocket.AcceptOptions{
		OriginPatterns: s.options.AllowedOrigins,
		Subprotocols:   []string{channelSubprotocol},
	}))
	if err != nil {
		return
	}
	ctx, revoked, release, err := s.sockets.trackAccount(r.Context(), socketKindEvents, connection, identity)
	if err != nil {
		_ = connection.CloseNow()
		return
	}
	defer release()
	defer s.superviseAccountSocket(ctx, connection, r, identity, revoked)()
	since, present, resync := parseEventCursor(r)
	subscriber, unsubscribe, err := s.events.subscribe(since, present, resync, s.eventFilter(identity))
	if err != nil {
		_ = connection.CloseNow()
		return
	}
	defer unsubscribe()
	s.pumpSocket(ctx, connection, func(ctx context.Context) (websocket.MessageType, []byte, func(), error) {
		select {
		case data := <-subscriber.queue:
			return websocket.MessageText, data, nil, nil
		case <-subscriber.done:
			return 0, nil, nil, ErrEventBrokerClosed
		case <-ctx.Done():
			return 0, nil, nil, ctx.Err()
		}
	})
}

func parseEventCursor(r *http.Request) (since uint64, present, resync bool) {
	raw := r.URL.Query().Get("since")
	if raw == "" {
		return 0, false, false
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, true, true
	}
	return value, true, false
}

// eventOwnerLookupTimeout bounds ownership lookups inside event filters so a
// stuck database cannot pin a subscription worker (and, with it, the pending
// delivery backlog) forever.
var eventOwnerLookupTimeout = 5 * time.Second

func eventOwnerLookupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), eventOwnerLookupTimeout)
}

// eventFilter filters events by subscriber identity: identity-less connections (open deployments/gateway) are not filtered;
// logged-in users receive owner-scoped resources plus explicitly public and legacy events. Failed ownership lookups,
// malformed payloads, empty owners, and unknown topics are never delivered (fail closed); superadmins are scoped likewise.
func (s *Server) eventFilter(identity *account.Identity) func(ipc.Event) bool {
	if identity == nil {
		return nil
	}
	userID := identity.UserID
	return func(event ipc.Event) bool {
		switch event.Event {
		case ipc.TopicSessionStatus:
			_, ok := event.Payload.(session.StatusEvent)
			return ok && event.OwnerID != "" && event.OwnerID == userID
		case ipc.TopicTerminalExit:
			_, ok := event.Payload.(session.ExitEvent)
			return ok && event.OwnerID != "" && event.OwnerID == userID
		case ipc.TopicTerminalThrottled:
			_, ok := event.Payload.(session.ThrottleEvent)
			return ok && event.OwnerID != "" && event.OwnerID == userID
		case ipc.TopicTerminalControl:
			_, ok := event.Payload.(session.ControlEvent)
			return ok && event.OwnerID != "" && event.OwnerID == userID
		case ipc.TopicTerminalAttention:
			_, ok := event.Payload.(session.AttentionEvent)
			return ok && event.OwnerID != "" && event.OwnerID == userID
		case ipc.TopicTerminalSessions:
			_, ok := event.Payload.(session.AggregateUpdate)
			return ok && event.OwnerID != "" && event.OwnerID == userID
		case ipc.TopicFSProgress:
			_, ok := event.Payload.(fslocal.Progress)
			return ok && event.OwnerID != "" && event.OwnerID == userID
		case ipc.TopicDeviceStatus:
			if s.fleet == nil {
				return false
			}
			status, ok := event.Payload.(ipc.DeviceStatusEvent)
			if !ok {
				return false
			}
			lookupCtx, cancel := eventOwnerLookupContext()
			defer cancel()
			ownerID, err := s.fleet.DeviceOwnerID(lookupCtx, status.DeviceID)
			return err == nil && ownerID == userID
		case ipc.TopicConnectProgress:
			if s.assetOwner == nil {
				return false
			}
			progress, ok := event.Payload.(ipc.ConnectProgressEvent)
			if !ok {
				return false
			}
			lookupCtx, cancel := eventOwnerLookupContext()
			defer cancel()
			ownerID, err := s.assetOwner(lookupCtx, progress.AssetID)
			return err == nil && ownerID == userID
		case ipc.TopicHelperProgress:
			if s.assetOwner == nil {
				return false
			}
			progress, ok := event.Payload.(ipc.HelperProgressEvent)
			if !ok {
				return false
			}
			lookupCtx, cancel := eventOwnerLookupContext()
			defer cancel()
			ownerID, err := s.assetOwner(lookupCtx, progress.AssetID)
			return err == nil && ownerID == userID
		case ipc.TopicAINotification:
			notification, ok := event.Payload.(ipc.AINotificationEvent)
			return ok && notification.OwnerID != "" && notification.OwnerID == userID
		case ipc.TopicAIWatchToolData:
			toolData, ok := event.Payload.(watches.ToolDataEvent)
			return ok && toolData.OwnerID != "" && toolData.OwnerID == userID
		case ipc.TopicLayoutChanged:
			_, ok := event.Payload.(ipc.LayoutChangedEvent)
			return ok && event.OwnerID != "" && event.OwnerID == userID
		case ipc.TopicInventoryChanged:
			_, ok := event.Payload.(ipc.InventoryChangedEvent)
			return ok && event.OwnerID != "" && event.OwnerID == userID
		case ipc.TopicSyncStatus:
			status, ok := event.Payload.(ipc.SyncStatusEvent)
			return ok && status.OwnerID != "" && status.OwnerID == userID
		case ipc.Topic("sync://round-complete"):
			_, ok := event.Payload.(struct{})
			return ok
		case ipc.TopicAppError, ipc.TopicUpdateProgress:
			return true
		case ipc.TopicLocalHelperStatus:
			// Only a single-account server runs a local helper, and its status
			// names no account data.
			return true
		default:
			return false
		}
	}
}

func (s *Server) serveChannel(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.admitWebSocket(w, r)
	if !ok {
		return
	}
	channelID := r.PathValue("id")
	resolver, _ := s.spectator.(channelOwnerResolver)
	if resolver == nil {
		resolver, _ = s.channels.(channelOwnerResolver)
	}
	var ownerID string
	var ownerErr error
	var generation uint64
	var finalize func()
	if resolver != nil {
		if generationResolver, ok := resolver.(interface {
			ChannelOwnerGeneration(string) (string, uint64, error)
		}); ok {
			ownerID, generation, ownerErr = generationResolver.ChannelOwnerGeneration(channelID)
			if ownerErr == nil && generation != 0 {
				if unregister, ok := resolver.(interface{ UnregisterChannelOwner(string, uint64) }); ok {
					finalize = func() { unregister.UnregisterChannelOwner(channelID, generation) }
				}
			}
		} else if identity != nil {
			ownerID, ownerErr = resolver.ChannelOwner(channelID)
		}
	}
	if identity != nil && (resolver == nil || ownerErr != nil || ownerID == "" || ownerID != identity.UserID) {
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "无权访问该终端通道"))
		return
	}
	connection, err := websocket.Accept(w, r, wscompress.Accept(wscompress.Terminal, &websocket.AcceptOptions{
		OriginPatterns: s.options.AllowedOrigins,
		Subprotocols:   []string{channelInputSubprotocol},
	}))
	if err != nil {
		return
	}
	clientID := r.URL.Query().Get("client")
	if len(clientID) > maxChannelClientID {
		clientID = ""
	}
	onRead := s.channelInputReader(r, connection, identity, clientID)
	if onRead != nil {
		// Input frames are tiny keystroke bundles; the default 32KiB read limit already bounds them.
		connection.SetReadLimit(channelInputMaxData + 512)
	}
	ctx, revoked, release, err := s.sockets.trackAccount(r.Context(), socketKindChannel, connection, identity)
	if err != nil {
		_ = connection.CloseNow()
		return
	}
	defer release()
	defer s.superviseAccountSocket(ctx, connection, r, identity, revoked)()
	receiver, err := s.channels.BindChannel(channelID)
	if err != nil {
		_ = connection.CloseNow()
		return
	}
	defer receiver.Close()
	s.pumpSocketWithRead(ctx, connection, func(ctx context.Context) (websocket.MessageType, []byte, func(), error) {
		frame, err := receiver.Next(ctx)
		if err != nil {
			if errors.Is(err, hub.ErrClosed) && finalize != nil {
				finalize()
			}
			return 0, nil, nil, err
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
	}, onRead)
}

// superviseAccountSocket closes the connection with a policy-violation code once its login session is revoked: either
// signalled through the tracker, or found invalid by the periodic recheck that backs up revocation paths which do not
// reach the tracker. Only a confirmed invalid session closes; transient lookup errors keep the socket. The close
// runs beside the pump (Close is safe against concurrent Write), and the returned stop waits for the supervisor.
func (s *Server) superviseAccountSocket(ctx context.Context, connection *websocket.Conn, r *http.Request, identity *account.Identity, revoked <-chan struct{}) (stop func()) {
	if identity == nil {
		return func() {}
	}
	interval := time.Duration(s.accountSocketRevalidateNanos.Load())
	if interval <= 0 {
		interval = defaultAccountSocketRevalidateInterval
	}
	cookieToken, _ := sessionCookie(r)
	s.sessionRecheck.acquire(identity.SessionID)
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-revoked:
				_ = connection.Close(websocket.StatusPolicyViolation, sessionReloginMessage)
				return
			case <-ticker.C:
				if s.accountSocketSessionInvalid(ctx, interval, cookieToken, identity) {
					_ = connection.Close(websocket.StatusPolicyViolation, sessionReloginMessage)
					return
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
		s.sessionRecheck.release(identity.SessionID)
	}
}

// accountSocketSessionInvalid rechecks the login session without sliding it. Sockets of one session share a single
// lookup per interval; a transient lookup error is not cached and never closes the socket.
func (s *Server) accountSocketSessionInvalid(ctx context.Context, interval time.Duration, cookieToken string, identity *account.Identity) bool {
	check := s.sessionCheckFn
	if check == nil {
		if s.accounts == nil {
			return false
		}
		check = s.accounts.CheckSession
	}
	if cookieToken == "" {
		return false
	}
	current, err := s.sessionRecheck.check(ctx, identity.SessionID, interval, func(ctx context.Context) (*account.Identity, error) {
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return check(checkCtx, cookieToken)
	})
	if err != nil {
		return confirmedAccountSessionInvalid(err)
	}
	return current.SessionID != identity.SessionID || current.UserID != identity.UserID || current.State == account.StateResetRequired
}

// pumpSocket see pumpSocketWithRead; no upstream handling (the read loop discards all upstream bytes).
func (s *Server) pumpSocket(ctx context.Context, connection *websocket.Conn, next func(context.Context) (websocket.MessageType, []byte, func(), error)) {
	s.pumpSocketWithRead(ctx, connection, next, nil)
}

// pumpSocketWithRead writes frames produced by next to the peer serially; the read loop only watches for disconnects
// and answers control frames; when onRead is non-nil, every upstream data frame is synchronously callbacked (still
// inside the read goroutine; it must not write to the connection directly, and writes back must be queued via next's frame source).
// When onRead is nil, all upstream bytes are discarded.
func (s *Server) pumpSocketWithRead(ctx context.Context, connection *websocket.Conn, next func(context.Context) (websocket.MessageType, []byte, func(), error), onRead func(context.Context, websocket.MessageType, []byte)) {
	ctx, cancel := context.WithCancel(ctx)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer cancel()
		if s.readGate != nil {
			s.readGate()
		}
		for {
			messageType, data, err := connection.Read(ctx)
			if err != nil {
				return
			}
			if onRead != nil {
				onRead(ctx, messageType, data)
			}
		}
	}()
	writeGate := make(chan struct{}, 1)
	if s.webSocket.KeepAlive > 0 {
		go s.keepAliveSocket(ctx, connection, writeGate)
	}
	defer func() {
		cancel()
		_ = connection.CloseNow()
		<-readDone
	}()

	for {
		messageType, data, ack, err := next(ctx)
		if err != nil {
			return
		}
		writeGate <- struct{}{}
		writeCtx, writeCancel := context.WithTimeout(ctx, s.webSocket.WriteTimeout)
		err = connection.Write(writeCtx, messageType, data)
		writeCancel()
		<-writeGate
		if err != nil {
			return
		}
		if ack != nil {
			ack()
		}
	}
}

func (s *Server) keepAliveSocket(ctx context.Context, connection *websocket.Conn, writeGate chan struct{}) {
	ticker := time.NewTicker(s.webSocket.KeepAlive)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		select {
		case writeGate <- struct{}{}:
		case <-ctx.Done():
			return
		}
		pingCtx, cancel := context.WithTimeout(ctx, s.webSocket.PingTimeout)
		err := connection.Ping(pingCtx)
		cancel()
		<-writeGate
		if err != nil {
			_ = connection.CloseNow()
			return
		}
	}
}

// sessionRechecker coalesces the periodic session recheck of account sockets per session: concurrent callers share one
// in-flight lookup and a conclusive result (valid or confirmed invalid) is reused for ttl. An entry lives only while
// at least one socket of the session is registered.
type sessionRechecker struct {
	mu      sync.Mutex
	entries map[string]*sessionRecheckEntry
	now     func() time.Time
}

type sessionRecheckEntry struct {
	refs     int
	at       time.Time
	has      bool
	identity *account.Identity
	err      error
	inflight *sessionRecheckCall
}

type sessionRecheckCall struct {
	done     chan struct{}
	identity *account.Identity
	err      error
}

func (c *sessionRechecker) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *sessionRechecker) acquire(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*sessionRecheckEntry)
	}
	e := c.entries[sessionID]
	if e == nil {
		e = &sessionRecheckEntry{}
		c.entries[sessionID] = e
	}
	e.refs++
}

func (c *sessionRechecker) release(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[sessionID]; e != nil {
		if e.refs--; e.refs <= 0 {
			delete(c.entries, sessionID)
		}
	}
}

func (c *sessionRechecker) check(ctx context.Context, sessionID string, ttl time.Duration, lookup func(context.Context) (*account.Identity, error)) (*account.Identity, error) {
	c.mu.Lock()
	e := c.entries[sessionID]
	if e == nil {
		c.mu.Unlock()
		return lookup(ctx)
	}
	if e.has && c.clock().Sub(e.at) < ttl {
		identity, err := e.identity, e.err
		c.mu.Unlock()
		return identity, err
	}
	if call := e.inflight; call != nil {
		c.mu.Unlock()
		select {
		case <-call.done:
			return call.identity, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &sessionRecheckCall{done: make(chan struct{})}
	e.inflight = call
	c.mu.Unlock()

	call.identity, call.err = lookup(ctx)

	c.mu.Lock()
	e.inflight = nil
	if call.err == nil || confirmedAccountSessionInvalid(call.err) {
		e.identity, e.err, e.at, e.has = call.identity, call.err, c.clock(), true
	}
	c.mu.Unlock()
	close(call.done)
	return call.identity, call.err
}
