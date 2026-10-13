package production

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
	"github.com/coder/websocket"
)

// Desktop read-only preview relay (phase 3 b): relay a local terminal tab's output through a server-side remote
// session shell into an anonymously openable read-only preview link. preview_share_create creates the shell + link +
// relay goroutine: AttachSpectator subscribes to the local tab (the subscription's first frame is the local screen
// snapshot, skipped - the shell produces its own snapshot for its viewers, and the relay only pushes incremental
// binary output frames), and output is written to the feed WS verbatim; on tab exit/close/account switch it sends
// the end control frame and DELETEs the shell. A disconnected feed redials with backoff to resume feeding
// (the shell is kept server-side), and exhausting the redial budget finishes the relay. Only one active relay is
// allowed per tab at a time; repeated creates return the existing link held in memory (the token is not persisted
// and is only available while the relay lives).

const (
	// previewRelayPollInterval is the idle heartbeat: the spectator channel carries no control events,
	// so tab exit/resize/account switch are all detected in the heartbeat (same pattern as pumpSharePreview).
	previewRelayPollInterval = time.Second
	// previewRelayRedialBudget is the total redial budget after a feed disconnect: within budget the hub subscription
	// queue (512 frames/16MiB) absorbs output without losing frames; on exhaustion the relay finishes, avoiding long-term backpressure on the local terminal.
	previewRelayRedialBudget = 45 * time.Second
	previewRelayRedialMin    = time.Second
	previewRelayRedialMax    = 15 * time.Second
	previewRelayWriteTimeout = 10 * time.Second
	previewRelayEndTimeout   = 3 * time.Second
	previewRelayStopTimeout  = 10 * time.Second
)

// previewRelayEndFrame is the control frame for the feeder actively finishing: the server closes the shell's write
// direction per process-exit semantics (snapshot preserved); the following DELETE is what terminates the shell with its preview streams.
var previewRelayEndFrame = []byte(`{"type":"end"}`)

// previewRelayRemote is the relay's minimal dependency on the remote proxy surface; *syncservice.RemoteProxy implements it.
type previewRelayRemote interface {
	RemoteSessionCreate(ctx context.Context, title, assetID string, cols, rows uint32) (*syncservice.RemoteSession, error)
	CreatePreviewLink(ctx context.Context, sessionID string, ttlMS int64) (*syncservice.RemotePreviewLinkCreate, error)
	RemoteSessionDelete(ctx context.Context, id string) (syncservice.RemoteOK, error)
	RemoteSessionFeedDial(ctx context.Context, sessionID string) (*websocket.Conn, error)
	ServerBaseURL(ctx context.Context) (string, error)
}

// previewRelaySyncStatus is the relay's probe surface for account state; *syncservice.Service implements it.
type previewRelaySyncStatus interface {
	Status(ctx context.Context) (syncservice.Status, error)
}

// previewRelayFeed is the minimal write surface of a remote feed connection; *websocket.Conn implements it directly, and tests
// inject scripted fake connections with write failures to drive the redial path.
type previewRelayFeed interface {
	Write(ctx context.Context, messageType websocket.MessageType, data []byte) error
	Close(code websocket.StatusCode, reason string) error
	CloseNow() error
}

type previewRelayFeedDial func(ctx context.Context, shellID string) (previewRelayFeed, error)

type previewRelay struct {
	tabID     string
	channelID string
	shellID   string
	token     string
	url       string
	username  string
	base      string
	cols      uint32
	rows      uint32
	ctx       context.Context
	cancel    context.CancelFunc
	receiver  *hub.Receiver
	// feed is the currently active feed connection held by the run goroutine (updated after redials); only run reads and
	// writes it, and finish is called by run's defer in the same goroutine, so no locking is needed.
	feed previewRelayFeed
	once sync.Once
}

func (r *previewRelay) result() *previewShareCreateResult {
	return &previewShareCreateResult{Token: r.token, URL: r.url, SessionID: r.shellID}
}

type previewRelayManager struct {
	sessions   *session.Manager
	remote     previewRelayRemote
	syncStatus previewRelaySyncStatus
	dialFeed   previewRelayFeedDial
	logger     *slog.Logger

	pollInterval time.Duration
	redialBudget time.Duration

	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	relays map[string]*previewRelay
	closed bool
	wg     sync.WaitGroup
}

