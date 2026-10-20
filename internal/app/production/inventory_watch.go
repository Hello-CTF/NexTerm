package production

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/docker"
	fleetinventory "github.com/Hello-CTF/NexTerm/internal/fleet/inventory"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/sshconfig"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

const (
	defaultInventoryWatchInterval = 2 * time.Second
	inventoryKubeconfigEnv        = "NEXTERM_INVENTORY_KUBECONFIG"
	inventoryWatchStoreFileName   = "inventory-ssh-watches.json"
	inventoryDefaultOwnerID       = "default"
)

// inventoryOwnerID maps the ownerless desktop context onto a stable watch
// owner; server sessions always carry a real owner.
func inventoryOwnerID(ctx context.Context) string {
	if ownerID := ipc.OwnerID(ctx); ownerID != "" {
		return ownerID
	}
	return inventoryDefaultOwnerID
}

// inventorySessionResolver resolves owner-scoped managed sessions.
type inventorySessionResolver interface {
	SessionFor(ctx context.Context, sessionID string) (*session.Session, error)
}

// inventoryDockerExecutor runs container exec through an authorized
// session's Docker backend.
type inventoryDockerExecutor interface {
	Exec(ctx context.Context, request docker.ExecRequest) (docker.ExecResult, error)
}

type inventoryServiceOption func(*inventoryService)

func withInventoryWatchInterval(interval time.Duration) inventoryServiceOption {
	return func(service *inventoryService) {
		if interval > 0 {
			service.watchInterval = interval
		}
	}
}

func withInventoryDockerAccess(sessions inventorySessionResolver, docker inventoryDockerExecutor) inventoryServiceOption {
	return func(service *inventoryService) {
		service.sessions = sessions
		service.docker = docker
	}
}

type inventoryService struct {
	database       *store.Store
	runner         fleetinventory.CommandRunner
	desktop        bool
	events         ipc.Emitter
	watchInterval  time.Duration
	sshLimits      sshconfig.Limits
	kubeconfigGate fleetinventory.KubeconfigAuthorizer
	sessions       inventorySessionResolver
	docker         inventoryDockerExecutor

	configureMu   sync.Mutex
	mu            sync.Mutex
	watchManager  *fleetinventory.WatchManager // nil when watchStoreErr is set
	watchStoreErr error
	watchResults  map[string]inventorySyncDTO
	closed        bool

	execMu      sync.Mutex
	execCancels map[string]*inventoryExecHandle // ownerID → in-flight exec cancel
}

// inventoryExecHandle tracks one in-flight target exec so the owner can
// cancel it through the cancel action.
type inventoryExecHandle struct{ cancel context.CancelFunc }

func newInventoryService(database *store.Store, events ipc.Emitter, desktop bool, dataDir string, options ...inventoryServiceOption) *inventoryService {
	service := &inventoryService{
		database: database, runner: fleetinventory.ExecRunner{}, desktop: desktop, events: events,
		watchInterval:  defaultInventoryWatchInterval,
		kubeconfigGate: inventoryKubeconfigGate(desktop),
		watchResults:   map[string]inventorySyncDTO{},
		execCancels:    map[string]*inventoryExecHandle{},
	}
	for _, option := range options {
		option(service)
	}
	watchStore, err := inventoryWatchStore(desktop, dataDir)
	if err != nil {
		service.watchStoreErr = err
		return service
	}
	manager, err := fleetinventory.NewWatchManager(
		watchStore, service.syncSSHWatch, fleetinventory.WithWatchInterval(service.watchInterval),
	)
	if err != nil {
		service.watchStoreErr = err
		return service
	}
	service.watchManager = manager
	return service
}

// inventoryKubeconfigGate keeps desktop behavior unchanged while the server
// only touches its local kubeconfig after an explicit opt-in.
func inventoryKubeconfigGate(desktop bool) fleetinventory.KubeconfigAuthorizer {
	if desktop {
		return fleetinventory.KubeconfigAllowed(true)
	}
	value := os.Getenv(inventoryKubeconfigEnv)
	return fleetinventory.KubeconfigAllowed(value == "1" || strings.EqualFold(value, "true"))
}

