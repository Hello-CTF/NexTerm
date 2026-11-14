package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	recStarted = "started"
	recScreen  = "screen"
	recOutput  = "output"
	recResize  = "resize"
	recEnded   = "ended"
)

// recordingEvent is one sink call with the owner its context carried.
type recordingEvent struct {
	kind      string
	owner     string
	info      TranscriptInfo
	sessionID string
	tabID     string
	cols      uint32
	rows      uint32
	data      []byte
}

// transcriptRecorder is a TranscriptSink that logs every call in order.
type transcriptRecorder struct {
	mu     sync.Mutex
	events []recordingEvent
}

func (r *transcriptRecorder) add(ctx context.Context, event recordingEvent) {
	event.owner = ipc.OwnerID(ctx)
	event.data = append([]byte(nil), event.data...)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *transcriptRecorder) RecordingStarted(ctx context.Context, info TranscriptInfo, cols, rows uint32, screen []byte) {
	r.add(ctx, recordingEvent{kind: recStarted, info: info, sessionID: info.SessionID, tabID: info.TabID, cols: cols, rows: rows, data: screen})
}

func (r *transcriptRecorder) RecordingScreen(ctx context.Context, sessionID, tabID string, cols, rows uint32, screen []byte) {
	r.add(ctx, recordingEvent{kind: recScreen, sessionID: sessionID, tabID: tabID, cols: cols, rows: rows, data: screen})
}

func (r *transcriptRecorder) RecordingOutput(ctx context.Context, sessionID, tabID string, data []byte) {
	r.add(ctx, recordingEvent{kind: recOutput, sessionID: sessionID, tabID: tabID, data: data})
}

func (r *transcriptRecorder) RecordingResize(ctx context.Context, sessionID, tabID string, cols, rows uint32) {
	r.add(ctx, recordingEvent{kind: recResize, sessionID: sessionID, tabID: tabID, cols: cols, rows: rows})
}

func (r *transcriptRecorder) RecordingEnded(ctx context.Context, sessionID, tabID string) {
	r.add(ctx, recordingEvent{kind: recEnded, sessionID: sessionID, tabID: tabID})
}

func (r *transcriptRecorder) snapshot() []recordingEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordingEvent(nil), r.events...)
}

// of returns the events of kind, for every tab when tabID is empty.
func (r *transcriptRecorder) of(kind, tabID string) []recordingEvent {
	var matched []recordingEvent
	for _, event := range r.snapshot() {
		if event.kind == kind && (tabID == "" || event.tabID == tabID) {
			matched = append(matched, event)
		}
	}
	return matched
}

// outputBytes is every recorded output, of every tab, in order.
func (r *transcriptRecorder) outputBytes() []byte {
	return r.tabOutput("")
}

func (r *transcriptRecorder) tabOutput(tabID string) []byte {
	var combined []byte
	for _, event := range r.of(recOutput, tabID) {
		combined = append(combined, event.data...)
	}
	return combined
}

func (r *transcriptRecorder) assertEveryCallCarries(t *testing.T, kind, want string) {
	t.Helper()
	events := r.of(kind, "")
	if len(events) == 0 {
		t.Fatalf("sink recorded no %s calls", kind)
	}
	for index, event := range events {
		if event.owner != want {
			t.Fatalf("%s call %d owner = %q, want %q", kind, index, event.owner, want)
		}
	}
}

func newTranscriptTestManager(connector Connector, sink TranscriptSink) *Manager {
	return NewManager(Config{
		Connector:   connector,
		Terminals:   newFakeTerminalFactory(),
		Transcripts: sink,
	})
}

// emitShown emits output on channel and waits until the tab's terminal shows
// it, which is after the feed decided whether to record it.
func emitShown(t *testing.T, tab *Tab, channel *fakeChannel, data string) {
	t.Helper()
	if err := channel.emit([]byte(data)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(tab.terminal.Dump(1<<20), []byte(data)) })
}

func assertRecordingScreen(t *testing.T, event recordingEvent, contains string) {
	t.Helper()
	if !bytes.HasPrefix(event.data, replayClear) || !bytes.Contains(event.data, []byte(contains)) {
		t.Fatalf("%s screen = %q, want a clear followed by the screen holding %q", event.kind, event.data, contains)
	}
}

