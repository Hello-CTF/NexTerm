package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	// MaxWatchDirs bounds the directories one watch holds; the rest are
	// reported unwatchable so the caller polls them.
	MaxWatchDirs = 256
	// watchBatch is how long changes collect before they are delivered, so a
	// burst of writes arrives as one event per directory.
	watchBatch = 200 * time.Millisecond
	// maxWatchNames bounds the names one event lists; past it the event asks
	// for the whole directory.
	maxWatchNames = 64
)

// Watch watches directories without recursion through the platform
// notification API (inotify, kqueue or ReadDirectoryChangesW).
func (f *FileSystem) Watch(ctx context.Context, sink func(base.WatchEvent), done func(error)) (base.WatchHandle, error) {
	notify, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("start file watch: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	w := &dirWatch{
		notify: notify, sink: sink, cancel: cancel,
		dirs: map[string][]string{}, pending: map[string]map[string]struct{}{},
		finished: make(chan struct{}),
	}
	go w.run(ctx, done)
	return w, nil
}

type dirWatch struct {
	notify *fsnotify.Watcher
	sink   func(base.WatchEvent)
	cancel context.CancelFunc

	mu sync.Mutex
	// dirs maps a watched real directory to the paths the caller named it by.
	dirs map[string][]string
	// pending holds the changed names per real directory; a nil set asks for
	// the whole directory.
	pending  map[string]map[string]struct{}
	closed   bool
	finished chan struct{}
}

func (w *dirWatch) Set(requested []string) error {
	wanted := map[string][]string{}
	var refused []base.WatchEvent
	for _, dir := range requested {
		resolved := filepath.Clean(real(dir))
		if _, ok := wanted[resolved]; !ok && len(wanted) >= MaxWatchDirs {
			refused = append(refused, base.WatchEvent{Dir: dir, Unwatchable: true, Reason: "超过监视目录数量上限"})
			continue
		}
		wanted[resolved] = append(wanted[resolved], dir)
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return errors.New("file watch closed")
	}
	for resolved := range w.dirs {
		if _, ok := wanted[resolved]; !ok {
			_ = w.notify.Remove(resolved)
			delete(w.dirs, resolved)
			delete(w.pending, resolved)
		}
	}
	for resolved, names := range wanted {
		if _, ok := w.dirs[resolved]; ok {
			w.dirs[resolved] = names
			continue
		}
		if reason := addWatch(w.notify, resolved); reason != "" {
			for _, name := range names {
				refused = append(refused, base.WatchEvent{Dir: name, Unwatchable: true, Reason: reason})
			}
			continue
		}
		w.dirs[resolved] = names
	}
	w.mu.Unlock()
	for _, event := range refused {
		w.sink(event)
	}
	return nil
}

// addWatch starts watching dir and returns why it cannot be watched, or "".
func addWatch(notify *fsnotify.Watcher, dir string) string {
	info, err := os.Stat(dir)
	if err != nil {
		return err.Error()
	}
	if !info.IsDir() {
		return "不是目录"
	}
	if kqueueEntryLimit > 0 {
		// kqueue holds a descriptor for every entry of a watched directory.
		if entries, err := os.ReadDir(dir); err == nil && len(entries) > kqueueEntryLimit {
			return "目录条目过多，改为定时刷新"
		}
	}
	if err := notify.Add(dir); err != nil {
		if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EMFILE) {
			return "已达到系统文件监视数量上限，改为定时刷新"
		}
		return err.Error()
	}
	return ""
}

func (w *dirWatch) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		<-w.finished
		return nil
	}
	w.closed = true
	w.mu.Unlock()
	w.cancel()
	<-w.finished
	return nil
}

func (w *dirWatch) run(ctx context.Context, done func(error)) {
	var failure error
	defer func() {
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()
		_ = w.notify.Close()
		close(w.finished)
		if done != nil {
			done(failure)
		}
	}()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	armed := false
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.notify.Events:
			if !ok {
				failure = errors.New("file watch ended")
				return
			}
			if w.note(event) && !armed {
				timer.Reset(watchBatch)
				armed = true
			}
		case err, ok := <-w.notify.Errors:
			if !ok {
				failure = errors.New("file watch ended")
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				w.overflow()
				if !armed {
					timer.Reset(watchBatch)
					armed = true
				}
				continue
			}
			failure = err
			return
		case <-timer.C:
			armed = false
			w.flush()
		}
	}
}

// note records one notification and reports whether it touched a watched
// directory.
func (w *dirWatch) note(event fsnotify.Event) bool {
	name := filepath.Clean(event.Name)
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.dirs[name]; ok {
		// The directory itself changed (removed or renamed): list it again.
		w.pending[name] = nil
		return true
	}
	dir := filepath.Dir(name)
	if _, ok := w.dirs[dir]; !ok {
		return false
	}
	names, seen := w.pending[dir]
	if seen && names == nil {
		return true
	}
	if names == nil {
		names = map[string]struct{}{}
		w.pending[dir] = names
	}
	if len(names) >= maxWatchNames {
		w.pending[dir] = nil
		return true
	}
	names[filepath.Base(name)] = struct{}{}
	return true
}

func (w *dirWatch) overflow() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for dir := range w.dirs {
		w.pending[dir] = nil
	}
}

func (w *dirWatch) flush() {
	w.mu.Lock()
	var events []base.WatchEvent
	for dir, names := range w.pending {
		var list []string
		if names != nil {
			list = make([]string, 0, len(names))
			for name := range names {
				list = append(list, name)
			}
			sort.Strings(list)
		}
		for _, requested := range w.dirs[dir] {
			events = append(events, base.WatchEvent{Dir: requested, Names: list, Overflow: names == nil})
		}
	}
	w.pending = map[string]map[string]struct{}{}
	w.mu.Unlock()
	sort.Slice(events, func(i, j int) bool { return events[i].Dir < events[j].Dir })
	for _, event := range events {
		w.sink(event)
	}
}

var _ base.FileWatcher = (*FileSystem)(nil)

// FileChannel names the local file system as its own channel.
func (f *FileSystem) FileChannel() string {
	return base.FileChannelLocal
}
