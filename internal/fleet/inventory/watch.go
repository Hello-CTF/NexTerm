package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/atomicfile"
)

const (
	defaultWatchInterval = 2 * time.Second
	watchStoreVersion    = 1
)

// SSHWatch is the persisted per-owner SSH config watch record.
type SSHWatch struct {
	OwnerID string `json:"ownerId"`
	Path    string `json:"path"`
	GroupID string `json:"groupId"`
}

func (w SSHWatch) validate() error {
	if strings.TrimSpace(w.OwnerID) == "" {
		return errors.New("ssh watch owner is required")
	}
	if strings.TrimSpace(w.Path) == "" {
		return errors.New("ssh watch path is required")
	}
	return nil
}

// WatchStore persists per-owner SSH watch records so watches can be
// restored after a restart.
type WatchStore interface {
	SaveSSHWatch(ctx context.Context, watch SSHWatch) error
	DeleteSSHWatch(ctx context.Context, ownerID string) error
	ListSSHWatches(ctx context.Context) ([]SSHWatch, error)
}

// FileWatchStore keeps watch records in a JSON document.
type FileWatchStore struct {
	path string
	mu   sync.Mutex
}

func NewFileWatchStore(path string) *FileWatchStore {
	return &FileWatchStore{path: path}
}

type sshWatchDocument struct {
	Version int        `json:"version"`
	Watches []SSHWatch `json:"watches"`
}

func (s *FileWatchStore) ListSSHWatches(context.Context) ([]SSHWatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []SSHWatch{}, nil
	}
	if err != nil {
		return nil, err
	}
	var document sshWatchDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("ssh watch store %s: %w", s.path, err)
	}
	watches := make([]SSHWatch, 0, len(document.Watches))
	seen := map[string]bool{}
	for _, watch := range document.Watches {
		if err := watch.validate(); err != nil {
			return nil, fmt.Errorf("ssh watch store %s: %w", s.path, err)
		}
		if seen[watch.OwnerID] {
			return nil, fmt.Errorf("ssh watch store %s: duplicate owner %q", s.path, watch.OwnerID)
		}
		seen[watch.OwnerID] = true
		watches = append(watches, watch)
	}
	sort.Slice(watches, func(i, j int) bool { return watches[i].OwnerID < watches[j].OwnerID })
	return watches, nil
}

func (s *FileWatchStore) SaveSSHWatch(_ context.Context, watch SSHWatch) error {
	if err := watch.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	watches, err := s.listLocked()
	if err != nil {
		return err
	}
	replaced := false
	for index := range watches {
		if watches[index].OwnerID == watch.OwnerID {
			watches[index] = watch
			replaced = true
			break
		}
	}
	if !replaced {
		watches = append(watches, watch)
	}
	return s.writeLocked(watches)
}

func (s *FileWatchStore) DeleteSSHWatch(_ context.Context, ownerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	watches, err := s.listLocked()
	if err != nil {
		return err
	}
	filtered := make([]SSHWatch, 0, len(watches))
	for _, watch := range watches {
		if watch.OwnerID != ownerID {
			filtered = append(filtered, watch)
		}
	}
	if len(filtered) == len(watches) {
		return nil
	}
	return s.writeLocked(filtered)
}

func (s *FileWatchStore) listLocked() ([]SSHWatch, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []SSHWatch{}, nil
	}
	if err != nil {
		return nil, err
	}
	var document sshWatchDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("ssh watch store %s: %w", s.path, err)
	}
	return document.Watches, nil
}

func (s *FileWatchStore) writeLocked(watches []SSHWatch) error {
	if watches == nil {
		watches = []SSHWatch{}
	}
	document := sshWatchDocument{Version: watchStoreVersion, Watches: watches}
	return atomicfile.Write(s.path, 0o600, func(writer io.Writer) error {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(document)
	})
}

// SSHWatchSyncFunc performs one synchronization pass for a watch.
type SSHWatchSyncFunc func(ctx context.Context, watch SSHWatch) error

// SSHWatchStatus is the runtime state of an owner's watch.
type SSHWatchStatus struct {
	Watch      SSHWatch
	LastSyncAt *time.Time
	LastError  string
}

// WatchManager owns per-owner SSH watch lifecycles on top of a WatchStore.
// Start persists before the first sync and rolls back when that sync fails;
// Restore relaunches persisted watches without dropping them on error;
// Shutdown stops loops but keeps persisted records for the next restore.
type WatchManager struct {
	store    WatchStore
	sync     SSHWatchSyncFunc
	interval time.Duration
	now      func() time.Time

	lifecycleMu sync.Mutex
	mu          sync.Mutex
	watchers    map[string]*sshWatchHandle
	closed      bool
}

type sshWatchHandle struct {
	watch      SSHWatch
	cancel     context.CancelFunc
	done       chan struct{}
	lastSyncAt *time.Time
	lastError  string
}

func (h *sshWatchHandle) status() SSHWatchStatus {
	return SSHWatchStatus{Watch: h.watch, LastSyncAt: h.lastSyncAt, LastError: h.lastError}
}

type WatchOption func(*WatchManager)

func WithWatchInterval(interval time.Duration) WatchOption {
	return func(manager *WatchManager) {
		if interval > 0 {
			manager.interval = interval
		}
	}
}

func WithWatchClock(now func() time.Time) WatchOption {
	return func(manager *WatchManager) {
		if now != nil {
			manager.now = now
		}
	}
}

