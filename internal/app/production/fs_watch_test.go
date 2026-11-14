package production

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type watchTestFS struct {
	base.FileSystem
	mu      sync.Mutex
	entries map[string][]base.FileEntry
	lists   int
}

func (f *watchTestFS) List(_ context.Context, dir string) ([]base.FileEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	return append([]base.FileEntry(nil), f.entries[dir]...), nil
}

func (f *watchTestFS) setEntries(dir string, entries ...base.FileEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[dir] = entries
}

type watchTestHandle struct {
	watcher *pushTestFS
}

func (h *watchTestHandle) Set(dirs []string) error {
	h.watcher.mu.Lock()
	h.watcher.dirs = append([]string(nil), dirs...)
	h.watcher.mu.Unlock()
	h.watcher.sets <- dirs
	return nil
}

func (h *watchTestHandle) Close() error { return nil }

type pushTestFS struct {
	watchTestFS
	sets  chan []string
	dirs  []string
	sink  func(base.WatchEvent)
	done  func(error)
	opens int
}

func (f *pushTestFS) Watch(_ context.Context, sink func(base.WatchEvent), done func(error)) (base.WatchHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sink, f.done = sink, done
	f.opens++
	return &watchTestHandle{watcher: f}, nil
}

func (f *pushTestFS) FileChannel() string { return base.FileChannelHelper }

func (f *pushTestFS) emit(event base.WatchEvent) {
	f.mu.Lock()
	sink := f.sink
	f.mu.Unlock()
	sink(event)
}

func (f *pushTestFS) fail(err error) {
	f.mu.Lock()
	done := f.done
	f.mu.Unlock()
	done(err)
}

type watchTestEvent struct {
	topic   ipc.Topic
	payload any
}

func newTestWatchHub(t *testing.T, filesystem base.FileSystem) (*fsWatchHub, chan watchTestEvent) {
	t.Helper()
	events := make(chan watchTestEvent, 256)
	hub := newFSWatchHub(func(context.Context, string) (base.FileSystem, base.Transport, error) {
		return filesystem, nil, nil
	}, func(_ context.Context, topic ipc.Topic, payload any) {
		events <- watchTestEvent{topic: topic, payload: payload}
	}, nil)
	hub.pollMin, hub.pollMax, hub.tick = 20*time.Millisecond, 40*time.Millisecond, 10*time.Millisecond
	hub.retryMin, hub.retryMax = 10*time.Millisecond, 20*time.Millisecond
	t.Cleanup(hub.close)
	return hub, events
}

func waitChanged(t *testing.T, events <-chan watchTestEvent, match func(fsChangedDTO) bool) fsChangedDTO {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-events:
			if changed, ok := event.payload.(fsChangedDTO); ok && match(changed) {
				return changed
			}
		case <-deadline:
			t.Fatal("no matching change event")
		}
	}
}

func waitStatus(t *testing.T, events <-chan watchTestEvent, mode string) fsWatchStatusDTO {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-events:
			if status, ok := event.payload.(fsWatchStatusDTO); ok && status.Mode == mode {
				return status
			}
		case <-deadline:
			t.Fatalf("no %s status", mode)
		}
	}
}

