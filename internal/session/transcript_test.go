package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type transcriptRecorder struct {
	mu      sync.Mutex
	started []TranscriptInfo
	outputs []recorderOutput
	inputs  []recorderOutput
	resizes []recorderResize
	ended   []string
	offsets durableTranscriptOffsetSource
}

type recorderOutput struct {
	sessionID string
	tabID     string
	data      []byte
}

type recorderResize struct {
	sessionID string
	tabID     string
	cols      uint32
	rows      uint32
}

func (r *transcriptRecorder) BindTranscriptOffsetSource(source durableTranscriptOffsetSource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.offsets = source
}

func (r *transcriptRecorder) SessionStarted(_ context.Context, info TranscriptInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, info)
}

func (r *transcriptRecorder) SessionOutput(_ context.Context, durableID, sessionID, tabID string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outputs = append(r.outputs, recorderOutput{sessionID: sessionID, tabID: tabID, data: append([]byte(nil), data...)})
	if durableID != "" && r.offsets != nil {
		r.offsets.PersistDurableTranscriptOffset(durableID, r.offsets.DurableTranscriptCatchUpBytes(durableID)+int64(len(data)))
	}
}

func (r *transcriptRecorder) SessionInput(_ context.Context, sessionID, tabID string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inputs = append(r.inputs, recorderOutput{sessionID: sessionID, tabID: tabID, data: append([]byte(nil), data...)})
}

func (r *transcriptRecorder) SessionResize(_ context.Context, sessionID, tabID string, cols, rows uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resizes = append(r.resizes, recorderResize{sessionID: sessionID, tabID: tabID, cols: cols, rows: rows})
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

func (r *transcriptRecorder) inputBytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	var combined []byte
	for _, input := range r.inputs {
		combined = append(combined, input.data...)
	}
	return combined
}

func (r *transcriptRecorder) resizeSnapshot() []recorderResize {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorderResize(nil), r.resizes...)
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