func NewWatchManager(store WatchStore, syncFn SSHWatchSyncFunc, options ...WatchOption) (*WatchManager, error) {
	if store == nil {
		return nil, errors.New("ssh watch store is required")
	}
	if syncFn == nil {
		return nil, errors.New("ssh watch sync is required")
	}
	manager := &WatchManager{
		store: store, sync: syncFn,
		interval: defaultWatchInterval, now: time.Now,
		watchers: map[string]*sshWatchHandle{},
	}
	for _, option := range options {
		option(manager)
	}
	return manager, nil
}

// Start replaces any existing watch for the owner, persists the new record,
// and runs the first synchronously. A failed first sync rolls the record
// back so a broken watch is never persisted.
func (m *WatchManager) Start(ctx context.Context, ownerID, path, groupID string) (SSHWatchStatus, error) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	watch := SSHWatch{OwnerID: strings.TrimSpace(ownerID), Path: strings.TrimSpace(path), GroupID: strings.TrimSpace(groupID)}
	if err := watch.validate(); err != nil {
		return SSHWatchStatus{}, err
	}
	if m.isClosed() {
		return SSHWatchStatus{}, errors.New("ssh watch manager is closed")
	}
	if err := m.stopLocked(ctx, watch.OwnerID); err != nil {
		return SSHWatchStatus{}, err
	}
	if err := m.store.SaveSSHWatch(ctx, watch); err != nil {
		return SSHWatchStatus{}, err
	}
	if _, err := m.startLocked(ctx, watch, true); err != nil {
		deleteErr := m.store.DeleteSSHWatch(context.WithoutCancel(ctx), watch.OwnerID)
		return SSHWatchStatus{}, errors.Join(err, deleteErr)
	}
	status, _ := m.Status(watch.OwnerID)
	return status, nil
}

// Stop cancels the owner's loop and removes the persisted record. It is
// idempotent for owners without an active watch.
func (m *WatchManager) Stop(ctx context.Context, ownerID string) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	return m.stopLocked(ctx, strings.TrimSpace(ownerID))
}

func (m *WatchManager) stopLocked(ctx context.Context, ownerID string) error {
	m.mu.Lock()
	handle := m.watchers[ownerID]
	if handle != nil {
		delete(m.watchers, ownerID)
		handle.cancel()
	}
	m.mu.Unlock()
	var waitErr error
	if handle != nil {
		waitErr = waitSSHWatchHandle(ctx, handle)
	}
	deleteErr := m.store.DeleteSSHWatch(context.WithoutCancel(ctx), ownerID)
	return errors.Join(waitErr, deleteErr)
}

// Status reports the runtime state of the owner's active watch.
func (m *WatchManager) Status(ownerID string) (SSHWatchStatus, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	handle := m.watchers[strings.TrimSpace(ownerID)]
	if handle == nil {
		return SSHWatchStatus{}, false
	}
	return handle.status(), true
}

// Restore relaunches every persisted watch. Records whose first sync fails
// stay persisted and surface the error through Status.
func (m *WatchManager) Restore(ctx context.Context) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.isClosed() {
		return errors.New("ssh watch manager is closed")
	}
	watches, err := m.store.ListSSHWatches(ctx)
	if err != nil {
		return err
	}
	for _, watch := range watches {
		if err := watch.validate(); err != nil {
			return fmt.Errorf("persisted ssh watch: %w", err)
		}
		if _, active := m.Status(watch.OwnerID); active {
			continue
		}
		if _, err := m.startLocked(ctx, watch, false); err != nil {
			return err
		}
	}
	return nil
}

// Shutdown stops every loop. Persisted records survive so a new manager
// can Restore them.
func (m *WatchManager) Shutdown(ctx context.Context) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	handles := make([]*sshWatchHandle, 0, len(m.watchers))
	for ownerID, handle := range m.watchers {
		delete(m.watchers, ownerID)
		handle.cancel()
		handles = append(handles, handle)
	}
	m.mu.Unlock()
	var errs []error
	for _, handle := range handles {
		if err := waitSSHWatchHandle(ctx, handle); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *WatchManager) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func (m *WatchManager) startLocked(ctx context.Context, watch SSHWatch, initialSync bool) (*sshWatchHandle, error) {
	handle := &sshWatchHandle{watch: watch, done: make(chan struct{})}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	handle.cancel = cancel
	m.mu.Lock()
	m.watchers[watch.OwnerID] = handle
	m.mu.Unlock()
	if initialSync {
		if err := m.syncAndRecord(runCtx, handle); err != nil {
			m.mu.Lock()
			if m.watchers[watch.OwnerID] == handle {
				delete(m.watchers, watch.OwnerID)
			}
			m.mu.Unlock()
			cancel()
			return nil, err
		}
	}
	go m.run(runCtx, handle, !initialSync)
	return handle, nil
}

func (m *WatchManager) run(ctx context.Context, handle *sshWatchHandle, immediate bool) {
	defer close(handle.done)
	if immediate {
		_ = m.syncAndRecord(ctx, handle)
	}
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = m.syncAndRecord(ctx, handle)
		}
	}
}

func (m *WatchManager) syncAndRecord(ctx context.Context, handle *sshWatchHandle) error {
	err := m.sync(ctx, handle.watch)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.watchers[handle.watch.OwnerID] != handle {
		return err
	}
	if err != nil {
		handle.lastError = err.Error()
		return err
	}
	now := m.now()
	handle.lastSyncAt = &now
	handle.lastError = ""
	return nil
}

func waitSSHWatchHandle(ctx context.Context, handle *sshWatchHandle) error {
	select {
	case <-handle.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