func TestTranscriptRecordsOnlyOutputAtSingleFeedPath(t *testing.T) {
	connector := newFakeConnector()
	recorder := &transcriptRecorder{}
	manager := newTranscriptTestManager(connector, recorder)
	t.Cleanup(func() { _ = manager.Close() })

	connected, err := manager.Connect(context.Background(), Asset{ID: "asset-ssh", Name: "web-01", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if started := recorder.of(recStarted, ""); len(started) != 0 {
		t.Fatalf("a session without a watched tab started recording: %+v", started)
	}

	tab := openTestTab(t, manager, connected, "client-a", "channel-a")
	started := recorder.of(recStarted, "")
	if len(started) != 1 {
		t.Fatalf("expected one recording start, got %+v", started)
	}
	want := TranscriptInfo{
		SessionID: connected.ID, TabID: tab.ID, TabTitle: tab.Info().Title,
		AssetID: "asset-ssh", AssetName: "web-01", AssetKind: KindSSH,
	}
	if started[0].info != want || started[0].cols != 80 || started[0].rows != 24 {
		t.Fatalf("recording start = %+v %dx%d, want %+v 80x24", started[0].info, started[0].cols, started[0].rows, want)
	}

	channel := connector.transport(0).channel(0)
	if err := channel.emit([]byte("output-one\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(recorder.outputBytes(), []byte("output-one")) })

	if err := manager.Write(context.Background(), tab.ID, "client-a", []byte("secret-input\r")); err != nil {
		t.Fatal(err)
	}
	if err := manager.WriteInternal(context.Background(), tab.ID, []byte("internal-input")); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("output-two\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(recorder.outputBytes(), []byte("output-two")) })

	if ended := recorder.of(recEnded, ""); len(ended) != 0 {
		t.Fatalf("tab still watched but recording ended: %+v", ended)
	}
	combined := recorder.outputBytes()
	if bytes.Contains(combined, []byte("secret-input")) || bytes.Contains(combined, []byte("internal-input")) {
		t.Fatal("input must not be recorded as output")
	}
	for _, output := range recorder.of(recOutput, "") {
		if output.sessionID != connected.ID || output.tabID != tab.ID {
			t.Fatalf("output misattributed: %+v", output)
		}
	}
	if !bytes.Contains(combined, []byte("output-one")) || !bytes.Contains(combined, []byte("output-two")) {
		t.Fatalf("missing recorded output: %q", combined)
	}
}

// A quick detach and re-attach without output in between keeps the recording
// as it was: no redraw, no second start and no duplicated replay.
func TestTranscriptReplayAfterReattachNotDuplicated(t *testing.T) {
	connector := newFakeConnector()
	recorder := &transcriptRecorder{}
	manager := newTranscriptTestManager(connector, recorder)
	t.Cleanup(func() { _ = manager.Close() })

	connected, err := manager.Connect(context.Background(), Asset{ID: "asset-ssh", Name: "web-01", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "channel-a")
	channel := connector.transport(0).channel(0)
	if err := channel.emit([]byte("recorded-once\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(recorder.outputBytes(), []byte("recorded-once")) })

	if err := manager.DetachChannel("channel-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-a", ChannelID: "channel-a", ReplayBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	emitShown(t, tab, channel, "after-reattach\r\n")
	waitFor(t, func() bool { return bytes.Contains(recorder.outputBytes(), []byte("after-reattach")) })

	if got := bytes.Count(recorder.outputBytes(), []byte("recorded-once")); got != 1 {
		t.Fatalf("replay after reattach recorded %d copies, want exactly 1", got)
	}
	if started, screens, ended := recorder.of(recStarted, ""), recorder.of(recScreen, ""), recorder.of(recEnded, ""); len(started) != 1 || len(screens) != 0 || len(ended) != 0 {
		t.Fatalf("reattach changed the recording: started %d, screens %d, ended %d", len(started), len(screens), len(ended))
	}
}

// Output produced before any client watches the tab is not recorded: the
// recording starts on attach with the tab size and the screen it shows.
func TestRecordingStartsOnAttachWithSizeAndScreen(t *testing.T) {
	connector := newFakeConnector()
	recorder := &transcriptRecorder{}
	manager := newTranscriptTestManager(connector, recorder)
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "asset-ssh", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{SessionID: connected.ID, Cols: 100, Rows: 30, Detached: true})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	emitShown(t, tab, channel, "before-attach\r\n")
	if events := recorder.snapshot(); len(events) != 0 {
		t.Fatalf("an unwatched tab was recorded: %+v", events)
	}

	if _, err := manager.AttachTab(ctx, info.ID, AttachOptions{ClientID: "client-a", ChannelID: "channel-a"}); err != nil {
		t.Fatal(err)
	}
	started := recorder.of(recStarted, info.ID)
	if len(started) != 1 || started[0].cols != 100 || started[0].rows != 30 || started[0].info.TabID != info.ID {
		t.Fatalf("recording start = %+v, want one start at 100x30", started)
	}
	assertRecordingScreen(t, started[0], "before-attach")

	emitShown(t, tab, channel, "after-attach\r\n")
	waitFor(t, func() bool { return bytes.Contains(recorder.outputBytes(), []byte("after-attach")) })
	if got := recorder.outputBytes(); bytes.Contains(got, []byte("before-attach")) {
		t.Fatalf("output from before the attach was recorded as output: %q", got)
	}
}

// When the last client leaves, output is no longer recorded; a client that
// returns within the grace period keeps the recording, which redraws the
// screen the unrecorded output left.
func TestRecordingRedrawsScreenWhenAClientReturnsWithinGrace(t *testing.T) {
	connector := newFakeConnector()
	recorder := &transcriptRecorder{}
	manager := newTranscriptTestManager(connector, recorder)
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "asset-ssh", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "channel-a")
	channel := connector.transport(0).channel(0)
	emitShown(t, tab, channel, "watched-1\r\n")
	waitFor(t, func() bool { return bytes.Contains(recorder.outputBytes(), []byte("watched-1")) })

	if err := manager.DetachChannel("channel-a"); err != nil {
		t.Fatal(err)
	}
	tab.mu.Lock()
	open, armed := tab.recording.open, tab.recording.timer != nil
	tab.mu.Unlock()
	if !open || !armed {
		t.Fatalf("after the last client left: open=%v grace armed=%v, want an open recording with the grace armed", open, armed)
	}
	emitShown(t, tab, channel, "unwatched\r\n")

	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "client-a", ChannelID: "channel-b"}); err != nil {
		t.Fatal(err)
	}
	screens := recorder.of(recScreen, tab.ID)
	if len(screens) != 1 || screens[0].sessionID != connected.ID || screens[0].cols != 80 || screens[0].rows != 24 {
		t.Fatalf("screens = %+v, want one redraw at 80x24", screens)
	}
	assertRecordingScreen(t, screens[0], "unwatched")
	tab.mu.Lock()
	armed = tab.recording.timer != nil
	tab.mu.Unlock()
	if armed {
		t.Fatal("the grace timer survived the returning client")
	}

	emitShown(t, tab, channel, "watched-2\r\n")
	waitFor(t, func() bool { return bytes.Contains(recorder.outputBytes(), []byte("watched-2")) })
	if got := recorder.outputBytes(); !bytes.Equal(got, []byte("watched-1\r\nwatched-2\r\n")) {
		t.Fatalf("recorded output %q, want only the watched output", got)
	}
	if started, ended := recorder.of(recStarted, ""), recorder.of(recEnded, ""); len(started) != 1 || len(ended) != 0 {
		t.Fatalf("a return within the grace started %d and ended %d recordings, want one open recording", len(started), len(ended))
	}
}

// The grace ending with nobody watching ends the recording; a grace timer
// that fires after a client returned is stale and leaves it open.
func TestRecordingEndsWhenTheGraceRunsOut(t *testing.T) {
	connector := newFakeConnector()
	recorder := &transcriptRecorder{}
	manager := newTranscriptTestManager(connector, recorder)
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "asset-ssh", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "channel-a")
	channel := connector.transport(0).channel(0)

	// graceEpoch stops the armed timer and returns the epoch it would fire
	// with, so the test fires it without waiting for recordingGrace.
	graceEpoch := func() uint64 {
		t.Helper()
		tab.mu.Lock()
		defer tab.mu.Unlock()
		if tab.recording.timer == nil {
			t.Fatal("no grace timer armed")
		}
		tab.recording.timer.Stop()
		return tab.recording.epoch
	}

	if err := manager.DetachChannel("channel-a"); err != nil {
		t.Fatal(err)
	}
	stale := graceEpoch()
	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "client-a", ChannelID: "channel-b"}); err != nil {
		t.Fatal(err)
	}
	manager.recordingGraceEnded(tab, stale)
	if ended := recorder.of(recEnded, ""); len(ended) != 0 {
		t.Fatalf("a stale grace timer ended the recording: %+v", ended)
	}

	if err := manager.DetachChannel("channel-b"); err != nil {
		t.Fatal(err)
	}
	manager.recordingGraceEnded(tab, graceEpoch())
	if ended := recorder.of(recEnded, tab.ID); len(ended) != 1 || ended[0].sessionID != connected.ID {
		t.Fatalf("ended = %+v, want the recording ended once the grace ran out", ended)
	}
	emitShown(t, tab, channel, "after-grace\r\n")

	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "client-a", ChannelID: "channel-c"}); err != nil {
		t.Fatal(err)
	}
	started := recorder.of(recStarted, tab.ID)
	if len(started) != 2 || len(recorder.of(recScreen, "")) != 0 {
		t.Fatalf("starts = %d screens = %d, want a new recording after the grace", len(started), len(recorder.of(recScreen, "")))
	}
	assertRecordingScreen(t, started[1], "after-grace")
	if got := recorder.outputBytes(); bytes.Contains(got, []byte("after-grace")) {
		t.Fatalf("output between recordings was recorded: %q", got)
	}
}

