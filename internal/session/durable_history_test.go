//go:build unix

package session

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func supervisorRemoteProvider(t *testing.T) *supervisor.RemoteProvider {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "supervisor")
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "nx-session-history-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	server, err := supervisor.NewServer(instance, filepath.Join(runDir, "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		_ = instance.Close()
	})
	return supervisor.NewRemoteProvider(supervisor.NewClient(server.SocketPath(), stateDir))
}

// readWholeHistory walks the helper's retained output backwards.
func readWholeHistory(t *testing.T, provider *supervisor.RemoteProvider, tabID string, identity base.DurableIdentity, next uint64) ([]byte, uint64) {
	t.Helper()
	var parts [][]byte
	before := next
	for {
		chunk, err := provider.ReadDurableHistory(context.Background(), tabID, identity, base.DurableHistoryRequest{Before: before})
		if err != nil {
			t.Fatal(err)
		}
		parts = append([][]byte{chunk.Data}, parts...)
		if chunk.Truncated || chunk.Start == 0 {
			return bytes.Join(parts, nil), chunk.Start
		}
		before = chunk.Start
	}
}

// waitForHelperOutput waits until the helper recorded marker and its output
// stopped growing, and returns the recorded end.
func waitForHelperOutput(t *testing.T, provider *supervisor.RemoteProvider, tabID string, identity base.DurableIdentity, marker string) uint64 {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		descriptor, found, err := findDurableDescriptor(context.Background(), provider, tabID)
		if err != nil || !found {
			t.Fatalf("descriptor of %s: found=%v err=%v", tabID, found, err)
		}
		chunk, err := provider.ReadDurableHistory(context.Background(), tabID, identity, base.DurableHistoryRequest{Before: descriptor.HistoryNext, MaxBytes: 4096})
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(chunk.Data, []byte(marker)) {
			return descriptor.HistoryNext
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper never recorded %q", marker)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func openRecordingManager(t *testing.T, provider base.DurableProvider, offsets *memoryTranscriptOffsets, recorder *gapRecorder, limit uint64) (*Manager, *Session) {
	t.Helper()
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), Durable: provider,
		TranscriptOffsets: offsets, Transcripts: recorder,
		DurableHistoryLimit: func(context.Context) uint64 { return limit },
		OpenAccess:          true,
	})
	connected, err := manager.Connect(context.Background(), Asset{ID: "history-local", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	return manager, connected
}

// A fresh open renders only the recent tail, while the transcript still gets
// every byte after its recorded offset exactly once.
func TestSupervisorDurableFreshOpenReplaysTailAndKeepsTranscript(t *testing.T) {
	provider := supervisorRemoteProvider(t)
	offsets := &memoryTranscriptOffsets{}
	ctx := context.Background()
	tabID := ids.New()

	firstRecorder := &gapRecorder{transcriptRecorder: &transcriptRecorder{}}
	first, connected := openRecordingManager(t, provider, offsets, firstRecorder, 0)
	if _, err := first.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "history-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}
	identity := *recoveryIdentity(first, tabID)
	if err := first.Write(ctx, tabID, "client-a", []byte("echo before-mark''er; sleep 1; seq -f 'away-%07g' 1 60000; echo after-mark''er\n")); err != nil {
		t.Fatal(err)
	}
	waitForTabOutput(t, first, tabID, "before-marker")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	next := waitForHelperOutput(t, provider, tabID, identity, "after-marker")
	recording, start := readWholeHistory(t, provider, tabID, identity, next)
	if start != 0 {
		t.Fatalf("history starts at %d, want the whole output retained", start)
	}

	secondRecorder := &gapRecorder{transcriptRecorder: &transcriptRecorder{}}
	second, reconnected := openRecordingManager(t, provider, offsets, secondRecorder, 0)
	t.Cleanup(func() { _ = second.Close() })
	if _, err := second.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: reconnected.ID, ClientID: "client-a", ChannelID: "history-2", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Recover: true, Identity: &identity},
	}); err != nil {
		t.Fatal(err)
	}
	shown := waitForTabOutput(t, second, tabID, "after-marker")
	if strings.Contains(shown, "away-0000001\r") || len(shown) > int(base.DurableFreshTailBytes)+4096 {
		t.Fatalf("fresh open rendered %d bytes including the start of the output", len(shown))
	}
	waitForLong(t, func() bool { return bytes.Contains(secondRecorder.outputBytes(), []byte("after-marker")) })
	// The shell may print its prompt after the marker; compare once the
	// transcript caught up with whatever the helper recorded last.
	var transcribed []byte
	waitForLong(t, func() bool {
		descriptor, found, err := findDurableDescriptor(ctx, provider, tabID)
		if err != nil || !found {
			return false
		}
		recording, _ = readWholeHistory(t, provider, tabID, identity, descriptor.HistoryNext)
		transcribed = append(firstRecorder.outputBytes(), secondRecorder.outputBytes()...)
		return len(transcribed) >= len(recording)
	})
	if !bytes.Equal(transcribed, recording) {
		t.Fatalf("transcript has %d bytes across the restart, the helper recorded %d", len(transcribed), len(recording))
	}
	if gaps := secondRecorder.skipped(); gaps != 0 {
		t.Fatalf("transcript reported %d dropped bytes with the whole history retained", gaps)
	}

	tab, err := second.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	tab.mu.Lock()
	historyFrom, truncated := tab.historyFrom, tab.historyTruncated
	tab.mu.Unlock()
	if historyFrom == 0 || truncated {
		t.Fatalf("history from %d truncated=%v, want a tail without truncation", historyFrom, truncated)
	}
	earlier, err := second.ReadDurableHistoryFor(ctx, tabID, historyFrom, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	if earlier.End != historyFrom || len(earlier.Data) == 0 || !bytes.Equal(earlier.Data, recording[earlier.Start:earlier.End]) {
		t.Fatalf("earlier history [%d, %d) does not end at the rendered start %d", earlier.Start, earlier.End, historyFrom)
	}
}

