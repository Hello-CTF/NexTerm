package fleetserver

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"github.com/coder/websocket"
)

// setDeviceKind rewrites an enrolled device's kind.
func (f *serviceFixture) setDeviceKind(t *testing.T, deviceID, kind string) {
	t.Helper()
	userDevice := query.Use(f.db).UserDevice
	if _, err := userDevice.WithContext(context.Background()).Where(userDevice.ID.Eq(deviceID)).Update(userDevice.Kind, kind); err != nil {
		t.Fatal(err)
	}
}

func (f *serviceFixture) reportDigest(t *testing.T, deviceID, digest string) *websocket.Conn {
	t.Helper()
	return f.reportHelper(t, deviceID, digest, "")
}

func (f *serviceFixture) reportHelper(t *testing.T, deviceID, digest, instanceID string) *websocket.Conn {
	t.Helper()
	_, control := wsPair(t)
	f.service.registry.RegisterControl(deviceID, control, digest, instanceID, nil)
	return control
}

func terminalDeviceIDs(t *testing.T, f *serviceFixture, userID string) []string {
	t.Helper()
	devices, err := f.service.TerminalDevices(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(devices))
	for _, device := range devices {
		ids = append(ids, device.ID)
	}
	return ids
}

func TestTerminalDevicesListsASharedHelperOnce(t *testing.T) {
	f := newServiceFixture(t)
	alice := f.createUser(t, "share-alice")
	machine := f.enrollAgent(t, alice, "machine")
	desktop := f.enrollAgent(t, alice, "desktop")
	f.setDeviceKind(t, desktop.DeviceID, agent.DeviceKindLocalHelper)

	// Offline: neither reports a digest, so both stay listed.
	if ids := terminalDeviceIDs(t, f, alice.ID); len(ids) != 2 {
		t.Fatalf("offline devices = %v; want both", ids)
	}
	// Only the local-helper device is online: it is the only route.
	f.reportHelper(t, desktop.DeviceID, "same", "inst")
	ids := terminalDeviceIDs(t, f, alice.ID)
	if len(ids) != 2 {
		t.Fatalf("one online device = %v; want the online one plus the offline agent", ids)
	}
	// Both online with one digest: the agent device is the canonical route.
	agentControl := f.reportHelper(t, machine.DeviceID, "same", "inst")
	ids = terminalDeviceIDs(t, f, alice.ID)
	if len(ids) != 1 || ids[0] != machine.DeviceID {
		t.Fatalf("shared helper devices = %v; want only the agent %s", ids, machine.DeviceID)
	}
	// The agent goes offline: the local-helper device is the fallback.
	f.service.registry.UnregisterControl(machine.DeviceID, agentControl)
	ids = terminalDeviceIDs(t, f, alice.ID)
	if len(ids) != 2 {
		t.Fatalf("agent offline devices = %v; want both listed (offline agent, online fallback)", ids)
	}
}

func TestTerminalDevicesNeverMergeAcrossAccountsOrEmptyDigests(t *testing.T) {
	f := newServiceFixture(t)
	alice := f.createUser(t, "merge-alice")
	bob := f.createUser(t, "merge-bob")
	a := f.enrollAgent(t, alice, "a")
	a2 := f.enrollAgent(t, alice, "a2")
	b := f.enrollAgent(t, bob, "b")
	f.reportHelper(t, a.DeviceID, "same", "inst")
	f.reportHelper(t, b.DeviceID, "same", "inst")
	f.reportDigest(t, a2.DeviceID, "")
	if ids := terminalDeviceIDs(t, f, alice.ID); len(ids) != 2 {
		t.Fatalf("alice = %v; want both (other account's digest and an empty digest never merge)", ids)
	}
	if ids := terminalDeviceIDs(t, f, bob.ID); len(ids) != 1 || ids[0] != b.DeviceID {
		t.Fatalf("bob = %v; want his device", ids)
	}
}