// A lost connection ends every tab recording at once; the reconnect starts a
// new recording for the tab still watched, with its current size.
func TestRecordingDisconnectEndsAndReconnectStartsNew(t *testing.T) {
	connector := newFakeConnector()
	recorder := &transcriptRecorder{}
	manager := newTranscriptTestManager(connector, recorder)
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "asset-ssh", Name: "web-01", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	first := openTestTab(t, manager, connected, "client-a", "channel-a")
	second := openTestTab(t, manager, connected, "client-a", "channel-b")
	if err := manager.Resize(ctx, first.ID, "client-a", 120, 40); err != nil {
		t.Fatal(err)
	}
	emitShown(t, first, connector.transport(0).channel(0), "before-drop\r\n")
	waitFor(t, func() bool { return bytes.Contains(recorder.tabOutput(first.ID), []byte("before-drop")) })

	if err := manager.Disconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
	for _, tab := range []*Tab{first, second} {
		if ended := recorder.of(recEnded, tab.ID); len(ended) != 1 || ended[0].sessionID != connected.ID {
			t.Fatalf("disconnect must end the recording of tab %s: %+v", tab.ID, ended)
		}
	}

	if err := manager.Reconnect(ctx, connected.ID); err != nil {
		t.Fatal(err)
	}
	started := recorder.of(recStarted, first.ID)
	if len(started) != 2 || started[1].cols != 120 || started[1].rows != 40 || started[1].info.AssetName != "web-01" {
		t.Fatalf("reconnect must start a new recording at the tab's current size: %+v", started)
	}
	assertRecordingScreen(t, started[1], "before-drop")
	if got := len(recorder.of(recStarted, second.ID)); got != 2 {
		t.Fatalf("second tab recordings started = %d, want 2", got)
	}

	if err := connector.transport(1).channel(0).emit([]byte("after-reconnect\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(recorder.outputBytes(), []byte("after-reconnect")) })
	if combined := recorder.outputBytes(); bytes.Contains(combined, []byte("[NexTerm] reconnected")) {
		t.Fatalf("reconnect notice must stay out of the recorded output: %q", combined)
	}
}

// Each tab of a session is its own recording; closing a tab ends only its own.
func TestRecordingPerTabAndClosedTabEnds(t *testing.T) {
	connector := newFakeConnector()
	recorder := &transcriptRecorder{}
	manager := newTranscriptTestManager(connector, recorder)
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "asset-ssh", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	first := openTestTab(t, manager, connected, "client-a", "channel-a")
	second := openTestTab(t, manager, connected, "client-a", "channel-b")
	firstChannel, secondChannel := connector.transport(0).channel(0), connector.transport(0).channel(1)
	started := recorder.of(recStarted, "")
	if len(started) != 2 || started[0].info.TabID == started[1].info.TabID {
		t.Fatalf("two watched tabs must start two recordings: %+v", started)
	}
	emitShown(t, first, firstChannel, "first-output\r\n")
	emitShown(t, second, secondChannel, "second-output\r\n")
	waitFor(t, func() bool {
		return bytes.Contains(recorder.tabOutput(first.ID), []byte("first-output")) &&
			bytes.Contains(recorder.tabOutput(second.ID), []byte("second-output"))
	})
	if bytes.Contains(recorder.tabOutput(first.ID), []byte("second-output")) || bytes.Contains(recorder.tabOutput(second.ID), []byte("first-output")) {
		t.Fatal("output recorded under the wrong tab")
	}

	if err := manager.CloseTab(first.ID); err != nil {
		t.Fatal(err)
	}
	if ended := recorder.of(recEnded, ""); len(ended) != 1 || ended[0].tabID != first.ID {
		t.Fatalf("closing a tab must end only its recording: %+v", ended)
	}
	emitShown(t, second, secondChannel, "second-after-close\r\n")
	waitFor(t, func() bool { return bytes.Contains(recorder.tabOutput(second.ID), []byte("second-after-close")) })
}

// A read-only spectator is not a client watching the tab: it neither starts
// a recording nor keeps one going.
func TestRecordingSpectatorDoesNotRecord(t *testing.T) {
	connector := newFakeConnector()
	recorder := &transcriptRecorder{}
	manager := newTranscriptTestManager(connector, recorder)
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "asset-ssh", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{SessionID: connected.ID, Cols: 80, Rows: 24, Detached: true})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachSpectator(ctx, info.ID, SpectateOptions{ClientID: "viewer", ChannelID: "spectate-1"}); err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	emitShown(t, tab, channel, "spectated\r\n")
	if events := recorder.snapshot(); len(events) != 0 {
		t.Fatalf("a spectator started a recording: %+v", events)
	}

	if _, err := manager.AttachTab(ctx, info.ID, AttachOptions{ClientID: "client-a", ChannelID: "channel-a"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.DetachChannel("channel-a"); err != nil {
		t.Fatal(err)
	}
	emitShown(t, tab, channel, "only-spectated\r\n")
	if got := recorder.outputBytes(); bytes.Contains(got, []byte("only-spectated")) {
		t.Fatalf("output only a spectator watched was recorded: %q", got)
	}
	tab.mu.Lock()
	armed := tab.recording.timer != nil
	tab.mu.Unlock()
	if !armed {
		t.Fatal("a remaining spectator kept the recording from winding down")
	}
}

func TestTranscriptWinRMExecAndInternalInjectRecorded(t *testing.T) {
	connector := newFakeConnector()
	recorder := &transcriptRecorder{}
	manager := newTranscriptTestManager(connector, recorder)
	t.Cleanup(func() { _ = manager.Close() })

	connected, err := manager.Connect(context.Background(), Asset{ID: "asset-winrm", Name: "win-01", Kind: KindWinRM})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "channel-a")
	if _, err := manager.ExecLine(context.Background(), tab.ID, "client-a", "whoami"); err != nil {
		t.Fatal(err)
	}
	if err := manager.InjectInternal(context.Background(), tab.ID, []byte("internal-notice\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		combined := recorder.outputBytes()
		return bytes.Contains(combined, []byte("ran:whoami")) && bytes.Contains(combined, []byte("internal-notice"))
	})
}

func TestTranscriptNilSinkIsNoop(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory()})
	t.Cleanup(func() { _ = manager.Close() })

	connected, err := manager.Connect(context.Background(), Asset{ID: "asset-ssh", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "channel-a")
	emitShown(t, tab, connector.transport(0).channel(0), "output\r\n")
	if err := manager.DetachChannel("channel-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-a", ChannelID: "channel-b"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Disconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptRecordsResize(t *testing.T) {
	connector := newFakeConnector()
	recorder := &transcriptRecorder{}
	manager := newTranscriptTestManager(connector, recorder)
	t.Cleanup(func() { _ = manager.Close() })

	connected, err := manager.Connect(context.Background(), Asset{ID: "asset-ssh", Name: "web-01", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "channel-a")

	if err := manager.Resize(context.Background(), tab.ID, "client-a", 120, 40); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		resizes := recorder.of(recResize, tab.ID)
		return len(resizes) > 0 && resizes[len(resizes)-1].cols == 120 && resizes[len(resizes)-1].rows == 40
	})
	if err := waitForChannelResize(t, connector.transport(0).channel(0), 120, 40); err != nil {
		t.Fatal(err)
	}
	for _, resize := range recorder.of(recResize, "") {
		if resize.sessionID != connected.ID || resize.tabID != tab.ID {
			t.Fatalf("resize misattributed: %+v", resize)
		}
	}

	if err := manager.Resize(context.Background(), tab.ID, "client-a", 80, 24); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		resizes := recorder.of(recResize, tab.ID)
		return resizes[len(resizes)-1].cols == 80 && resizes[len(resizes)-1].rows == 24
	})
}

