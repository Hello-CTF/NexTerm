package session

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/terminal"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestCoreTerminalRawReplayAndResponseIntegration(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, ReconnectBackoff: []time.Duration{0}})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "core-terminal", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	recordingPath := filepath.Join(t.TempDir(), "recording.log")
	if err := manager.StartRecording(tab.ID, recordingPath); err != nil {
		t.Fatal(err)
	}
	raw := []byte{0xff, 0x00, 'r', 'a', 'w'}
	if err := channel.emit(raw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(tab.terminal.Dump(1024), raw) })
	if dump, err := manager.RawDump(tab.ID, 1024); err != nil || !bytes.Contains(dump, raw) {
		t.Fatalf("shared raw dump = %v, err = %v", dump, err)
	}
	waitFor(t, func() bool {
		current, err := manager.RecordingBytes(tab.ID)
		if err != nil {
			t.Fatal(err)
		}
		return current >= uint64(len(raw))
	})
	recorded, err := manager.StopRecording(tab.ID)
	if err != nil || recorded < uint64(len(raw)) {
		t.Fatalf("stopped recording bytes = %d, err = %v", recorded, err)
	}
	recording, err := os.ReadFile(recordingPath)
	if err != nil || !bytes.Contains(recording, raw) {
		t.Fatalf("recording = %v, err = %v", recording, err)
	}
	exportPath := filepath.Join(t.TempDir(), "export.log")
	if exported, err := manager.ExportLog(tab.ID, exportPath, 1024); err != nil || exported < uint64(len(raw)) {
		t.Fatalf("exported bytes = %d, err = %v", exported, err)
	}
	if snapshot, err := manager.TerminalSnapshot(tab.ID); err != nil || snapshot.Cols != 80 || snapshot.Rows != 24 {
		t.Fatalf("shared snapshot = %+v, err = %v", snapshot, err)
	}
	if _, err := manager.ScreenText(tab.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.TailLines(tab.ID, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.TerminalModeState(tab.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.SwitchEncoding(tab.ID, "utf8"); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetVisible(tab.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetVisible(tab.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1", ReplayBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	receiver := bindTestReceiver(t, manager, "b-1")
	receiveTestAnchor(t, receiver)
	assertFrameData(t, receiver, replayClear)
	frame := receiveTestFrame(t, receiver)
	if !bytes.Contains(frame.Data, raw) {
		t.Fatalf("production terminal altered raw replay: %v", frame.Data)
	}
	// The headless screen answers queries only while no client is attached
	// (an attached client's emulator answers them itself).
	for _, channelID := range []string{"a-1", "b-1"} {
		if err := manager.DetachChannel(channelID); err != nil {
			t.Fatal(err)
		}
	}
	if err := channel.emit([]byte("\x1b[6n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		written := channel.written()
		return bytes.Contains(written, []byte("\x1b[")) && bytes.HasSuffix(written, []byte("R"))
	})
	if err := manager.WriteInternal(context.Background(), tab.ID, []byte("internal")); err != nil {
		t.Fatal(err)
	}
	if got := channel.written(); !bytes.HasSuffix(got, []byte("internal")) {
		t.Fatalf("trusted internal write = %q", got)
	}
}

func TestTabInfoOutputAgeIsListOnly(t *testing.T) {
	var clock atomic.Int64
	clock.Store(1_000_000)
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector,
		Terminals: TerminalFactoryFunc(func(config TerminalConfig) (TerminalState, error) {
			return terminal.NewTab(config.TabID, config.SessionID, int(config.Cols), int(config.Rows), terminal.UTF8, terminal.WithClock(func() time.Time {
				return time.UnixMilli(clock.Load())
			})), nil
		}),
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "list-only-output-age", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "list-only-output-age")

	clock.Add(5000)
	before := tab.Info()
	if before.LastOutputMSAgo != 0 {
		t.Fatalf("general Tab.Info output age = %d, want zero", before.LastOutputMSAgo)
	}
	infos := manager.ListTabs()
	if len(infos) != 1 || infos[0].ID != tab.ID || infos[0].LastOutputMSAgo != 5000 {
		t.Fatalf("ListTabs output age = %+v, want tab %s with age 5000", infos, tab.ID)
	}
	if got := tab.Info().LastOutputMSAgo; got != 0 {
		t.Fatalf("general Tab.Info after ListTabs output age = %d, want zero", got)
	}
	clock.Add(1)
	if after := tab.Info(); after != before {
		t.Fatalf("general Tab.Info changed across clock advance: before=%+v after=%+v", before, after)
	}
}

func TestRecordingLifecycleEmitsControlInvalidations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	connector := newFakeConnector()
	events := make(chan Event, 8)
	manager := NewManager(Config{
		Connector: connector,
		Emitter: EmitterFunc(func(_ context.Context, event Event) error {
			events <- event
			return nil
		}),
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "recording-events", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "a-1")
	_ = waitRecordingEvents(t, events, 1)
	// The tab's attention state is published on its own schedule; only
	// control events are recording invalidations.
	// wait zero only takes events already emitted.
	nextControl := func(wait time.Duration) (Event, bool) {
		deadline := time.After(wait)
		for {
			var event Event
			if wait == 0 {
				select {
				case event = <-events:
				default:
					return Event{}, false
				}
			} else {
				select {
				case event = <-events:
				case <-deadline:
					return Event{}, false
				}
			}
			if event.Topic != TopicTerminalAttention {
				return event, true
			}
		}
	}
	assertInvalidation := func() {
		t.Helper()
		event, ok := nextControl(time.Second)
		if !ok {
			t.Fatal("recording control invalidation not emitted")
		}
		if event.Topic != TopicTerminalControl {
			t.Fatalf("event topic = %q, want %q", event.Topic, TopicTerminalControl)
		}
	}

	if err := manager.StartRecording(tab.ID, path); err != nil {
		t.Fatal(err)
	}
	assertInvalidation()
	status, err := manager.RecordingStatus(tab.ID)
	if err != nil || status.State != terminal.RecordingRunning || status.Generation != 1 || status.Path != path {
		t.Fatalf("running status = %+v, err = %v", status, err)
	}
	if _, err := manager.StopRecording(tab.ID); err != nil {
		t.Fatal(err)
	}
	assertInvalidation()
	status, err = manager.RecordingStatus(tab.ID)
	if err != nil || status.State != terminal.RecordingStopped || status.Generation != 1 {
		t.Fatalf("stopped status = %+v, err = %v", status, err)
	}
	if _, err := manager.StopRecording(tab.ID); err != nil {
		t.Fatal(err)
	}
	if event, ok := nextControl(0); ok {
		t.Fatalf("repeated stop emitted another event: %+v", event)
	}
	if err := manager.StartRecording(tab.ID, path); err != nil {
		t.Fatal(err)
	}
	assertInvalidation()
	status, err = manager.RecordingStatus(tab.ID)
	if err != nil || status.State != terminal.RecordingRunning || status.Generation != 2 {
		t.Fatalf("restarted status = %+v, err = %v", status, err)
	}
}

func TestRepeatedStopDoesNotEmitAnotherEvent(t *testing.T) {
	connector := newFakeConnector()
	events := make(chan Event, 8)
	manager := NewManager(Config{
		Connector: connector,
		Emitter: EmitterFunc(func(_ context.Context, event Event) error {
			events <- event
			return nil
		}),
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "stop-only", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "a-1")
	_ = waitRecordingEvents(t, events, 1)
	if err := manager.StartRecording(tab.ID, filepath.Join(t.TempDir(), "stop-only.log")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StopRecording(tab.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StopRecording(tab.ID); err != nil {
		t.Fatal(err)
	}
	got := waitRecordingEvents(t, events, 2)
	if len(got) != 2 {
		t.Fatalf("events = %+v, want 2", got)
	}
	assertControlVersionsIncrease(t, got)
}

func TestCoreTerminalRejectsUnknownEncodingBeforeOpeningPTY(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "encoding", Kind: KindSSH, Encoding: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.OpenTab(context.Background(), OpenTabOptions{
		SessionID: session.ID, ChannelID: "a-1", Cols: 80, Rows: 24,
	}); err == nil {
		t.Fatal("unknown terminal encoding was accepted")
	}
	if got := connector.transport(0).channelCount(); got != 0 {
		t.Fatalf("encoding failure opened %d PTYs", got)
	}
}

type requestTerminalFixture struct {
	manager       *Manager
	connector     *fakeConnector
	session       *Session
	tab           *Tab
	recordingPath string
}

func newRequestTerminalFixture(t *testing.T, kind string) *requestTerminalFixture {
	t.Helper()
	recordingPath := filepath.Join(t.TempDir(), "recording.log")
	connector := newFakeConnector()
	provider := newFakeDurableProvider()
	manager := NewManager(Config{
		Connector: connector, ReconnectBackoff: []time.Duration{0},
		DurableResolver: DurableResolverFunc(func(context.Context, base.Transport) (base.DurableProvider, error) { return provider, nil }),
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(requestTestUser("owner-a"), Asset{ID: "request-asset", Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(requestTestUser("owner-a"), OpenTabOptions{
		SessionID: connected.ID, ClientID: "owner-client", ChannelID: "owner-channel", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &requestTerminalFixture{
		manager: manager, connector: connector, session: connected, tab: tab,
		recordingPath: recordingPath,
	}
}

func TestRequestTerminalOperationsEnforceOwner(t *testing.T) {
	actors := []struct {
		name    string
		ctx     context.Context
		allowed bool
	}{
		{name: "owner", ctx: requestTestUser("owner-a"), allowed: true},
		{name: "same-account-second-client", ctx: requestTestUser("owner-a"), allowed: true},
		{name: "open", ctx: WithOpenAccess(context.Background()), allowed: false},
		{name: "closed", ctx: context.Background(), allowed: false},
		{name: "other-account", ctx: requestTestUser("owner-b"), allowed: false},
		{name: "superadmin", ctx: requestTestAdmin("admin"), allowed: false},
	}
	startRecording := func(t *testing.T, fixture *requestTerminalFixture) {
		t.Helper()
		if err := fixture.manager.StartRecording(fixture.tab.ID, fixture.recordingPath); err != nil {
			t.Fatal(err)
		}
	}
	operations := []struct {
		name    string
		kind    string
		prepare func(*testing.T, *requestTerminalFixture)
		invoke  func(context.Context, *requestTerminalFixture) error
	}{
		{name: "open-tab", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			_, err := fixture.manager.OpenTabFor(ctx, OpenTabOptions{
				SessionID: fixture.session.ID, ClientID: "owner-client", ChannelID: "next-channel", Cols: 80, Rows: 24,
			})
			return err
		}},
		{name: "create-tab", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			_, err := fixture.manager.CreateTabFor(ctx, CreateTabOptions{SessionID: fixture.session.ID, Title: "created"})
			return err
		}},
		{name: "attach", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			_, err := fixture.manager.AttachTabFor(ctx, fixture.tab.ID, AttachOptions{ClientID: "owner-client", ChannelID: "next-channel"})
			return err
		}},
		{name: "snapshot", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			_, err := fixture.manager.TerminalSnapshotFor(ctx, fixture.tab.ID)
			return err
		}},
		{name: "screen-text", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			_, err := fixture.manager.ScreenTextFor(ctx, fixture.tab.ID)
			return err
		}},
		{name: "tail", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			_, err := fixture.manager.TailLinesFor(ctx, fixture.tab.ID, 10)
			return err
		}},
		{name: "raw-dump", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			_, err := fixture.manager.RawDumpFor(ctx, fixture.tab.ID, 1024)
			return err
		}},
		{name: "output-since", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			_, _, _, _, err := fixture.manager.OutputSinceFor(ctx, fixture.tab.ID, 0, 1024)
			return err
		}},
		{name: "durable-history", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			_, err := fixture.manager.ReadDurableHistoryFor(ctx, fixture.tab.ID, DurableHistoryLatest, 1024)
			// The fixture tab is regular: past the owner check the read is
			// refused as unsupported.
			if errors.Is(err, ErrUnsupported) {
				return nil
			}
			return err
		}},
		{name: "mode-state", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			_, err := fixture.manager.TerminalModeStateFor(ctx, fixture.tab.ID)
			return err
		}},
		{name: "screen-seq", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			_, err := fixture.manager.ScreenSeqFor(ctx, fixture.tab.ID)
			return err
		}},
		{name: "grid-snapshot", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			_, err := fixture.manager.GridSnapshotFor(ctx, fixture.tab.ID)
			return err
		}},
		{name: "export-log", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			_, err := fixture.manager.ExportLogFor(ctx, fixture.tab.ID, fixture.recordingPath, 1024)
			return err
		}},
		{name: "write", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			return fixture.manager.WriteFor(ctx, fixture.tab.ID, "owner-client", []byte("owner-input"))
		}},
		{name: "exec-line", kind: KindWinRM, invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			_, err := fixture.manager.ExecLineFor(ctx, fixture.tab.ID, "owner-client", "whoami")
			return err
		}},
		{name: "resize", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			return fixture.manager.ResizeFor(ctx, fixture.tab.ID, "owner-client", 100, 30)
		}},
		{name: "flush-resize", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			return fixture.manager.FlushResizeFor(ctx, fixture.tab.ID, "owner-client")
		}},
		{name: "rename", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			_, err := fixture.manager.RenameTabFor(ctx, fixture.tab.ID, "renamed")
			return err
		}},
		{name: "detach-channel", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			return fixture.manager.DetachChannelFor(ctx, "owner-channel")
		}},
		{name: "detach-client", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			return fixture.manager.DetachClientFor(ctx, fixture.tab.ID, "owner-client")
		}},
		{name: "detach-all", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			return fixture.manager.DetachAllFor(ctx, fixture.tab.ID)
		}},
		{name: "record-start", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			return fixture.manager.StartRecordingFor(ctx, fixture.tab.ID, fixture.recordingPath)
		}},
		{name: "record-stop", prepare: startRecording, invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			_, err := fixture.manager.StopRecordingFor(ctx, fixture.tab.ID)
			return err
		}},
		{name: "recording-bytes", prepare: startRecording, invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			_, err := fixture.manager.RecordingBytesFor(ctx, fixture.tab.ID)
			return err
		}},
		{name: "recording-status", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			_, err := fixture.manager.RecordingStatusFor(ctx, fixture.tab.ID)
			return err
		}},
		{name: "set-visible", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			return fixture.manager.SetVisibleFor(ctx, fixture.tab.ID, false)
		}},
		{name: "switch-encoding", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			return fixture.manager.SwitchEncodingFor(ctx, fixture.tab.ID, "utf8")
		}},
		{name: "disconnect", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			return fixture.manager.DisconnectFor(ctx, fixture.session.ID)
		}},
		{name: "start-reconnect", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			return fixture.manager.StartReconnectFor(ctx, fixture.session.ID)
		}},
		{name: "reconnect", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			return fixture.manager.ReconnectFor(ctx, fixture.session.ID)
		}},
		{name: "transport", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			_, err := fixture.manager.TransportFor(ctx, fixture.session.ID)
			return err
		}},
		{name: "close", invoke: func(ctx context.Context, fixture *requestTerminalFixture) error {
			return fixture.manager.CloseTabFor(ctx, fixture.tab.ID)
		}},
	}
	for _, operation := range operations {
		for _, actor := range actors {
			t.Run(operation.name+"/"+actor.name, func(t *testing.T) {
				kind := operation.kind
				if kind == "" {
					kind = KindSSH
				}
				fixture := newRequestTerminalFixture(t, kind)
				if operation.prepare != nil {
					operation.prepare(t, fixture)
				}
				beforeInfo := fixture.tab.Info()
				beforeTabs := len(fixture.manager.ListTabs())
				hasPTY := fixture.connector.transport(0).channelCount() > 0
				var beforeInput []byte
				if hasPTY {
					beforeInput = fixture.connector.transport(0).channel(0).written()
				}
				err := operation.invoke(actor.ctx, fixture)
				assertRequestResult(t, err, actor.allowed)
				if actor.allowed {
					return
				}
				if after := fixture.tab.Info(); after != beforeInfo {
					t.Fatalf("denied operation changed tab: before=%+v after=%+v", beforeInfo, after)
				}
				if after := len(fixture.manager.ListTabs()); after != beforeTabs {
					t.Fatalf("denied operation changed tab count: before=%d after=%d", beforeTabs, after)
				}
				if hasPTY {
					if after := fixture.connector.transport(0).channel(0).written(); !bytes.Equal(after, beforeInput) {
						t.Fatalf("denied operation wrote PTY input: before=%q after=%q", beforeInput, after)
					}
				}
			})
		}
	}
}