func newPreviewRelayManager(sessions *session.Manager, remote previewRelayRemote, syncStatus previewRelaySyncStatus, logger *slog.Logger) *previewRelayManager {
	if logger == nil {
		logger = slog.Default()
	}
	manager := &previewRelayManager{
		sessions: sessions, remote: remote, syncStatus: syncStatus, logger: logger,
		pollInterval: previewRelayPollInterval, redialBudget: previewRelayRedialBudget,
		relays: make(map[string]*previewRelay),
	}
	if remote != nil {
		manager.dialFeed = func(ctx context.Context, shellID string) (previewRelayFeed, error) {
			conn, err := remote.RemoteSessionFeedDial(ctx, shellID)
			if err != nil {
				return nil, err
			}
			return conn, nil
		}
	}
	return manager
}

func (m *previewRelayManager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.ctx, m.cancel = context.WithCancel(ctx)
	return nil
}

// Create builds a read-only preview for a local tab: register the remote shell -> create the preview link -> dial the feed ->
// subscribe to local output and start the relay. Any step failing recycles in order (the shell is always deleted, and the
// preview link expires naturally via TTL); if the tab already has an active relay, the existing link is returned directly.
func (m *previewRelayManager) Create(ctx context.Context, tabID string, ttlMS int64) (*previewShareCreateResult, error) {
	if m.sessions == nil || m.remote == nil || m.dialFeed == nil {
		return nil, ipc.NewError(ipc.CodeUnsupported, "只读预览仅在桌面端可用")
	}
	tab, err := m.sessions.Tab(tabID)
	if err != nil {
		return nil, ipc.NewError(ipc.CodeNotFound, "终端标签页不存在或已关闭")
	}
	info := tab.Info()
	if info.Exited {
		return nil, ipc.NewError(ipc.CodeBadParam, "终端已退出，无法创建只读预览")
	}

	m.mu.Lock()
	if existing := m.relays[tabID]; existing != nil {
		result := existing.result()
		m.mu.Unlock()
		return result, nil
	}
	if m.closed || m.ctx == nil {
		m.mu.Unlock()
		return nil, ipc.NewError(ipc.CodeDisconnected, "预览中继服务未就绪")
	}
	m.mu.Unlock()

	// Title/asset metadata comes from the tab's session info (the remote shell is display metadata only and does not participate in asset dedup).
	title, assetID := "", ""
	if owner, err := m.sessions.Session(tab.SessionID); err == nil {
		asset := owner.Asset()
		title, assetID = asset.Name, asset.ID
	}

	shell, err := m.remote.RemoteSessionCreate(ctx, title, assetID, info.Cols, info.Rows)
	if err != nil {
		return nil, err
	}
	cleanupShell := true
	defer func() {
		if cleanupShell {
			deleteCtx, cancel := context.WithTimeout(context.Background(), previewRelayStopTimeout)
			defer cancel()
			if _, err := m.remote.RemoteSessionDelete(deleteCtx, shell.SessionID); err != nil {
				m.logger.Warn("preview relay: cleanup remote session failed", "shell", shell.SessionID, "error", err)
			}
		}
	}()

	link, err := m.remote.CreatePreviewLink(ctx, shell.SessionID, ttlMS)
	if err != nil {
		return nil, err
	}
	base, err := m.remote.ServerBaseURL(ctx)
	if err != nil {
		return nil, err
	}
	feed, err := m.dialFeed(ctx, shell.SessionID)
	if err != nil {
		return nil, err
	}

	username := ""
	if m.syncStatus != nil {
		if status, err := m.syncStatus.Status(ctx); err == nil {
			username = status.Username
		}
	}

	relayCtx, cancel := context.WithCancel(m.ctx)
	relay := &previewRelay{
		tabID: tabID, channelID: ids.New(), shellID: shell.SessionID,
		token: link.Token, url: base + "/share/preview/" + link.Token,
		username: username, base: base,
		cols: info.Cols, rows: info.Rows,
		ctx: relayCtx, cancel: cancel,
	}
	// Subscribe to local output: the first snapshot frame is not forwarded (the shell produces its own snapshot for its viewers); only incremental binary frames are pushed.
	if _, err := m.sessions.AttachSpectator(relayCtx, tabID, session.SpectateOptions{
		ChannelID: relay.channelID, ClientID: "preview-relay",
	}); err != nil {
		cancel()
		_ = feed.CloseNow()
		return nil, err
	}
	receiver, err := m.sessions.Bind(relay.channelID)
	if err != nil {
		cancel()
		_ = feed.CloseNow()
		_ = m.sessions.DetachChannel(relay.channelID)
		return nil, err
	}
	relay.receiver = receiver

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		_ = receiver.Close()
		_ = feed.CloseNow()
		_ = m.sessions.DetachChannel(relay.channelID)
		return nil, ipc.NewError(ipc.CodeDisconnected, "预览中继服务未就绪")
	}
	if existing := m.relays[tabID]; existing != nil {
		// Concurrent create race: the later arrival yields, and its self-built shell is recycled by defer.
		result := existing.result()
		m.mu.Unlock()
		cancel()
		_ = receiver.Close()
		_ = feed.CloseNow()
		_ = m.sessions.DetachChannel(relay.channelID)
		return result, nil
	}
	m.relays[tabID] = relay
	m.wg.Add(1)
	m.mu.Unlock()
	cleanupShell = false
	go relay.run(m, feed)
	return relay.result(), nil
}

