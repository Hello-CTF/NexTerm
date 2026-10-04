package session

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type transcriptRecorder struct {
	mu      sync.Mutex
	started []TranscriptInfo
	outputs []recorderOutput
	ended   []string
}

type recorderOutput struct {
	sessionID string
	tabID     string
	data      []byte
}

func (r *transcriptRecorder) SessionStarted(_ context.Context, info TranscriptInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, info)
}

func (r *transcriptRecorder) SessionOutput(_ context.Context, sessionID, tabID string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outputs = append(r.outputs, recorderOutput{sessionID: sessionID, tabID: tabID, data: append([]byte(nil), data...)})
}

func (r *transcriptRecorder) SessionEnded(_ context.Context, sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ended = append(r.ended, sessionID)
}

func (r *transcriptRecorder) snapshot() ([]TranscriptInfo, []recorderOutput, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	started := append([]TranscriptInfo(nil), r.started...)
	outputs := append([]recorderOutput(nil), r.outputs...)
	ended := append([]string(nil), r.ended...)
	return started, outputs, ended
}

func (r *transcriptRecorder) outputBytes() []byte {
	_, outputs, _ := r.snapshot()
	var combined []byte
	for _, output := range outputs {
		combined = append(combined, output.data...)
	}
	return combined
}

func newTranscriptTestManager(connector Connector, sink TranscriptSink) *Manager {
	return NewManager(Config{
		Connector:   connector,
		Terminals:   newFakeTerminalFactory(),
		Transcripts: sink,
	})
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
	started, _, _ := recorder.snapshot()
	if len(started) != 1 {
		t.Fatalf("expected one transcript start, got %+v", started)
	}
	if started[0].SessionID != connected.ID || started[0].AssetID != "asset-ssh" || started[0].AssetName != "web-01" || started[0].AssetKind != KindSSH {
		t.Fatalf("unexpected transcript identity: %+v", started[0])
	}

	tab := openTestTab(t, manager, connected, "client-a", "channel-a")
	channel := connector.transport(0).channel(0)
	if err := channel.emit([]byte("output-one\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(recorder.outputBytes(), []byte("output-one")) })

	if err := manager.Write(context.Background(), tab.ID, "client-a", []byte("secret-input\r")); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("output-two\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(recorder.outputBytes(), []byte("output-two")) })

	_, outputs, ended := recorder.snapshot()
	if len(ended) != 0 {
		t.Fatalf("session still connected but transcript ended: %+v", ended)
	}
	combined := recorder.outputBytes()
	if bytes.Contains(combined, []byte("secret-input")) {
		t.Fatal("user input must not be recorded as transcript output")
	}
	for _, output := range outputs {
		if output.sessionID != connected.ID || output.tabID != tab.ID {
			t.Fatalf("output misattributed: %+v", output)
		}
	}
	if !bytes.Contains(combined, []byte("output-one")) || !bytes.Contains(combined, []byte("output-two")) {
		t.Fatalf("missing recorded output: %q", combined)
	}
}

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
	time.Sleep(100 * time.Millisecond)

	_, outputs, _ := recorder.snapshot()
	count := 0
	for _, output := range outputs {
		if bytes.Contains(output.data, []byte("recorded-once")) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("replay after reattach recorded %d copies, want exactly 1", count)
	}
}

func TestTranscriptReconnectStartsNewConnectionPeriod(t *testing.T) {
	connector := newFakeConnector()
	recorder := &transcriptRecorder{}
	manager := newTranscriptTestManager(connector, recorder)
	t.Cleanup(func() { _ = manager.Close() })

	connected, err := manager.Connect(context.Background(), Asset{ID: "asset-ssh", Name: "web-01", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	openTestTab(t, manager, connected, "client-a", "channel-a")
	if err := connector.transport(0).channel(0).emit([]byte("before-drop\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(recorder.outputBytes(), []byte("before-drop")) })

	if err := manager.Disconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
	_, _, ended := recorder.snapshot()
	if len(ended) != 1 || ended[0] != connected.ID {
		t.Fatalf("disconnect must end the transcript: %+v", ended)
	}

	if err := manager.Reconnect(context.Background(), connected.ID); err != nil {
		t.Fatal(err)
	}
	started, _, _ := recorder.snapshot()
	if len(started) != 2 {
		t.Fatalf("reconnect must start a new transcript period: %+v", started)
	}
	combined := recorder.outputBytes()
	if !bytes.Contains(combined, []byte("reconnected")) {
		t.Fatalf("reconnect banner must be recorded: %q", combined)
	}

	if err := connector.transport(1).channel(0).emit([]byte("after-reconnect\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(recorder.outputBytes(), []byte("after-reconnect")) })
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
	if err := connector.transport(0).channel(0).emit([]byte("output\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return bytes.Contains(tab.terminal.Dump(1<<20), []byte("output"))
	})
	if err := manager.Disconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
}

type transcriptDurableRecord struct {
	reads       [][]byte
	transcribed int64
	channel     *fakeChannel
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

func (p *transcriptDurableProvider) Attach(_ context.Context, id string) (base.DurableAttachment, error) {
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
	}
	return attachment, nil
}

func (p *transcriptDurableProvider) DurableTranscriptCatchUpBytes(tabID string) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if record := p.records[tabID]; record != nil {
		return record.transcribed
	}
	return 0
}

func (p *transcriptDurableProvider) PersistDurableTranscriptOffset(tabID string, offset int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if record := p.records[tabID]; record != nil {
		record.transcribed = offset
	}
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
}

func (a *transcriptDurableAttachment) Kill(context.Context) error { return nil }

func TestTranscriptDurableRecoveryCatchUpNotRecorded(t *testing.T) {
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
		Cols: 80, Rows: 24, Durable: &DurableTabOptions{Recover: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	recoveredTab, err := second.Tab(recoveredInfo.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return bytes.Contains(recoveredTab.terminal.Dump(1<<20), []byte("durable-replay-marker"))
	})
	waitFor(t, func() bool { return bytes.Contains(secondRecorder.outputBytes(), []byte("live-after-boundary")) })
	recorded := secondRecorder.outputBytes()
	if bytes.Contains(recorded, []byte("durable-replay-marker")) {
		t.Fatalf("recovery catch-up must not be transcribed again: %q", recorded)
	}
	if !bytes.Contains(recorded, []byte("live-after-boundary\r\n")) {
		t.Fatalf("output after the catch-up boundary must be recorded: %q", recorded)
	}
	if bytes.Contains(recorded, []byte("ker\r\n")) {
		t.Fatalf("replay tail inside a straddling read must be suppressed: %q", recorded)
	}
}
