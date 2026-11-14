package production

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	// fsWatchLease is how long a subscription lives without being renewed:
	// a closed window or a lost browser tab stops costing anything after it.
	fsWatchLease = 60 * time.Second
	// fsWatchMaxDirs bounds the directories one session follows.
	fsWatchMaxDirs = 256
	// fsPollMin and fsPollMax bound how often a polled directory is listed;
	// the interval doubles while nothing changes.
	fsPollMin = 5 * time.Second
	fsPollMax = 20 * time.Second
	// fsPollMaxDirs bounds the directories one session lists per round.
	fsPollMaxDirs = 32
	// fsWatchUnsupportedRetry is how long a channel that cannot watch, such
	// as an older device agent, is polled before a watch is tried again.
	fsWatchUnsupportedRetry = 5 * time.Minute
	fsWatchRetryMin         = 2 * time.Second
	fsWatchRetryMax         = 30 * time.Second
	fsWatchTick             = time.Second
)

// Watch modes as the file manager shows them.
const (
	fsWatchPush = "push"
	fsWatchPoll = "poll"
)

type fsWatchStatusDTO struct {
	SessionID string `json:"sessionId"`
	Mode      string `json:"mode"`
	Channel   string `json:"channel"`
	Reason    string `json:"reason,omitempty"`
}

type fsChangedDTO struct {
	SessionID string   `json:"sessionId"`
	Dir       string   `json:"dir"`
	Names     []string `json:"names,omitempty"`
}

type fsWatchRequest struct {
	SessionID string   `json:"sessionId"`
	WatcherID string   `json:"watcherId"`
	Dirs      []string `json:"dirs"`
}

type fsUnwatchRequest struct {
	SessionID string `json:"sessionId"`
	WatcherID string `json:"watcherId"`
}

type fsWatchKey struct {
	owner     string
	sessionID string
}

// fsWatchHub follows the directories the file manager shows, one runner per
// session shared by every panel and editor of that session.
type fsWatchHub struct {
	filesystem func(ctx context.Context, sessionID string) (base.FileSystem, base.Transport, error)
	emit       func(ctx context.Context, topic ipc.Topic, payload any)
	logger     *slog.Logger
	now        func() time.Time
	// The timings default to the constants above; tests shorten them.
	lease, pollMin, pollMax, tick, retryMin, retryMax time.Duration

	mu      sync.Mutex
	runners map[fsWatchKey]*fsWatchRunner
	closed  bool
}

func newFSWatchHub(filesystem func(ctx context.Context, sessionID string) (base.FileSystem, base.Transport, error), emit func(ctx context.Context, topic ipc.Topic, payload any), logger *slog.Logger) *fsWatchHub {
	if logger == nil {
		logger = slog.Default()
	}
	return &fsWatchHub{
		filesystem: filesystem, emit: emit, logger: logger, now: time.Now, runners: map[fsWatchKey]*fsWatchRunner{},
		lease: fsWatchLease, pollMin: fsPollMin, pollMax: fsPollMax, tick: fsWatchTick, retryMin: fsWatchRetryMin, retryMax: fsWatchRetryMax,
	}
}

// watch replaces one subscriber's directories and renews its lease.
func (h *fsWatchHub) watch(ctx context.Context, request fsWatchRequest) (fsWatchStatusDTO, error) {
	if request.SessionID == "" || request.WatcherID == "" {
		return fsWatchStatusDTO{}, ipc.BadParam(errors.New("sessionId 和 watcherId 不能为空"))
	}
	key := fsWatchKey{owner: ipc.OwnerID(ctx), sessionID: request.SessionID}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return fsWatchStatusDTO{}, errors.New("文件监视已停止")
	}
	defer h.mu.Unlock()
	runner := h.runners[key]
	// The subscription is in place before a new runner starts, and a runner
	// only retires under h.mu, so it never misses a subscriber.
	status := func() fsWatchStatusDTO {
		if runner != nil {
			return runner.subscribe(request.WatcherID, request.Dirs, h.now().Add(h.lease))
		}
		runner = newFSWatchRunner(h, key)
		h.runners[key] = runner
		status := runner.subscribe(request.WatcherID, request.Dirs, h.now().Add(h.lease))
		go runner.run()
		return status
	}()
	return status, nil
}

