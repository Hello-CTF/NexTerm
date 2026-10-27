// Package exposure exposes the local helper of this desktop to the server
// account the desktop is linked to. With exposure on, the desktop enrolls
// itself as a device of that account and runs the device agent in process,
// bridged to its own local helper, so the account's browsers and phones can
// list and attach to the desktop's local terminals through the server.
//
// Exposure is off by default and only the user turns it on. It stops, and
// stays off, when the server revokes the device or when the desktop is linked
// to another account; it pauses while the local helper is disabled or local
// terminals are set to the regular mode. Only the owning account reaches the
// device: the server enrolls it with the local helper kind, which it never
// shares, and checks every bridge like any other device session.
package exposure

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ids"
)

// State is the exposure state shown to the user.
type State string

const (
	// StateOff means the user has not turned exposure on, or it was turned
	// off by a revocation or an account change named in Reason.
	StateOff State = "off"
	// StateUnavailable means exposure is on but cannot run now; Reason says why.
	StateUnavailable State = "unavailable"
	StateEnrolling   State = "enrolling"
	// StateConnecting means the agent runs but has no control channel to the
	// server; the device shows as offline there.
	StateConnecting State = "connecting"
	StateOnline     State = "online"
	// StateRevoked means the server revoked the device; exposure is off.
	StateRevoked State = "revoked"
	// StateFailed means exposure stopped on an error; turning it on again retries.
	StateFailed State = "failed"
)

// Status is the user-visible exposure status.
type Status struct {
	Enabled  bool   `json:"enabled"`
	State    State  `json:"state"`
	Reason   string `json:"reason,omitempty"`
	DeviceID string `json:"deviceId,omitempty"`
	// Server is the server URL the device is enrolled with.
	Server string `json:"server,omitempty"`
}

// Account is the server account this desktop is linked to.
type Account struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Insecure bool   `json:"insecure,omitempty"`
}

// Same reports whether two accounts name the same user on the same server.
func (a Account) Same(other Account) bool {
	return normalizeURL(a.URL) == normalizeURL(other.URL) && strings.EqualFold(strings.TrimSpace(a.Username), strings.TrimSpace(other.Username))
}

func normalizeURL(raw string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return strings.ToLower(trimmed)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String()
}

// Enrollment is the device credential of this desktop, bound to the account
// that enrolled it.
type Enrollment struct {
	Account Account      `json:"account"`
	Config  agent.Config `json:"config"`
}

var (
	// ErrLocked is returned by a Host whose credential vault is locked.
	ErrLocked = errors.New("credential vault is locked")
	// ErrNoAccount is returned by a Host that is not linked to a server account.
	ErrNoAccount = errors.New("no server account is linked")
)

// Host is what the controller needs from the desktop. Every method must be
// safe for concurrent use.
type Host interface {
	// Enabled reports the user's choice; it is false until the user turns
	// exposure on.
	Enabled(ctx context.Context) (bool, error)
	SetEnabled(ctx context.Context, enabled bool) error
	// Blocked explains why the local helper cannot be exposed now, or
	// returns "".
	Blocked(ctx context.Context) string
	// Account returns the linked account, ErrNoAccount or ErrLocked.
	Account(ctx context.Context) (Account, error)
	// LoadEnrollment returns nil without an enrollment, or ErrLocked.
	LoadEnrollment(ctx context.Context) (*Enrollment, error)
	SaveEnrollment(ctx context.Context, enrollment *Enrollment) error
	RemoveEnrollment(ctx context.Context) error
	// Enroll registers this desktop with the linked account.
	Enroll(ctx context.Context, account Account, request agent.EnrollRequest) (*agent.EnrollResponse, error)
	// Revoke revokes a device of the linked account.
	Revoke(ctx context.Context, deviceID string) error
}

// Runner is a device agent runtime.
type Runner interface {
	Run(ctx context.Context) error
}

// RunnerFactory builds the agent runtime for an enrollment. store holds the
// config, and onChannel must be called when the control channel comes up
// (true) or goes away (false).
type RunnerFactory func(config *agent.Config, store agent.ConfigStore, onChannel func(connected bool)) (Runner, error)

