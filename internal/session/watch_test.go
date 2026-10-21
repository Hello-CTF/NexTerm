package session

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

func newWatchTestManager(t *testing.T) *Manager {
	t.Helper()
	manager := NewManager(Config{IdleTimeout: -1, OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func openWatchTestTab(t *testing.T, manager *Manager) TabInfo {
	t.Helper()
	tab, err := manager.RegisterRemoteSession(context.Background(), RemoteSessionOptions{OwnerID: "owner-a", AssetID: "asset-1", Title: "watched"})
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

func feedWatchTestTab(t *testing.T, manager *Manager, tabID string, data []byte) {
	t.Helper()
	tab, err := manager.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.feed(context.Background(), tab, 1, data); err != nil {
		t.Fatal(err)
	}
}

func watchTestEpisode(t *testing.T, manager *Manager, tabID string) string {
	t.Helper()
	state, ok := manager.WatchStreamState(context.Background(), tabID)
	if !ok || !state.EpisodeOK || state.WatchIdentity() == "" {
		t.Fatalf("watch stream state = %+v, %v", state, ok)
	}
	return state.WatchIdentity()
}

func TestWatchSourceStreamsAbsolutePositions(t *testing.T) {
	manager := newWatchTestManager(t)
	tab := openWatchTestTab(t, manager)
	episode := watchTestEpisode(t, manager, tab.ID)
	ctx := context.Background()
	source, err := manager.OpenWatchSource(ctx, tab.ID, episode, WatchSourceOptions{PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if end, err := source.End(ctx); err != nil || end != 0 {
		t.Fatalf("End = %d, %v; want 0, nil", end, err)
	}
	feedWatchTestTab(t, manager, tab.ID, []byte("hello "))
	feedWatchTestTab(t, manager, tab.ID, []byte("world"))

	chunk, next, err := source.Chunk(ctx, 0)
	if err != nil || string(chunk) != "hello world" || next != 11 {
		t.Fatalf("Chunk(0) = %q, %d, %v; want %q, 11, nil", chunk, next, err, "hello world")
	}
	if end, err := source.End(ctx); err != nil || end != 11 {
		t.Fatalf("End = %d, %v; want 11, nil", end, err)
	}
	tail, err := source.Tail(ctx, 11, 5)
	if err != nil || string(tail) != "world" {
		t.Fatalf("Tail(11, 5) = %q, %v; want %q, nil", tail, err, "world")
	}
	tail, err = source.Tail(ctx, 3, 8)
	if err != nil || string(tail) != "hel" {
		t.Fatalf("Tail(3, 8) = %q, %v; want %q, nil", tail, err, "hel")
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWatchSourceBlocksUntilOutputArrives(t *testing.T) {
	manager := newWatchTestManager(t)
	tab := openWatchTestTab(t, manager)
	source, err := manager.OpenWatchSource(context.Background(), tab.ID, watchTestEpisode(t, manager, tab.ID), WatchSourceOptions{PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got atomic.Value
	done := make(chan struct{})
	go func() {
		defer close(done)
		chunk, next, err := source.Chunk(ctx, 0)
		got.Store([3]any{string(chunk), next, err})
	}()
	select {
	case <-done:
		t.Fatal("Chunk returned before any output arrived")
	case <-time.After(20 * time.Millisecond):
	}
	feedWatchTestTab(t, manager, tab.ID, []byte("ready"))
	<-done
	result := got.Load().([3]any)
	if result[0] != "ready" || result[1] != int64(5) || result[2] != nil {
		t.Fatalf("blocked Chunk = %+v; want ready, 5, nil", result)
	}
}

func TestWatchSourceReportsEOFOnlyAfterTheFinalBytes(t *testing.T) {
	manager := newWatchTestManager(t)
	tab := openWatchTestTab(t, manager)
	ctx := context.Background()
	source, err := manager.OpenWatchSource(ctx, tab.ID, watchTestEpisode(t, manager, tab.ID), WatchSourceOptions{PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	feedWatchTestTab(t, manager, tab.ID, []byte("last"))
	tabObject, err := manager.Tab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	tabObject.mu.Lock()
	tabObject.exited = true
	tabObject.mu.Unlock()

	chunk, next, err := source.Chunk(ctx, 0)
	if err != nil || string(chunk) != "last" || next != 4 {
		t.Fatalf("final Chunk = %q, %d, %v; want the retained bytes first", chunk, next, err)
	}
	if _, _, err := source.Chunk(ctx, 4); err != io.EOF {
		t.Fatalf("Chunk at the exited end = %v; want io.EOF", err)
	}
}

func TestWatchSourceFailsOnEpisodeReplacement(t *testing.T) {
	manager := newWatchTestManager(t)
	tab := openWatchTestTab(t, manager)
	ctx := context.Background()
	identity := watchTestEpisode(t, manager, tab.ID)
	source, err := manager.OpenWatchSource(ctx, tab.ID, identity, WatchSourceOptions{PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	tabObject, err := manager.Tab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The coordinate incarnation is the stream epoch: recreating the ring
	// replaces the stream even when nothing else about the tab changed.
	tabObject.mu.Lock()
	tabObject.streamEpoch = "epoch-recreated"
	tabObject.mu.Unlock()

	var replaced *WatchEpisodeReplacedError
	if _, _, err := source.Chunk(ctx, 0); !errors.As(err, &replaced) {
		t.Fatalf("Chunk after replacement = %v; want WatchEpisodeReplacedError", err)
	}
	state, ok := manager.WatchStreamState(ctx, tab.ID)
	if !ok || replaced.Current != state.WatchIdentity() {
		t.Fatalf("replacement evidence = %q; want the current watch identity %q", replaced.Current, state.WatchIdentity())
	}
	if _, err := manager.OpenWatchSource(ctx, tab.ID, identity, WatchSourceOptions{}); !errors.As(err, &replaced) {
		t.Fatalf("open with a stale identity = %v; want WatchEpisodeReplacedError", err)
	}
	if _, err := manager.OpenWatchSource(ctx, tab.ID, "", WatchSourceOptions{}); !errors.Is(err, ErrWatchEpisodeUnevidenced) {
		t.Fatalf("open with an empty identity = %v; want ErrWatchEpisodeUnevidenced", err)
	}
	if _, err := manager.OpenWatchSource(ctx, "tab-missing", "identity", WatchSourceOptions{}); !errors.Is(err, ErrWatchTargetGone) {
		t.Fatalf("open on a missing tab = %v; want ErrWatchTargetGone", err)
	}
}

// fakeWatchSeqReader simulates a scrollback that has already evicted the
// prefix before base.
type fakeWatchSeqReader struct {
	data []byte
	base uint64
}

func (r *fakeWatchSeqReader) LatestSeq() uint64 { return r.base + uint64(len(r.data)) }

func (r *fakeWatchSeqReader) ReplayFrom(seq uint64, max int) ([]byte, uint64, bool) {
	start := seq
	clamped := false
	if start < r.base {
		start = r.base
		clamped = true
	}
	if start > r.LatestSeq() {
		start = r.LatestSeq()
	}
	n := int(r.LatestSeq() - start)
	if max > 0 && n > max {
		n = max
	}
	return append([]byte(nil), r.data[int(start-r.base):int(start-r.base)+n]...), start, clamped
}

func TestWatchSourceGapReportsTheHonestResumePosition(t *testing.T) {
	manager := newWatchTestManager(t)
	tab := openWatchTestTab(t, manager)
	episode := watchTestEpisode(t, manager, tab.ID)
	source := &WatchSource{
		manager: manager, tabID: tab.ID, identity: episode,
		reader:  &fakeWatchSeqReader{data: []byte("retained"), base: 96},
		options: WatchSourceOptions{PollInterval: time.Millisecond, MaxChunkBytes: 256},
		ctx:     context.Background(),
	}
	var gap *WatchGapError
	if _, _, err := source.Chunk(context.Background(), 2); !errors.As(err, &gap) {
		t.Fatalf("Chunk below retention = %v; want WatchGapError", err)
	} else if gap.FromSeq != 96 {
		t.Fatalf("gap resume = %d; want 96", gap.FromSeq)
	}
	// At the resumed position the retained bytes read with true coordinates.
	chunk, next, err := source.Chunk(context.Background(), 96)
	if err != nil || string(chunk) != "retained" || next != 104 {
		t.Fatalf("Chunk at resume = %q, %d, %v; want retained, 104, nil", chunk, next, err)
	}
}

func TestWatchSourceRequiresSequenceTracking(t *testing.T) {
	manager := NewManager(Config{
		IdleTimeout: -1, OpenAccess: true,
		Terminals: TerminalFactoryFunc(func(config TerminalConfig) (TerminalState, error) {
			return &stubTerminal{}, nil
		}),
	})
	t.Cleanup(func() { _ = manager.Close() })
	tab := openWatchTestTab(t, manager)
	_, err := manager.OpenWatchSource(context.Background(), tab.ID, watchTestEpisode(t, manager, tab.ID), WatchSourceOptions{})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("open without sequence tracking = %v; want ErrUnsupported", err)
	}
}

type stubTerminal struct{ closed bool }

func (t *stubTerminal) Feed([]byte)                     {}
func (t *stubTerminal) Dump(int) []byte                 { return nil }
func (t *stubTerminal) Resize(int, int) error           { return nil }
func (t *stubTerminal) SetResponseHandler(func([]byte)) {}
func (t *stubTerminal) Close()                          { t.closed = true }