type transcriptDurableRecord struct {
	reads   [][]byte
	channel *fakeChannel
}

type transcriptDurableProvider struct {
	mu      sync.Mutex
	records map[string]*transcriptDurableRecord
}

func newTranscriptDurableProvider() *transcriptDurableProvider {
	return &transcriptDurableProvider{records: make(map[string]*transcriptDurableRecord)}
}

func (p *transcriptDurableProvider) Create(_ context.Context, options base.DurableCreateOptions) (base.DurableAttachment, error) {
	channel := newFakeChannel(1)
	p.mu.Lock()
	p.records[options.ID] = &transcriptDurableRecord{channel: channel}
	p.mu.Unlock()
	return &transcriptDurableAttachment{fakeChannel: channel}, nil
}

// transcriptDurableIdentity is the single incarnation this fake reports; the
// tests here exercise transcript catch-up, not identity checks.
var transcriptDurableIdentity = base.DurableIdentity{Incarnation: "transcript-incarnation"}

func (p *transcriptDurableProvider) AttachWithIdentity(ctx context.Context, id string, identity base.DurableIdentity) (base.DurableAttachment, error) {
	return p.AttachFromWithIdentity(ctx, id, identity, 0)
}

// AttachFromWithIdentity replays every queued read from offset zero and
// reports it as the attach's replay, as the supervisor provider does.
func (p *transcriptDurableProvider) AttachFromWithIdentity(_ context.Context, id string, identity base.DurableIdentity, _ uint64) (base.DurableAttachment, error) {
	if identity.Incarnation != transcriptDurableIdentity.Incarnation {
		return nil, durable.ErrIdentity
	}
	p.mu.Lock()
	record := p.records[id]
	if record == nil {
		p.mu.Unlock()
		return nil, errors.New("no such durable session")
	}
	channel := newFakeChannel(1)
	record.channel = channel
	reads := record.reads
	p.mu.Unlock()
	attachment := &transcriptDurableAttachment{fakeChannel: channel}
	for _, read := range reads {
		channel.reads <- append([]byte(nil), read...)
		attachment.replay.HistoryNext += uint64(len(read))
	}
	return attachment, nil
}