// inventoryWatchStore persists per-owner SSH watches on the server data dir;
// desktop keeps watches in memory so persistence stays a server capability.
// A server without a resolvable data dir is a visible error, never a silent
// in-memory fallback.
func inventoryWatchStore(desktop bool, dataDir string) (fleetinventory.WatchStore, error) {
	if desktop {
		return fleetinventory.NewMemoryWatchStore(), nil
	}
	if strings.TrimSpace(dataDir) == "" {
		return nil, ipc.NewError(ipc.CodeUnsupported, "服务端模式缺少数据目录，无法持久化 SSH 清单 watch")
	}
	return fleetinventory.NewFileWatchStore(filepath.Join(dataDir, inventoryWatchStoreFileName)), nil
}

// syncSSHWatch runs one SSH config synchronization for a persisted watch and
// carries the resolved inventory group back onto the record.
func (s *inventoryService) syncSSHWatch(ctx context.Context, watch fleetinventory.SSHWatch) (fleetinventory.SSHWatch, error) {
	if watch.OwnerID != inventoryDefaultOwnerID {
		ctx = ipc.WithUserID(ctx, watch.OwnerID)
		ctx = ipc.WithEventOwner(ctx, watch.OwnerID)
	}
	result, warnings, err := s.syncSSH(ctx, watch.Path, watch.GroupID)
	if err != nil {
		return watch, err
	}
	dto := inventorySyncDTOFromStore(result, warnings)
	s.mu.Lock()
	s.watchResults[watch.OwnerID] = dto
	s.mu.Unlock()
	if inventoryResultChanged(result) {
		s.emitChanged(ctx, store.InventorySourceSSHConfig, result)
	}
	watch.GroupID = result.GroupID
	return watch, nil
}

// Start restores the persisted per-owner SSH watches. Without a resolvable
// server data dir there is nothing to restore; enabling a watch then fails
// visibly through startWatch.
func (s *inventoryService) Start(ctx context.Context) error {
	s.configureMu.Lock()
	defer s.configureMu.Unlock()
	if s.watchStoreErr != nil {
		return nil
	}
	return s.watchManager.Restore(ctx)
}

func (s *inventoryService) Shutdown(ctx context.Context) error {
	s.configureMu.Lock()
	defer s.configureMu.Unlock()
	s.closed = true
	if s.watchManager == nil {
		return nil
	}
	err := s.watchManager.Shutdown(ctx)
	s.mu.Lock()
	s.watchResults = map[string]inventorySyncDTO{}
	s.mu.Unlock()
	return err
}

func (s *inventoryService) watchStatus(ctx context.Context) inventorySSHWatchDTO {
	ownerID := inventoryOwnerID(ctx)
	if s.watchManager == nil {
		return inventorySSHWatchDTO{Watching: false, Path: effectiveInventorySSHPath("")}
	}
	status, active := s.watchManager.Status(ownerID)
	if !active {
		return inventorySSHWatchDTO{Watching: false, Path: effectiveInventorySSHPath("")}
	}
	dto := inventorySSHWatchDTO{
		Watching: true, Path: status.Watch.Path, GroupID: status.Watch.GroupID, LastError: status.LastError,
	}
	if status.LastSyncAt != nil {
		millis := status.LastSyncAt.UnixMilli()
		dto.LastSyncAt = &millis
	}
	s.mu.Lock()
	if result, exists := s.watchResults[ownerID]; exists {
		dto.LastResult = &result
	}
	s.mu.Unlock()
	return dto
}

func (s *inventoryService) stopWatch(ctx context.Context, ownerID string) error {
	s.configureMu.Lock()
	defer s.configureMu.Unlock()
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		ownerID = inventoryDefaultOwnerID
	}
	if s.watchManager == nil {
		return nil
	}
	if err := s.watchManager.Stop(ctx, ownerID); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.watchResults, ownerID)
	s.mu.Unlock()
	return nil
}

func (s *inventoryService) startWatch(ctx context.Context, path, groupID string) (inventorySSHWatchDTO, error) {
	s.configureMu.Lock()
	defer s.configureMu.Unlock()
	if s.closed {
		return inventorySSHWatchDTO{}, ipc.NewError(ipc.CodeUnsupported, "清单服务已关闭")
	}
	if s.watchStoreErr != nil {
		return inventorySSHWatchDTO{}, s.watchStoreErr
	}
	path = effectiveInventorySSHPath(path)
	if _, err := s.watchManager.Start(ctx, inventoryOwnerID(ctx), path, groupID); err != nil {
		return inventorySSHWatchDTO{}, err
	}
	return s.watchStatus(ctx), nil
}
