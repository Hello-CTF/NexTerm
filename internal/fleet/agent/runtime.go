package agent

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/supervisor"
	"github.com/coder/websocket"
)

const (
	syncTimeout         = 30 * time.Second
	helperEnsureTimeout = 30 * time.Second
	retrySleep          = 2 * time.Second
	channelMaxBackoff   = time.Minute
)

type Options struct {
	Store   *Store
	DataDir string
	Prober  *Prober
	Manager ServiceManager

	Executable   string
	StateDir     string
	Collector    Collector
	EnsureHelper func(ctx context.Context) error
	Bridge       BridgeSpawner
	Logger       *slog.Logger
	Now          func() time.Time
}

type Runtime struct {
	store        *Store
	dataDir      string
	prober       *Prober
	manager      ServiceManager
	collector    Collector
	ensureHelper func(ctx context.Context) error
	bridge       BridgeSpawner
	stateDir     string
	executable   string
	logger       *slog.Logger
	now          func() time.Time

	deviceID    string
	secret      string
	platform    string
	appVersion  string
	stateDigest string

	mu           sync.Mutex
	desired      ConfigUpdate
	serviceState ServiceState
	currentURL   string

	runCtx     context.Context
	loopWg     sync.WaitGroup
	revokeCh   chan struct{}
	fatalCh    chan error
	reconfigCh chan struct{}

	revokeOnce sync.Once
	fatalOnce  sync.Once
}

func New(options Options) (*Runtime, error) {
	if options.Store == nil {
		return nil, errors.New("agent store is required")
	}
	if options.Prober == nil {
		return nil, errors.New("agent prober is required")
	}
	if options.Manager == nil {
		return nil, errors.New("agent service manager is required")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	executable := options.Executable
	if executable == "" {
		resolved, err := os.Executable()
		if err != nil {
			return nil, err
		}
		executable = resolved
	}
	stateDir := options.StateDir
	if stateDir == "" {
		stateDir = filepath.Join(options.DataDir, "durable", "supervisor")
	}
	collector := options.Collector
	if collector == nil {
		collector = NewCollector(options.DataDir, now)
	}
	ensureHelper := options.EnsureHelper
	if ensureHelper == nil {
		ensureHelper = func(ctx context.Context) error {
			_, err := supervisor.ConnectHelper(ctx, supervisor.HelperConfig{StateDir: stateDir, Executable: executable})
			return err
		}
	}
	bridge := options.Bridge
	if bridge == nil {
		bridge = SubprocessBridge(executable)
	}
	return &Runtime{
		store:        options.Store,
		dataDir:      options.DataDir,
		prober:       options.Prober,
		manager:      options.Manager,
		collector:    collector,
		ensureHelper: ensureHelper,
		bridge:       bridge,
		stateDir:     stateDir,
		executable:   executable,
		logger:       logger,
		now:          now,
		revokeCh:     make(chan struct{}),
		fatalCh:      make(chan error, 1),
		reconfigCh:   make(chan struct{}, 1),
	}, nil
}

// Run drives the agent until the context ends, the device is revoked, or a
// fatal protocol mismatch surfaces. It returns ErrRevoked on revocation and
// ErrProtocolMismatch when the server speaks an incompatible control
// protocol; a plain context cancellation is a clean stop (nil).
func (r *Runtime) Run(ctx context.Context) error {
	config, err := r.store.Load()
	if err != nil {
		return err
	}
	r.deviceID = config.DeviceID
	r.secret = config.Secret
	r.platform = config.Platform
	r.appVersion = config.AppVersion
	r.mu.Lock()
	r.desired = ConfigUpdate{
		DesiredAutostart:  config.DesiredAutostart,
		MetricsIntervalMS: clampMetricsInterval(config.MetricsIntervalMS),
		TerminalEnabled:   config.TerminalEnabled,
	}
	r.currentURL = config.CurrentURL
	r.mu.Unlock()
	r.computeStateDigest()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.runCtx = runCtx

	client, err := r.selectEndpoint(runCtx)
	if err != nil {
		return err
	}
	r.notifyCurrentURL(runCtx, client)

	r.reconcileService(runCtx)
	if r.desiredSnapshot().TerminalEnabled {
		r.ensureHelperOnce(runCtx)
	}

	r.loopWg.Add(1)
	go func() {
		defer r.loopWg.Done()
		r.channelLoop(runCtx)
	}()

	interval := time.Duration(r.desiredSnapshot().MetricsIntervalMS) * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-runCtx.Done():
			cancel()
			r.loopWg.Wait()
			return nil
		case <-r.revokeCh:
			cancel()
			r.loopWg.Wait()
			return ErrRevoked
		case err := <-r.fatalCh:
			cancel()
			r.loopWg.Wait()
			return err
		case <-r.reconfigCh:
			r.reconcileService(runCtx)
			if updated := time.Duration(r.desiredSnapshot().MetricsIntervalMS) * time.Millisecond; updated != interval {
				interval = updated
				ticker.Reset(interval)
			}
		case <-ticker.C:
			r.reconcileService(runCtx)
			r.syncOnce(runCtx)
		}
	}
}

