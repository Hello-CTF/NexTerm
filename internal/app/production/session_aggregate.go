package production

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/device"
)

const (
	// sessionAggregateDefaultWait is how long a listing waits for hosts
	// that are still answering when the caller names no wait; the rest
	// arrive as terminal://sessions events.
	sessionAggregateDefaultWait = 1500 * time.Millisecond
	sessionAggregateMaxWait     = 5 * time.Second
	// sessionAggregateDevicesTimeout bounds the enumeration of the
	// account's devices, and the server's answer beyond the wait it was
	// given.
	sessionAggregateDevicesTimeout = 2 * time.Second
	// sessionAggregateDevicesScope keeps the server's device listing for a
	// desktop or phone apart from the same account's own listing.
	sessionAggregateDevicesScope = "devices"
)

// AggregateDevice is a device of the requesting account whose terminal
// helper the cross-host session listing may ask through the device bridge.
type AggregateDevice struct {
	ID   string
	Name string
	// Kind is the device's enroll kind, such as local-helper.
	Kind     string
	Platform string
	// Online reports a device connected to the server now; Digest is its
	// supervisor state digest.
	Online          bool
	Digest          string
	TerminalEnabled bool
}

type sessionAggregateRequest struct {
	// WaitMS is how long to wait for hosts that are still answering; zero
	// takes the default, a negative value does not wait, and the result
	// never waits longer than 5 s.
	WaitMS int `json:"waitMs,omitempty"`
}

// sessionAggregateService answers terminal_sessions_aggregate: the attention
// state of the requester's durable sessions on every host it can reach
// without connecting anything new. On a server it also answers GET
// /fleet/sessions, the device part of that listing for a desktop or phone.
type sessionAggregateService struct {
	sessions   *session.Manager
	connector  *productionConnector
	aggregator *session.Aggregator
	publish    func(owner string, update session.AggregateUpdate)
	now        func() time.Time

	mu      sync.Mutex
	closed  bool
	devices map[string]aggregateDeviceListing
	remote  aggregateRemoteListing
	// epoch discards background results that a reset overtook.
	epoch uint64
	// wg tracks background refreshes.
	wg sync.WaitGroup
}

// aggregateDeviceListing is the last enumeration of one owner's devices.
type aggregateDeviceListing struct {
	at      time.Time
	devices []AggregateDevice
	// good reports that devices came from a successful enumeration; failed
	// that the latest enumeration failed after it.
	good       bool
	failed     bool
	err        error
	refreshing bool
}

// aggregateRemoteListing is the last device listing a desktop or phone got
// from its server.
type aggregateRemoteListing struct {
	at         time.Time
	list       session.AggregateList
	good       bool
	failed     bool
	err        error
	refreshing bool
}

func newSessionAggregateService(sessions *session.Manager, connector *productionConnector, events ipc.Emitter) *sessionAggregateService {
	service := &sessionAggregateService{
		sessions: sessions, connector: connector, devices: make(map[string]aggregateDeviceListing), now: time.Now,
		publish: func(owner string, update session.AggregateUpdate) {
			if events == nil {
				return
			}
			ctx := context.Background()
			if owner != "" {
				ctx = ipc.WithEventOwner(ctx, owner)
			}
			_ = ipc.Emit(ctx, events, ipc.TopicTerminalSessions, update)
		},
	}
	service.aggregator = session.NewAggregator(session.AggregatorConfig{
		Describe: describeAggregateError,
		Publish:  func(owner string, update session.AggregateUpdate) { service.publish(owner, update) },
	})
	return service
}

func describeAggregateError(err error) (string, string) {
	var ipcErr *ipc.Error
	if errors.As(terminalIPCError(err), &ipcErr) {
		return string(ipcErr.Code), ipcErr.Message
	}
	return "", err.Error()
}

func (s *sessionAggregateService) register(dispatcher *ipc.Dispatcher) error {
	return ipc.Register(dispatcher, "terminal_sessions_aggregate", func(ctx context.Context, _ *ipc.Call, input sessionAggregateRequest) (session.AggregateList, error) {
		return s.list(productionSessionRequestContext(ctx), input)
	})
}

