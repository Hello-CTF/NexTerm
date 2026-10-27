package production

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
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
	// account's devices, which every listing needs before it can start.
	sessionAggregateDevicesTimeout = 2 * time.Second
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
	// takes the default, and the result never waits longer than 5 s.
	WaitMS int `json:"waitMs,omitempty"`
}

// sessionAggregateService answers terminal_sessions_aggregate: the attention
// state of the requester's durable sessions on every host it can reach
// without connecting anything new.
type sessionAggregateService struct {
	sessions   *session.Manager
	connector  *productionConnector
	aggregator *session.Aggregator

	mu      sync.Mutex
	devices map[string]aggregateDeviceListing
	now     func() time.Time
	// wg tracks background device enumerations.
	wg sync.WaitGroup
}

type aggregateDeviceListing struct {
	at         time.Time
	devices    []AggregateDevice
	err        error
	refreshing bool
}

func newSessionAggregateService(sessions *session.Manager, connector *productionConnector, events ipc.Emitter) *sessionAggregateService {
	service := &sessionAggregateService{
		sessions: sessions, connector: connector, devices: make(map[string]aggregateDeviceListing), now: time.Now,
	}
	service.aggregator = session.NewAggregator(session.AggregatorConfig{
		Describe: describeAggregateError,
		Publish: func(owner string, update session.AggregateUpdate) {
			if events == nil {
				return
			}
			ctx := context.Background()
			if owner != "" {
				ctx = ipc.WithEventOwner(ctx, owner)
			}
			_ = ipc.Emit(ctx, events, ipc.TopicTerminalSessions, update)
		},
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

func (s *sessionAggregateService) list(ctx context.Context, input sessionAggregateRequest) (session.AggregateList, error) {
	targets, err := s.sessions.AggregateTargetsFor(ctx)
	if err != nil {
		return session.AggregateList{}, terminalIPCError(err)
	}
	owner := ipc.OwnerID(ctx)
	targets = append(targets, s.deviceTargets(ctx, owner, targets)...)
	wait := sessionAggregateDefaultWait
	if input.WaitMS > 0 {
		wait = min(time.Duration(input.WaitMS)*time.Millisecond, sessionAggregateMaxWait)
	} else if input.WaitMS < 0 {
		wait = 0
	}
	return s.aggregator.List(ctx, owner, targets, wait), nil
}

// deviceTargets adds the account's devices that no session of this client
// reaches already. They are listed through the device bridge, so a device
// shows its sessions while no workspace holds it, and a desktop that
// exposes its local terminals shows them while it is closed.
func (s *sessionAggregateService) deviceTargets(ctx context.Context, owner string, known []session.AggregateTarget) []session.AggregateTarget {
	if s.connector == nil {
		return nil
	}
	config := s.connector.device.Load()
	if config == nil || config.Devices == nil || config.Dial == nil {
		return nil
	}
	devices, err := s.accountDevices(ctx, owner, config)
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
		target := session.AggregateTarget{Host: host}
		switch {
		case !listed.TerminalEnabled:
			target.Failed = ipc.NewError(ipc.CodeForbidden, "设备已关闭终端访问")
		case !listed.Online || listed.Digest == "":
			target.Offline = true
		default:
			dial := config.Dial
			deviceID, digest, platform := listed.ID, listed.Digest, listed.Platform
			target.List = func(ctx context.Context) ([]base.DurableDescriptor, error) {
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

// accountDevices enumerates the owner's devices. An answer is reused for the
// aggregate cache lifetime; an older one still answers at once while a
// background refresh replaces it, so only an owner's first listing waits
// for the enumeration.
func (s *sessionAggregateService) accountDevices(ctx context.Context, owner string, config *DeviceConnectorConfig) ([]AggregateDevice, error) {
	now := s.now()
	s.mu.Lock()
	for key, listing := range s.devices {
		if now.Sub(listing.at) >= session.DefaultAggregateRetain && !listing.refreshing {
			delete(s.devices, key)
		}
	}
	cached, ok := s.devices[owner]
	if ok && !cached.at.IsZero() {
		if now.Sub(cached.at) >= session.DefaultAggregateTTL && !cached.refreshing {
			cached.refreshing = true
			s.devices[owner] = cached
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.enumerateDevices(context.WithoutCancel(ctx), owner, config)
			}()
		}
		s.mu.Unlock()
		return cached.devices, cached.err
	}
	s.mu.Unlock()
	return s.enumerateDevices(ctx, owner, config)
}

func (s *sessionAggregateService) enumerateDevices(ctx context.Context, owner string, config *DeviceConnectorConfig) ([]AggregateDevice, error) {
	listCtx, cancel := context.WithTimeout(ctx, sessionAggregateDevicesTimeout)
	defer cancel()
	devices, err := config.Devices(listCtx, owner)
	s.mu.Lock()
	s.devices[owner] = aggregateDeviceListing{at: s.now(), devices: devices, err: err}
	s.mu.Unlock()
	return devices, err
}

func (s *sessionAggregateService) shutdown(context.Context) error {
	s.aggregator.Close()
	// An enumeration is bounded by its timeout.
	s.wg.Wait()
	return nil
}

// remoteAggregateDevices turns the linked account's device list into the
// devices a listing may ask, leaving out revoked devices, devices without an
// agent, and this desktop's own exposed device (self).
func remoteAggregateDevices(devices []syncservice.RemoteFleetDevice, self string) []AggregateDevice {
	result := make([]AggregateDevice, 0, len(devices))
	for _, listed := range devices {
		if listed.ID == "" || listed.ID == self || listed.RevokedAt != 0 || listed.Agent == nil {
			continue
		}
		result = append(result, AggregateDevice{
			ID: listed.ID, Name: listed.Name, Kind: listed.Kind, Platform: listed.Agent.Platform,
			Online: listed.Agent.StateDigest != "", Digest: listed.Agent.StateDigest, TerminalEnabled: listed.Agent.TerminalEnabled,
		})
	}
	return result
}
