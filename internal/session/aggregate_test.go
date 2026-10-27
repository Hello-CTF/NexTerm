package session

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// aggregateHostFake is one host's listing whose answers the test releases.
type aggregateHostFake struct {
	calls   atomic.Int32
	release chan struct{}
	// block keeps the listing waiting until its context ends.
	block bool
	err   error
	ids   []string
	state *base.SessionState
}

func (h *aggregateHostFake) list(ctx context.Context) ([]base.DurableDescriptor, error) {
	h.calls.Add(1)
	if h.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if h.release != nil {
		select {
		case <-h.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if h.err != nil {
		return nil, h.err
	}
	descriptors := make([]base.DurableDescriptor, 0, len(h.ids))
	for _, id := range h.ids {
		descriptors = append(descriptors, base.DurableDescriptor{ID: id, Title: "t-" + id, Cols: 80, Rows: 24, State: h.state})
	}
	return descriptors, nil
}

type aggregateUpdates struct {
	ch chan struct {
		owner  string
		update AggregateUpdate
	}
}

func newAggregateUpdates() *aggregateUpdates {
	return &aggregateUpdates{ch: make(chan struct {
		owner  string
		update AggregateUpdate
	}, 64)}
}

func (u *aggregateUpdates) publish(owner string, update AggregateUpdate) {
	u.ch <- struct {
		owner  string
		update AggregateUpdate
	}{owner, update}
}

// next returns the next published update.
func (u *aggregateUpdates) next(t *testing.T) (string, AggregateUpdate) {
	t.Helper()
	select {
	case got := <-u.ch:
		return got.owner, got.update
	case <-time.After(10 * time.Second):
		t.Fatal("no aggregate update was published")
		return "", AggregateUpdate{}
	}
}

func aggregateTarget(key string, host *aggregateHostFake) AggregateTarget {
	return AggregateTarget{Host: AggregateHost{Key: key, Kind: KindSSH, Name: key}, List: host.list}
}

func hostStatus(t *testing.T, list AggregateList, key string) AggregateHostStatus {
	t.Helper()
	for _, host := range list.Hosts {
		if host.Key == key {
			return host
		}
	}
	t.Fatalf("host %q missing from %+v", key, list.Hosts)
	return AggregateHostStatus{}
}

func sessionIDs(list AggregateList) map[string]string {
	ids := make(map[string]string, len(list.Sessions))
	for _, item := range list.Sessions {
		ids[item.TabID] = item.HostKey
	}
	return ids
}

func TestAggregatorReportsSlowAndFailedHostsWithoutHoldingTheAnswer(t *testing.T) {
	updates := newAggregateUpdates()
	aggregator := NewAggregator(AggregatorConfig{Publish: updates.publish})
	t.Cleanup(aggregator.Close)
	needsMe := &base.SessionState{State: base.SessionNeedsMe, Confidence: base.ConfidenceSignal, Reason: "bell", Since: time.Unix(100, 0)}
	fast := &aggregateHostFake{ids: []string{"a1", "a2"}, state: needsMe}
	broken := &aggregateHostFake{err: errors.New("helper gone")}
	slow := &aggregateHostFake{ids: []string{"s1"}, release: make(chan struct{})}
	targets := []AggregateTarget{
		aggregateTarget("fast", fast),
		aggregateTarget("broken", broken),
		aggregateTarget("slow", slow),
		{Host: AggregateHost{Key: "away", Kind: KindSSH, Name: "away"}, Offline: true},
	}

	// The first answer does not wait: every refresh is still running.
	first := aggregator.List(t.Context(), "", targets, 0)
	if first.Complete {
		t.Fatalf("first list = %+v, want incomplete", first)
	}
	if got := hostStatus(t, first, "away").Status; got != AggregateHostOffline {
		t.Fatalf("offline host status = %q", got)
	}
	for range 2 {
		updates.next(t)
	}

	partial := aggregator.List(t.Context(), "", targets, 0)
	if got := hostStatus(t, partial, "fast"); got.Status != AggregateHostOK || got.FetchedAt == 0 {
		t.Fatalf("fast host = %+v", got)
	}
	if got := hostStatus(t, partial, "broken"); got.Status != AggregateHostFailed || got.Error != "helper gone" {
		t.Fatalf("broken host = %+v", got)
	}
	if got := hostStatus(t, partial, "slow").Status; got != AggregateHostPending {
		t.Fatalf("slow host status = %q, want pending", got)
	}
	if partial.Complete {
		t.Fatal("a list with a pending host must not be complete")
	}
	if got := sessionIDs(partial); len(got) != 2 || got["a1"] != "fast" || got["a2"] != "fast" {
		t.Fatalf("partial sessions = %v", got)
	}
	for _, item := range partial.Sessions {
		if item.Attention.State != base.SessionNeedsMe || item.Attention.TabID != item.TabID || item.Attention.Source != AttentionSourceHelper {
			t.Fatalf("session attention = %+v", item.Attention)
		}
	}
	if fast.calls.Load() != 1 || slow.calls.Load() != 1 {
		t.Fatalf("listing calls fast=%d slow=%d; a running or fresh host is not asked again", fast.calls.Load(), slow.calls.Load())
	}

	close(slow.release)
	owner, update := updates.next(t)
	if owner != "" || update.Host.Key != "slow" || update.Host.Status != AggregateHostOK || len(update.Sessions) != 1 || update.Sessions[0].TabID != "s1" {
		t.Fatalf("slow update = %q %+v", owner, update)
	}
	final := aggregator.List(t.Context(), "", targets, 0)
	if got := sessionIDs(final); len(got) != 3 || got["s1"] != "slow" {
		t.Fatalf("final sessions = %v", got)
	}
}

func TestAggregatorWaitsForHostsThatAnswerInTime(t *testing.T) {
	aggregator := NewAggregator(AggregatorConfig{})
	t.Cleanup(aggregator.Close)
	host := &aggregateHostFake{ids: []string{"x"}}
	list := aggregator.List(t.Context(), "", []AggregateTarget{aggregateTarget("h", host)}, time.Minute)
	if !list.Complete || hostStatus(t, list, "h").Status != AggregateHostOK || len(list.Sessions) != 1 {
		t.Fatalf("list = %+v, want the answered host", list)
	}
}

func TestAggregatorTimesOutAHostThatDoesNotAnswer(t *testing.T) {
	updates := newAggregateUpdates()
	aggregator := NewAggregator(AggregatorConfig{Timeout: 20 * time.Millisecond, Publish: updates.publish})
	t.Cleanup(aggregator.Close)
	host := &aggregateHostFake{block: true}
	targets := []AggregateTarget{aggregateTarget("stuck", host)}
	aggregator.List(t.Context(), "", targets, 0)
	_, update := updates.next(t)
	if update.Host.Status != AggregateHostFailed || update.Host.ErrorCode != "timeout" || len(update.Sessions) != 0 {
		t.Fatalf("timed out host = %+v", update)
	}
}

func TestAggregatorBoundsConcurrentListings(t *testing.T) {
	updates := newAggregateUpdates()
	aggregator := NewAggregator(AggregatorConfig{Concurrency: 2, Publish: updates.publish})
	t.Cleanup(aggregator.Close)
	release := make(chan struct{})
	var mu sync.Mutex
	running, peak := 0, 0
	started := make(chan struct{}, 8)
	targets := make([]AggregateTarget, 0, 5)
	for _, key := range []string{"h1", "h2", "h3", "h4", "h5"} {
		targets = append(targets, AggregateTarget{Host: AggregateHost{Key: key}, List: func(ctx context.Context) ([]base.DurableDescriptor, error) {
			mu.Lock()
			running++
			peak = max(peak, running)
			mu.Unlock()
			started <- struct{}{}
			<-release
			mu.Lock()
			running--
			mu.Unlock()
			return nil, nil
		}})
	}
	aggregator.List(t.Context(), "", targets, 0)
	for range 2 {
		<-started
	}
	mu.Lock()
	busy := running
	mu.Unlock()
	if busy != 2 {
		t.Fatalf("running listings = %d, want the limit of 2", busy)
	}
	close(release)
	for range 5 {
		updates.next(t)
	}
	if peak != 2 {
		t.Fatalf("peak concurrent listings = %d, want 2", peak)
	}
}

func TestAggregatorServesCachedListingsAndMarksThemStale(t *testing.T) {
	clock := newSessionTestClock()
	updates := newAggregateUpdates()
	aggregator := NewAggregator(AggregatorConfig{TTL: 5 * time.Second, Retain: time.Minute, Now: clock.Now, Publish: updates.publish})
	t.Cleanup(aggregator.Close)
	host := &aggregateHostFake{ids: []string{"c1"}}
	targets := []AggregateTarget{aggregateTarget("h", host)}
	aggregator.List(t.Context(), "", targets, time.Minute)
	updates.next(t)

	clock.Advance(4 * time.Second)
	cached := aggregator.List(t.Context(), "", targets, time.Minute)
	if host.calls.Load() != 1 || !cached.Complete || len(cached.Sessions) != 1 {
		t.Fatalf("within the TTL: calls=%d list=%+v; want the cached answer", host.calls.Load(), cached)
	}

	host.release = make(chan struct{})
	clock.Advance(2 * time.Second)
	stale := aggregator.List(t.Context(), "", targets, 0)
	if got := hostStatus(t, stale, "h"); got.Status != AggregateHostOK || !got.Stale || stale.Complete || len(stale.Sessions) != 1 {
		t.Fatalf("refreshing host = %+v (list %+v); want the earlier listing marked stale", got, stale)
	}
	close(host.release)
	if _, update := updates.next(t); update.Host.Stale || update.Host.Status != AggregateHostOK {
		t.Fatalf("refreshed update = %+v", update)
	}
	if host.calls.Load() != 2 {
		t.Fatalf("listing calls = %d, want one refresh after the TTL", host.calls.Load())
	}
}

func TestAggregatorHidesSessionsOfAHostWhoseRefreshFailed(t *testing.T) {
	clock := newSessionTestClock()
	updates := newAggregateUpdates()
	aggregator := NewAggregator(AggregatorConfig{TTL: time.Second, Now: clock.Now, Publish: updates.publish})
	t.Cleanup(aggregator.Close)
	host := &aggregateHostFake{ids: []string{"gone"}}
	targets := []AggregateTarget{aggregateTarget("h", host)}
	aggregator.List(t.Context(), "", targets, time.Minute)
	updates.next(t)
	host.err = errors.New("unreachable")
	clock.Advance(2 * time.Second)
	aggregator.List(t.Context(), "", targets, 0)
	updates.next(t)
	list := aggregator.List(t.Context(), "", targets, 0)
	if got := hostStatus(t, list, "h"); got.Status != AggregateHostFailed || len(list.Sessions) != 0 {
		t.Fatalf("failed refresh = %+v sessions %+v; want no guessed sessions", got, list.Sessions)
	}
}

func TestAggregatorKeepsOwnersApart(t *testing.T) {
	updates := newAggregateUpdates()
	aggregator := NewAggregator(AggregatorConfig{Publish: updates.publish})
	t.Cleanup(aggregator.Close)
	alice := &aggregateHostFake{ids: []string{"alice-tab"}}
	bob := &aggregateHostFake{ids: []string{"bob-tab"}}
	aliceList := aggregator.List(t.Context(), "alice", []AggregateTarget{aggregateTarget("same-key", alice)}, time.Minute)
	if owner, _ := updates.next(t); owner != "alice" {
		t.Fatalf("alice's refresh published to %q", owner)
	}
	bobList := aggregator.List(t.Context(), "bob", []AggregateTarget{aggregateTarget("same-key", bob)}, time.Minute)
	if owner, _ := updates.next(t); owner != "bob" {
		t.Fatalf("bob's refresh published to %q", owner)
	}
	if got := sessionIDs(aliceList); len(got) != 1 || got["alice-tab"] == "" {
		t.Fatalf("alice sees %v", got)
	}
	if got := sessionIDs(bobList); len(got) != 1 || got["bob-tab"] == "" {
		t.Fatalf("bob sees %v; another owner's cached listing must not answer", got)
	}
	if bob.calls.Load() != 1 {
		t.Fatalf("bob's host was listed %d times, want 1", bob.calls.Load())
	}
}

func TestAggregatorListsASessionReachedTwiceOnce(t *testing.T) {
	aggregator := NewAggregator(AggregatorConfig{})
	t.Cleanup(aggregator.Close)
	local := &aggregateHostFake{ids: []string{"shared"}}
	device := &aggregateHostFake{ids: []string{"shared", "other"}}
	list := aggregator.List(t.Context(), "", []AggregateTarget{aggregateTarget("local", local), aggregateTarget("device:d", device)}, time.Minute)
	if got := sessionIDs(list); len(got) != 2 || got["shared"] != "local" || got["other"] != "device:d" {
		t.Fatalf("sessions = %v; the first host lists a shared session", got)
	}
}

func TestAggregateTargetsListOnlyTheRequestersHosts(t *testing.T) {
	provider := &descriptorListingDurableProvider{listingDurableProvider: &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}}
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: &countingResolver{provider: provider}})
	t.Cleanup(func() { _ = manager.Close() })
	alice := requestTestUser("alice")
	mine, err := manager.Connect(alice, Asset{ID: "asset-a", Name: "alpha", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Connect(requestTestUser("bob"), Asset{ID: "asset-b", Name: "bravo", Kind: KindSSH}); err != nil {
		t.Fatal(err)
	}
	away, err := manager.Connect(alice, Asset{ID: "asset-c", Name: "charlie", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Disconnect(away.ID); err != nil {
		t.Fatal(err)
	}
	targets, err := manager.AggregateTargetsFor(alice)
	if err != nil {
		t.Fatal(err)
	}
	byKey := make(map[string]AggregateTarget, len(targets))
	for _, target := range targets {
		byKey[target.Host.Key] = target
	}
	if _, ok := byKey["asset:asset-b"]; ok {
		t.Fatalf("targets %+v include another account's host", targets)
	}
	connected, ok := byKey["asset:asset-a"]
	if !ok || connected.Offline || connected.List == nil || connected.Host.SessionID != mine.ID || connected.Host.Name != "alpha" {
		t.Fatalf("connected target = %+v", connected)
	}
	if _, ok := byKey[AggregateKeyLocal]; ok {
		t.Fatal("an identified requester must not reach the process's local helper")
	}
	if disconnected, ok := byKey["asset:asset-c"]; ok && (!disconnected.Offline || disconnected.List != nil) {
		t.Fatalf("disconnected target = %+v; want offline without a listing", disconnected)
	}
	if _, err := manager.AggregateTargetsFor(context.Background()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("anonymous targets error = %v, want ErrForbidden", err)
	}
}
