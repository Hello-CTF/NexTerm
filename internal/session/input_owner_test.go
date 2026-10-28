package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/hub"
)

// ownerStream is what one subscriber received, in order: each input-owner
// announcement is recorded as "active" or "passive", output bytes as text.
type ownerStream struct {
	receiver *hub.Receiver
	events   []string
	output   []byte
}

// readUntil reads frames until the output contains want.
func (s *ownerStream) readUntil(t *testing.T, want []byte) {
	t.Helper()
	for !bytes.Contains(s.output, want) {
		frame := receiveTestFrame(t, s.receiver)
		if frame.Kind != hub.FrameJSON {
			s.output = append(s.output, frame.Data...)
			s.events = append(s.events, "output:"+string(frame.Data))
			continue
		}
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(frame.Data, &header); err != nil {
			t.Fatalf("decode frame %s: %v", frame.Data, err)
		}
		if header.Type != inputOwnerType {
			continue
		}
		var owner inputOwnerFrame
		if err := json.Unmarshal(frame.Data, &owner); err != nil {
			t.Fatal(err)
		}
		if owner.Active {
			s.events = append(s.events, "active")
		} else {
			s.events = append(s.events, "passive")
		}
	}
}

// announcements returns the input-owner states received so far and clears
// the record.
func (s *ownerStream) announcements() []string {
	var out []string
	for _, event := range s.events {
		if event == "active" || event == "passive" {
			out = append(out, event)
		}
	}
	s.events = nil
	return out
}

// announcedBefore reports whether state was announced before output
// containing marker arrived.
func (s *ownerStream) announcedBefore(state, marker string) bool {
	seen := false
	for _, event := range s.events {
		if event == state {
			seen = true
		}
		if bytes.Contains([]byte(event), []byte(marker)) {
			return seen
		}
	}
	return false
}

func openOwnerTestTab(t *testing.T) (*Manager, *Tab, *fakeChannel) {
	t.Helper()
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: terminalFactory{}, OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "input-owner", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "cmd-channel")
	return manager, tab, connector.transport(0).channel(0)
}

// readAnnouncement reads frames until an input-owner announcement arrives.
func (s *ownerStream) readAnnouncement(t *testing.T) {
	t.Helper()
	for {
		for _, event := range s.events {
			if event == "active" || event == "passive" {
				return
			}
		}
		frame := receiveTestFrame(t, s.receiver)
		if frame.Kind != hub.FrameJSON {
			s.output = append(s.output, frame.Data...)
			s.events = append(s.events, "output:"+string(frame.Data))
			continue
		}
		var owner inputOwnerFrame
		if json.Unmarshal(frame.Data, &owner) == nil && owner.Type == inputOwnerType {
			if owner.Active {
				s.events = append(s.events, "active")
			} else {
				s.events = append(s.events, "passive")
			}
		}
	}
}

func writeAs(t *testing.T, manager *Manager, tab *Tab, client, data string) {
	t.Helper()
	if err := manager.Write(context.Background(), tab.ID, client, []byte(data)); err != nil {
		t.Fatal(err)
	}
}

func assertAnnouncements(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s announcements = %v, want %v", what, got, want)
	}
}

