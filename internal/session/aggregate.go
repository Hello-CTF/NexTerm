package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// TopicTerminalSessions carries one host's freshly listed durable sessions
// after an aggregate refresh of that host ends.
const TopicTerminalSessions = "terminal://sessions"

// Aggregate host statuses.
const (
	// AggregateHostOK: the host answered; its sessions are listed.
	AggregateHostOK = "ok"
	// AggregateHostPending: the host is being asked and has not answered
	// yet; an update event follows when it does.
	AggregateHostPending = "pending"
	// AggregateHostFailed: the host could not be listed; Error says why.
	AggregateHostFailed = "failed"
	// AggregateHostOffline: the host is known but not reachable now, so the
	// state of its sessions is unknown. It is never guessed.
	AggregateHostOffline = "offline"
)

// Aggregate defaults.
const (
	DefaultAggregateTTL         = 5 * time.Second
	DefaultAggregateTimeout     = 10 * time.Second
	DefaultAggregateRetain      = 2 * time.Minute
	DefaultAggregateConcurrency = 8
)

// AggregateHost is one place durable sessions live: a connected SSH or
// device session, the local helper, or a device reached through the device
// bridge without a session.
type AggregateHost struct {
	// Key identifies the host across sessions and refreshes.
	Key  string `json:"key"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	// SessionID is the connected session of this client the host's sessions
	// can be taken over through; empty when there is none.
	SessionID string `json:"sessionId,omitempty"`
	AssetID   string `json:"assetId,omitempty"`
	DeviceID  string `json:"deviceId,omitempty"`
	// DeviceKind is the device's enroll kind, such as local-helper for a
	// desktop that exposes its local terminals.
	DeviceKind string `json:"deviceKind,omitempty"`
}

// AggregateHostStatus is a host and how its listing went.
type AggregateHostStatus struct {
	AggregateHost
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
	// FetchedAt is when the listed sessions were read (Unix ms); zero when
	// none were.
	FetchedAt int64 `json:"fetchedAt,omitempty"`
	// Stale reports that the listed sessions are older than the cache
	// lifetime because a refresh is still running.
	Stale bool `json:"stale,omitempty"`
}

// AggregateSession is one durable session of a host.
type AggregateSession struct {
	HostKey string `json:"hostKey"`
	// TabID is the durable session ID, which is also the tab ID it is
	// attached under.
	TabID     string `json:"tabId"`
	Title     string `json:"title,omitempty"`
	CreatedAt int64  `json:"createdAt,omitempty"`
	Cols      uint32 `json:"cols"`
	Rows      uint32 `json:"rows"`
	Dead      bool   `json:"dead,omitempty"`
	ExitCode  *int   `json:"exitCode,omitempty"`
	// Attention is the helper's state of the session; unknown when the
	// helper reports none. Its version is zero: a client prefers any state
	// an attached tab reports.
	Attention AttentionEvent `json:"attention"`
	// LastOutputAt and DetachedAt are Unix ms, zero when unknown;
	// Attached reports a client attached to it right now.
	LastOutputAt int64 `json:"lastOutputAt,omitempty"`
	DetachedAt   int64 `json:"detachedAt,omitempty"`
	Attached     bool  `json:"attached,omitempty"`
}

// AggregateList is the cross-host view of the requester's durable sessions.
type AggregateList struct {
	Hosts    []AggregateHostStatus `json:"hosts"`
	Sessions []AggregateSession    `json:"sessions"`
	// Complete reports that every reachable host answered and no listed
	// session is stale.
	Complete bool `json:"complete"`
}

// AggregateUpdate is the payload of TopicTerminalSessions: one host's status
// and its sessions, replacing what a client knew of that host.
type AggregateUpdate struct {
	Host     AggregateHostStatus `json:"host"`
	Sessions []AggregateSession  `json:"sessions"`
}

// AggregateTarget is a host to list. Offline and Failed hosts are reported
// without contacting them.
type AggregateTarget struct {
	Host    AggregateHost
	Offline bool
	// Failed lists the host as failed with this error.
	Failed error
	// Stale marks a host taken from an earlier listing of hosts (a device
	// list whose refresh failed); its sessions are reported stale.
	Stale bool
	List  func(context.Context) ([]base.DurableDescriptor, error)
}

// ErrHelperNotReady reports a host whose helper neither runs for its
// connection nor is being deployed. A listing never deploys one, so the host
// is reported offline: the state of its sessions is unknown.
var ErrHelperNotReady = errors.New("the host's helper is not running for this connection")

// AggregatorConfig configures an Aggregator; zero values take the defaults.
type AggregatorConfig struct {
	// TTL is how long a host's listing is served without asking it again.
	TTL time.Duration
	// Timeout bounds one host's listing, including the wait for a slot.
	Timeout time.Duration
	// Retain is how long a listing is kept to answer at once while a
	// refresh runs, and how long an idle owner's cache is kept.
	Retain      time.Duration
	Concurrency int
	Now         func() time.Time
	// Describe turns a listing error into the code and message shown.
	Describe func(error) (code, message string)
	// Publish receives each host refresh as it ends.
	Publish func(owner string, update AggregateUpdate)
}

// Aggregator lists durable sessions across hosts with a bounded fan-out,
// caches each host's answer briefly per owner, and publishes each refresh as
// it ends, so a caller gets cached or partial results at once and the rest
// as they arrive.
type Aggregator struct {
	config AggregatorConfig
	slots  chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	owners map[aggregateCacheKey]*aggregateOwner
	closed bool
}

// aggregateCacheKey separates owners, and the listings of one owner that ask
// different sets of hosts.
type aggregateCacheKey struct {
	owner string
	scope string
}

type aggregateOwner struct {
	hosts  map[string]*aggregateEntry
	usedAt time.Time
}

type aggregateEntry struct {
	host      AggregateHost
	sessions  []AggregateSession
	fetchedAt time.Time
	err       error
	errAt     time.Time
	// inflight is closed when the running refresh ends; nil when none runs.
	inflight chan struct{}
}

func NewAggregator(config AggregatorConfig) *Aggregator {
	if config.TTL <= 0 {
		config.TTL = DefaultAggregateTTL
	}
	if config.Timeout <= 0 {
		config.Timeout = DefaultAggregateTimeout
	}
	if config.Retain <= 0 {
		config.Retain = DefaultAggregateRetain
	}
	if config.Concurrency <= 0 {
		config.Concurrency = DefaultAggregateConcurrency
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Describe == nil {
		config.Describe = func(err error) (string, string) { return "", err.Error() }
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Aggregator{
		config: config,
		slots:  make(chan struct{}, config.Concurrency),
		ctx:    ctx,
		cancel: cancel,
		owners: make(map[aggregateCacheKey]*aggregateOwner),
	}
}

// List returns owner's sessions on targets. Hosts whose listing is fresh are
// answered from the cache; the others are refreshed in the background and
// List waits up to wait for them. A host still running then is reported
// pending, or with its previous listing marked stale; its result is
// published when it ends. Requests for the same owner share refreshes;
// owners never share a cache.
func (a *Aggregator) List(ctx context.Context, owner string, targets []AggregateTarget, wait time.Duration) AggregateList {
	return a.ListScoped(ctx, owner, "", targets, wait)
}

// ListScoped is List for one of several sets of hosts an owner lists: each
// scope keeps its own cache, so listing one set never drops another's hosts.
// Refreshes are published to owner whatever the scope.
func (a *Aggregator) ListScoped(ctx context.Context, owner, scope string, targets []AggregateTarget, wait time.Duration) AggregateList {
	cacheKey := aggregateCacheKey{owner: owner, scope: scope}
	now := a.config.Now()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return a.snapshotClosed(targets)
	}
	a.pruneOwnersLocked(now)
	cache := a.owners[cacheKey]
	if cache == nil {
		cache = &aggregateOwner{hosts: make(map[string]*aggregateEntry)}
		a.owners[cacheKey] = cache
	}
	cache.usedAt = now
	wanted := make(map[string]bool, len(targets))
	ordered := make([]AggregateTarget, 0, len(targets))
	var waits []chan struct{}
	for _, target := range targets {
		key := target.Host.Key
		if key == "" || wanted[key] {
			continue
		}
		wanted[key] = true
		ordered = append(ordered, target)
		if target.Offline || target.Failed != nil || target.List == nil {
			delete(cache.hosts, key)
			continue
		}
		entry := cache.hosts[key]
		if entry == nil || entry.host != target.Host {
			// A host that now resolves differently (another session, a
			// renamed device) starts over.
			entry = &aggregateEntry{host: target.Host}
			cache.hosts[key] = entry
		}
		if entry.inflight == nil && a.dueLocked(entry, now) {
			entry.inflight = make(chan struct{})
			a.startRefresh(ctx, cacheKey, cache, entry, target.List)
		}
		if entry.inflight != nil {
			waits = append(waits, entry.inflight)
		}
	}
	for key := range cache.hosts {
		if !wanted[key] {
			delete(cache.hosts, key)
		}
	}
	a.mu.Unlock()

	a.await(ctx, waits, wait)

	a.mu.Lock()
	defer a.mu.Unlock()
	return a.snapshotLocked(cache, ordered, a.config.Now())
}

func (a *Aggregator) dueLocked(entry *aggregateEntry, now time.Time) bool {
	last := entry.fetchedAt
	if entry.errAt.After(last) {
		last = entry.errAt
	}
	return last.IsZero() || now.Sub(last) >= a.config.TTL
}

func (a *Aggregator) pruneOwnersLocked(now time.Time) {
	for owner, cache := range a.owners {
		if now.Sub(cache.usedAt) < a.config.Retain {
			continue
		}
		busy := false
		for _, entry := range cache.hosts {
			busy = busy || entry.inflight != nil
		}
		if !busy {
			delete(a.owners, owner)
		}
	}
}

// startRefresh lists one host in the background. It runs detached from the
// request, which may return before the host answers, but keeps its values
// (the requester's identity) and is bounded by the per-host timeout.
func (a *Aggregator) startRefresh(ctx context.Context, key aggregateCacheKey, cache *aggregateOwner, entry *aggregateEntry, list func(context.Context) ([]base.DurableDescriptor, error)) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.config.Timeout)
		defer cancel()
		stop := context.AfterFunc(a.ctx, cancel)
		defer stop()
		descriptors, err := a.listHost(refreshCtx, list)
		at := a.config.Now()

		a.mu.Lock()
		if err != nil {
			entry.err, entry.errAt = err, at
		} else {
			entry.sessions = aggregateSessions(entry.host.Key, descriptors)
			entry.fetchedAt, entry.err, entry.errAt = at, nil, time.Time{}
		}
		close(entry.inflight)
		entry.inflight = nil
		current := cache.hosts[entry.host.Key] == entry && a.owners[key] == cache && !a.closed
		var update AggregateUpdate
		if current {
			update.Host = a.statusLocked(entry, at)
			update.Sessions = visibleSessions(update.Host, entry)
		}
		a.mu.Unlock()
		if current && a.config.Publish != nil {
			a.config.Publish(key.owner, update)
		}
	}()
}

func (a *Aggregator) listHost(ctx context.Context, list func(context.Context) ([]base.DurableDescriptor, error)) ([]base.DurableDescriptor, error) {
	select {
	case a.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-a.slots }()
	return list(ctx)
}

func (a *Aggregator) await(ctx context.Context, waits []chan struct{}, wait time.Duration) {
	if len(waits) == 0 || wait <= 0 {
		return
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for _, done := range waits {
		select {
		case <-done:
		case <-timer.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (a *Aggregator) statusLocked(entry *aggregateEntry, now time.Time) AggregateHostStatus {
	status := AggregateHostStatus{AggregateHost: entry.host}
	fresh := !entry.fetchedAt.IsZero() && now.Sub(entry.fetchedAt) < a.config.Retain
	switch {
	case entry.err != nil && !entry.errAt.Before(entry.fetchedAt) && errors.Is(entry.err, ErrHelperNotReady):
		status.Status = AggregateHostOffline
		status.ErrorCode, status.Error = "helper_not_ready", "主机上的持久终端组件尚未就绪，状态未知"
	case entry.err != nil && !entry.errAt.Before(entry.fetchedAt):
		status.Status = AggregateHostFailed
		status.ErrorCode, status.Error = a.describe(entry.err)
	case fresh:
		status.Status = AggregateHostOK
		status.FetchedAt = entry.fetchedAt.UnixMilli()
		status.Stale = now.Sub(entry.fetchedAt) >= a.config.TTL
	default:
		status.Status = AggregateHostPending
	}
	return status
}

func (a *Aggregator) describe(err error) (string, string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", "主机在限定时间内没有回应"
	}
	return a.config.Describe(err)
}

// visibleSessions lists a host's sessions only while its listing is shown:
// a failed or pending host lists none rather than a guess.
func visibleSessions(status AggregateHostStatus, entry *aggregateEntry) []AggregateSession {
	if status.Status != AggregateHostOK {
		return []AggregateSession{}
	}
	return append([]AggregateSession{}, entry.sessions...)
}

func (a *Aggregator) snapshotLocked(cache *aggregateOwner, targets []AggregateTarget, now time.Time) AggregateList {
	list := AggregateList{Hosts: make([]AggregateHostStatus, 0, len(targets)), Sessions: []AggregateSession{}, Complete: true}
	seen := make(map[string]bool)
	for _, target := range targets {
		var status AggregateHostStatus
		var sessions []AggregateSession
		switch {
		case target.Failed != nil:
			status = AggregateHostStatus{AggregateHost: target.Host, Status: AggregateHostFailed}
			status.ErrorCode, status.Error = a.describe(target.Failed)
		case target.Offline || target.List == nil:
			status = AggregateHostStatus{AggregateHost: target.Host, Status: AggregateHostOffline}
		default:
			entry := cache.hosts[target.Host.Key]
			if entry == nil {
				status = AggregateHostStatus{AggregateHost: target.Host, Status: AggregateHostPending}
				break
			}
			status = a.statusLocked(entry, now)
			sessions = visibleSessions(status, entry)
			if target.Stale && status.Status == AggregateHostOK {
				status.Stale = true
			}
		}
		if status.Status == AggregateHostPending || status.Stale {
			list.Complete = false
		}
		list.Hosts = append(list.Hosts, status)
		for _, item := range sessions {
			// The same helper can be reached two ways (the local helper and
			// the device that exposes it); the first host lists it.
			if seen[item.TabID] {
				continue
			}
			seen[item.TabID] = true
			list.Sessions = append(list.Sessions, item)
		}
	}
	return list
}

func (a *Aggregator) snapshotClosed(targets []AggregateTarget) AggregateList {
	list := AggregateList{Hosts: make([]AggregateHostStatus, 0, len(targets)), Sessions: []AggregateSession{}}
	for _, target := range targets {
		list.Hosts = append(list.Hosts, AggregateHostStatus{AggregateHost: target.Host, Status: AggregateHostOffline})
	}
	return list
}

// Reset drops every cached listing, for example after the account a client
// lists through changed. Running refreshes end without publishing.
func (a *Aggregator) Reset() {
	a.mu.Lock()
	a.owners = make(map[aggregateCacheKey]*aggregateOwner)
	a.mu.Unlock()
}

// Close stops every refresh and waits for them; later lists report every
// host offline.
func (a *Aggregator) Close() {
	a.mu.Lock()
	a.closed = true
	a.owners = make(map[aggregateCacheKey]*aggregateOwner)
	a.mu.Unlock()
	a.cancel()
	a.wg.Wait()
}

func aggregateSessions(hostKey string, descriptors []base.DurableDescriptor) []AggregateSession {
	sessions := make([]AggregateSession, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if descriptor.ID == "" {
			continue
		}
		item := AggregateSession{
			HostKey: hostKey, TabID: descriptor.ID, Title: descriptor.Title,
			Cols: descriptor.Cols, Rows: descriptor.Rows, Dead: descriptor.Dead,
		}
		if !descriptor.CreatedAt.IsZero() {
			item.CreatedAt = descriptor.CreatedAt.UnixMilli()
		}
		if descriptor.ExitCode != nil {
			code := *descriptor.ExitCode
			item.ExitCode = &code
		}
		item.Attention = attentionFromState(descriptor.State, AttentionSourceHelper)
		if descriptor.Dead && (descriptor.State == nil || descriptor.State.State != base.SessionExited) {
			item.Attention = AttentionEvent{State: base.SessionExited, Confidence: base.ConfidenceSignal, Source: AttentionSourceHelper, Reason: "exit", ExitCode: item.ExitCode}
		}
		item.Attention.TabID = descriptor.ID
		if activity := descriptor.Activity; activity != nil {
			if activity.LastOutputAt != nil {
				item.LastOutputAt = activity.LastOutputAt.UnixMilli()
			}
			if activity.DetachedAt != nil {
				item.DetachedAt = activity.DetachedAt.UnixMilli()
			}
			item.Attached = activity.Attached
		}
		sessions = append(sessions, item)
	}
	return sessions
}

// Aggregate host key prefixes.
const (
	AggregateKeyLocal = "local"
	aggregateKeyAsset = "asset:"
	// AggregateKeyDevice prefixes a device ID; a device session and the
	// device reached without one share the key.
	AggregateKeyDevice  = "device:"
	aggregateKeySession = "session:"
	deviceAssetPrefix   = "device/"
)

// AggregateTargetsFor lists the hosts whose durable sessions the requester
// may see through this manager: every SSH and device session it may access
// (connected ones are listed, the others reported offline), local sessions
// when local terminals run in a helper, and the local helper itself when no
// local session is connected. It never connects anything.
func (m *Manager) AggregateTargetsFor(ctx context.Context) ([]AggregateTarget, error) {
	infos, err := m.ListSessionsFor(ctx)
	if err != nil {
		return nil, err
	}
	targets := make([]AggregateTarget, 0, len(infos)+1)
	index := make(map[string]int, len(infos)+1)
	add := func(target AggregateTarget, connected bool) {
		if at, ok := index[target.Host.Key]; ok {
			// Several sessions reach the same host; a connected one lists it.
			if connected && targets[at].Offline {
				targets[at] = target
			}
			return
		}
		index[target.Host.Key] = len(targets)
		targets = append(targets, target)
	}
	for _, info := range infos {
		if !m.durableHostKind(info.Kind) {
			continue
		}
		if info.Kind == KindSSH {
			// A host set to regular terminals runs no durable sessions here.
			if mode, err := m.TerminalModeFor(ctx, info.ID); err != nil || mode == TerminalModeRegular {
				continue
			}
		}
		host := AggregateHost{Key: aggregateHostKey(info), Kind: info.Kind, Name: info.Name, SessionID: info.ID, AssetID: info.AssetID}
		if info.Kind == KindDevice {
			host.DeviceID = strings.TrimPrefix(info.AssetID, deviceAssetPrefix)
		}
		connected := info.Status == StatusConnected
		target := AggregateTarget{Host: host, Offline: !connected}
		if connected {
			sessionID := info.ID
			target.List = func(ctx context.Context) ([]base.DurableDescriptor, error) {
				return m.listReadyDurable(ctx, sessionID)
			}
		}
		add(target, connected)
	}
	if _, ok := index[AggregateKeyLocal]; !ok && m.localDurableEnabled() && userIDFromContext(ctx) == "" && m.requestOpenAccess(ctx) {
		// The local helper belongs to the account that runs this process;
		// a multi-account server runs none, and an identified requester
		// never reaches it.
		add(AggregateTarget{
			Host: AggregateHost{Key: AggregateKeyLocal, Kind: KindLocal, Name: "本机"},
			List: m.listLocalDurable,
		}, true)
	}
	return targets, nil
}

func aggregateHostKey(info SessionInfo) string {
	switch {
	case info.Kind == KindLocal:
		return AggregateKeyLocal
	case info.Kind == KindDevice && strings.HasPrefix(info.AssetID, deviceAssetPrefix):
		return AggregateKeyDevice + strings.TrimPrefix(info.AssetID, deviceAssetPrefix)
	case info.AssetID != "":
		return aggregateKeyAsset + info.AssetID
	default:
		return aggregateKeySession + info.ID
	}
}

// listReadyDurable lists a connected session's durable sessions without ever
// deploying a helper. An SSH host is listed through the helper this
// connection already resolved, or the deployment already running for it;
// otherwise it answers ErrHelperNotReady. Device and local sessions reach
// their helper without deploying anything and list as usual.
func (m *Manager) listReadyDurable(ctx context.Context, sessionID string) ([]base.DurableDescriptor, error) {
	session, err := m.Session(sessionID)
	if err != nil {
		return nil, err
	}
	session.mu.Lock()
	kind := session.asset.Kind
	connected := session.status == StatusConnected && session.transport != nil
	transport, generation := session.transport, session.generation
	allowed := sessionOwnerAllowed(session, userIDFromContext(ctx))
	session.mu.Unlock()
	if kind != KindSSH {
		return m.ListRemoteDurable(ctx, sessionID)
	}
	if !allowed {
		return nil, ErrForbidden
	}
	if !connected {
		return nil, ErrDisconnected
	}
	session.durableMu.Lock()
	var provider base.DurableProvider
	if session.durableProvider != nil && session.durableGen == generation && session.durableTransport == transport {
		provider = session.durableProvider
	}
	session.durableMu.Unlock()
	if provider == nil {
		entry := matchingBackgroundHelper(session, transport, generation)
		if entry == nil {
			return nil, ErrHelperNotReady
		}
		select {
		case <-entry.done:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
		if entry.err != nil {
			return nil, entry.err
		}
		if entry.provider == nil {
			return nil, ErrHelperNotReady
		}
		provider = entry.provider
	}
	lister, ok := provider.(base.DurableDescriptorLister)
	if !ok {
		return nil, ErrUnsupported
	}
	descriptors, err := lister.ListDurableDescriptors(ctx)
	if err != nil {
		return nil, err
	}
	if descriptors == nil {
		descriptors = []base.DurableDescriptor{}
	}
	return descriptors, nil
}

// listLocalDurable lists the local helper's sessions without a local
// session. With no helper running none of them can be alive.
func (m *Manager) listLocalDurable(ctx context.Context) ([]base.DurableDescriptor, error) {
	provider, err := m.localDurableProvider(ctx, localRecoverWait, true)
	if localHelperNotRunning(KindLocal, err) {
		return []base.DurableDescriptor{}, nil
	}
	if err != nil {
		return nil, err
	}
	lister, ok := provider.(base.DurableDescriptorLister)
	if !ok {
		return nil, ErrUnsupported
	}
	descriptors, err := lister.ListDurableDescriptors(ctx)
	if err != nil {
		return nil, err
	}
	if descriptors == nil {
		descriptors = []base.DurableDescriptor{}
	}
	return descriptors, nil
}