// Config configures a Controller.
type Config struct {
	Host      Host
	NewRunner RunnerFactory
	// DeviceName names the device on the server; empty uses the host name.
	DeviceName string
	AppVersion string
	Logger     *slog.Logger
	// OnChange is called after every status change, outside any lock.
	OnChange func(Status)
	// RecheckInterval is how often the setting, the prerequisites and the
	// linked account are checked again.
	RecheckInterval time.Duration
	// EnrollTimeout bounds one enrollment.
	EnrollTimeout time.Duration
}

// Controller runs exposure in one goroutine.
type Controller struct {
	config Config

	mu     sync.Mutex
	status Status

	wake     chan struct{}
	requests chan request
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	start    sync.Once
	started  bool

	// Owned by the run goroutine.
	running *runningAgent
	// stopped is the status shown while exposure is off after a revocation
	// or an account change, until the user turns it on again.
	stopped *Status
	// failure holds a failure until the user turns exposure on again.
	failure string
}

// request runs in the controller goroutine.
type request struct {
	run   func() error
	reply chan error
}

type runningAgent struct {
	enrollment *Enrollment
	cancel     context.CancelFunc
	done       chan error
	connected  chan bool
}

// New returns a stopped controller; Start launches it.
func New(config Config) *Controller {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.RecheckInterval <= 0 {
		config.RecheckInterval = 30 * time.Second
	}
	if config.EnrollTimeout <= 0 {
		config.EnrollTimeout = 30 * time.Second
	}
	if config.DeviceName == "" {
		if hostname, err := os.Hostname(); err == nil {
			config.DeviceName = hostname
		}
	}
	if config.DeviceName == "" {
		config.DeviceName = "NexTerm desktop"
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Controller{
		config:   config,
		status:   Status{State: StateOff},
		wake:     make(chan struct{}, 1),
		requests: make(chan request),
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
}

// Start launches the controller goroutine and returns at once.
func (c *Controller) Start() {
	c.start.Do(func() {
		c.mu.Lock()
		c.started = true
		c.mu.Unlock()
		go c.run()
	})
}

// Close stops exposure and waits for the agent to stop. The enrollment and
// the user's choice are kept, so the next start exposes again.
func (c *Controller) Close() {
	c.mu.Lock()
	started := c.started
	c.mu.Unlock()
	c.cancel()
	if started {
		<-c.done
	}
}

// Status returns the current status.
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Reconcile asks the controller to check the setting, the prerequisites and
// the linked account again, for example after the vault was unlocked or a
// setting changed.
func (c *Controller) Reconcile() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// SetEnabled records the user's choice and applies it. Turning exposure on
// needs a linked account and a usable local helper; it enrolls this desktop
// when it is not enrolled with that account yet.
func (c *Controller) SetEnabled(ctx context.Context, enabled bool) (Status, error) {
	err := c.do(ctx, func() error { return c.apply(enabled) })
	return c.Status(), err
}

// do runs fn in the controller goroutine and waits for it.
func (c *Controller) do(ctx context.Context, fn func() error) error {
	c.Start()
	req := request{run: fn, reply: make(chan error, 1)}
	select {
	case c.requests <- req:
	case <-c.done:
		return errors.New("exposure controller is closed")
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	select {
	case err := <-req.reply:
		return err
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (c *Controller) setStatus(status Status) {
	c.mu.Lock()
	if status == c.status {
		c.mu.Unlock()
		return
	}
	c.status = status
	onChange := c.config.OnChange
	c.mu.Unlock()
	if onChange != nil {
		onChange(status)
	}
}

func (c *Controller) run() {
	defer close(c.done)
	ticker := time.NewTicker(c.config.RecheckInterval)
	defer ticker.Stop()
	c.reconcile()
	for {
		var exited <-chan error
		var connected <-chan bool
		if c.running != nil {
			exited, connected = c.running.done, c.running.connected
		}
		select {
		case <-c.ctx.Done():
			c.stopAgent()
			return
		case req := <-c.requests:
			req.reply <- req.run()
		case <-c.wake:
			c.reconcile()
		case <-ticker.C:
			c.reconcile()
		case up := <-connected:
			state := StateConnecting
			if up {
				state = StateOnline
			}
			c.setStatus(c.agentStatus(state, ""))
		case err := <-exited:
			c.agentExited(err)
		}
	}
}

func (c *Controller) agentStatus(state State, reason string) Status {
	status := Status{Enabled: true, State: state, Reason: reason}
	if c.running != nil {
		status.DeviceID = c.running.enrollment.Config.DeviceID
		status.Server = c.running.enrollment.Account.URL
	}
	return status
}

// apply handles the user turning exposure on or off.
func (c *Controller) apply(enabled bool) error {
	ctx := c.ctx
	if !enabled {
		if err := c.config.Host.SetEnabled(ctx, false); err != nil {
			return err
		}
		c.stopAgent()
		c.stopped, c.failure = nil, ""
		c.setStatus(Status{State: StateOff})
		return nil
	}
	if reason := c.config.Host.Blocked(ctx); reason != "" {
		return errors.New(reason)
	}
	account, err := c.config.Host.Account(ctx)
	if err != nil {
		return accountError(err)
	}
	// An enrollment of another account is replaced: the user now chose to
	// expose to the account the desktop is linked to.
	if c.running != nil && !c.running.enrollment.Account.Same(account) {
		c.stopAgent()
	}
	if c.running == nil {
		enrollment, err := c.config.Host.LoadEnrollment(ctx)
		if err != nil {
			return accountError(err)
		}
		if enrollment != nil && !enrollment.Account.Same(account) {
			if err := c.config.Host.RemoveEnrollment(ctx); err != nil {
				return err
			}
		}
	}
	if err := c.config.Host.SetEnabled(ctx, true); err != nil {
		return err
	}
	c.stopped, c.failure = nil, ""
	c.reconcile()
	if status := c.Status(); status.State == StateFailed {
		return errors.New(status.Reason)
	}
	return nil
}

func accountError(err error) error {
	switch {
	case errors.Is(err, ErrLocked):
		return errors.New("凭据库已锁定，请先解锁")
	case errors.Is(err, ErrNoAccount):
		return errors.New("请先在同步设置中登录服务器账号")
	}
	return err
}

// reconcile brings the agent in line with the setting, the prerequisites
// and the linked account.
func (c *Controller) reconcile() {
	ctx := c.ctx
	host := c.config.Host
	enabled, err := host.Enabled(ctx)
	if err != nil {
		c.config.Logger.Warn("read the local terminal exposure setting", "error", err)
		return
	}
	if !enabled {
		c.stopAgent()
		if c.stopped != nil {
			c.setStatus(*c.stopped)
		} else {
			c.setStatus(Status{State: StateOff})
		}
		return
	}
	if reason := host.Blocked(ctx); reason != "" {
		c.stopAgent()
		c.setStatus(Status{Enabled: true, State: StateUnavailable, Reason: reason})
		return
	}
	account, err := host.Account(ctx)
	switch {
	case errors.Is(err, ErrLocked):
		// The running agent keeps its credentials; a locked vault only
		// delays a start.
		if c.running == nil {
			c.setStatus(Status{Enabled: true, State: StateUnavailable, Reason: "凭据库已锁定，解锁后开放本机终端"})
		}
		return
	case err != nil:
		c.stopAgent()
		c.setStatus(Status{Enabled: true, State: StateUnavailable, Reason: "未登录服务器账号，本机终端暂不开放"})
		return
	}
	if c.running != nil {
		if !c.running.enrollment.Account.Same(account) {
			c.forget(accountChangedReason)
		}
		return
	}
	if c.failure != "" {
		c.setStatus(Status{Enabled: true, State: StateFailed, Reason: c.failure})
		return
	}
	enrollment, err := host.LoadEnrollment(ctx)
	switch {
	case errors.Is(err, ErrLocked):
		c.setStatus(Status{Enabled: true, State: StateUnavailable, Reason: "凭据库已锁定，解锁后开放本机终端"})
		return
	case err != nil:
		c.fail(fmt.Sprintf("读取本机设备凭据失败：%v", err))
		return
	}
	if enrollment != nil && !enrollment.Account.Same(account) {
		// Linked to another account since the user turned exposure on: it
		// is never moved to that account without the user.
		c.forget(accountChangedReason)
		return
	}
	if enrollment == nil {
		c.setStatus(Status{Enabled: true, State: StateEnrolling, Server: account.URL})
		enrollment, err = c.enroll(account)
		if err != nil {
			c.fail(err.Error())
			return
		}
	}
	c.startAgent(enrollment)
}

// accountChangedReason explains a stop after the linked account changed; the
// earlier account still lists the device, offline, until it is revoked there.
const accountChangedReason = "已更换同步账号，本机终端不再向原账号开放。原账号的设备列表中仍有这台设备（离线），建议在那里吊销；如需开放给当前账号，请重新开启"

// pinnedBaseURLs is the only server the agent of an enrollment talks to: the
// linked account's server, never an address the enroll response or the agent
// itself advertises.
func pinnedBaseURLs(account Account) []agent.BaseURLEntry {
	return []agent.BaseURLEntry{{URL: account.URL, Insecure: account.Insecure}}
}

// failClosed revokes a device that must not stay enrolled. When the revoke
// fails the user is told which device to revoke by hand.
func (c *Controller) failClosed(ctx context.Context, deviceID, reason string) error {
	if err := c.config.Host.Revoke(ctx, deviceID); err != nil {
		c.config.Logger.Warn("revoke a device that must not stay enrolled", "device_id", deviceID, "error", err)
		return fmt.Errorf("%s；自动吊销设备 %s 失败，请在设备列表中吊销：%v", reason, deviceID, err)
	}
	return errors.New(reason)
}

// forget stops exposure for good after the linked account changed: the
// enrollment of the earlier account is dropped and the setting turned off.
func (c *Controller) forget(reason string) {
	c.stopAgent()
	if err := c.config.Host.RemoveEnrollment(c.ctx); err != nil {
		c.config.Logger.Warn("remove the local terminal exposure enrollment", "error", err)
	}
	if err := c.config.Host.SetEnabled(c.ctx, false); err != nil {
		c.config.Logger.Warn("turn local terminal exposure off", "error", err)
	}
	c.stopped = &Status{State: StateOff, Reason: reason}
	c.setStatus(*c.stopped)
}

func (c *Controller) fail(reason string) {
	c.failure = reason
	c.config.Logger.Warn("local terminal exposure failed", "reason", reason)
	c.setStatus(Status{Enabled: true, State: StateFailed, Reason: reason})
}

func (c *Controller) enroll(account Account) (*Enrollment, error) {
	ctx, cancel := context.WithTimeout(c.ctx, c.config.EnrollTimeout)
	defer cancel()
	request := agent.EnrollRequest{
		Name: c.config.DeviceName, Platform: runtime.GOOS, AppVersion: c.config.AppVersion, Kind: agent.DeviceKindLocalHelper,
	}
	response, err := c.config.Host.Enroll(ctx, account, request)
	if err != nil {
		if isUnknownKindError(err) {
			return nil, errors.New(serverTooOldReason)
		}
		return nil, fmt.Errorf("向服务器注册本机失败：%w", err)
	}
	if response.Kind != agent.DeviceKindLocalHelper {
		// The server enrolled an ordinary, shareable device: take it back.
		return nil, c.failClosed(ctx, response.DeviceID, serverTooOldReason)
	}
	// The link may have moved to another account while enrolling: the device
	// must not stay with an account the user no longer links to.
	if current, err := c.config.Host.Account(ctx); err != nil || !current.Same(account) {
		return nil, c.failClosed(ctx, response.DeviceID, "注册期间同步账号已变更，已放弃这次注册")
	}
	enrollment := &Enrollment{
		Account: account,
		Config: agent.Config{
			Version: agent.ConfigVersion, DeviceID: response.DeviceID, Secret: response.Secret,
			Name: request.Name, Platform: request.Platform, AppVersion: request.AppVersion,
			BaseURLs: pinnedBaseURLs(account), MetricsIntervalMS: response.MetricsIntervalMS,
			DesiredAutostart: response.DesiredAutostart, TerminalEnabled: response.TerminalEnabled,
			EnrolledAt: ids.NowMS(),
		},
	}
	if err := c.config.Host.SaveEnrollment(ctx, enrollment); err != nil {
		return nil, c.failClosed(ctx, response.DeviceID, fmt.Sprintf("保存本机设备凭据失败：%v", err))
	}
	return enrollment, nil
}

func (c *Controller) startAgent(enrollment *Enrollment) {
	// An enrollment saved by an earlier build may carry other addresses.
	enrollment.Config.BaseURLs = pinnedBaseURLs(enrollment.Account)
	store := &enrollmentStore{host: c.config.Host, enrollment: *enrollment, logger: c.config.Logger}
	connected := make(chan bool, 1)
	onChannel := func(up bool) {
		// Only the latest report matters; an unread one is replaced.
		for {
			select {
			case connected <- up:
				return
			default:
			}
			select {
			case <-connected:
			default:
			}
		}
	}
	config := enrollment.Config
	runner, err := c.config.NewRunner(&config, store, onChannel)
	if err != nil {
		c.fail(fmt.Sprintf("启动设备代理失败：%v", err))
		return
	}
	ctx, cancel := context.WithCancel(c.ctx)
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	c.running = &runningAgent{enrollment: enrollment, cancel: cancel, done: done, connected: connected}
	c.setStatus(c.agentStatus(StateConnecting, ""))
}

// stopAgent stops a running agent and waits for it; its bridges close with it.
func (c *Controller) stopAgent() {
	if c.running == nil {
		return
	}
	c.running.cancel()
	<-c.running.done
	c.running = nil
}

func (c *Controller) agentExited(err error) {
	running := c.running
	c.running = nil
	running.cancel()
	switch {
	case errors.Is(err, agent.ErrRevoked):
		c.config.Logger.Warn("the server revoked this desktop; local terminals are no longer exposed", "device_id", running.enrollment.Config.DeviceID)
		if removeErr := c.config.Host.RemoveEnrollment(c.ctx); removeErr != nil {
			c.config.Logger.Warn("remove the revoked exposure enrollment", "error", removeErr)
		}
		if setErr := c.config.Host.SetEnabled(c.ctx, false); setErr != nil {
			c.config.Logger.Warn("turn local terminal exposure off", "error", setErr)
		}
		c.stopped = &Status{State: StateRevoked, Reason: "服务器已吊销此设备，本机终端不再开放；重新开启会重新注册"}
		c.setStatus(*c.stopped)
	case errors.Is(err, agent.ErrProtocolMismatch):
		c.fail("服务器的设备协议版本与本机不同，请将桌面端与服务器升级到同一版本")
	case err == nil:
		// The agent stops cleanly only when its context ends.
		c.fail("设备代理已停止")
	default:
		c.fail(fmt.Sprintf("设备代理已停止：%v", err))
	}
}

const serverTooOldReason = "服务器版本过旧，不支持开放桌面本机终端，请升级服务器"

// isUnknownKindError reports a server that predates the enroll kind and
// rejected the request field.
func isUnknownKindError(err error) bool {
	message := err.Error()
	return strings.Contains(message, "unknown field") && strings.Contains(message, `"kind"`)
}

// enrollmentStore is the agent config store of a running agent. The agent
// saves its current server URL through it; a save that cannot reach the
// vault is kept in memory and logged.
type enrollmentStore struct {
	host   Host
	logger *slog.Logger

	mu         sync.Mutex
	enrollment Enrollment
}

func (s *enrollmentStore) Load() (*agent.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	config := s.enrollment.Config
	config.BaseURLs = append([]agent.BaseURLEntry(nil), config.BaseURLs...)
	return &config, nil
}

func (s *enrollmentStore) Save(config *agent.Config) error {
	s.mu.Lock()
	pinned := s.enrollment.Config.BaseURLs
	s.enrollment.Config = *config
	// The pinned server stays the only one, whatever the agent saves.
	s.enrollment.Config.BaseURLs = pinned
	enrollment := s.enrollment
	s.mu.Unlock()
	if err := s.host.SaveEnrollment(context.Background(), &enrollment); err != nil {
		s.logger.Warn("save the exposure enrollment", "error", err)
	}
	return nil
}
