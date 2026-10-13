package agent

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
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
	Upgrader     *Upgrader
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
	upgrader     *Upgrader
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
	// 内嵌分发 (nexterm-server agent, cliServicePrefix 非空) 不做自升级:
	// 替换会拿 agent 资产覆盖 server 二进制。该路径保持 ErrProtocolMismatch。
	upgrader := options.Upgrader
	if upgrader == nil && len(cliServicePrefix) == 0 {
		upgrader = defaultUpgrader(executable)
	}
	return &Runtime{
		store:        options.Store,
		dataDir:      options.DataDir,
		prober:       options.Prober,
		manager:      options.Manager,
		collector:    collector,
		ensureHelper: ensureHelper,
		bridge:       bridge,
		upgrader:     upgrader,
		stateDir:     stateDir,
		executable:   executable,
		logger:       logger,
		now:          now,
		revokeCh:     make(chan struct{}),
		fatalCh:      make(chan error, 1),
		reconfigCh:   make(chan struct{}, 1),
	}, nil
}

// Run drives the agent until the context ends, the device is revoked, or the
// control channel protocol mismatches. It returns ErrRevoked on revocation; on
// protocol mismatch it first attempts a self-upgrade to the server-reported
// version and returns errUpgradeStaged once the deferred replace is scheduled,
// or ErrProtocolMismatch when self-upgrade is unavailable or exhausted. A
// plain context cancellation is a clean stop (nil).
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
				r.selfUpgrade(ctx, serverError.ServerVersion)
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
			r.selfUpgrade(ctx, mismatchServerVersion(err))
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

func mismatchServerVersion(err error) string {
	var mismatch *ProtocolMismatchError
	if errors.As(err, &mismatch) {
		return mismatch.ServerVersion
	}
	return ""
}

// selfUpgrade 在协议不匹配时尝试把本机 agent 升级到 server 上报的版本。
// 升级暂存成功 -> fatal(errUpgradeStaged) 退出让替换脚本接管; 自升级不可用
// (内嵌分发) 或 server 未上报版本 (旧 server) 或重试耗尽 -> fatal
// (ErrProtocolMismatch) 停留在待人工状态, 日志明确区分两条路径。
func (r *Runtime) selfUpgrade(ctx context.Context, serverVersion string) {
	if r.upgrader == nil {
		r.logger.Warn("control channel protocol mismatch; self-upgrade unavailable for this distribution, reinstall with install-device.sh")
		r.fatal(ErrProtocolMismatch)
		return
	}
	if serverVersion == "" {
		r.logger.Warn("control channel protocol mismatch; server did not report its version, reinstall with install-device.sh")
		r.fatal(ErrProtocolMismatch)
		return
	}
	r.logger.Info("control channel protocol mismatch; attempting self-upgrade from the official release source", "target_version", serverVersion)
	if err := r.upgrader.Upgrade(ctx, serverVersion); err != nil {
		r.logger.Error("self-upgrade failed; staying on the current binary for manual intervention", "target_version", serverVersion, "error", err)
		r.fatal(ErrProtocolMismatch)
		return
	}
	r.logger.Info("self-upgrade staged; deferred replace will swap the binary and restart the service", "target_version", serverVersion)
	r.fatal(errUpgradeStaged)
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