func aggregateWait(waitMS int) time.Duration {
	switch {
	case waitMS > 0:
		return min(time.Duration(waitMS)*time.Millisecond, sessionAggregateMaxWait)
	case waitMS < 0:
		return 0
	default:
		return sessionAggregateDefaultWait
	}
}

func (s *sessionAggregateService) deviceConfig() *DeviceConnectorConfig {
	if s.connector == nil {
		return nil
	}
	return s.connector.device.Load()
}

func (s *sessionAggregateService) list(ctx context.Context, input sessionAggregateRequest) (session.AggregateList, error) {
	targets, err := s.sessions.AggregateTargetsFor(ctx)
	if err != nil {
		return session.AggregateList{}, terminalIPCError(err)
	}
	owner := ipc.OwnerID(ctx)
	wait := aggregateWait(input.WaitMS)
	config := s.deviceConfig()
	if config != nil && config.Devices != nil && config.Dial != nil {
		targets = append(targets, s.deviceTargets(ctx, owner, config, targets)...)
	}
	result := s.aggregator.List(ctx, owner, targets, wait)
	if config != nil && config.RemoteSessions != nil {
		result = mergeAggregateLists(result, s.remoteSessions(ctx, config, wait))
	}
	return result, nil
}

// DeviceSessions answers GET /fleet/sessions: the sessions on userID's
// devices, listed by this server through its own device bridges.
func (p *Production) DeviceSessions(ctx context.Context, userID string, wait time.Duration) (session.AggregateList, error) {
	s := p.Services.sessionAggregate
	if s == nil || userID == "" {
		return session.AggregateList{Hosts: []session.AggregateHostStatus{}, Sessions: []session.AggregateSession{}, Complete: true}, nil
	}
	ctx = ipc.WithUserID(ctx, userID)
	var targets []session.AggregateTarget
	if config := s.deviceConfig(); config != nil && config.Devices != nil && config.Dial != nil {
		targets = s.deviceTargets(ctx, userID, config, nil)
	}
	return s.aggregator.ListScoped(ctx, userID, sessionAggregateDevicesScope, targets, min(wait, sessionAggregateMaxWait)), nil
}

// deviceTargets adds the account's devices that no session of this process
// reaches already. They are listed through the device bridge, so a device
// shows its sessions while no workspace holds it, and a desktop that
// exposes its local terminals shows them while it runs.
func (s *sessionAggregateService) deviceTargets(ctx context.Context, owner string, config *DeviceConnectorConfig, known []session.AggregateTarget) []session.AggregateTarget {
	devices, stale, err := s.accountDevices(ctx, owner, config)
	if err != nil {
		return []session.AggregateTarget{{
			Host:   session.AggregateHost{Key: session.AggregateKeyDevice + "*", Kind: session.KindDevice, Name: "设备列表"},
			Failed: err,
		}}
	}
	have := make(map[string]bool, len(known))
	for _, target := range known {
		have[target.Host.Key] = true
	}
	targets := make([]session.AggregateTarget, 0, len(devices))
	for _, listed := range devices {
		host := session.AggregateHost{
			Key: session.AggregateKeyDevice + listed.ID, Kind: session.KindDevice, Name: listed.Name,
			DeviceID: listed.ID, DeviceKind: listed.Kind,
		}
		if listed.ID == "" || have[host.Key] {
			continue
		}
		target := session.AggregateTarget{Host: host, Stale: stale}
		switch {
		case !listed.TerminalEnabled:
			target.Failed = ipc.NewError(ipc.CodeForbidden, "设备已关闭终端访问")
		case !listed.Online || listed.Digest == "":
			target.Offline = true
		default:
			dial, check := config.Dial, config.CheckDevice
			deviceID, digest, platform := listed.ID, listed.Digest, listed.Platform
			target.List = func(ctx context.Context) ([]base.DurableDescriptor, error) {
				// The device list may be older than the owner, revocation
				// and terminal switch it was made under.
				if check != nil {
					if err := check(ctx, owner, deviceID); err != nil {
						return nil, err
					}
				}
				transport := device.New(deviceID, digest, func(ctx context.Context, purpose string) (net.Conn, error) {
					return dial(ctx, deviceID, purpose)
				}, 0, platform)
				return transport.Provider().ListDurableDescriptors(ctx)
			}
		}
		targets = append(targets, target)
	}
	return targets
}

