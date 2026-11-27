// Package client is the desktop and mobile half of direct connections: a per-device path manager that keeps every
// device stream on an rstream, starts on the relay, builds a direct leg in the background, moves the live streams
// onto it one at a time and back to the relay when the leg degrades, dies, is revoked or the user picks the
// relay (design 2.4, 4).
package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/p2p/clock"
	"github.com/Hello-CTF/NexTerm/internal/p2p/grant"
	"github.com/Hello-CTF/NexTerm/internal/p2p/leg"
	"github.com/Hello-CTF/NexTerm/internal/p2p/natprobe"
	"github.com/Hello-CTF/NexTerm/internal/p2p/selector"
	"github.com/Hello-CTF/NexTerm/internal/rstream"
)

// Path values of the status.
const (
	PathRelay            = "relay"
	PathDirect           = "direct"
	PathConnectingDirect = "connecting_direct"
)

// Reasons why the path is not direct (the contract's reason strings).
const (
	ReasonNone              = ""
	ReasonModeRelay         = "mode_relay"
	ReasonServerDisabled    = "server_disabled"
	ReasonDeviceUnsupported = "device_unsupported"
	ReasonKeyPending        = "key_pending"
	ReasonBrowserOrShare    = "browser_or_share"
	ReasonNATBlocked        = "nat_blocked"
	ReasonConnectFailed     = "connect_failed"
	ReasonUnstable          = "unstable"
	ReasonBackground        = "background"
	ReasonProxy             = "proxy"
	ReasonInsecure          = "insecure"
	ReasonOffline           = "offline"
)

// Timing from design 2.4; tests shorten some through Options.
const (
	// BackoffMin and BackoffMax bound the wait after a failed attempt; a network change or return to the
	// foreground resets it.
	BackoffMin = 5 * time.Minute
	BackoffMax = 30 * time.Minute
	// IdleClose closes the direct session of a device without streams.
	IdleClose = 5 * time.Minute
	// ConfigTTL is how long a fetched config is reused.
	ConfigTTL = 10 * time.Minute
	// MigrationSpacing separates the migrations of two streams so they do not re-send at once.
	MigrationSpacing = 200 * time.Millisecond
	// ForegroundDelay lets the relay recover after a return to the foreground before probing again.
	ForegroundDelay = 2 * time.Second
	// DialBudget bounds the NAT probe, the offer/answer exchange and the leg.
	DialBudget = natprobe.DefaultTimeout + 5*time.Second + leg.DefaultGatherTimeout + leg.DefaultCheckTimeout + leg.DefaultHandshakeTimeout
	// ExpiryMargin moves streams off a leg whose grant could not be renewed before the agent stops accepting.
	ExpiryMargin = 5 * time.Second
	// RecentSwitches is how many path switches the diagnostics keep.
	RecentSwitches = 10
)

// Grant is the POST /fleet/devices/{id}/p2p/grant response.
type Grant struct {
	Grant        string    `json:"grant"`
	GrantID      string    `json:"grant_id"`
	DeviceID     string    `json:"device_id"`
	AgentPub     grant.Key `json:"agent_pub"`
	Scope        []string  `json:"scope"`
	IssuedAt     int64     `json:"issued_at"`
	ExpiresAt    int64     `json:"expires_at"`
	RenewAfterMS int64     `json:"renew_after_ms"`
}

// Endpoint is how the client reaches the server.
type Endpoint struct {
	// URL is the server base URL.
	URL string
	// Proxied reports a proxy, manual or from the environment: direct UDP would bypass it, so no direct
	// connection is tried.
	Proxied bool
	// Insecure reports plain HTTP or unverified TLS to the server, which could not vouch for the agent key of a
	// grant, so no direct connection is tried.
	Insecure bool
}

// Backend is the server surface the manager uses; the sync service's RemoteProxy provides it.
type Backend interface {
	// DialRelay opens a relay bridge for purpose asking for a stream carrier; stream reports whether the server
	// and agent carry one. With resume, errors that a retry cannot fix are rstream.Permanent.
	DialRelay(ctx context.Context, deviceID, purpose string, resume bool) (conn net.Conn, stream bool, err error)
	// DialSignal opens a raw p2p-signal bridge.
	DialSignal(ctx context.Context, deviceID string) (net.Conn, error)
	Config(ctx context.Context) (*agent.P2PConfig, error)
	Grant(ctx context.Context, deviceID string, clientPub ed25519.PublicKey) (*Grant, error)
	// DeviceKeyState is the device's direct-connection key state ("bound", "pending", "none"; "" when the server
	// does not report one).
	DeviceKeyState(ctx context.Context, deviceID string) (string, error)
	// Mode reads the device's stored connection mode.
	Mode(ctx context.Context, deviceID string) (selector.Mode, error)
	Endpoint(ctx context.Context) (Endpoint, error)
}