// Active returns the remote shell id of the tab's currently active relay (the desktop menu filters "in-progress previews" by it);
// empty when there is no active relay.
func (m *previewRelayManager) Active(tabID string) previewShareActiveResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	if relay := m.relays[tabID]; relay != nil {
		return previewShareActiveResult{SessionID: relay.shellID}
	}
	return previewShareActiveResult{}
}

func (m *previewRelayManager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// run is the relay goroutine proper: it pumps output until the tab ends/closes, the account switches, the service stops, or
// the feed is unrecoverable; exit always goes through finish for cleanup (end frame + shell delete + unsubscribe).
func (r *previewRelay) run(m *previewRelayManager, feed previewRelayFeed) {
	defer m.wg.Done()
	defer r.cancel()
	r.feed = feed
	defer func() { m.finish(r, r.feed) }()
	r.pump(m, feed)
}

func (r *previewRelay) pump(m *previewRelayManager, feed previewRelayFeed) {
	cols, rows := r.cols, r.rows
	var pending *hub.Frame
	for {
		if r.ctx.Err() != nil {
			return
		}
		if pending == nil {
			frameCtx, cancel := context.WithTimeout(r.ctx, m.pollInterval)
			frame, err := r.receiver.Next(frameCtx)
			cancel()
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					if !m.pollRelay(r, feed, &cols, &rows) {
						return
					}
					continue
				}
				// Channel closed = tab closed/manager stopped/unsubscribed; finish up.
				return
			}
			if frame.Kind != hub.FrameBinary {
				// Snapshot and other JSON frames are not forwarded: the shell's viewers take snapshots from the shell's terminal model.
				_ = r.receiver.Ack(frame.Sequence)
				continue
			}
			pending = &frame
		}
		writeCtx, cancel := context.WithTimeout(r.ctx, previewRelayWriteTimeout)
		err := feed.Write(writeCtx, websocket.MessageBinary, pending.Data)
		cancel()
		if err == nil {
			_ = r.receiver.Ack(pending.Sequence)
			pending = nil
			continue
		}
		_ = feed.CloseNow()
		feed = r.redial(m, cols, rows)
		r.feed = feed
		if feed == nil {
			return
		}
	}
}