// accountDevices enumerates the owner's devices. A good answer is reused for
// the aggregate cache lifetime, and after it answers at once while a
// background refresh replaces it, so only an owner's first listing waits for
// the enumeration. When a refresh fails the last good devices stay in use,
// reported stale; the enumeration fails only without any good answer.
func (s *sessionAggregateService) accountDevices(ctx context.Context, owner string, config *DeviceConnectorConfig) ([]AggregateDevice, bool, error) {
	now := s.now()
	s.mu.Lock()
	for key, listing := range s.devices {
		if now.Sub(listing.at) >= session.DefaultAggregateRetain && !listing.refreshing {
			delete(s.devices, key)
		}
	}
	cached, ok := s.devices[owner]
	if ok && cached.good {
		if now.Sub(cached.at) >= session.DefaultAggregateTTL && !cached.refreshing && s.startLocked() {
			cached.refreshing = true
			s.devices[owner] = cached
			epoch := s.epoch
			go func() {
				defer s.wg.Done()
				s.enumerateDevices(context.WithoutCancel(ctx), owner, config, epoch)
			}()
		}
		s.mu.Unlock()
		return cached.devices, cached.failed, nil
	}
	epoch := s.epoch
	s.mu.Unlock()
	return s.enumerateDevices(ctx, owner, config, epoch)
}

func (s *sessionAggregateService) enumerateDevices(ctx context.Context, owner string, config *DeviceConnectorConfig, epoch uint64) ([]AggregateDevice, bool, error) {
	listCtx, cancel := context.WithTimeout(ctx, sessionAggregateDevicesTimeout)
	defer cancel()
	devices, err := config.Devices(listCtx, owner)
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.devices[owner]
	next := aggregateDeviceListing{at: s.now(), devices: devices, good: err == nil, err: err}
	if err != nil && previous.good {
		next.devices, next.good, next.failed = previous.devices, true, true
	}
	if epoch == s.epoch {
		s.devices[owner] = next
	}
	if next.good {
		return next.devices, next.failed, nil
	}
	return nil, false, err
}

// remoteSessions returns the device listing of the linked server account,
// asking the server once for every device instead of dialing each. A good
// answer is reused for the cache lifetime, and after it, or while some of
// its devices had not answered yet, answers at once while a background
// refresh publishes each device as terminal://sessions.
func (s *sessionAggregateService) remoteSessions(ctx context.Context, config *DeviceConnectorConfig, wait time.Duration) session.AggregateList {
	now := s.now()
	s.mu.Lock()
	cached := s.remote
	if cached.good && now.Sub(cached.at) < session.DefaultAggregateRetain {
		due := now.Sub(cached.at) >= session.DefaultAggregateTTL || !cached.list.Complete
		if due && !cached.refreshing && s.startLocked() {
			s.remote.refreshing = true
			epoch := s.epoch
			go func() {
				defer s.wg.Done()
				if list, current := s.fetchRemote(context.WithoutCancel(ctx), config, sessionAggregateDefaultWait, epoch); current {
					s.publishRemote(list)
				}
			}()
		}
		s.mu.Unlock()
		return markRemoteStale(cached.list, cached.failed || now.Sub(cached.at) >= session.DefaultAggregateTTL)
	}
	epoch := s.epoch
	s.mu.Unlock()
	list, _ := s.fetchRemote(ctx, config, wait, epoch)
	return list
}

// fetchRemote asks the server and records its answer. current is false when a
// reset overtook the request, whose answer then belongs to another account.
func (s *sessionAggregateService) fetchRemote(ctx context.Context, config *DeviceConnectorConfig, wait time.Duration, epoch uint64) (session.AggregateList, bool) {
	fetchCtx, cancel := context.WithTimeout(ctx, wait+sessionAggregateDevicesTimeout)
	defer cancel()
	list, err := config.RemoteSessions(fetchCtx, int(wait/time.Millisecond))
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.remote
	next := aggregateRemoteListing{at: s.now(), list: list, good: err == nil, err: err}
	if err != nil && previous.good {
		next.list, next.good, next.failed = previous.list, true, true
	}
	current := epoch == s.epoch
	if current {
		s.remote = next
	}
	if next.good {
		return markRemoteStale(next.list, next.failed), current
	}
	failed := session.AggregateHostStatus{
		AggregateHost: session.AggregateHost{Key: session.AggregateKeyDevice + "*", Kind: session.KindDevice, Name: "设备列表"},
		Status:        session.AggregateHostFailed,
	}
	failed.ErrorCode, failed.Error = describeAggregateError(err)
	return session.AggregateList{Hosts: []session.AggregateHostStatus{failed}, Sessions: []session.AggregateSession{}}, current
}