// DialLegFunc builds a leg; leg.Dial in production.
type DialLegFunc func(ctx context.Context, cfg leg.Config, local, remote leg.Credentials, sig leg.Signaler) (leg.Leg, error)

// ProbeFunc classifies this side's NAT mapping against servers.
type ProbeFunc func(ctx context.Context, servers []string) natprobe.Mapping

// Options tunes the manager; zero values select production behaviour.
type Options struct {
	Clock   clock.Clock
	DialLeg DialLegFunc
	Probe   ProbeFunc
	// LegConfig adjusts every leg config before dialing (test hook).
	LegConfig func(*leg.Config)
	// Emit receives every status change.
	Emit func(DevicePathStatus)
	// Durations; zero selects the constants above.
	MigrationSpacing time.Duration
	BackoffMin       time.Duration
	BackoffMax       time.Duration
	IdleClose        time.Duration
	ForegroundDelay  time.Duration
	// Stream configures the rstream streams (tests set a clock or timeouts).
	Stream rstream.Options
	Logger *slog.Logger
}

func (o *Options) defaults() {
	if o.Clock == nil {
		o.Clock = clock.Real{}
	}
	if o.DialLeg == nil {
		o.DialLeg = leg.Dial
	}
	if o.Probe == nil {
		o.Probe = defaultProbe
	}
	if o.MigrationSpacing <= 0 {
		o.MigrationSpacing = MigrationSpacing
	}
	if o.BackoffMin <= 0 {
		o.BackoffMin = BackoffMin
	}
	if o.BackoffMax <= 0 {
		o.BackoffMax = BackoffMax
	}
	if o.IdleClose <= 0 {
		o.IdleClose = IdleClose
	}
	if o.ForegroundDelay <= 0 {
		o.ForegroundDelay = ForegroundDelay
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

func defaultProbe(ctx context.Context, servers []string) natprobe.Mapping {
	if len(servers) == 0 {
		return natprobe.Unknown
	}
	result, err := natprobe.Probe(ctx, servers, natprobe.Options{})
	if err != nil {
		return natprobe.Blocked
	}
	return result.Mapping
}

// Manager keeps the path of every device this process talks to.
type Manager struct {
	backend Backend
	opts    Options
	clock   clock.Clock
	logger  *slog.Logger
	// key is this process's client identity; it never leaves memory.
	key ed25519.PrivateKey

	mu         sync.Mutex
	devices    map[string]*device
	background bool
	closed     bool
	config     *agent.P2PConfig
	configAt   time.Time
	wg         sync.WaitGroup
}

// New returns a manager over backend.
func New(backend Backend, opts Options) *Manager {
	opts.defaults()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return &Manager{backend: backend, opts: opts, clock: opts.Clock, logger: opts.Logger, key: key, devices: make(map[string]*device)}
}

func (m *Manager) device(ctx context.Context, deviceID string) (*device, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, false
	}
	d := m.devices[deviceID]
	if d == nil {
		d = newDevice(m, deviceID, ctx)
		m.devices[deviceID] = d
	}
	return d, true
}

func (m *Manager) existing(deviceID string) *device {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.devices[deviceID]
}

// Dial opens a device stream of purpose. It returns at once on the preferred path (the direct leg when the
// streams use it, the relay otherwise); the first relay bridge to a device starts the background work that may
// move the streams to a direct leg. A server or agent without stream carriers gets the raw relay bridge.
func (m *Manager) Dial(ctx context.Context, deviceID, purpose string) (net.Conn, error) {
	if deviceID == "" {
		return nil, ipc.NewError(ipc.CodeBadParam, "deviceId 不能为空")
	}
	d, ok := m.device(ctx, deviceID)
	if !ok {
		return nil, ipc.NewError(ipc.CodeDisconnected, "直连管理已停止")
	}
	return d.dial(ctx, purpose)
}

// Status reports the device's path; a device never dialed reports the relay with its stored mode.
func (m *Manager) Status(ctx context.Context, deviceID string) DevicePathStatus {
	if d := m.existing(deviceID); d != nil {
		return d.status()
	}
	mode, _ := m.backend.Mode(ctx, deviceID)
	status := DevicePathStatus{DeviceID: deviceID, Mode: string(selector.ParseMode(string(mode))), Path: PathRelay}
	if mode == selector.ModeRelay {
		status.Reason = ReasonModeRelay
	}
	return status
}

// Diagnostics reports the device's measurements and policy.
func (m *Manager) Diagnostics(ctx context.Context, deviceID string) DeviceP2PDiagnostics {
	var diag DeviceP2PDiagnostics
	if d := m.existing(deviceID); d != nil {
		diag = d.diagnostics()
	} else {
		status := m.Status(ctx, deviceID)
		diag = DeviceP2PDiagnostics{Status: status, LocalNAT: string(natprobe.Unknown), RemoteNAT: string(natprobe.Unknown), RecentSwitches: []PathSwitch{}}
		diag.Policy = P2PPolicy{Server: PolicyUnknown, Device: PolicyUnknown, Mode: status.Mode}
	}
	// The policy layers are read now, so a device that never tried (relay mode, proxy, background) reports them;
	// a failed read keeps the last value an attempt saw.
	if config, err := m.loadConfig(ctx); err == nil {
		diag.Policy.Server = serverPolicy(config)
	}
	if state, err := m.backend.DeviceKeyState(ctx, deviceID); err == nil {
		diag.Policy.Device, diag.Policy.DeviceKey = devicePolicy(state), state
	}
	if diag.Status.Path == PathRelay && diag.Status.Reason == ReasonNone {
		diag.Status.Reason = blockingReason(diag.Policy)
	}
	return diag
}

// blockingReason is the status reason of a policy layer that rules out a direct connection, mirroring connect.
func blockingReason(policy P2PPolicy) string {
	switch {
	case policy.Server == PolicyOff:
		return ReasonServerDisabled
	case policy.Device == PolicyOff && policy.DeviceKey == agent.P2PKeyStatePending:
		return ReasonKeyPending
	case policy.Device == PolicyOff:
		return ReasonDeviceUnsupported
	}
	return ReasonNone
}

// ModeChanged applies a newly stored mode of deviceID to its live streams at once.
func (m *Manager) ModeChanged(deviceID string) {
	if d := m.existing(deviceID); d != nil {
		d.post(event{kind: evMode})
	}
}

// WatchNetwork calls NetworkChanged whenever the set of local interface addresses changes, checking every
// interval until ctx ends. Platforms that refuse to list interfaces simply never report a change.
func (m *Manager) WatchNetwork(ctx context.Context, interval time.Duration) {
	snapshot := func() string {
		addrs, err := net.InterfaceAddrs()
		if err != nil {
			return ""
		}
		texts := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			texts = append(texts, addr.String())
		}
		slices.Sort(texts)
		return strings.Join(texts, ",")
	}
	last := snapshot()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if current := snapshot(); current != last && current != "" {
				last = current
				m.logger.Info("local network changed; retrying direct connections")
				m.NetworkChanged()
			}
		}
	}
}