// retire removes a runner whose subscriptions all ended, unless one arrived
// meanwhile; it reports whether the runner may stop.
func (h *fsWatchHub) retire(runner *fsWatchRunner) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	runner.mu.Lock()
	remaining := len(runner.subscribers)
	runner.mu.Unlock()
	if remaining > 0 {
		return false
	}
	if h.runners[runner.key] == runner {
		delete(h.runners, runner.key)
	}
	return true
}

func (h *fsWatchHub) unwatch(ctx context.Context, request fsUnwatchRequest) {
	key := fsWatchKey{owner: ipc.OwnerID(ctx), sessionID: request.SessionID}
	h.mu.Lock()
	runner := h.runners[key]
	h.mu.Unlock()
	if runner != nil {
		runner.unsubscribe(request.WatcherID)
	}
}

// transportChanged restarts the runners of the sessions served by transport,
// whose file channel just changed.
func (h *fsWatchHub) transportChanged(transport base.Transport) {
	h.mu.Lock()
	runners := make([]*fsWatchRunner, 0, len(h.runners))
	for _, runner := range h.runners {
		runners = append(runners, runner)
	}
	h.mu.Unlock()
	for _, runner := range runners {
		runner.transportChanged(transport)
	}
}

// remove drops a runner that ended, unless a newer one took its key.
func (h *fsWatchHub) remove(runner *fsWatchRunner) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.runners[runner.key] == runner {
		delete(h.runners, runner.key)
	}
}

func (h *fsWatchHub) close() {
	h.mu.Lock()
	h.closed = true
	runners := h.runners
	h.runners = map[fsWatchKey]*fsWatchRunner{}
	h.mu.Unlock()
	for _, runner := range runners {
		runner.stop()
	}
	for _, runner := range runners {
		<-runner.finished
	}
}

type fsWatchSubscriber struct {
	dirs    []string
	expires time.Time
}

type fsPollState struct {
	fingerprint string
	listed      bool
	interval    time.Duration
	next        time.Time
}

type fsWatchRunner struct {
	hub      *fsWatchHub
	key      fsWatchKey
	ctx      context.Context
	cancel   context.CancelFunc
	wake     chan struct{}
	finished chan struct{}

	mu          sync.Mutex
	subscribers map[string]fsWatchSubscriber
	// restart asks the runner to open its watch again.
	restart bool
	// failed is set by a push watch that ended.
	failed error
	// unwatchable holds the directories a push watch cannot follow.
	unwatchable map[string]string
	status      fsWatchStatusDTO
	transport   base.Transport
}

func newFSWatchRunner(hub *fsWatchHub, key fsWatchKey) *fsWatchRunner {
	ctx, cancel := context.WithCancel(ownerContext(context.Background(), key.owner))
	return &fsWatchRunner{
		hub: hub, key: key, ctx: ctx, cancel: cancel,
		wake: make(chan struct{}, 1), finished: make(chan struct{}),
		subscribers: map[string]fsWatchSubscriber{}, unwatchable: map[string]string{},
		status: fsWatchStatusDTO{SessionID: key.sessionID, Mode: fsWatchPoll},
	}
}

func (r *fsWatchRunner) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *fsWatchRunner) subscribe(id string, dirs []string, expires time.Time) fsWatchStatusDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subscribers[id] = fsWatchSubscriber{dirs: append([]string(nil), dirs...), expires: expires}
	r.signal()
	return r.status
}

func (r *fsWatchRunner) unsubscribe(id string) {
	r.mu.Lock()
	delete(r.subscribers, id)
	r.mu.Unlock()
	r.signal()
}

func (r *fsWatchRunner) transportChanged(transport base.Transport) {
	r.mu.Lock()
	if r.transport == transport {
		r.restart = true
	}
	r.mu.Unlock()
	r.signal()
}

func (r *fsWatchRunner) stop() {
	r.cancel()
}

// dirs returns the union of the live subscriptions, dropping expired ones,
// and whether any subscriber is left.
func (r *fsWatchRunner) dirs(now time.Time) ([]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]struct{}{}
	var union []string
	for id, subscriber := range r.subscribers {
		if now.After(subscriber.expires) {
			delete(r.subscribers, id)
			continue
		}
		for _, dir := range subscriber.dirs {
			if _, ok := seen[dir]; ok || dir == "" || len(union) >= fsWatchMaxDirs {
				continue
			}
			seen[dir] = struct{}{}
			union = append(union, dir)
		}
	}
	sort.Strings(union)
	return union, len(r.subscribers) > 0
}