// publishRemote publishes each device of a refreshed server listing, so a
// client that showed the earlier answer fills in the rest.
func (s *sessionAggregateService) publishRemote(next session.AggregateList) {
	sessionsByHost := make(map[string][]session.AggregateSession)
	for _, item := range next.Sessions {
		sessionsByHost[item.HostKey] = append(sessionsByHost[item.HostKey], item)
	}
	for _, host := range next.Hosts {
		sessions := sessionsByHost[host.Key]
		if sessions == nil {
			sessions = []session.AggregateSession{}
		}
		s.publish("", session.AggregateUpdate{Host: host, Sessions: sessions})
	}
}

// markRemoteStale reports an earlier server answer as stale: its devices
// were listed before the cache lifetime, or the latest refresh failed.
func markRemoteStale(list session.AggregateList, stale bool) session.AggregateList {
	if !stale {
		return list
	}
	hosts := make([]session.AggregateHostStatus, len(list.Hosts))
	for index, host := range list.Hosts {
		if host.Status == session.AggregateHostOK {
			host.Stale = true
		}
		hosts[index] = host
	}
	list.Hosts, list.Complete = hosts, false
	return list
}

// mergeAggregateLists adds the server's device listing to this client's:
// a device a session here reaches is listed through that session, and a
// session reached both ways is listed once.
func mergeAggregateLists(local, remote session.AggregateList) session.AggregateList {
	have := make(map[string]bool, len(local.Hosts))
	for _, host := range local.Hosts {
		have[host.Key] = true
	}
	seen := make(map[string]bool, len(local.Sessions))
	for _, item := range local.Sessions {
		seen[item.TabID] = true
	}
	merged := session.AggregateList{
		Hosts:    append([]session.AggregateHostStatus{}, local.Hosts...),
		Sessions: append([]session.AggregateSession{}, local.Sessions...),
		Complete: local.Complete && remote.Complete,
	}
	added := make(map[string]bool, len(remote.Hosts))
	for _, host := range remote.Hosts {
		if have[host.Key] {
			continue
		}
		added[host.Key] = true
		merged.Hosts = append(merged.Hosts, host)
	}
	for _, item := range remote.Sessions {
		if !added[item.HostKey] || seen[item.TabID] {
			continue
		}
		seen[item.TabID] = true
		merged.Sessions = append(merged.Sessions, item)
	}
	return merged
}

// withoutDevice leaves deviceID's host and sessions out of a server listing:
// a desktop lists its own exposed local terminals locally.
func withoutDevice(list session.AggregateList, deviceID string) session.AggregateList {
	if deviceID == "" {
		return list
	}
	key := session.AggregateKeyDevice + deviceID
	result := session.AggregateList{Hosts: []session.AggregateHostStatus{}, Sessions: []session.AggregateSession{}, Complete: list.Complete}
	for _, host := range list.Hosts {
		if host.Key != key {
			result.Hosts = append(result.Hosts, host)
		}
	}
	for _, item := range list.Sessions {
		if item.HostKey != key {
			result.Sessions = append(result.Sessions, item)
		}
	}
	return result
}

// startLocked admits one background refresh unless the service shut down.
// The caller holds s.mu.
func (s *sessionAggregateService) startLocked() bool {
	if s.closed {
		return false
	}
	s.wg.Add(1)
	return true
}

// reset drops every cached listing, for example after the linked account
// changed; background refreshes still running are discarded.
func (s *sessionAggregateService) reset() {
	s.mu.Lock()
	s.epoch++
	s.devices = make(map[string]aggregateDeviceListing)
	s.remote = aggregateRemoteListing{}
	s.mu.Unlock()
	s.aggregator.Reset()
}

func (s *sessionAggregateService) shutdown(context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.aggregator.Close()
	// Background refreshes are bounded by their timeouts.
	s.wg.Wait()
	return nil
}
