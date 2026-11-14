//go:build unix

package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// TestReconnectDurableReplayContinuity proves a reattached durable tab resumes
// after the consumed prefix: the daemon recording holds both the pre-reconnect
// marker and output produced while the session was away, and each reaches the
// terminal and the transcript exactly once.
func TestReconnectDurableReplayContinuity(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "supervisor")
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "nx-replay-continuity-")
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
	client := supervisor.NewClient(server.SocketPath(), stateDir)
	provider := supervisor.NewRemoteProvider(client)
	resolver := &countingResolver{provider: provider}

	recorder := &transcriptRecorder{}
	tabID := ""
	connector := newFakeConnector()
	connector.connect = func(ctx context.Context, asset Asset, generation uint64, call int) (base.Transport, error) {
		if call == 2 {
			writeDuringDisconnect(t, ctx, client, tabID)
		}
		return newFakeTransport(asset.Kind, uint64(call)+1000), nil
	}
	manager := NewManager(Config{
		Connector:        connector,
		Terminals:        newFakeTerminalFactory(),
		DurableResolver:  resolver,
		ReconnectBackoff: []time.Duration{10 * time.Millisecond},
		Transcripts:      recorder,
	})
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-replay", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "replay-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tabID = info.ID
	if err := manager.Write(ctx, tabID, "client-a", []byte("echo before-mark''er\n")); err != nil {
		t.Fatal(err)
	}
	waitForTabMarker(t, manager, tabID, "before-marker")

	if err := manager.Reconnect(ctx, connected.ID); err != nil {
		t.Fatal(err)
	}
	waitForTabMarker(t, manager, tabID, "during-marker")

	raw, err := manager.RawDump(tabID, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(raw), "before-marker"); count != 1 {
		t.Fatalf("pre-reconnect marker appears %d times in the terminal; want 1: %q", count, raw)
	}
	if count := strings.Count(string(raw), "during-marker"); count != 1 {
		t.Fatalf("disconnect-window marker appears %d times in the terminal; want 1: %q", count, raw)
	}
	// The connection's recording holds the marker once; the reconnect starts
	// a new recording whose screen and later output hold each marker once.
	waitForLong(t, func() bool {
		return len(recordingAfterStart(recorder, 1)) > 0 && strings.Contains(string(recordingAfterStart(recorder, 1)), "during-marker")
	})
	if count := strings.Count(string(recordingAfterStart(recorder, 0)), "before-marker"); count != 1 {
		t.Fatalf("pre-reconnect marker recorded %d times in the first recording; want 1", count)
	}
	second := string(recordingAfterStart(recorder, 1))
	if count := strings.Count(second, "before-marker"); count != 1 {
		t.Fatalf("pre-reconnect marker recorded %d times in the reconnect recording; want 1: %q", count, second)
	}
	if count := strings.Count(second, "during-marker"); count != 1 {
		t.Fatalf("disconnect-window marker recorded %d times in the reconnect recording; want 1: %q", count, second)
	}
	if err := manager.CloseTab(tabID); err != nil {
		t.Fatal(err)
	}
}

func writeDuringDisconnect(t *testing.T, ctx context.Context, client *supervisor.Client, id string) {
	t.Helper()
	stream, err := client.Attach(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Write([]byte("echo during-mark''er\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	buffer := make([]byte, 4096)
	var seen strings.Builder
	for !strings.Contains(seen.String(), "during-marker") {
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not record the disconnect-window output: %q", seen.String())
		}
		count, err := stream.Read(buffer)
		if err != nil {
			t.Fatalf("read daemon recording: %v", err)
		}
		seen.Write(buffer[:count])
	}
}

// offsetRecordingProvider wraps a durable provider and records the offsets
// presented to successful checked resumes.
type offsetRecordingProvider struct {
	base.DurableProvider
	mu      sync.Mutex
	offsets []uint64
}

func (p *offsetRecordingProvider) AttachFromWithIdentity(ctx context.Context, id string, identity base.DurableIdentity, offset uint64) (base.DurableAttachment, error) {
	attachment, err := p.DurableProvider.AttachFromWithIdentity(ctx, id, identity, offset)
	if err == nil {
		p.recordOffset(offset)
	}
	return attachment, err
}

func (p *offsetRecordingProvider) recordOffset(offset uint64) {
	p.mu.Lock()
	p.offsets = append(p.offsets, offset)
	p.mu.Unlock()
}

// TestReconnectDurableAttachUsesConsumedOffset proves the reconnect path
// resumes the daemon stream from the consumed recording offset instead of
// replaying from byte zero: exactly one checked offset resume happens and its
// offset is nonzero yet below the final recording size (the disconnect-window
// output is streamed, the pre-reconnect prefix is not).
func TestReconnectDurableAttachUsesConsumedOffset(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "supervisor")
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "nx-replay-offset-")
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
	client := supervisor.NewClient(server.SocketPath(), stateDir)
	provider := &offsetRecordingProvider{DurableProvider: supervisor.NewRemoteProvider(client)}
	resolver := &countingResolver{provider: provider}

	tabID := ""
	connector := newFakeConnector()
	connector.connect = func(ctx context.Context, asset Asset, generation uint64, call int) (base.Transport, error) {
		if call == 2 {
			writeDuringDisconnect(t, ctx, client, tabID)
		}
		return newFakeTransport(asset.Kind, uint64(call)+1000), nil
	}
	manager := NewManager(Config{
		Connector:        connector,
		Terminals:        newFakeTerminalFactory(),
		DurableResolver:  resolver,
		ReconnectBackoff: []time.Duration{10 * time.Millisecond},
	})
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-offset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "offset-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tabID = info.ID
	if err := manager.Write(ctx, tabID, "client-a", []byte("echo before-mark''er\n")); err != nil {
		t.Fatal(err)
	}
	waitForTabMarker(t, manager, tabID, "before-marker")

	if err := manager.Reconnect(ctx, connected.ID); err != nil {
		t.Fatal(err)
	}
	waitForTabMarker(t, manager, tabID, "during-marker")

	stat, err := os.Stat(filepath.Join(stateDir, "sessions", tabID, "seg-00000000000000000000.raw"))
	if err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.offsets) != 1 {
		t.Fatalf("AttachFrom calls = %v, want exactly one offset resume", provider.offsets)
	}
	offset := provider.offsets[0]
	if offset == 0 || offset >= uint64(stat.Size()) {
		t.Fatalf("AttachFrom offset = %d, want within (0, %d)", offset, stat.Size())
	}
	if err := manager.CloseTab(tabID); err != nil {
		t.Fatal(err)
	}
}

// recordingAfterStart is the content of the index-th recording: the screen it
// started with followed by its output, up to its end.
func recordingAfterStart(recorder *transcriptRecorder, index int) []byte {
	var content []byte
	started := -1
	for _, event := range recorder.snapshot() {
		switch event.kind {
		case recStarted:
			started++
			if started == index {
				content = append(content, event.data...)
			}
		case recScreen, recOutput:
			if started == index {
				content = append(content, event.data...)
			}
		case recEnded:
			if started == index {
				return content
			}
		}
	}
	return content
}