func (r *Runtime) hello(bridgeID string) HelloMessage {
	return HelloMessage{
		Type:        "hello",
		Protocol:    ProtocolVersion,
		DeviceID:    r.deviceID,
		Secret:      r.secret,
		Platform:    r.platform,
		AppVersion:  r.appVersion,
		BridgeID:    bridgeID,
		StateDigest: r.stateDigest,
	}
}

// computeStateDigest 计算并缓存设备端 supervisor 状态摘要, 供控制通道 hello
// 上报; 失败只降级分享代管能力, 不影响 agent 主流程。
func (r *Runtime) computeStateDigest() {
	identity, err := currentIdentity()
	if err != nil {
		r.logger.Warn("resolve user identity failed; state digest unavailable", "error", err)
		return
	}
	absolute, err := filepath.Abs(r.stateDir)
	if err != nil {
		r.logger.Warn("resolve state dir failed; state digest unavailable", "error", err)
		return
	}
	digest, err := supervisor.StateDigest(absolute, identity)
	if err != nil {
		r.logger.Warn("compute state digest failed; state digest unavailable", "error", err)
		return
	}
	r.stateDigest = digest
}

func (r *Runtime) desiredSnapshot() ConfigUpdate {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.desired
}

func (r *Runtime) applyDesired(update ConfigUpdate) {
	if update.MetricsIntervalMS <= 0 {
		update.MetricsIntervalMS = defaultMetricsIntervalMS
	}
	r.mu.Lock()
	changed := r.desired != update
	r.desired = update
	r.mu.Unlock()
	if changed {
		select {
		case r.reconfigCh <- struct{}{}:
		default:
		}
	}
}

func (r *Runtime) serviceStateSnapshot() *ServiceState {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := r.serviceState
	return &snapshot
}

func (r *Runtime) revoke() {
	r.revokeOnce.Do(func() {
		r.logger.Warn("device revoked by server; agent stopping")
		close(r.revokeCh)
	})
}

func (r *Runtime) fatal(err error) {
	r.fatalOnce.Do(func() {
		r.fatalCh <- err
	})
}

func (r *Runtime) selectEndpoint(ctx context.Context) (*EndpointClient, error) {
	for {
		client, err := r.prober.Select(ctx)
		if err == nil {
			return client, nil
		}
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		delay := retrySleep
		var noEndpoint *NoEndpointError
		if errors.As(err, &noEndpoint) && noEndpoint.RetryAfter > delay {
			delay = noEndpoint.RetryAfter
		}
		r.logger.Warn("no healthy server endpoint; retrying", "error", err, "delay", delay)
		if !sleepContext(ctx, delay) {
			return nil, context.Cause(ctx)
		}
	}
}

func (r *Runtime) syncOnce(ctx context.Context) {
	client := r.prober.Current()
	if client == nil {
		selected, err := r.prober.Select(ctx)
		if err != nil {
			r.logger.Warn("agent sync skipped: no healthy endpoint", "error", err)
			return
		}
		client = selected
	}
	response, err := r.postSync(ctx, client)
	if err != nil {
		if isForbidden(err) {
			r.revoke()
			return
		}
		r.logger.Warn("agent sync failed", "url", client.BaseURL(), "error", err)
		r.prober.ReportFailure(client)
		selected, selectErr := r.prober.Select(ctx)
		if selectErr != nil {
			r.logger.Warn("failover selection failed", "error", selectErr)
			return
		}
		if selected == client {
			return
		}
		response, err = r.postSync(ctx, selected)
		if err != nil {
			if isForbidden(err) {
				r.revoke()
				return
			}
			r.logger.Warn("agent sync retry failed", "url", selected.BaseURL(), "error", err)
			r.prober.ReportFailure(selected)
			return
		}
		client = selected
	}
	r.prober.ReportSuccess(client)
	r.notifyCurrentURL(ctx, client)
	r.applyDesired(ConfigUpdate{
		DesiredAutostart:  response.DesiredAutostart,
		MetricsIntervalMS: clampMetricsInterval(response.MetricsIntervalMS),
		TerminalEnabled:   response.TerminalEnabled,
	})
}

func (r *Runtime) postSync(ctx context.Context, client *EndpointClient) (*SyncResponse, error) {
	request := SyncRequest{
		DeviceID:     r.deviceID,
		Secret:       r.secret,
		ServiceState: r.serviceStateSnapshot(),
	}
	if sample, err := r.collector.Collect(); err == nil {
		request.Sample = &sample
	} else if !errors.Is(err, ErrMetricsUnsupported) {
		r.logger.Warn("metrics collection failed", "error", err)
	}
	syncCtx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()
	return client.Sync(syncCtx, request)
}