func (p *transcriptDurableProvider) AttachTailWithIdentity(ctx context.Context, id string, identity base.DurableIdentity, _ base.DurableTailOptions) (base.DurableAttachment, error) {
	return p.AttachWithIdentity(ctx, id, identity)
}

// The transcript tests never list, rename, kill by identity or page history.
func (p *transcriptDurableProvider) ListDurableDescriptors(context.Context) ([]base.DurableDescriptor, error) {
	return nil, base.ErrUnsupported
}

func (p *transcriptDurableProvider) RenameDurable(context.Context, base.DurableRenameOptions) (base.DurableRenameResult, error) {
	return base.DurableRenameResult{}, base.ErrUnsupported
}

func (p *transcriptDurableProvider) KillWithIdentity(context.Context, string, base.DurableIdentity) error {
	return base.ErrUnsupported
}

func (p *transcriptDurableProvider) ReadDurableHistory(context.Context, string, base.DurableIdentity, base.DurableHistoryRequest) (base.DurableHistoryChunk, error) {
	return base.DurableHistoryChunk{}, base.ErrUnsupported
}

func (p *transcriptDurableProvider) channel(id string) *fakeChannel {
	p.mu.Lock()
	defer p.mu.Unlock()
	if record := p.records[id]; record != nil {
		return record.channel
	}
	return nil
}

