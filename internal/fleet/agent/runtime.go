package agent

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/version"
	"github.com/coder/websocket"
)

const (
	syncTimeout         = 30 * time.Second
	helperEnsureTimeout = 30 * time.Second
	retrySleep          = 2 * time.Second
	channelMaxBackoff   = time.Minute
	// The owner account being disabled can last hours, so the wait is longer and capped higher than a network blip.
	ownerMinBackoff = 30 * time.Second
	ownerMaxBackoff = 10 * time.Minute
	// serviceRecheckCycles is how many reconcile cycles reuse a settled service status before the manager is asked again.
	serviceRecheckCycles = 5
	// reconnectJitter spreads reconnect delays by +-20% so a fleet that lost its server does not retry in lockstep.
	reconnectJitter = 0.2
)

// ConfigStore holds the enrolled device config. *Store keeps it in a file;
// a desktop keeps it in its credential vault.
type ConfigStore interface {
	Load() (*Config, error)
	Save(config *Config) error
}

type Options struct {
	Store   ConfigStore
	DataDir string
	Prober  *Prober
	Manager ServiceManager

	Executable   string
	StateDir     string
	Collector    Collector
	EnsureHelper func(ctx context.Context) error
	Bridge       BridgeSpawner
	Upgrader     *Upgrader
	// NoSelfUpgrade keeps a protocol mismatch fatal instead of replacing the
	// executable: a desktop updates itself through its own updater.
	NoSelfUpgrade bool
	// OnChannel, when set, is called with true once the control channel is
	// up and with false when it is lost.
	OnChannel func(connected bool)
	Logger    *slog.Logger
	Now       func() time.Time
	// Sleep, when set, replaces the real wait between owner_unavailable retries (tests).
	Sleep func(ctx context.Context, d time.Duration) bool
}

type Runtime struct {
	store        ConfigStore
	dataDir      string
	prober       *Prober
	manager      ServiceManager
	collector    Collector
	ensureHelper func(ctx context.Context) error
	bridge       BridgeSpawner
	upgrader     *Upgrader
	onChannel    func(connected bool)
	stateDir     string
	executable   string
	logger       *slog.Logger
	now          func() time.Time
	// jitter returns a uniform value in [0,1) used to spread reconnect delays; tests inject a fixed source.
	jitter func() float64
	// sleep waits between owner_unavailable retries; tests inject a fake that records the delay.
	sleep func(ctx context.Context, d time.Duration) bool

	// owner tracks the temporary owner_unavailable state; guarded by mu.
	owner ownerWait

	// The last settled service manager Status is reused so the manager (systemctl, schtasks) is not forked on every sync:
	// it is re-read when the desired autostart flips or after serviceRecheck reconcile cycles (default serviceRecheckCycles),
	// which bounds how long external drift stays undetected.
	serviceRecheck        int
	serviceSkipped        int
	serviceCheckedDesired bool
	serviceChecked        bool

	deviceID    string
	secret      string
	platform    string
	appVersion  string
	stateDigest string

	mu           sync.Mutex
	desired      ConfigUpdate
	serviceState ServiceState
	currentURL   string
	// purposeHandlers serves non-terminal bridge purposes; terminal is built in and never enters the map.
	purposeHandlers map[string]PurposeHandler

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
		bridge = DirectHelperBridge
	}
	// Embedded distributions (nexterm-server agent, non-empty cliServicePrefix) do not self-upgrade:
	// replacing would overwrite the server binary with the agent asset. This path keeps ErrProtocolMismatch.
	upgrader := options.Upgrader
	if upgrader == nil && len(cliServicePrefix) == 0 && !options.NoSelfUpgrade {
		upgrader = defaultUpgrader(executable)
	}
	return &Runtime{
		store:           options.Store,
		dataDir:         options.DataDir,
		prober:          options.Prober,
		manager:         options.Manager,
		collector:       collector,
		ensureHelper:    ensureHelper,
		bridge:          bridge,
		upgrader:        upgrader,
		onChannel:       options.OnChannel,
		stateDir:        stateDir,
		executable:      executable,
		logger:          logger,
		now:             now,
		sleep:           options.Sleep,
		purposeHandlers: make(map[string]PurposeHandler),
		revokeCh:        make(chan struct{}),
		fatalCh:         make(chan error, 1),
		reconfigCh:      make(chan struct{}, 1),
	}, nil
}