// Two devices view one tab. After B types, a live OSC 52 is announced as
// passive to A ahead of the bytes and B is never told to stop; when A types
// the roles swap, and when the input owner leaves the other view is active
// again.
func TestLiveClipboardFollowsTheLastInputDevice(t *testing.T) {
	manager, tab, channel := openOwnerTestTab(t)
	ctx := context.Background()
	a := &ownerStream{receiver: bindTestReceiver(t, manager, "cmd-channel")}
	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-channel", ReplayBytes: -1}); err != nil {
		t.Fatal(err)
	}
	b := &ownerStream{receiver: bindTestReceiver(t, manager, "b-channel")}

	writeAs(t, manager, tab, "client-b", "copy\r")
	clipB := "\x1b]52;c;Yg==\x07from-b"
	if err := channel.emit([]byte(clipB)); err != nil {
		t.Fatal(err)
	}
	a.readUntil(t, []byte("from-b"))
	b.readUntil(t, []byte("from-b"))
	if !a.announcedBefore("passive", "from-b") {
		t.Fatalf("A stream = %q, want passive before the clipboard write", a.events)
	}
	assertAnnouncements(t, "A after B typed", a.announcements(), "passive")
	assertAnnouncements(t, "B after B typed", b.announcements())

	// B typing again changes nothing.
	writeAs(t, manager, tab, "client-b", "x")
	writeAs(t, manager, tab, "client-a", "copy\r")
	if err := channel.emit([]byte("\x1b]52;c;YQ==\x07from-a")); err != nil {
		t.Fatal(err)
	}
	a.readUntil(t, []byte("from-a"))
	b.readUntil(t, []byte("from-a"))
	if !b.announcedBefore("passive", "from-a") || !a.announcedBefore("active", "from-a") {
		t.Fatalf("streams A = %q, B = %q", a.events, b.events)
	}
	assertAnnouncements(t, "A after A typed", a.announcements(), "active")
	assertAnnouncements(t, "B after A typed", b.announcements(), "passive")

	// The input owner leaves: B is the only device and writes again.
	if err := manager.DetachChannel("cmd-channel"); err != nil {
		t.Fatal(err)
	}
	b.readAnnouncement(t)
	assertAnnouncements(t, "B after A left", b.announcements(), "active")
}

// A single device never receives an announcement, however often it types.
func TestSingleDeviceGetsNoInputOwnerFrames(t *testing.T) {
	manager, tab, channel := openOwnerTestTab(t)
	a := &ownerStream{receiver: bindTestReceiver(t, manager, "cmd-channel")}
	writeAs(t, manager, tab, "client-a", "copy\r")
	writeAs(t, manager, tab, "client-a", "again\r")
	if err := channel.emit([]byte("\x1b]52;c;YQ==\x07done")); err != nil {
		t.Fatal(err)
	}
	a.readUntil(t, []byte("done"))
	assertAnnouncements(t, "single device", a.announcements())
}

// A device attaching after another one typed starts passive: its anchor
// says so, and no separate announcement follows.
func TestLateAttachStartsPassive(t *testing.T) {
	manager, tab, channel := openOwnerTestTab(t)
	ctx := context.Background()
	a := &ownerStream{receiver: bindTestReceiver(t, manager, "cmd-channel")}
	writeAs(t, manager, tab, "client-a", "copy\r")
	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-late", ReplayBytes: -1}); err != nil {
		t.Fatal(err)
	}
	b := &ownerStream{receiver: bindTestReceiver(t, manager, "b-late")}
	if anchor := receiveTestAnchor(t, b.receiver); !anchor.InputPassive {
		t.Fatalf("late device anchor = %+v, want inputPassive", anchor)
	}
	if err := channel.emit([]byte("\x1b]52;c;YQ==\x07late")); err != nil {
		t.Fatal(err)
	}
	b.readUntil(t, []byte("late"))
	a.readUntil(t, []byte("late"))
	assertAnnouncements(t, "late device", b.announcements())
	assertAnnouncements(t, "typing device", a.announcements())
}

