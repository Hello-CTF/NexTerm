package production

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/forward"
	"github.com/Hello-CTF/NexTerm/internal/fs/chunked"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// forwardRuntime wires the forward service's auto-start rules, opt-in
// startup recovery, chunked-upload resume, and the crash staging reaper into
// the production lifecycle. The composition root creates it, registers it as
// a lifecycle module, and drives SessionConnected from the session status
// emitter. Per-device startup recovery stays default-off: RecoverStartup only
// runs when a caller explicitly enumerates devices.
type forwardRuntime struct {
	forward  *forward.Service
	sessions *session.Manager
	store    *store.Store
	logger   *slog.Logger
	connect  func(ctx context.Context, assetID string) (string, error)

	reaper       *chunked.Scheduler
	reaperCancel context.CancelFunc
	reaperWG     sync.WaitGroup
}

type forwardRuntimeConfig struct {
	Forward  *forward.Service
	Sessions *session.Manager
	Store    *store.Store
	Logger   *slog.Logger
	// Connect establishes a session for an asset and returns its session ID.
	Connect func(ctx context.Context, assetID string) (string, error)
	// ReaperInterval and ReaperMinAge tune the crash staging reaper; a nil
	// ReapTargets disables the reaper.
	ReaperInterval time.Duration
	ReaperMinAge   time.Duration
	ReapTargets    func(ctx context.Context) ([]chunked.Target, error)
}

// defaultForwardReaperMinAge keeps the automatic reaper from treating fresh,
// in-flight upload staging as crash leftovers when the composition root does
// not configure a minimum age.
const defaultForwardReaperMinAge = 24 * time.Hour

func newForwardRuntime(config forwardRuntimeConfig) *forwardRuntime {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	runtime := &forwardRuntime{
		forward:  config.Forward,
		sessions: config.Sessions,
		store:    config.Store,
		logger:   logger,
		connect:  config.Connect,
	}
	if config.ReapTargets != nil {
		minAge := config.ReaperMinAge
		if minAge <= 0 {
			minAge = defaultForwardReaperMinAge
		}
		runtime.reaper = chunked.NewScheduler(config.ReaperInterval, minAge, config.ReapTargets, func(err error) {
			logger.Warn("chunked upload staging reaper sweep failed", "error", err)
		})
	}
	return runtime
}

// Start implements the lifecycle component: it launches the crash staging
// reaper. Startup recovery of auto-start rules is not started here — it stays
// opt-in per device through RecoverStartup.
func (r *forwardRuntime) Start(ctx context.Context) error {
	if r.reaper != nil {
		reaperCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		r.reaperCancel = cancel
		r.reaperWG.Add(1)
		go func() {
			defer r.reaperWG.Done()
			r.reaper.Run(reaperCtx)
		}()
	}
	return nil
}

// Shutdown implements the lifecycle component: it stops the reaper and waits
// for it to finish.
func (r *forwardRuntime) Shutdown(context.Context) error {
	if r.reaperCancel != nil {
		r.reaperCancel()
		r.reaperWG.Wait()
	}
	return nil
}

// SessionConnected starts the forward auto-start rules bound to the session's
// asset. Background reconnects publish connected without a user context, so
// the account user is restored from the trusted event owner while desktop
// open-access (no owner, no user) keeps working unchanged. Failures are
// logged with the session id and stay visible without stopping other rules.
func (r *forwardRuntime) SessionConnected(ctx context.Context, sessionID string) {
	if r.forward == nil {
		return
	}
	if _, err := r.forward.StartAutoStartRules(forwardRuntimeOwnerContext(ctx), sessionID); err != nil {
		r.logger.Warn("forward auto-start rules failed", "session", sessionID, "error", err)
	}
}

// forwardRuntimeOwnerContext restores the account user context from a trusted
// event owner for background reconnects; an existing user context or a bare
// desktop context passes through unchanged.
func forwardRuntimeOwnerContext(ctx context.Context) context.Context {
	if _, ok := ipc.UserIDFromContext(ctx); ok {
		return ctx
	}
	if ownerID := ipc.EventOwnerID(ctx); ownerID != "" {
		return ipc.WithUserID(ctx, ownerID)
	}
	return ctx
}

// RecoverStartup reconnects the given devices and starts their auto-start
// rules. It is opt-in and default-off: production assets are skipped and at
// most forward.MaxConcurrentRecoveryConnections device connections are made.
func (r *forwardRuntime) RecoverStartup(ctx context.Context, devices []forward.RecoveryDevice) []forward.RecoveryOutcome {
	if r.forward == nil {
		outcomes := make([]forward.RecoveryOutcome, len(devices))
		for index, device := range devices {
			outcomes[index] = forward.RecoveryOutcome{
				AssetID: device.AssetID,
				Err:     ipc.NewError(ipc.CodeUnsupported, "转发服务未配置"),
			}
		}
		return outcomes
	}
	return r.forward.RecoverAutoStartRules(ctx, devices)
}

// ConnectDeviceForRecovery returns a recovery device whose Connect resolves
// the asset through the owner-scoped store, converts it with the full
// production asset mapping (host, options, auth metadata, StoreBacked), and
// establishes a session through the session manager bound to the asset's
// owner.
func (r *forwardRuntime) ConnectDeviceForRecovery(ctx context.Context, assetID string) forward.RecoveryDevice {
	return forward.RecoveryDevice{
		AssetID: assetID,
		Connect: func(connectCtx context.Context) (string, error) {
			if r.connect != nil {
				return r.connect(connectCtx, assetID)
			}
			row, err := r.store.AssetGet(connectCtx, assetID)
			if err != nil {
				return "", err
			}
			asset, err := productionSessionAsset(row, false, row.OwnerID)
			if err != nil {
				return "", err
			}
			sessionCtx := connectCtx
			if _, ok := ipc.UserIDFromContext(sessionCtx); !ok && row.OwnerID != "" {
				sessionCtx = ipc.WithUserID(sessionCtx, row.OwnerID)
			}
			connected, err := r.sessions.ConnectFor(sessionCtx, asset)
			if err != nil {
				return "", err
			}
			return connected.ID, nil
		},
	}
}

// ResumeUploadStaging rebuilds a chunked upload's staged progress from the
// destination so a reconnected session can continue an interrupted upload
// instead of re-sending every chunk.
func (r *forwardRuntime) ResumeUploadStaging(ctx context.Context, destination base.FileSystem, upload *chunked.Upload) (chunked.ResumeStatus, error) {
	return upload.Resume(ctx, destination)
}

// ReapStaleUploadStaging sweeps the configured upload staging targets once.
func (r *forwardRuntime) ReapStaleUploadStaging(ctx context.Context) (int, error) {
	if r.reaper == nil {
		return 0, nil
	}
	return r.reaper.Sweep(ctx)
}