// effectiveAppVersion reports the running build's version when it is a real release build, so a manually
// swapped binary self-reports; dev builds keep the enroll-time config value.
func effectiveAppVersion(configVersion string) string {
	if version.Version != "" && version.Version != "dev" {
		return version.Version
	}
	return configVersion
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
	r.appVersion = effectiveAppVersion(config.AppVersion)
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
	r.clearOwnerMarker()
	defer r.clearOwnerMarker()

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

func (r *Runtime) hello(bridgeID, purpose string) HelloMessage {
	return HelloMessage{
		Type:         "hello",
		Protocol:     ProtocolVersion,
		DeviceID:     r.deviceID,
		Secret:       r.secret,
		Platform:     r.platform,
		AppVersion:   r.appVersion,
		BridgeID:     bridgeID,
		Purpose:      purpose,
		StateDigest:  r.stateDigest,
		Capabilities: r.capabilities(),
	}
}

// capabilities reports the bridge purposes this agent serves: terminal always, plus every registered purpose handler.
func (r *Runtime) capabilities() []string {
	r.mu.Lock()
	extra := make([]string, 0, len(r.purposeHandlers))
	for purpose := range r.purposeHandlers {
		extra = append(extra, purpose)
	}
	r.mu.Unlock()
	sort.Strings(extra)
	return append([]string{PurposeTerminal}, extra...)
}

// computeStateDigest computes and caches the device-side supervisor state digest for the control channel hello
// report; failure only degrades the managed-share capability and does not affect the agent's main flow.
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
		serverGiven := false
		var noEndpoint *NoEndpointError
		if errors.As(err, &noEndpoint) && noEndpoint.RetryAfter > delay {
			delay = noEndpoint.RetryAfter
			serverGiven = true
		}
		r.logger.Warn("no healthy server endpoint; retrying", "error", err, "delay", delay)
		if !sleepContext(ctx, retryWait(delay, serverGiven, r.jittered(delay))) {
			return nil, context.Cause(ctx)
		}
	}
}

func (r *Runtime) syncOnce(ctx context.Context) {
	if r.ownerWaiting() {
		return
	}
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
		if r.handleDenied(err) {
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
			if r.handleDenied(err) {
				return
			}
			r.logger.Warn("agent sync retry failed", "url", selected.BaseURL(), "error", err)
			r.prober.ReportFailure(selected)
			return
		}
		client = selected
	}
	r.prober.ReportSuccess(client)
	r.ownerRecovered()
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
		if r.handleDenied(err) {
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
	now := r.now()
	r.mu.Lock()
	cached := r.serviceState
	recheck := r.serviceRecheck
	if recheck <= 0 {
		recheck = serviceRecheckCycles
	}
	fresh := r.serviceChecked && r.serviceCheckedDesired == desired.DesiredAutostart && r.serviceSkipped < recheck-1
	r.mu.Unlock()
	// A settled status (already matching the desired state, no error) is reused until the TTL passes or the desired state flips.
	settled := cached.LastError == "" && (desired.DesiredAutostart && cached.Installed && cached.Enabled || !desired.DesiredAutostart && !cached.Installed && !cached.Enabled)
	if fresh && settled {
		cached.LastReconcileAt = now.UnixMilli()
		r.mu.Lock()
		r.serviceSkipped++
		r.serviceState = cached
		r.mu.Unlock()
		return
	}
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
	status.LastReconcileAt = now.UnixMilli()
	r.mu.Lock()
	r.serviceState = status
	r.serviceChecked = true
	r.serviceSkipped = 0
	r.serviceCheckedDesired = desired.DesiredAutostart
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
		channel, err := DialChannel(ctx, client.WSURL(), r.hello("", ""), client.Insecure(), defaultWSKeepAlive, defaultWSPingTimeout)
		if err != nil {
			if isOwnerUnavailable(err) {
				if !r.waitOwner(ctx) {
					return
				}
				continue
			}
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
			if !sleepContext(ctx, r.jittered(backoff)) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		r.prober.ReportSuccess(client)
		r.ownerRecovered()
		r.notifyCurrentURL(ctx, client)
		backoff = retrySleep
		r.reportChannel(true)
		err = channel.Run(ctx, channelHandlers{
			onConfig: r.applyDesired,
			onBridge: r.handleBridge,
			onRevoke: r.revoke,
		})
		_ = channel.Close()
		r.reportChannel(false)
		if ctx.Err() != nil || errors.Is(err, ErrRevoked) {
			return
		}
		if errors.Is(err, ErrProtocolMismatch) {
			r.selfUpgrade(ctx, mismatchServerVersion(err))
			return
		}
		if isOwnerUnavailable(err) {
			// The server kicked the channel because the owner account was disabled; wait for it to recover.
			if !r.waitOwner(ctx) {
				return
			}
			continue
		}
		r.logger.Warn("control channel lost; reconnecting", "error", err)
		r.prober.ReportFailure(client)
		if !sleepContext(ctx, r.jittered(backoff)) {
			return
		}
		backoff = nextBackoff(backoff)
	}
}