func TestTranscriptReconnectRecordsPeriodGeometry(t *testing.T) {
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
	waitFor(t, func() bool { return len(recorder.resizeSnapshot()) == 2 })

	if err := manager.Disconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconnect(context.Background(), connected.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(recorder.resizeSnapshot()) == 3 })
	resizes := recorder.resizeSnapshot()
	last := resizes[2]
	if last.sessionID != connected.ID || last.tabID != tab.ID || last.cols != 120 || last.rows != 40 {
		t.Fatalf("reconnect must open the new period with the tab's current geometry: %+v", resizes)
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

func (p *transcriptDurableProvider) DeleteDurableTranscriptOffset(tabID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if record := p.records[tabID]; record != nil {
		record.transcribed = 0
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
	if resizes := secondRecorder.resizeSnapshot(); len(resizes) != 1 || resizes[0].tabID != recoveredInfo.ID ||
		resizes[0].cols != 80 || resizes[0].rows != 24 {
		t.Fatalf("durable recover must record the initial geometry: %+v", resizes)
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

func TestTranscriptDurableRecoveryCatchUpLogsPrefixSkip(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	provider := newTranscriptDurableProvider()
	firstRecorder := &transcriptRecorder{}
	first := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		Durable: provider, Transcripts: firstRecorder, Logger: logger,
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
	if err := provider.channel(info.ID).emit([]byte("recorded-prefix\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(firstRecorder.outputBytes(), []byte("recorded-prefix")) })
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	provider.mu.Lock()
	provider.records[info.ID].reads = [][]byte{[]byte("recorded-prefix\r\nlive-tail\r\n")}
	provider.mu.Unlock()

	secondRecorder := &transcriptRecorder{}
	second := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		Durable: provider, Transcripts: secondRecorder, Logger: logger,
	})
	t.Cleanup(func() { _ = second.Close() })
	recovered, err := second.Connect(context.Background(), Asset{ID: "asset-local", Name: "当前设备", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.OpenTab(context.Background(), OpenTabOptions{
		TabID: info.ID, SessionID: recovered.ID, ClientID: "client-b", ChannelID: "channel-b",
		Cols: 80, Rows: 24, Durable: &DurableTabOptions{Recover: true},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(secondRecorder.outputBytes(), []byte("live-tail")) })
	logs := logBuf.String()
	if !strings.Contains(logs, "catch-up armed") || !strings.Contains(logs, "bytes=17") {
		t.Fatalf("catch-up arm log must carry the persisted offset: %q", logs)
	}
	if !strings.Contains(logs, "catch-up complete") {
		t.Fatalf("catch-up completion log missing: %q", logs)
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
			Cols: 80, Rows: 24, Durable: &DurableTabOptions{Recover: true},
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

func TestTranscriptDurableOffsetDeletedOnKill(t *testing.T) {
	provider := newTranscriptDurableProvider()
	recorder := &transcriptRecorder{}
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		Durable: provider, Transcripts: recorder,
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "asset-local", Name: "当前设备", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(context.Background(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "channel-a",
		Cols: 80, Rows: 24, Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.channel(info.ID).emit([]byte("recorded-before-kill\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return provider.DurableTranscriptCatchUpBytes(info.ID) > 0 })

	if err := manager.CloseTab(info.ID); err != nil {
		t.Fatal(err)
	}
	if got := provider.DurableTranscriptCatchUpBytes(info.ID); got != 0 {
		t.Fatalf("kill must clear the durable transcript offset, got %d", got)
	}
}

func TestTranscriptDurableOffsetDeletedWhenGoneOnReconnect(t *testing.T) {
	provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	resolver := &countingResolver{provider: provider}
	manager := NewManager(Config{
		Connector:         newFakeConnector(),
		Terminals:         newFakeTerminalFactory(),
		DurableResolver:   resolver,
		Transcripts:       &transcriptRecorder{},
		TranscriptOffsets: provider,
		ReconnectBackoff:  []time.Duration{10 * time.Millisecond},
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "ssh-offset-gone", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(context.Background(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "gone-offset-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	attachment := provider.records[info.ID].attachments[0]
	provider.mu.Unlock()
	if err := attachment.emit([]byte("recorded-before-drop\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return provider.DurableTranscriptCatchUpBytes(info.ID) > 0 })

	provider.mu.Lock()
	provider.records[info.ID].killed = 1
	provider.mu.Unlock()
	if err := manager.Reconnect(context.Background(), connected.ID); err != nil {
		t.Fatal(err)
	}
	if got := provider.DurableTranscriptCatchUpBytes(info.ID); got != 0 {
		t.Fatalf("a durable confirmed gone on reconnect must clear the transcript offset, got %d", got)
	}
}

func TestTranscriptRecordsInputAndResize(t *testing.T) {
	connector := newFakeConnector()
	recorder := &transcriptRecorder{}
	manager := newTranscriptTestManager(connector, recorder)
	t.Cleanup(func() { _ = manager.Close() })

	connected, err := manager.Connect(context.Background(), Asset{ID: "asset-ssh", Name: "web-01", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "channel-a")

	resizes := recorder.resizeSnapshot()
	if len(resizes) != 1 || resizes[0].sessionID != connected.ID || resizes[0].tabID != tab.ID ||
		resizes[0].cols != 80 || resizes[0].rows != 24 {
		t.Fatalf("open tab must record the initial geometry: %+v", resizes)
	}

	if err := manager.Write(context.Background(), tab.ID, "client-a", []byte("ls -la\r")); err != nil {
		t.Fatal(err)
	}
	if err := manager.WriteInternal(context.Background(), tab.ID, []byte("internal-input")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		inputs := recorder.inputBytes()
		return bytes.Contains(inputs, []byte("ls -la\r")) && bytes.Contains(inputs, []byte("internal-input"))
	})
	if bytes.Contains(recorder.outputBytes(), []byte("ls -la")) || bytes.Contains(recorder.outputBytes(), []byte("internal-input")) {
		t.Fatal("input must not leak into recorded output")
	}

	if err := manager.Write(context.Background(), tab.ID, "client-b", []byte("rejected")); err == nil {
		t.Fatal("non-controller write must fail")
	}
	if bytes.Contains(recorder.inputBytes(), []byte("rejected")) {
		t.Fatal("failed write must not be recorded")
	}

	if err := manager.Resize(context.Background(), tab.ID, "client-a", 120, 40); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		resizes := recorder.resizeSnapshot()
		return len(resizes) == 2 && resizes[1].cols == 120 && resizes[1].rows == 40
	})
	if err := waitForChannelResize(t, connector.transport(0).channel(0), 120, 40); err != nil {
		t.Fatal(err)
	}

	if err := manager.Resize(context.Background(), tab.ID, "client-a", 80, 24); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(recorder.resizeSnapshot()) == 3 })
}