// Output the helper dropped under its limit while no process recorded it is
// reported to the transcript as a gap, keeping the transcript offset aligned.
func TestSupervisorDurableRecoverReportsDroppedTranscriptOutput(t *testing.T) {
	provider := supervisorRemoteProvider(t)
	offsets := &memoryTranscriptOffsets{}
	ctx := context.Background()
	tabID := ids.New()

	firstRecorder := &gapRecorder{transcriptRecorder: &transcriptRecorder{}}
	first, connected := openRecordingManager(t, provider, offsets, firstRecorder, supervisor.MinHistoryLimit)
	if _, err := first.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "dropped-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}
	identity := *recoveryIdentity(first, tabID)
	if err := first.Write(ctx, tabID, "client-a", []byte("echo before-mark''er; sleep 1; seq -f 'away-%07g' 1 300000; echo after-mark''er\n")); err != nil {
		t.Fatal(err)
	}
	waitForTabOutput(t, first, tabID, "before-marker")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	next := waitForHelperOutput(t, provider, tabID, identity, "after-marker")
	descriptor, _, err := findDurableDescriptor(ctx, provider, tabID)
	if err != nil {
		t.Fatal(err)
	}
	if !descriptor.HistoryTruncated || descriptor.HistoryBase == 0 {
		t.Fatalf("descriptor = %+v, want history dropped by the 1 MiB limit", descriptor)
	}

	secondRecorder := &gapRecorder{transcriptRecorder: &transcriptRecorder{}}
	second, reconnected := openRecordingManager(t, provider, offsets, secondRecorder, supervisor.MinHistoryLimit)
	t.Cleanup(func() { _ = second.Close() })
	if _, err := second.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: reconnected.ID, ClientID: "client-a", ChannelID: "dropped-2", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Recover: true, Identity: &identity},
	}); err != nil {
		t.Fatal(err)
	}
	waitForTabOutput(t, second, tabID, "after-marker")
	waitForLong(t, func() bool {
		current, found, err := findDurableDescriptor(ctx, provider, tabID)
		return err == nil && found && current.HistoryNext >= next && offsets.DurableTranscriptCatchUpBytesContext(ctx, tabID) == int64(current.HistoryNext)
	})
	if secondRecorder.skipped() == 0 {
		t.Fatal("transcript was not told about the dropped output")
	}
	tab, err := second.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	tab.mu.Lock()
	historyBase, truncated := tab.historyBase, tab.historyTruncated
	tab.mu.Unlock()
	if !truncated || historyBase != descriptor.HistoryBase {
		t.Fatalf("tab history base %d truncated=%v, want base %d truncated", historyBase, truncated, descriptor.HistoryBase)
	}
}

// A transcript offset beyond the helper's recording (output lost in a host
// crash) no longer fails the recovery: it is realigned and new output is
// transcribed instead of being swallowed as catch-up.
func TestSupervisorDurableRecoverRealignsTranscriptOffsetAheadOfRecording(t *testing.T) {
	provider := supervisorRemoteProvider(t)
	offsets := &memoryTranscriptOffsets{}
	ctx := context.Background()
	tabID := ids.New()

	first, connected := openRecordingManager(t, provider, offsets, &gapRecorder{transcriptRecorder: &transcriptRecorder{}}, 0)
	if _, err := first.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "ahead-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}
	identity := *recoveryIdentity(first, tabID)
	if err := first.Write(ctx, tabID, "client-a", []byte("echo before-mark''er\n")); err != nil {
		t.Fatal(err)
	}
	waitForTabOutput(t, first, tabID, "before-marker")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	next := waitForHelperOutput(t, provider, tabID, identity, "before-marker")
	offsets.PersistDurableTranscriptOffsetContext(ctx, tabID, int64(next)+5000)

	recorder := &gapRecorder{transcriptRecorder: &transcriptRecorder{}}
	second, reconnected := openRecordingManager(t, provider, offsets, recorder, 0)
	t.Cleanup(func() { _ = second.Close() })
	if _, err := second.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: reconnected.ID, ClientID: "client-a", ChannelID: "ahead-2", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Recover: true, Identity: &identity},
	}); err != nil {
		t.Fatalf("recover with a transcript offset ahead of the recording: %v", err)
	}
	if err := second.Write(ctx, tabID, "client-a", []byte("echo after-mark''er\n")); err != nil {
		t.Fatal(err)
	}
	waitForLong(t, func() bool { return bytes.Contains(recorder.outputBytes(), []byte("after-marker")) })
	waitForHelperOutput(t, provider, tabID, identity, "after-marker")
	waitForLong(t, func() bool {
		descriptor, found, err := findDurableDescriptor(ctx, provider, tabID)
		return err == nil && found && offsets.DurableTranscriptCatchUpBytesContext(ctx, tabID) == int64(descriptor.HistoryNext)
	})
}

func waitForLong(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