func (r *Runtime) reportChannel(connected bool) {
	if r.onChannel != nil {
		r.onChannel(connected)
	}
}

func mismatchServerVersion(err error) string {
	var mismatch *ProtocolMismatchError
	if errors.As(err, &mismatch) {
		return mismatch.ServerVersion
	}
	return ""
}

// selfUpgrade tries to upgrade the local agent to the version reported by the server when the protocol mismatches.
// Successful staging -> fatal(errUpgradeStaged) exits so the replacement script takes over; self-upgrade unavailable
// (embedded distribution), or the server did not report a version (old server), or the target version already equals the current version
// (a mismatch would persist after upgrading, and downloading would only form a replace-restart-mismatch loop), or the target is lower than the running version (no server-driven downgrade), or retries exhausted ->
// fatal(ErrProtocolMismatch) stays in the pending-manual state; the log clearly distinguishes the two paths.
// A ctx cancellation during download is an intentional user stop: return directly and take the clean stop path, not recorded as fatal.
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
	if serverVersion == version.Version {
		r.logger.Error("control channel protocol mismatch but already at the server-reported version; refusing a replace-and-restart loop, manual intervention required", "target_version", serverVersion)
		r.fatal(ErrProtocolMismatch)
		return
	}
	// The server-driven path never downgrades (only `update --force` may); non-semver dev builds are not compared.
	if cmp, ok := compareReleaseVersions(version.Version, serverVersion); ok && cmp > 0 {
		r.logger.Error("control channel protocol mismatch but the server-reported version is lower than the running version; refusing a server-driven downgrade, use 'nexterm-agent update --force' to downgrade manually", "current_version", version.Version, "target_version", serverVersion)
		r.fatal(ErrProtocolMismatch)
		return
	}
	r.logger.Info("control channel protocol mismatch; attempting self-upgrade from the official release source", "target_version", serverVersion)
	if err := r.upgrader.Upgrade(ctx, serverVersion); err != nil {
		if ctx.Err() != nil {
			return
		}
		r.logger.Error("self-upgrade failed; staying on the current binary for manual intervention", "target_version", serverVersion, "error", err)
		r.fatal(ErrProtocolMismatch)
		return
	}
	r.logger.Info("self-upgrade staged; deferred replace will swap the binary and restart the service", "target_version", serverVersion)
	r.fatal(errUpgradeStaged)
}

// PurposeHandler serves one bridge of a non-terminal purpose; it owns the full bridge lifecycle
// (dialing included) and runs under the runtime's loopWg bookkeeping until it returns.
type PurposeHandler func(ctx context.Context, bridgeID string) error

// RegisterPurposeHandler registers the handler serving bridges of one non-terminal purpose; the purpose is
// advertised in hello capabilities from the next control channel dial on, so the server gates requests to it.
func (r *Runtime) RegisterPurposeHandler(purpose string, handler PurposeHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.purposeHandlers[purpose] = handler
}