// A recovery anchor carries the passive state, so the view never writes
// OSC 52 from the bytes it redelivers, and no announcement follows them.
func TestRecoveryAnchorCarriesPassiveState(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: terminalFactory{}, OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "input-owner-recover", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.CreateTab(ctx, CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range []string{"client-a", "client-b"} {
		if _, err := manager.AttachTab(ctx, info.ID, AttachOptions{ClientID: client, ChannelID: client + "-channel"}); err != nil {
			t.Fatal(err)
		}
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := &ownerStream{receiver: bindTestReceiver(t, manager, "client-a-channel")}
	writeAs(t, manager, tab, "client-b", "copy\r")
	tab.mu.Lock()
	generation := tab.generation
	tab.mu.Unlock()
	feed := func(data string) {
		t.Helper()
		if err := manager.feed(ctx, tab, generation, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	redelivered := "\x1b]52;c;YQ==\x07recover"
	feed("before-" + redelivered)
	a.readUntil(t, []byte("recover"))
	assertAnnouncements(t, "A before recovery", a.announcements(), "passive")

	tab.mu.Lock()
	subscriber := tab.subscribers["client-a-channel"]
	tab.mu.Unlock()
	if _, _, err := manager.RecoverChannelFor(ctx, tab.ID, "client-a-channel", "client-a", subscriber.attachmentId, subscriber.flushedOffset-int64(len(redelivered))); err != nil {
		t.Fatal(err)
	}
	anchor := receiveTestAnchor(t, a.receiver)
	if anchor.Epoch != "recovery-"+tab.ID || !anchor.InputPassive {
		t.Fatalf("anchor = %+v, want a passive recovery anchor", anchor)
	}
	assertFrameData(t, a.receiver, []byte(redelivered))
	if err := manager.ResumeChannelFor(ctx, tab.ID, "client-a-channel", "client-a", subscriber.attachmentId, tabEpisodeGen(tab, "client-a-channel")); err != nil {
		t.Fatal(err)
	}
	a.events, a.output = nil, nil
	feed("after-recover")
	a.readUntil(t, []byte("after-recover"))
	assertAnnouncements(t, "A after recovery", a.announcements())
}

func tabEpisodeGen(tab *Tab, channelID string) uint64 {
	tab.mu.Lock()
	defer tab.mu.Unlock()
	return tab.subscribers[channelID].episodeGen
}

// Input refused for a read-only spectator does not move the input owner.
func TestRejectedInputDoesNotMoveOwner(t *testing.T) {
	manager, tab, _ := openOwnerTestTab(t)
	ctx := context.Background()
	if _, err := manager.AttachSpectator(ctx, tab.ID, SpectateOptions{ClientID: "viewer", ChannelID: "viewer-reject"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Write(ctx, tab.ID, "viewer", []byte("x")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("write as spectator = %v", err)
	}
	tab.mu.Lock()
	owner := tab.lastInputClient
	tab.mu.Unlock()
	if owner != "" {
		t.Fatalf("last input client = %q after a rejected write", owner)
	}
}

// A write that fails before reaching the shell (cancelled, or the session
// is disconnected) does not move the input owner: the other device is not
// told to stop writing its clipboard.
func TestFailedWriteDoesNotMoveOwner(t *testing.T) {
	manager, tab, channel := openOwnerTestTab(t)
	a := &ownerStream{receiver: bindTestReceiver(t, manager, "cmd-channel")}
	writeAs(t, manager, tab, "client-a", "copy\r")
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-failed", ReplayBytes: -1}); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for range 20 {
		if err := manager.Write(cancelled, tab.ID, "client-b", []byte("x")); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled write = %v", err)
		}
	}
	session := tab.session
	session.mu.Lock()
	status := session.status
	session.status = StatusDisconnected
	session.mu.Unlock()
	err := manager.Write(context.Background(), tab.ID, "client-b", []byte("x"))
	session.mu.Lock()
	session.status = status
	session.mu.Unlock()
	if !errors.Is(err, ErrDisconnected) {
		t.Fatalf("disconnected write = %v", err)
	}
	if written := channel.written(); bytes.Contains(written, []byte("x")) {
		t.Fatalf("shell input = %q, want no failed write", written)
	}
	if err := channel.emit([]byte("\x1b]52;c;YQ==\x07marker")); err != nil {
		t.Fatal(err)
	}
	a.readUntil(t, []byte("marker"))
	assertAnnouncements(t, "device A", a.announcements())
}