type transcriptDurableAttachment struct {
	*fakeChannel
	replay   base.DurableReplay
	consumed atomic.Uint64
}

func (a *transcriptDurableAttachment) DurableReplay() base.DurableReplay { return a.replay }

func (a *transcriptDurableAttachment) DurableOffset() uint64 {
	return a.replay.ReplayFrom + a.consumed.Load()
}

func (a *transcriptDurableAttachment) Read(p []byte) (int, error) {
	count, err := a.fakeChannel.Read(p)
	a.consumed.Add(uint64(count))
	return count, err
}

func (a *transcriptDurableAttachment) Kill(context.Context) error { return nil }

// TestTranscriptDurableRecoveryRecordsOnlyLiveOutput: the replay of a
// recovered durable tab is history and never recorded as output; output after
// the attach is.
func TestTranscriptDurableRecoveryRecordsOnlyLiveOutput(t *testing.T) {
	provider := newTranscriptDurableProvider()
	firstRecorder := &transcriptRecorder{}
	first := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		Durable: provider, Transcripts: firstRecorder,
	})
	connected, err := first.Connect(context.Background(), Asset{ID: "asset-local", Name: "当前设备", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	info, err := first.OpenTab(context.Background(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "channel-a",
		Cols: 80, Rows: 24, Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.channel(info.ID).emit([]byte("durable-replay-marker\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(firstRecorder.outputBytes(), []byte("durable-replay-marker")) })
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if ended := firstRecorder.of(recEnded, info.ID); len(ended) != 1 {
		t.Fatalf("closing the manager must end the tab's recording: %+v", ended)
	}

	provider.mu.Lock()
	provider.records[info.ID].reads = [][]byte{
		[]byte("durable-replay-mar"),
		append([]byte("ker\r\n"), []byte("live-after-boundary\r\n")...),
	}
	provider.mu.Unlock()

	secondRecorder := &transcriptRecorder{}
	second := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		Durable: provider, Transcripts: secondRecorder,
	})
	t.Cleanup(func() { _ = second.Close() })
	recovered, err := second.Connect(context.Background(), Asset{ID: "asset-local", Name: "当前设备", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	recoveredInfo, err := second.OpenTab(context.Background(), OpenTabOptions{
		TabID: info.ID, SessionID: recovered.ID, ClientID: "client-b", ChannelID: "channel-b",
		Cols: 80, Rows: 24, Durable: &DurableTabOptions{Recover: true, Identity: &transcriptDurableIdentity},
	})
	if err != nil {
		t.Fatal(err)
	}
	if started := secondRecorder.of(recStarted, recoveredInfo.ID); len(started) != 1 || started[0].cols != 80 || started[0].rows != 24 {
		t.Fatalf("durable recover must start a recording at the tab size: %+v", started)
	}
	recoveredTab, err := second.Tab(recoveredInfo.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return bytes.Contains(recoveredTab.terminal.Dump(1<<20), []byte("live-after-boundary"))
	})
	if err := provider.channel(info.ID).emit([]byte("live-after-attach\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(secondRecorder.outputBytes(), []byte("live-after-attach")) })
	if recorded := secondRecorder.outputBytes(); !bytes.Equal(recorded, []byte("live-after-attach\r\n")) {
		t.Fatalf("only output after the attach may be recorded: %q", recorded)
	}
}

func TestTranscriptDurableRecoveryRepeatedNoDuplication(t *testing.T) {
	provider := newTranscriptDurableProvider()
	firstRecorder := &transcriptRecorder{}
	first := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		Durable: provider, Transcripts: firstRecorder,
	})
	connected, err := first.Connect(context.Background(), Asset{ID: "asset-local", Name: "当前设备", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	info, err := first.OpenTab(context.Background(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "channel-a",
		Cols: 80, Rows: 24, Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.channel(info.ID).emit([]byte("epoch-00\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(firstRecorder.outputBytes(), []byte("epoch-00")) })
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	accumulated := []byte("epoch-00\r\n")
	for epoch := 1; epoch <= 10; epoch++ {
		marker := []byte(fmt.Sprintf("epoch-%02d\r\n", epoch))
		provider.mu.Lock()
		provider.records[info.ID].reads = [][]byte{append([]byte(nil), accumulated...)}
		provider.mu.Unlock()

		recorder := &transcriptRecorder{}
		manager := NewManager(Config{
			Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
			Durable: provider, Transcripts: recorder,
		})
		recovered, err := manager.Connect(context.Background(), Asset{ID: "asset-local", Name: "当前设备", Kind: KindLocal})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.OpenTab(context.Background(), OpenTabOptions{
			TabID: info.ID, SessionID: recovered.ID, ClientID: "client-b", ChannelID: "channel-b",
			Cols: 80, Rows: 24, Durable: &DurableTabOptions{Recover: true, Identity: &transcriptDurableIdentity},
		}); err != nil {
			t.Fatal(err)
		}
		if err := provider.channel(info.ID).emit(marker); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return bytes.Contains(recorder.outputBytes(), marker) })
		for previous := 0; previous < epoch; previous++ {
			if bytes.Contains(recorder.outputBytes(), []byte(fmt.Sprintf("epoch-%02d", previous))) {
				t.Fatalf("epoch %d: recorder contains replayed epoch-%02d: %q", epoch, previous, recorder.outputBytes())
			}
		}
		if err := manager.Close(); err != nil {
			t.Fatal(err)
		}
		accumulated = append(accumulated, marker...)
	}
}

// A recovered durable tab's recording starts before its history is replayed;
// once the replay is fed, the recording redraws the screen it restored, and
// the replay itself is never recorded as output.
func TestRecordingRedrawsRecoveredDurableScreen(t *testing.T) {
	provider := newTranscriptDurableProvider()
	first := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), Durable: provider})
	connected, err := first.Connect(context.Background(), Asset{ID: "asset-local", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	info, err := first.OpenTab(context.Background(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "channel-a",
		Cols: 80, Rows: 24, Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	provider.records[info.ID].reads = [][]byte{[]byte("restored-"), []byte("screen\r\n")}
	provider.mu.Unlock()

	recorder := &transcriptRecorder{}
	second := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		Durable: provider, Transcripts: recorder,
	})
	t.Cleanup(func() { _ = second.Close() })
	recovered, err := second.Connect(context.Background(), Asset{ID: "asset-local", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.OpenTab(context.Background(), OpenTabOptions{
		TabID: info.ID, SessionID: recovered.ID, ClientID: "client-b", ChannelID: "channel-b",
		Cols: 80, Rows: 24, Durable: &DurableTabOptions{Recover: true, Identity: &transcriptDurableIdentity},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(recorder.of(recScreen, info.ID)) > 0 })
	if err := provider.channel(info.ID).emit([]byte("live-after-recover\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(recorder.outputBytes(), []byte("live-after-recover")) })

	var kinds []string
	for _, event := range recorder.snapshot() {
		if event.kind != recResize {
			kinds = append(kinds, event.kind)
		}
	}
	if len(kinds) < 2 || kinds[0] != recStarted || kinds[1] != recScreen {
		t.Fatalf("recording events = %v, want a start followed by a screen redraw", kinds)
	}
	screens := recorder.of(recScreen, info.ID)
	if len(screens) != 1 || screens[0].cols != 80 || screens[0].rows != 24 {
		t.Fatalf("screens = %+v, want one redraw at 80x24", screens)
	}
	assertRecordingScreen(t, screens[0], "restored-screen")
	if got := recorder.outputBytes(); bytes.Contains(got, []byte("restored")) || !bytes.Equal(got, []byte("live-after-recover\r\n")) {
		t.Fatalf("recorded output %q, want only the live output after the replay", got)
	}
}
