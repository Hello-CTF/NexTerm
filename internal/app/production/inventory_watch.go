package production

import (
	"context"
	"errors"
	"sync"
	"time"

	fleetinventory "github.com/Hello-CTF/NexTerm/internal/fleet/inventory"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/sshconfig"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

const defaultInventoryWatchInterval = 2 * time.Second

type inventoryWatcher struct {
	path       string
	groupID    string
	cancel     context.CancelFunc
	done       chan struct{}
	lastSyncAt *int64
	lastResult *inventorySyncDTO
	lastError  string
}

type inventoryService struct {
	database      *store.Store
	runner        fleetinventory.CommandRunner
	desktop       bool
	events        ipc.Emitter
	watchInterval time.Duration
	sshLimits     sshconfig.Limits

	configureMu sync.Mutex
	mu          sync.Mutex
	watchers    map[string]*inventoryWatcher
	closed      bool
}

func newInventoryService(database *store.Store, events ipc.Emitter, desktop bool) *inventoryService {
	return &inventoryService{
		database: database, runner: fleetinventory.ExecRunner{}, desktop: desktop, events: events,
		watchInterval: defaultInventoryWatchInterval, watchers: map[string]*inventoryWatcher{},
	}
}

func (s *inventoryService) Start(context.Context) error { return nil }

func (s *inventoryService) Shutdown(ctx context.Context) error {
	s.configureMu.Lock()
	defer s.configureMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	watchers := make([]*inventoryWatcher, 0, len(s.watchers))
	for owner, watcher := range s.watchers {
		delete(s.watchers, owner)
		watcher.cancel()
		watchers = append(watchers, watcher)
	}
	s.mu.Unlock()
	return waitInventoryWatchers(ctx, watchers)
}

func waitInventoryWatchers(ctx context.Context, watchers []*inventoryWatcher) error {
	var errs []error
	for _, watcher := range watchers {
		select {
		case <-watcher.done:
		case <-ctx.Done():
			errs = append(errs, ctx.Err())
		}
	}
	return errors.Join(errs...)
}

func (s *inventoryService) watchStatus(ctx context.Context) inventorySSHWatchDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	watcher := s.watchers[ipc.OwnerID(ctx)]
	if watcher == nil {
		return inventorySSHWatchDTO{Watching: false, Path: effectiveInventorySSHPath("")}
	}
	return inventorySSHWatchDTO{
		Watching: true, Path: watcher.path, GroupID: watcher.groupID,
		LastSyncAt: watcher.lastSyncAt, LastResult: watcher.lastResult, LastError: watcher.lastError,
	}
}

func (s *inventoryService) stopWatch(ctx context.Context, ownerID string) error {
	s.configureMu.Lock()
	defer s.configureMu.Unlock()
	return s.stopWatchLocked(ctx, ownerID)
}

func (s *inventoryService) stopWatchLocked(ctx context.Context, ownerID string) error {
	s.mu.Lock()
	watcher := s.watchers[ownerID]
	if watcher != nil {
		delete(s.watchers, ownerID)
		watcher.cancel()
	}
	s.mu.Unlock()
	if watcher == nil {
		return nil
	}
	select {
	case <-watcher.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *inventoryService) startWatch(ctx context.Context, path, groupID string) (inventorySSHWatchDTO, error) {
	s.configureMu.Lock()
	defer s.configureMu.Unlock()
	ownerID := ipc.OwnerID(ctx)
	if err := s.stopWatchLocked(ctx, ownerID); err != nil {
		return inventorySSHWatchDTO{}, err
	}
	path = effectiveInventorySSHPath(path)
	result, warnings, err := s.syncSSH(ctx, path, groupID)
	if err != nil {
		return inventorySSHWatchDTO{}, err
	}
	now := time.Now().UnixMilli()
	dto := inventorySyncDTOFromStore(result, warnings)
	ownerCtx := ipc.WithUserID(context.Background(), ownerID)
	ownerCtx = ipc.WithEventOwner(ownerCtx, ownerID)
	runCtx, cancel := context.WithCancel(ownerCtx)
	watcher := &inventoryWatcher{
		path: path, groupID: result.GroupID, cancel: cancel, done: make(chan struct{}),
		lastSyncAt: &now, lastResult: &dto,
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return inventorySSHWatchDTO{}, ipc.NewError(ipc.CodeUnsupported, "清单服务已关闭")
	}
	s.watchers[ownerID] = watcher
	s.mu.Unlock()
	go s.runWatch(runCtx, ownerID, watcher)
	s.emitChanged(ctx, store.InventorySourceSSHConfig, result)
	return inventorySSHWatchDTO{
		Watching: true, Path: path, GroupID: result.GroupID,
		LastSyncAt: &now, LastResult: &dto,
	}, nil
}

func (s *inventoryService) runWatch(ctx context.Context, ownerID string, watcher *inventoryWatcher) {
	defer close(watcher.done)
	interval := s.watchInterval
	if interval <= 0 {
		interval = defaultInventoryWatchInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, warnings, err := s.syncSSH(ctx, watcher.path, watcher.groupID)
			s.mu.Lock()
			if s.watchers[ownerID] != watcher {
				s.mu.Unlock()
				return
			}
			if err != nil {
				watcher.lastError = err.Error()
				s.mu.Unlock()
				continue
			}
			now := time.Now().UnixMilli()
			dto := inventorySyncDTOFromStore(result, warnings)
			watcher.lastSyncAt = &now
			watcher.lastResult = &dto
			watcher.lastError = ""
			s.mu.Unlock()
			if inventoryResultChanged(result) {
				s.emitChanged(ctx, store.InventorySourceSSHConfig, result)
			}
		}
	}
}
