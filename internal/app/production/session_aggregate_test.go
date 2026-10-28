package production

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

type aggregateTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func newAggregateTestClock() *aggregateTestClock {
	return &aggregateTestClock{now: time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)}
}

func (c *aggregateTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *aggregateTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newAggregateTestService(t *testing.T, config *DeviceConnectorConfig, events ipc.Emitter) *sessionAggregateService {
	t.Helper()
	connector := &productionConnector{}
	if config != nil {
		connector.device.Store(config)
	}
	manager := session.NewManager(session.Config{OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	service := newSessionAggregateService(manager, connector, events)
	t.Cleanup(func() { _ = service.shutdown(context.Background()) })
	return service
}

func hostsByKey(list session.AggregateList) map[string]session.AggregateHostStatus {
	hosts := make(map[string]session.AggregateHostStatus, len(list.Hosts))
	for _, host := range list.Hosts {
		hosts[host.Key] = host
	}
	return hosts
}

func TestSessionAggregateListsTheAccountsDevicesThroughTheBridge(t *testing.T) {
	var mu sync.Mutex
	var askedFor, checked, dialed []string
	config := &DeviceConnectorConfig{
		Dial: func(_ context.Context, deviceID, purpose string) (net.Conn, error) {
			mu.Lock()
			dialed = append(dialed, deviceID+"/"+purpose)
			mu.Unlock()
			return nil, errors.New("bridge down")
		},
		Devices: func(_ context.Context, userID string) ([]AggregateDevice, error) {
			mu.Lock()
			askedFor = append(askedFor, userID)
			mu.Unlock()
			return []AggregateDevice{
				{ID: "on", Name: "online", Kind: "local-helper", Online: true, Digest: "digest", TerminalEnabled: true},
				{ID: "revoked", Name: "revoked since", Online: true, Digest: "digest", TerminalEnabled: true},
				{ID: "off", Name: "offline", TerminalEnabled: true},
				{ID: "closed", Name: "closed", Online: true, Digest: "digest"},
			}, nil
		},
		CheckDevice: func(_ context.Context, userID, deviceID string) error {
			mu.Lock()
			checked = append(checked, userID+"/"+deviceID)
			mu.Unlock()
			if deviceID == "revoked" {
				return ipc.NewError(ipc.CodeForbidden, "设备已吊销")
			}
			return nil
		},
	}
	events := make(chan ipc.Event, 8)
	service := newAggregateTestService(t, config, ipc.EmitterFunc(func(_ context.Context, event ipc.Event) error {
		events <- event
		return nil
	}))

	list, err := service.list(ipc.WithUserID(context.Background(), "alice"), sessionAggregateRequest{WaitMS: -1})
	if err != nil {
		t.Fatal(err)
	}
	statuses := hostsByKey(list)
	if got := statuses["device:on"]; got.Status != session.AggregateHostPending || got.DeviceKind != "local-helper" || got.SessionID != "" {
		t.Fatalf("online device = %+v; want a pending listing without a session", got)
	}
	if got := statuses["device:off"].Status; got != session.AggregateHostOffline {
		t.Fatalf("offline device status = %q", got)
	}
	if got := statuses["device:closed"]; got.Status != session.AggregateHostFailed || got.ErrorCode != string(ipc.CodeForbidden) {
		t.Fatalf("terminal-disabled device = %+v", got)
	}

	failed := map[string]bool{}
	for range 2 {
		select {
		case event := <-events:
			update, ok := event.Payload.(session.AggregateUpdate)
			if event.Event != ipc.TopicTerminalSessions || event.OwnerID != "alice" || !ok || update.Host.Status != session.AggregateHostFailed {
				t.Fatalf("event = %+v; want alice's failed device refresh", event)
			}
			failed[update.Host.Key] = true
		case <-time.After(10 * time.Second):
			t.Fatal("no refresh event")
		}
	}
	if !failed["device:on"] || !failed["device:revoked"] {
		t.Fatalf("failed refreshes = %v", failed)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(askedFor) != 1 || askedFor[0] != "alice" {
		t.Fatalf("devices listed for %v; want the requester only", askedFor)
	}
	if len(checked) != 2 {
		t.Fatalf("checked %v; want each dialable device re-checked for alice", checked)
	}
	if len(dialed) != 1 || dialed[0] != "on/terminal" {
		t.Fatalf("dialed %v; a device that fails the re-check must not be dialed", dialed)
	}
}

func TestSessionAggregateKeepsTheLastGoodDevicesWhenAnEnumerationFails(t *testing.T) {
	clock := newAggregateTestClock()
	var mu sync.Mutex
	fail := false
	config := &DeviceConnectorConfig{
		Dial: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("unused") },
		Devices: func(context.Context, string) ([]AggregateDevice, error) {
			mu.Lock()
			defer mu.Unlock()
			if fail {
				return nil, errors.New("server unreachable")
			}
			return []AggregateDevice{{ID: "box"}}, nil
		},
	}
	service := newAggregateTestService(t, nil, nil)
	service.now = clock.Now
	ctx := context.Background()
	if devices, stale, err := service.accountDevices(ctx, "alice", config); err != nil || stale || len(devices) != 1 {
		t.Fatalf("first listing = %+v stale=%v %v", devices, stale, err)
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	clock.Advance(session.DefaultAggregateTTL)
	service.accountDevices(ctx, "alice", config)
	service.wg.Wait()
	devices, stale, err := service.accountDevices(ctx, "alice", config)
	if err != nil || !stale || len(devices) != 1 || devices[0].ID != "box" {
		t.Fatalf("after a failed refresh = %+v stale=%v %v; want the last good devices, stale", devices, stale, err)
	}
	if _, _, err := service.accountDevices(ctx, "bob", config); err == nil {
		t.Fatal("an owner without a good listing must see the failure")
	}
}

func TestSessionAggregateAnswersWithKnownDevicesWhileItRefreshesThem(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	release := make(chan struct{})
	config := &DeviceConnectorConfig{
		Devices: func(context.Context, string) ([]AggregateDevice, error) {
			mu.Lock()
			calls++
			call := calls
			mu.Unlock()
			if call == 1 {
				return []AggregateDevice{{ID: "first"}}, nil
			}
			<-release
			return []AggregateDevice{{ID: "second"}}, nil
		},
	}
	clock := newAggregateTestClock()
	service := newAggregateTestService(t, nil, nil)
	service.now = clock.Now
	ctx := context.Background()
	if devices, _, err := service.accountDevices(ctx, "alice", config); err != nil || len(devices) != 1 || devices[0].ID != "first" {
		t.Fatalf("first listing = %+v, %v", devices, err)
	}
	clock.Advance(session.DefaultAggregateTTL)
	// The refresh is held, so this answer can only come from the earlier listing.
	if devices, _, err := service.accountDevices(ctx, "alice", config); err != nil || len(devices) != 1 || devices[0].ID != "first" {
		t.Fatalf("expired listing = %+v, %v; want the earlier answer at once", devices, err)
	}
	close(release)
	service.wg.Wait()
	if devices, _, _ := service.accountDevices(ctx, "alice", config); len(devices) != 1 || devices[0].ID != "second" {
		t.Fatalf("refreshed listing = %+v", devices)
	}
}

func TestSessionAggregateAsksTheServerOnceForEveryDevice(t *testing.T) {
	clock := newAggregateTestClock()
	var mu sync.Mutex
	calls := 0
	var fail error
	remote := session.AggregateList{
		Hosts: []session.AggregateHostStatus{
			{AggregateHost: session.AggregateHost{Key: "device:box", Kind: session.KindDevice, Name: "box", DeviceID: "box"}, Status: session.AggregateHostOK},
			{AggregateHost: session.AggregateHost{Key: "device:slow", Kind: session.KindDevice, Name: "slow", DeviceID: "slow"}, Status: session.AggregateHostPending},
		},
		Sessions: []session.AggregateSession{{HostKey: "device:box", TabID: "t1"}},
	}
	config := &DeviceConnectorConfig{
		Dial: func(context.Context, string, string) (net.Conn, error) {
			t.Error("a desktop must not dial a device bridge to list sessions")
			return nil, errors.New("unexpected")
		},
		RemoteSessions: func(_ context.Context, waitMS int) (session.AggregateList, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if fail != nil {
				return session.AggregateList{}, fail
			}
			return remote, nil
		},
	}
	events := make(chan session.AggregateUpdate, 8)
	service := newAggregateTestService(t, config, ipc.EmitterFunc(func(_ context.Context, event ipc.Event) error {
		if update, ok := event.Payload.(session.AggregateUpdate); ok {
			events <- update
		}
		return nil
	}))
	service.now = clock.Now
	ctx := session.WithOpenAccess(context.Background())

	first, err := service.list(ctx, sessionAggregateRequest{WaitMS: -1})
	if err != nil {
		t.Fatal(err)
	}
	if hosts := hostsByKey(first); hosts["device:box"].Status != session.AggregateHostOK || hosts["device:slow"].Status != session.AggregateHostPending || first.Complete {
		t.Fatalf("first answer = %+v", first)
	}
	if len(first.Sessions) != 1 || first.Sessions[0].TabID != "t1" {
		t.Fatalf("first sessions = %+v", first.Sessions)
	}

	// An incomplete answer is refreshed in the background and each device published.
	mu.Lock()
	remote.Hosts[1].Status = session.AggregateHostOK
	remote.Complete = true
	mu.Unlock()
	if _, err := service.list(ctx, sessionAggregateRequest{WaitMS: -1}); err != nil {
		t.Fatal(err)
	}
	service.wg.Wait()
	published := map[string]string{}
	for len(published) < 2 {
		select {
		case update := <-events:
			published[update.Host.Key] = update.Host.Status
		case <-time.After(10 * time.Second):
			t.Fatalf("published %v; want both devices", published)
		}
	}
	if published["device:slow"] != session.AggregateHostOK {
		t.Fatalf("published %v", published)
	}

	// A complete answer is served from the cache until it expires.
	before := calls
	if _, err := service.list(ctx, sessionAggregateRequest{WaitMS: -1}); err != nil {
		t.Fatal(err)
	}
	service.wg.Wait()
	if calls != before {
		t.Fatalf("server asked %d more times within the cache lifetime", calls-before)
	}

	// A failed refresh keeps the last good answer, stale.
	mu.Lock()
	fail = errors.New("server unreachable")
	mu.Unlock()
	clock.Advance(session.DefaultAggregateTTL)
	if _, err := service.list(ctx, sessionAggregateRequest{WaitMS: -1}); err != nil {
		t.Fatal(err)
	}
	service.wg.Wait()
	stale, err := service.list(ctx, sessionAggregateRequest{WaitMS: -1})
	if err != nil {
		t.Fatal(err)
	}
	if host := hostsByKey(stale)["device:box"]; host.Status != session.AggregateHostOK || !host.Stale || len(stale.Sessions) != 1 {
		t.Fatalf("after a failed refresh = %+v", stale)
	}

	// A new link starts over: without a good answer the failure shows.
	service.reset()
	failedList, err := service.list(ctx, sessionAggregateRequest{WaitMS: -1})
	if err != nil {
		t.Fatal(err)
	}
	if host := hostsByKey(failedList)["device:*"]; host.Status != session.AggregateHostFailed || len(failedList.Sessions) != 0 {
		t.Fatalf("after a reset = %+v", failedList)
	}
}

func TestMergeAggregateListsPrefersThisClientsHosts(t *testing.T) {
	local := session.AggregateList{
		Hosts:    []session.AggregateHostStatus{{AggregateHost: session.AggregateHost{Key: "device:box", SessionID: "s1"}, Status: session.AggregateHostOK}},
		Sessions: []session.AggregateSession{{HostKey: "device:box", TabID: "t1"}},
		Complete: true,
	}
	remote := session.AggregateList{
		Hosts: []session.AggregateHostStatus{
			{AggregateHost: session.AggregateHost{Key: "device:box"}, Status: session.AggregateHostOK},
			{AggregateHost: session.AggregateHost{Key: "device:other"}, Status: session.AggregateHostOK},
		},
		Sessions: []session.AggregateSession{{HostKey: "device:box", TabID: "t1"}, {HostKey: "device:box", TabID: "t2"}, {HostKey: "device:other", TabID: "t3"}},
		Complete: true,
	}
	merged := mergeAggregateLists(local, remote)
	if len(merged.Hosts) != 2 || merged.Hosts[0].SessionID != "s1" || merged.Hosts[1].Key != "device:other" {
		t.Fatalf("hosts = %+v", merged.Hosts)
	}
	ids := make([]string, 0, len(merged.Sessions))
	for _, item := range merged.Sessions {
		ids = append(ids, item.TabID)
	}
	if len(ids) != 2 || ids[0] != "t1" || ids[1] != "t3" {
		t.Fatalf("sessions = %v; a host listed here is not taken from the server", ids)
	}
	self := withoutDevice(remote, "other")
	if len(self.Hosts) != 1 || len(self.Sessions) != 2 {
		t.Fatalf("without this desktop's device = %+v", self)
	}
}

func TestDeviceSessionsListsOnlyTheRequestersDevices(t *testing.T) {
	var mu sync.Mutex
	var askedFor []string
	config := &DeviceConnectorConfig{
		Dial: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("bridge down") },
		Devices: func(_ context.Context, userID string) ([]AggregateDevice, error) {
			mu.Lock()
			askedFor = append(askedFor, userID)
			mu.Unlock()
			return []AggregateDevice{
				{ID: userID + "-box", Name: userID, TerminalEnabled: true, Online: true, Digest: "digest"},
				{ID: userID + "-off", Name: userID, TerminalEnabled: true},
			}, nil
		},
	}
	events := 0
	service := newAggregateTestService(t, config, ipc.EmitterFunc(func(context.Context, ipc.Event) error {
		mu.Lock()
		events++
		mu.Unlock()
		return nil
	}))
	production := &Production{Services: ProductionServices{sessionAggregate: service}}
	// The wait covers the device's failed listing, which a scoped listing must not publish.
	list, err := production.DeviceSessions(context.Background(), "alice", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	hosts := hostsByKey(list)
	if len(list.Hosts) != 2 || hosts["device:alice-box"].Status != session.AggregateHostFailed || hosts["device:alice-off"].Status != session.AggregateHostOffline {
		t.Fatalf("alice's device sessions = %+v", list)
	}
	if none, err := production.DeviceSessions(context.Background(), "", 0); err != nil || len(none.Hosts) != 0 {
		t.Fatalf("anonymous device sessions = %+v, %v", none, err)
	}
	if err := service.shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(askedFor) != 1 || askedFor[0] != "alice" {
		t.Fatalf("devices listed for %v", askedFor)
	}
	if events != 0 {
		t.Fatalf("published %d events for a server listing a desktop asked for", events)
	}
}

func TestSyncLinkChangeReconcilesExposureAndStartsTheDeviceListingOver(t *testing.T) {
	calls := 0
	config := &DeviceConnectorConfig{
		RemoteSessions: func(context.Context, int) (session.AggregateList, error) {
			calls++
			return session.AggregateList{
				Hosts:    []session.AggregateHostStatus{{AggregateHost: session.AggregateHost{Key: "device:old"}, Status: session.AggregateHostOK}},
				Sessions: []session.AggregateSession{},
				Complete: true,
			}, nil
		},
	}
	service := newAggregateTestService(t, config, nil)
	ctx := session.WithOpenAccess(context.Background())
	if _, err := service.list(ctx, sessionAggregateRequest{WaitMS: -1}); err != nil {
		t.Fatal(err)
	}
	reconciled := 0
	onSyncLinkChanged(func() { reconciled++ }, service)()
	if _, err := service.list(ctx, sessionAggregateRequest{WaitMS: -1}); err != nil {
		t.Fatal(err)
	}
	if reconciled != 1 || calls != 2 {
		t.Fatalf("reconciled=%d server listings=%d; want one reconcile and a fresh listing after the link change", reconciled, calls)
	}
	// Without exposure or listing the callback still runs.
	onSyncLinkChanged(nil, nil)()
}