func (r *fsWatchRunner) setStatus(status fsWatchStatusDTO) {
	status.SessionID = r.key.sessionID
	r.mu.Lock()
	changed := r.status != status
	r.status = status
	r.mu.Unlock()
	if changed {
		r.hub.emit(r.ctx, ipc.TopicFSWatch, status)
	}
}

func (r *fsWatchRunner) changed(dir string, names []string) {
	r.hub.emit(r.ctx, ipc.TopicFSChanged, fsChangedDTO{SessionID: r.key.sessionID, Dir: dir, Names: names})
}

func (r *fsWatchRunner) run() {
	defer close(r.finished)
	defer r.hub.remove(r)
	defer r.cancel()
	var (
		handle    base.WatchHandle
		stopWatch context.CancelFunc
		current   []string
		retryAt   time.Time
		retry     = r.hub.retryMin
		polls     = map[string]*fsPollState{}
	)
	closeWatch := func() {
		if handle != nil {
			stopWatch()
			_ = handle.Close()
			handle = nil
		}
	}
	defer closeWatch()
	ticker := time.NewTicker(r.hub.tick)
	defer ticker.Stop()
	for {
		now := r.hub.now()
		dirs, live := r.dirs(now)
		if !live {
			if r.hub.retire(r) {
				return
			}
			continue
		}
		r.mu.Lock()
		restart, failed := r.restart, r.failed
		r.restart, r.failed = false, nil
		r.mu.Unlock()
		if handle != nil && (restart || failed != nil) {
			closeWatch()
			if failed != nil {
				retryAt = now.Add(retry)
				retry = min(retry*2, r.hub.retryMax)
			} else {
				retryAt = time.Time{}
			}
			r.mu.Lock()
			r.unwatchable = map[string]string{}
			r.mu.Unlock()
		} else if restart {
			retryAt = time.Time{}
		}
		if handle == nil && !now.Before(retryAt) {
			opened, cancel, err := r.open()
			switch {
			case err == nil:
				handle, stopWatch = opened, cancel
				current = nil
				retry = r.hub.retryMin
				// Changes made while nothing was watching are unknown.
				for _, dir := range dirs {
					r.changed(dir, nil)
				}
			case errors.Is(err, base.ErrUnsupported):
				retryAt = now.Add(fsWatchUnsupportedRetry)
			default:
				retryAt = now.Add(retry)
				retry = min(retry*2, r.hub.retryMax)
			}
		}
		if handle != nil && !equalStrings(current, dirs) {
			if err := handle.Set(dirs); err != nil {
				r.mu.Lock()
				r.failed = err
				r.mu.Unlock()
				continue
			}
			current = dirs
		}
		r.poll(now, dirs, handle != nil, polls)
		select {
		case <-r.ctx.Done():
			return
		case <-r.wake:
		case <-ticker.C:
		}
	}
}

// open starts a push watch, or reports why the session is polled.
func (r *fsWatchRunner) open() (base.WatchHandle, context.CancelFunc, error) {
	filesystem, transport, err := r.hub.filesystem(r.ctx, r.key.sessionID)
	r.mu.Lock()
	r.transport = transport
	r.mu.Unlock()
	if err != nil {
		r.setStatus(fsWatchStatusDTO{Mode: fsWatchPoll, Reason: err.Error()})
		return nil, nil, err
	}
	channel, reason := fileChannelStatus(r.ctx, filesystem, transport)
	watcher, ok := filesystem.(base.FileWatcher)
	if !ok {
		if reason == "" {
			reason = "该文件通道不支持实时监视，改为定时刷新"
		}
		r.setStatus(fsWatchStatusDTO{Mode: fsWatchPoll, Channel: channel, Reason: reason})
		return nil, nil, base.ErrUnsupported
	}
	ctx, cancel := context.WithCancel(r.ctx)
	handle, err := watcher.Watch(ctx, func(event base.WatchEvent) {
		if event.Unwatchable {
			r.mu.Lock()
			r.unwatchable[event.Dir] = event.Reason
			r.mu.Unlock()
			return
		}
		r.changed(event.Dir, event.Names)
	}, func(err error) {
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("file watch ended")
		}
		r.mu.Lock()
		r.failed = err
		r.mu.Unlock()
		r.signal()
	})
	if err != nil {
		cancel()
		if !errors.Is(err, base.ErrUnsupported) {
			r.hub.logger.Debug("file watch failed", "session", r.key.sessionID, "error", err)
		}
		r.setStatus(fsWatchStatusDTO{Mode: fsWatchPoll, Channel: channel, Reason: fmt.Sprintf("实时监视不可用，改为定时刷新：%v", err)})
		return nil, nil, err
	}
	r.setStatus(fsWatchStatusDTO{Mode: fsWatchPush, Channel: channel, Reason: reason})
	return handle, cancel, nil
}