// redial redials the feed with backoff within budget (the shell is kept server-side and feeding can resume); after a successful
// redial it resends the current size (resize control frames during the disconnect may have been lost), and returns nil on budget
// exhaustion or service stop.
func (r *previewRelay) redial(m *previewRelayManager, cols, rows uint32) previewRelayFeed {
	backoff := previewRelayRedialMin
	deadline := time.Now().Add(m.redialBudget)
	for {
		feed, err := m.dialFeed(r.ctx, r.shellID)
		if err == nil {
			m.writeResize(r, feed, cols, rows)
			return feed
		}
		if r.ctx.Err() != nil || time.Now().Add(backoff).After(deadline) {
			return nil
		}
		select {
		case <-r.ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > previewRelayRedialMax {
			backoff = previewRelayRedialMax
		}
	}
}

// pollRelay is the idle heartbeat: tab gone/exited or account switch -> false (finish up); resize -> resend the
// control frame (best effort; write failures are covered by the output path's redial).
func (m *previewRelayManager) pollRelay(r *previewRelay, feed previewRelayFeed, cols, rows *uint32) bool {
	tab, err := m.sessions.Tab(r.tabID)
	if err != nil {
		return false
	}
	info := tab.Info()
	if info.Exited {
		return false
	}
	if info.Cols != *cols || info.Rows != *rows {
		*cols, *rows = info.Cols, info.Rows
		m.writeResize(r, feed, info.Cols, info.Rows)
	}
	return m.accountIntact(r)
}

// accountIntact detects account switch/disconnect: a cleared link, or a changed username or server address, means the relay
// loses its ownership and stops; transient state read failures (vault locked, etc.) do not touch the relay, 401 re-login is
// covered by remoteCall, so LoggedIn flapping is not judged here.
func (m *previewRelayManager) accountIntact(r *previewRelay) bool {
	if m.syncStatus != nil {
		status, err := m.syncStatus.Status(r.ctx)
		if err == nil && (!status.Configured || (r.username != "" && status.Username != r.username)) {
			return false
		}
	}
	base, err := m.remote.ServerBaseURL(r.ctx)
	if err == nil && base != r.base {
		return false
	}
	return true
}

func (m *previewRelayManager) writeResize(r *previewRelay, feed previewRelayFeed, cols, rows uint32) {
	payload, err := json.Marshal(map[string]any{"type": "resize", "cols": cols, "rows": rows})
	if err != nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(r.ctx, previewRelayWriteTimeout)
	defer cancel()
	_ = feed.Write(writeCtx, websocket.MessageText, payload)
}

// finish is the unified (idempotent) cleanup: remove from map -> unsubscribe local output -> end frame -> close feed -> DELETE the shell.
// Removing from the map precedes network cleanup, guaranteeing new creates during cleanup get a brand-new relay; end/delete use
// independent timeout contexts (the relay ctx is already canceled), failures are only logged, and the server-side idle reaping is the backstop.
func (m *previewRelayManager) finish(r *previewRelay, feed previewRelayFeed) {
	r.once.Do(func() {
		m.mu.Lock()
		if m.relays[r.tabID] == r {
			delete(m.relays, r.tabID)
		}
		m.mu.Unlock()
		if r.receiver != nil {
			_ = r.receiver.Close()
		}
		if err := m.sessions.DetachChannel(r.channelID); err != nil {
			m.logger.Debug("preview relay: detach channel failed", "tab", r.tabID, "error", err)
		}
		if feed != nil {
			endCtx, cancel := context.WithTimeout(context.Background(), previewRelayEndTimeout)
			_ = feed.Write(endCtx, websocket.MessageText, previewRelayEndFrame)
			cancel()
			_ = feed.Close(websocket.StatusNormalClosure, "preview relay stopped")
		}
		deleteCtx, cancel := context.WithTimeout(context.Background(), previewRelayStopTimeout)
		defer cancel()
		if _, err := m.remote.RemoteSessionDelete(deleteCtx, r.shellID); err != nil {
			m.logger.Warn("preview relay: delete remote session failed", "shell", r.shellID, "error", err)
		}
	})
}

var _ Component = (*previewRelayManager)(nil)

type previewShareCreateRequest struct {
	TabID string `json:"tabId"`
	TTLMS int64  `json:"ttlMs"`
}

// previewShareCreateResult's token is delivered only with the create response on the spot, for building/displaying the public
// URL, and is never persisted; sessionId is the remote shell id, which the desktop menu uses to filter "in-progress previews".
type previewShareCreateResult struct {
	Token     string `json:"token"`
	URL       string `json:"url"`
	SessionID string `json:"sessionId"`
}

type previewShareActiveRequest struct {
	TabID string `json:"tabId"`
}

type previewShareActiveResult struct {
	SessionID string `json:"sessionId,omitempty"`
}

func registerPreviewRelayCommands(dispatcher *ipc.Dispatcher, relay *previewRelayManager) error {
	registrations := []func() error{
		func() error {
			return ipc.RegisterNested(dispatcher, "preview_share_create", func(ctx context.Context, _ *ipc.Call, input previewShareCreateRequest) (*previewShareCreateResult, error) {
				if input.TabID == "" || input.TTLMS <= 0 {
					return nil, ipc.NewError(ipc.CodeBadParam, "tabId/ttlMs 不能为空")
				}
				return relay.Create(ctx, input.TabID, input.TTLMS)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "preview_share_active", func(ctx context.Context, _ *ipc.Call, input previewShareActiveRequest) (previewShareActiveResult, error) {
				if input.TabID == "" {
					return previewShareActiveResult{}, ipc.NewError(ipc.CodeBadParam, "tabId 不能为空")
				}
				return relay.Active(input.TabID), nil
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}