func TestFSWatchHubPushesChangesForTheUnionOfSubscribers(t *testing.T) {
	filesystem := &pushTestFS{watchTestFS: watchTestFS{entries: map[string][]base.FileEntry{}}, sets: make(chan []string, 16)}
	hub, events := newTestWatchHub(t, filesystem)
	ctx := context.Background()
	if _, err := hub.watch(ctx, fsWatchRequest{SessionID: "s1", WatcherID: "tree", Dirs: []string{"/a", "/b"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.watch(ctx, fsWatchRequest{SessionID: "s1", WatcherID: "editor", Dirs: []string{"/b", "/c"}}); err != nil {
		t.Fatal(err)
	}
	status := waitStatus(t, events, fsWatchPush)
	if status.Channel != base.FileChannelHelper || status.SessionID != "s1" {
		t.Fatalf("status = %+v", status)
	}
	for dirs := range filesystem.sets {
		if slices.Equal(dirs, []string{"/a", "/b", "/c"}) {
			break
		}
	}
	filesystem.emit(base.WatchEvent{Dir: "/c", Names: []string{"x.txt"}})
	changed := waitChanged(t, events, func(changed fsChangedDTO) bool { return changed.Dir == "/c" && len(changed.Names) == 1 })
	if changed.SessionID != "s1" || changed.Names[0] != "x.txt" {
		t.Fatalf("changed = %+v", changed)
	}
	hub.unwatch(ctx, fsUnwatchRequest{SessionID: "s1", WatcherID: "editor"})
	for dirs := range filesystem.sets {
		if slices.Equal(dirs, []string{"/a", "/b"}) {
			break
		}
	}
}

func TestFSWatchHubPollsDirectoriesThePushWatchCannotFollow(t *testing.T) {
	filesystem := &pushTestFS{watchTestFS: watchTestFS{entries: map[string][]base.FileEntry{}}, sets: make(chan []string, 16)}
	hub, events := newTestWatchHub(t, filesystem)
	if _, err := hub.watch(context.Background(), fsWatchRequest{SessionID: "s1", WatcherID: "tree", Dirs: []string{"/big"}}); err != nil {
		t.Fatal(err)
	}
	<-filesystem.sets
	filesystem.emit(base.WatchEvent{Dir: "/big", Unwatchable: true, Reason: "too many entries"})
	time.Sleep(50 * time.Millisecond)
	filesystem.setEntries("/big", base.FileEntry{Name: "new", Kind: base.FileFile})
	waitChanged(t, events, func(changed fsChangedDTO) bool { return changed.Dir == "/big" && len(changed.Names) == 0 })
}

func TestFSWatchHubPollsWhenTheChannelCannotWatch(t *testing.T) {
	filesystem := &watchTestFS{entries: map[string][]base.FileEntry{"/data": {{Name: "a"}}}}
	hub, events := newTestWatchHub(t, filesystem)
	status, err := hub.watch(context.Background(), fsWatchRequest{SessionID: "s1", WatcherID: "tree", Dirs: []string{"/data"}})
	if err != nil {
		t.Fatal(err)
	}
	if status.Mode != fsWatchPoll {
		t.Fatalf("initial status = %+v", status)
	}
	time.Sleep(50 * time.Millisecond)
	filesystem.setEntries("/data", base.FileEntry{Name: "a"}, base.FileEntry{Name: "b"})
	waitChanged(t, events, func(changed fsChangedDTO) bool { return changed.Dir == "/data" })
}

func TestFSWatchHubReopensAFailedWatchAndRefreshesEverything(t *testing.T) {
	filesystem := &pushTestFS{watchTestFS: watchTestFS{entries: map[string][]base.FileEntry{}}, sets: make(chan []string, 16)}
	hub, events := newTestWatchHub(t, filesystem)
	if _, err := hub.watch(context.Background(), fsWatchRequest{SessionID: "s1", WatcherID: "tree", Dirs: []string{"/a"}}); err != nil {
		t.Fatal(err)
	}
	<-filesystem.sets
	waitChanged(t, events, func(changed fsChangedDTO) bool { return changed.Dir == "/a" })
	filesystem.fail(errors.New("channel lost"))
	<-filesystem.sets
	waitChanged(t, events, func(changed fsChangedDTO) bool { return changed.Dir == "/a" && len(changed.Names) == 0 })
	filesystem.mu.Lock()
	opens := filesystem.opens
	filesystem.mu.Unlock()
	if opens != 2 {
		t.Fatalf("watch opened %d times, want a reopen after the failure", opens)
	}
}

// A subscriber that stops renewing its lease, such as a closed browser tab,
// stops the session's watch.
func TestFSWatchHubDropsExpiredSubscriptions(t *testing.T) {
	filesystem := &pushTestFS{watchTestFS: watchTestFS{entries: map[string][]base.FileEntry{}}, sets: make(chan []string, 16)}
	hub, _ := newTestWatchHub(t, filesystem)
	var clock sync.Mutex
	now := time.Now()
	hub.now = func() time.Time {
		clock.Lock()
		defer clock.Unlock()
		return now
	}
	if _, err := hub.watch(context.Background(), fsWatchRequest{SessionID: "s1", WatcherID: "tree", Dirs: []string{"/a"}}); err != nil {
		t.Fatal(err)
	}
	<-filesystem.sets
	clock.Lock()
	now = now.Add(2 * fsWatchLease)
	clock.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		hub.mu.Lock()
		remaining := len(hub.runners)
		hub.mu.Unlock()
		if remaining == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("an expired subscription kept its watch")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
