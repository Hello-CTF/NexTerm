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

	var transcriptMu sync.Mutex
	var transcript strings.Builder
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
		Transcripts: TranscriptSinkFuncs{OutputFunc: func(_ context.Context, _, _, _ string, data []byte) {
			transcriptMu.Lock()
			defer transcriptMu.Unlock()
			transcript.Write(data)
		}},
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
	transcriptMu.Lock()
	transcribed := transcript.String()
	transcriptMu.Unlock()
	if count := strings.Count(transcribed, "before-marker"); count != 1 {
		t.Fatalf("pre-reconnect marker transcribed %d times; want 1: %q", count, transcribed)
	}
	if count := strings.Count(transcribed, "during-marker"); count != 1 {
		t.Fatalf("disconnect-window marker transcribed %d times; want 1: %q", count, transcribed)
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
// presented to successful checked and legacy resumes.
type offsetRecordingProvider struct {
	base.DurableProvider
	mu      sync.Mutex
	offsets []uint64
}

func (p *offsetRecordingProvider) AttachFrom(ctx context.Context, id string, offset uint64) (base.DurableAttachment, error) {
	attacher, ok := p.DurableProvider.(interface {
		AttachFrom(context.Context, string, uint64) (base.DurableAttachment, error)
	})
	if !ok {
		return nil, base.ErrUnsupported
	}
	attachment, err := attacher.AttachFrom(ctx, id, offset)
	if err == nil {
		p.recordOffset(offset)
	}
	return attachment, err
}

func (p *offsetRecordingProvider) AttachWithIdentity(ctx context.Context, id string, identity base.DurableIdentity) (base.DurableAttachment, error) {
	attacher, ok := p.DurableProvider.(base.DurableIdentityAttacher)
	if !ok {
		return nil, base.ErrUnsupported
	}
	return attacher.AttachWithIdentity(ctx, id, identity)
}

func (p *offsetRecordingProvider) AttachFromWithIdentity(ctx context.Context, id string, identity base.DurableIdentity, offset uint64) (base.DurableAttachment, error) {
	attacher, ok := p.DurableProvider.(base.DurableIdentityAttacher)
	if !ok {
		return nil, base.ErrUnsupported
	}
	attachment, err := attacher.AttachFromWithIdentity(ctx, id, identity, offset)
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

	stat, err := os.Stat(filepath.Join(stateDir, "sessions", tabID, "output.raw"))
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