// SetBackground closes every direct leg when the app goes to the background (its streams resume on the relay)
// and probes again shortly after it returns to the foreground.
func (m *Manager) SetBackground(background bool) {
	m.mu.Lock()
	changed := m.background != background
	m.background = background
	devices := m.snapshotLocked()
	m.mu.Unlock()
	if !changed {
		return
	}
	for _, d := range devices {
		if background {
			d.post(event{kind: evBackground})
		} else {
			d.post(event{kind: evForeground})
		}
	}
}

// NetworkChanged resets the retry backoff of every device and tries again.
func (m *Manager) NetworkChanged() {
	m.mu.Lock()
	devices := m.snapshotLocked()
	m.mu.Unlock()
	for _, d := range devices {
		d.post(event{kind: evNetwork})
	}
}

func (m *Manager) snapshotLocked() []*device {
	devices := make([]*device, 0, len(m.devices))
	for _, d := range m.devices {
		devices = append(devices, d)
	}
	return devices
}

func (m *Manager) inBackground() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.background
}

// Close ends every direct session; live streams resume on the relay.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	devices := m.snapshotLocked()
	m.mu.Unlock()
	for _, d := range devices {
		d.stop()
	}
	m.wg.Wait()
}

func (m *Manager) loadConfig(ctx context.Context) (*agent.P2PConfig, error) {
	now := m.clock.Now()
	m.mu.Lock()
	if m.config != nil && now.Sub(m.configAt) < ConfigTTL {
		config := m.config
		m.mu.Unlock()
		return config, nil
	}
	m.mu.Unlock()
	config, err := m.backend.Config(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.config, m.configAt = config, now
	m.mu.Unlock()
	return config, nil
}

func (m *Manager) forgetConfig() {
	m.mu.Lock()
	m.config = nil
	m.mu.Unlock()
}

// grantReason maps a refused grant to a status reason. Only the error code is structured: offline devices come
// back disconnected, an unsupported purpose as unsupported, every policy refusal as forbidden.
func grantReason(err error) string {
	var ipcErr *ipc.Error
	if errors.As(err, &ipcErr) {
		switch ipcErr.Code {
		case ipc.CodeDisconnected:
			return ReasonOffline
		case ipc.CodeUnsupported:
			return ReasonDeviceUnsupported
		case ipc.CodeForbidden:
			return ReasonServerDisabled
		}
	}
	return ReasonConnectFailed
}

// endpointReason is why the way the client reaches the server rules out a direct connection, "" when it does not.
func endpointReason(endpoint Endpoint) string {
	switch {
	case endpoint.Proxied:
		return ReasonProxy
	case endpoint.Insecure:
		return ReasonInsecure
	}
	return ""
}