// poll lists the directories a push watch does not cover when they are due
// and reports the ones whose listing changed.
func (r *fsWatchRunner) poll(now time.Time, dirs []string, pushing bool, polls map[string]*fsPollState) {
	wanted := map[string]struct{}{}
	r.mu.Lock()
	for _, dir := range dirs {
		if _, unwatchable := r.unwatchable[dir]; !pushing || unwatchable {
			wanted[dir] = struct{}{}
		}
	}
	r.mu.Unlock()
	for dir := range polls {
		if _, ok := wanted[dir]; !ok {
			delete(polls, dir)
		}
	}
	due := make([]string, 0, len(wanted))
	for dir := range wanted {
		state := polls[dir]
		if state == nil {
			state = &fsPollState{interval: r.hub.pollMin, next: now}
			polls[dir] = state
		}
		if !now.Before(state.next) {
			due = append(due, dir)
		}
	}
	if len(due) == 0 {
		return
	}
	sort.Slice(due, func(i, j int) bool { return polls[due[i]].next.Before(polls[due[j]].next) })
	if len(due) > fsPollMaxDirs {
		due = due[:fsPollMaxDirs]
	}
	filesystem, _, err := r.hub.filesystem(r.ctx, r.key.sessionID)
	if err != nil {
		for _, dir := range due {
			polls[dir].next = now.Add(r.hub.pollMax)
		}
		return
	}
	for _, dir := range due {
		state := polls[dir]
		listCtx, cancel := context.WithTimeout(r.ctx, 15*time.Second)
		entries, err := filesystem.List(listCtx, dir)
		cancel()
		if r.ctx.Err() != nil {
			return
		}
		fingerprint := "error"
		if err == nil {
			fingerprint = listingFingerprint(entries)
		}
		if state.listed && fingerprint != state.fingerprint {
			r.changed(dir, nil)
			state.interval = r.hub.pollMin
		} else if state.listed {
			state.interval = min(state.interval*2, r.hub.pollMax)
		}
		state.fingerprint, state.listed = fingerprint, true
		state.next = r.hub.now().Add(state.interval)
	}
}

func listingFingerprint(entries []base.FileEntry) string {
	sorted := append([]base.FileEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	hash := sha256.New()
	for _, entry := range sorted {
		hash.Write([]byte(entry.Name))
		hash.Write([]byte{0})
		hash.Write([]byte(entry.Kind))
		hash.Write([]byte{0})
		hash.Write([]byte(strconv.FormatInt(entry.Size, 10)))
		hash.Write([]byte{0})
		hash.Write([]byte(strconv.FormatInt(entry.ModTime.UnixNano(), 10)))
		hash.Write([]byte{0})
		hash.Write([]byte(entry.Mode.String()))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// fileChannelStatus names the channel serving filesystem and, for an SSH
// host that fell back to SFTP, why.
func fileChannelStatus(ctx context.Context, filesystem base.FileSystem, transport base.Transport) (string, string) {
	channel := ""
	if reporter, ok := filesystem.(base.FileChannelReporter); ok {
		channel = reporter.FileChannel()
	}
	if status, ok := transport.(interface {
		FileChannelStatus(context.Context) (string, string)
	}); ok {
		_, reason := status.FileChannelStatus(ctx)
		if channel != base.FileChannelSFTP {
			reason = ""
		}
		return channel, reason
	}
	return channel, ""
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