func TestDropSharedHelperDevicesTieBreaks(t *testing.T) {
	devices := []TerminalDevice{
		{ID: "b", Kind: "agent", Digest: "d", HelperInstanceID: "i", TerminalEnabled: true},
		{ID: "a", Kind: "agent", Digest: "d", HelperInstanceID: "i", TerminalEnabled: true},
		{ID: "l", Kind: agent.DeviceKindLocalHelper, Digest: "d", HelperInstanceID: "i", TerminalEnabled: true},
		{ID: "x", Kind: "agent"}, {ID: "y", Kind: "agent"},
	}
	got := dropSharedHelperDevices(devices, map[string]int64{"a": 5, "b": 5, "l": 1})
	var ids []string
	for _, device := range got {
		ids = append(ids, device.ID)
	}
	want := []string{"a", "x", "y"}
	if len(ids) != len(want) || ids[0] != "a" || ids[1] != "x" || ids[2] != "y" {
		t.Fatalf("kept %v; want %v", ids, want)
	}
}

func TestDropSharedHelperDevicesKeepsATerminalEnabledRoute(t *testing.T) {
	ids := func(devices []TerminalDevice) string {
		out := ""
		for _, device := range devices {
			out += device.ID + ","
		}
		return out
	}
	// The agent has terminal access off: the usable local-helper device is the route.
	got := dropSharedHelperDevices([]TerminalDevice{
		{ID: "agent", Kind: "agent", Digest: "d", HelperInstanceID: "i"},
		{ID: "desk", Kind: agent.DeviceKindLocalHelper, Digest: "d", HelperInstanceID: "i", TerminalEnabled: true},
	}, nil)
	if ids(got) != "desk," {
		t.Fatalf("kept %s; want only the terminal-enabled desk", ids(got))
	}
}

func TestDropSharedHelperDevicesKeepsBothWhenNeitherCanServe(t *testing.T) {
	got := dropSharedHelperDevices([]TerminalDevice{
		{ID: "a", Kind: "agent", Digest: "d", HelperInstanceID: "i"}, {ID: "l", Kind: agent.DeviceKindLocalHelper, Digest: "d", HelperInstanceID: "i"},
	}, nil)
	if len(got) != 2 {
		t.Fatalf("kept %+v; a device is dropped only for an enabled one", got)
	}
}

func TestTerminalDevicesKeepsMachinesThatShareUIDAndPath(t *testing.T) {
	f := newServiceFixture(t)
	alice := f.createUser(t, "twin-alice")
	one := f.enrollAgent(t, alice, "one")
	two := f.enrollAgent(t, alice, "two")
	f.reportHelper(t, one.DeviceID, "same", "instance-one")
	f.reportHelper(t, two.DeviceID, "same", "instance-two")
	if ids := terminalDeviceIDs(t, f, alice.ID); len(ids) != 2 {
		t.Fatalf("devices = %v; want both machines listed", ids)
	}
}

func TestDropSharedHelperDevicesNeedsBothDigestAndInstanceID(t *testing.T) {
	agentDev := func(id, instance string) TerminalDevice {
		return TerminalDevice{ID: id, Kind: "agent", Digest: "d", HelperInstanceID: instance, TerminalEnabled: true}
	}
	local := func(id, instance string) TerminalDevice {
		return TerminalDevice{ID: id, Kind: agent.DeviceKindLocalHelper, Digest: "d", HelperInstanceID: instance, TerminalEnabled: true}
	}
	count := func(devices ...TerminalDevice) int { return len(dropSharedHelperDevices(devices, nil)) }
	if n := count(agentDev("a", "i1"), agentDev("b", "i2")); n != 2 {
		t.Errorf("different instance ids kept %d; want 2", n)
	}
	if n := count(agentDev("a", "i1"), local("l", "i1")); n != 1 {
		t.Errorf("same helper kept %d; want 1", n)
	}
	if n := count(agentDev("a", "i1"), local("l", "")); n != 2 {
		t.Errorf("one side without an id kept %d; want 2", n)
	}
	if n := count(agentDev("a", ""), local("l", "")); n != 2 {
		t.Errorf("no ids kept %d; want 2", n)
	}
}