func (r *Runtime) notifyCurrentURL(ctx context.Context, client *EndpointClient) {
	base := client.BaseURL()
	r.mu.Lock()
	last := r.currentURL
	r.mu.Unlock()
	if base == last {
		return
	}
	reason := "failover"
	if last == "" {
		reason = "initial"
	}
	reportCtx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()
	if err := client.ReportCurrentURL(reportCtx, r.deviceID, r.secret, base, reason); err != nil {
		if isForbidden(err) {
			r.revoke()
			return
		}
		r.logger.Warn("failover report failed", "url", base, "error", err)
		return
	}
	r.mu.Lock()
	r.currentURL = base
	r.mu.Unlock()
	if config, err := r.store.Load(); err == nil {
		config.CurrentURL = base
		if err := r.store.Save(config); err != nil {
			r.logger.Warn("persist current URL failed", "error", err)
		}
	}
}

func (r *Runtime) reconcileService(ctx context.Context) {
	desired := r.desiredSnapshot()
	status := r.manager.Status(ctx)
	switch {
	case desired.DesiredAutostart && (!status.Installed || !status.Enabled):
		r.logger.Info("installing agent autostart service")
		if err := r.manager.Install(ctx, r.executable, r.dataDir); err != nil {
			r.logger.Warn("autostart install failed", "error", err)
			status.LastError = err.Error()
		} else {
			status = r.manager.Status(ctx)
		}
	case !desired.DesiredAutostart && (status.Installed || status.Enabled):
		r.logger.Info("removing agent autostart service")
		if err := r.manager.Uninstall(ctx); err != nil {
			r.logger.Warn("autostart uninstall failed", "error", err)
			status.LastError = err.Error()
		} else {
			status = r.manager.Status(ctx)
		}
	}
	status.LastReconcileAt = r.now().UnixMilli()
	r.mu.Lock()
	r.serviceState = status
	r.mu.Unlock()
}

func (r *Runtime) ensureHelperOnce(ctx context.Context) {
	ensureCtx, cancel := context.WithTimeout(ctx, helperEnsureTimeout)
	defer cancel()
	if err := r.ensureHelper(ensureCtx); err != nil {
		r.logger.Warn("supervisor helper unavailable; terminal bridging disabled until it recovers", "error", err)
	}
}

func (r *Runtime) channelLoop(ctx context.Context) {
	backoff := retrySleep
	for {
		if ctx.Err() != nil {
			return
		}
		client, err := r.selectEndpoint(ctx)
		if err != nil {
			return
		}
		channel, err := DialChannel(ctx, client.WSURL(), r.hello(""), client.Insecure(), defaultWSKeepAlive, defaultWSPingTimeout)
		if err != nil {
			if isForbidden(err) {
				r.revoke()
				return
			}
			var serverError *ServerError
			if errors.As(err, &serverError) && serverError.Code == "version_mismatch" {
				r.fatal(ErrProtocolMismatch)
				return
			}
			r.logger.Warn("control channel dial failed", "url", client.BaseURL(), "error", err)
			r.prober.ReportFailure(client)
			if !sleepContext(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		r.prober.ReportSuccess(client)
		r.notifyCurrentURL(ctx, client)
		backoff = retrySleep
		err = channel.Run(ctx, channelHandlers{
			onConfig: r.applyDesired,
			onBridge: r.handleBridge,
			onRevoke: r.revoke,
		})
		_ = channel.Close()
		if ctx.Err() != nil || errors.Is(err, ErrRevoked) {
			return
		}
		if errors.Is(err, ErrProtocolMismatch) {
			r.fatal(err)
			return
		}
		r.logger.Warn("control channel lost; reconnecting", "error", err)
		r.prober.ReportFailure(client)
		if !sleepContext(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff)
	}
}

func (r *Runtime) handleBridge(bridgeID string) {
	if !r.desiredSnapshot().TerminalEnabled {
		r.logger.Warn("bridge requested but terminal access is disabled", "bridge_id", bridgeID)
		return
	}
	r.loopWg.Add(1)
	go func() {
		defer r.loopWg.Done()
		ctx := r.runCtx
		ensureCtx, cancel := context.WithTimeout(ctx, helperEnsureTimeout)
		err := r.ensureHelper(ensureCtx)
		cancel()
		if err != nil {
			r.logger.Warn("bridge aborted: supervisor helper unavailable", "bridge_id", bridgeID, "error", err)
			return
		}
		client := r.prober.Current()
		if client == nil {
			r.logger.Warn("bridge aborted: no healthy endpoint", "bridge_id", bridgeID)
			return
		}
		conn, err := DialBridge(ctx, client.WSURL(), r.hello(bridgeID), client.Insecure())
		if err != nil {
			if isForbidden(err) {
				r.revoke()
				return
			}
			r.logger.Warn("bridge dial failed", "bridge_id", bridgeID, "error", err)
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
		_ = BridgePipe(ctx, conn, r.bridge, r.stateDir)
	}()
}

func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > channelMaxBackoff || next <= 0 {
		return channelMaxBackoff
	}
	return next
}

func sleepContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