// DialPurposeBridge dials one purpose bridge through the current endpoint with this runtime's authenticated hello.
// The composition root plugs the purpose servers (package purpose imports this package, so they cannot be wired
// from here) into the runtime through it.
func (r *Runtime) DialPurposeBridge(ctx context.Context, bridgeID, purpose string) (net.Conn, error) {
	client := r.prober.Current()
	if client == nil {
		return nil, ErrNoEndpoint
	}
	conn, err := DialBridge(ctx, client.WSURL(), r.hello(bridgeID, purpose), client.Insecure())
	if err != nil {
		return nil, err
	}
	return websocket.NetConn(ctx, conn, websocket.MessageBinary), nil
}

func (r *Runtime) handleBridge(bridgeID, purpose string) {
	if purpose != "" && purpose != PurposeTerminal {
		r.handlePurposeBridge(bridgeID, purpose)
		return
	}
	r.handleTerminalBridge(bridgeID, purpose)
}

// handlePurposeBridge dispatches a non-terminal bridge to its registered handler under the same loopWg
// bookkeeping as terminal bridges; without a handler the request is logged and dropped before any dial.
func (r *Runtime) handlePurposeBridge(bridgeID, purpose string) {
	if !r.desiredSnapshot().TerminalEnabled {
		r.logger.Warn("purpose bridge requested but terminal access is disabled", "bridge_id", bridgeID, "purpose", purpose)
		return
	}
	r.mu.Lock()
	handler := r.purposeHandlers[purpose]
	r.mu.Unlock()
	if handler == nil {
		r.logger.Warn("bridge requested for an unhandled purpose; ignoring", "bridge_id", bridgeID, "purpose", purpose)
		return
	}
	r.loopWg.Add(1)
	go func() {
		defer r.loopWg.Done()
		if err := handler(r.runCtx, bridgeID); err != nil {
			r.logger.Warn("purpose bridge failed", "bridge_id", bridgeID, "purpose", purpose, "error", err)
		}
	}()
}

func (r *Runtime) handleTerminalBridge(bridgeID, purpose string) {
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
		switch {
		case errors.Is(err, supervisor.ErrHelperUpgradeRequired):
			// An older helper still owns the state directory. The bridge is still
			// dialed so the helper bridge answers the server's hello with the
			// upgrade reason instead of leaving the server to time out.
			r.logger.Warn("supervisor helper needs upgrade; bridging to report it", "bridge_id", bridgeID, "error", err)
		case err != nil:
			r.logger.Warn("bridge aborted: supervisor helper unavailable", "bridge_id", bridgeID, "error", err)
			return
		}
		client := r.prober.Current()
		if client == nil {
			r.logger.Warn("bridge aborted: no healthy endpoint", "bridge_id", bridgeID)
			return
		}
		conn, err := DialBridge(ctx, client.WSURL(), r.hello(bridgeID, purpose), client.Insecure())
		if err != nil {
			if r.handleDenied(err) {
				return
			}
			r.logger.Warn("bridge dial failed", "bridge_id", bridgeID, "error", err)
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
		_ = BridgePipe(ctx, conn, r.bridge, r.stateDir)
	}()
}

// jitterDelay scales delay by a factor in [1-reconnectJitter, 1+reconnectJitter) chosen by unit in [0,1).
func jitterDelay(delay time.Duration, unit float64) time.Duration {
	return time.Duration(float64(delay) * (1 - reconnectJitter + 2*reconnectJitter*unit))
}

// retryWait applies an already jittered wait. A server-provided Retry-After is a minimum, so for it the jitter only
// ever lengthens the wait (a shortened value is mirrored above the original delay).
func retryWait(delay time.Duration, serverGiven bool, jittered time.Duration) time.Duration {
	if serverGiven && jittered < delay {
		return delay + (delay - jittered)
	}
	return jittered
}

func (r *Runtime) jittered(delay time.Duration) time.Duration {
	if r.jitter != nil {
		return jitterDelay(delay, r.jitter())
	}
	return jitterDelay(delay, rand.Float64())
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
