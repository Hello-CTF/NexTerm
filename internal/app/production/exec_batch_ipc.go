package production

import (
	"context"
	"errors"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type execBatchIPCService struct {
	batches *execbatch.Service
	history execBatchHistoryReader
}

type execBatchCancelRequest struct {
	BatchID string `json:"batchId"`
}

type execBatchHistoryGetRequest struct {
	BatchID string `json:"batchId"`
	Scope   string `json:"scope,omitempty"`
}

type execBatchAssetReader interface {
	AssetGetLive(context.Context, string) (store.AssetRow, error)
}

type execBatchHistoryReader interface {
	ExecBatchHistoryList(context.Context, store.ExecBatchHistoryQuery) (store.ExecBatchHistoryPage, error)
	ExecBatchHistoryGet(context.Context, string, ...string) (store.ExecBatchHistory, error)
}

func newExecBatchService(sessions *session.Manager, assets execBatchAssetReader, auditor execbatch.Auditor, history ...execbatch.History) *execbatch.Service {
	var historyStore execbatch.History
	if len(history) > 0 {
		historyStore = history[0]
	}
	return execbatch.New(execbatch.Config{
		Resolver: execBatchResolver{sessions: sessions, assets: assets},
		Auditor:  auditor,
		History:  historyStore,
	})
}

func newExecBatchIPCService(sessions *session.Manager, assets execBatchAssetReader, auditor execbatch.Auditor, history ...execBatchHistoryReader) *execBatchIPCService {
	var historyStore execBatchHistoryReader
	var historyWriter execbatch.History
	if len(history) > 0 {
		historyStore = history[0]
		historyWriter, _ = history[0].(execbatch.History)
	}
	return &execBatchIPCService{
		batches: newExecBatchService(sessions, assets, auditor, historyWriter),
		history: historyStore,
	}
}

func registerExecBatchCommands(dispatcher *ipc.Dispatcher, batches *execbatch.Service, history *store.Store) error {
	var reader execBatchHistoryReader
	if history != nil {
		reader = history
	}
	if err := (&execBatchIPCService{batches: batches, history: reader}).register(dispatcher); err != nil {
		return err
	}
	return registerServiceCommands(dispatcher, batches)
}

func (s *execBatchIPCService) register(dispatcher *ipc.Dispatcher) error {
	if err := ipc.Register(dispatcher, "exec_batch_execute", func(ctx context.Context, _ *ipc.Call, input execbatch.Request) (execbatch.Batch, error) {
		ctx = productionSessionRequestContext(ctx)
		batch, err := s.batches.Execute(ctx, ipc.OwnerID(ctx), input)
		if err != nil {
			return execbatch.Batch{}, execBatchIPCError(err)
		}
		return batch, nil
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "exec_batch_cancel", func(ctx context.Context, _ *ipc.Call, input execBatchCancelRequest) (execbatch.CancelResult, error) {
		result, err := s.batches.Cancel(ctx, ipc.OwnerID(ctx), input.BatchID)
		if err != nil {
			return execbatch.CancelResult{}, execBatchIPCError(err)
		}
		return result, nil
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "exec_batch_history_list", func(ctx context.Context, _ *ipc.Call, input store.ExecBatchHistoryQuery) (store.ExecBatchHistoryPage, error) {
		if s.history == nil {
			return store.ExecBatchHistoryPage{}, ipc.NewError(ipc.CodeUnsupported, "批量执行历史不可用")
		}
		return s.history.ExecBatchHistoryList(ctx, input)
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "exec_batch_history_get", func(ctx context.Context, _ *ipc.Call, input execBatchHistoryGetRequest) (store.ExecBatchHistory, error) {
		if s.history == nil {
			return store.ExecBatchHistory{}, ipc.NewError(ipc.CodeUnsupported, "批量执行历史不可用")
		}
		return s.history.ExecBatchHistoryGet(ctx, input.BatchID, input.Scope)
	})
}

type execBatchResolver struct {
	sessions *session.Manager
	assets   execBatchAssetReader
}

func (r execBatchResolver) Resolve(ctx context.Context, sessionID string) (execbatch.Target, error) {
	if r.sessions == nil {
		return execbatch.Target{}, fmt.Errorf("%w: session manager unavailable", execbatch.ErrUnsupported)
	}
	connected, err := r.sessions.SessionFor(ctx, sessionID)
	if err != nil {
		return execbatch.Target{}, err
	}
	info := connected.Info()
	if info.Kind != session.KindSSH {
		return execbatch.Target{}, fmt.Errorf("%w: %s is %s", execbatch.ErrUnsupported, sessionID, info.Kind)
	}
	if info.Status != session.StatusConnected {
		return execbatch.Target{}, fmt.Errorf("%w: %s is %s", execbatch.ErrDisconnected, sessionID, info.Status)
	}
	transport, err := r.sessions.TransportFor(ctx, sessionID)
	if err != nil {
		return execbatch.Target{}, err
	}
	if !transport.IsAlive() {
		return execbatch.Target{}, fmt.Errorf("%w: %s transport is not alive", execbatch.ErrDisconnected, sessionID)
	}
	target := execbatch.Target{
		SessionID: info.ID, SessionName: info.Name, AssetID: info.AssetID,
		Transport: transport, Generation: transport.Generation(),
	}
	if info.AssetID == "" {
		return target, nil
	}
	if r.assets == nil {
		return execbatch.Target{}, fmt.Errorf("resolve asset environment for %s: asset reader unavailable", sessionID)
	}
	asset, err := r.assets.AssetGetLive(ctx, info.AssetID)
	if err != nil {
		return execbatch.Target{}, fmt.Errorf("resolve asset environment for %s: %w", sessionID, err)
	}
	target.Environment = store.AssetEnvironment(asset)
	if asset.Host != nil {
		target.Hostname = *asset.Host
	}
	return target, nil
}

func execBatchIPCError(err error) error {
	if err == nil {
		return nil
	}
	var confirmation *execbatch.ConfirmationRequiredError
	if errors.As(err, &confirmation) {
		return ipc.WrapError(ipc.CodeNeedsConfirm, "生产环境批量执行需要显式确认", err).WithDetail(confirmation.IPCDetail())
	}
	cause := err
	message := err.Error()
	var preflight *execbatch.PreflightError
	if errors.As(err, &preflight) {
		message = "批量执行预检失败，未执行任何命令: " + preflight.Error()
		if len(preflight.Failures) > 0 {
			cause = preflight.Failures[0].Err
		}
	}
	var appErr *ipc.Error
	if errors.As(cause, &appErr) {
		return ipc.WrapError(appErr.Code, message, err)
	}
	code := ipc.CodeInternal
	switch {
	case errors.Is(cause, execbatch.ErrStaleProductionConfirmation):
		code = ipc.Code("conflict")
		message = "生产确认已 stale，请重新 preflight：目标或环境已变化，请移除旧确认后重试"
	case errors.Is(cause, execbatch.ErrInvalid):
		code = ipc.CodeBadParam
	case errors.Is(cause, execbatch.ErrConfirmationRequired):
		code = ipc.CodeNeedsConfirm
	case errors.Is(cause, execbatch.ErrBatchNotFound), errors.Is(cause, session.ErrSessionNotFound):
		code = ipc.CodeNotFound
	case errors.Is(cause, execbatch.ErrBatchConflict), errors.Is(cause, execbatch.ErrPendingCancelLimit):
		code = ipc.Code("conflict")
	case errors.Is(cause, execbatch.ErrForbidden), errors.Is(cause, session.ErrForbidden):
		code = ipc.CodeForbidden
	case errors.Is(cause, execbatch.ErrUnsupported), errors.Is(cause, session.ErrUnsupported):
		code = ipc.CodeUnsupported
	case errors.Is(cause, execbatch.ErrDisconnected), errors.Is(cause, session.ErrDisconnected), errors.Is(cause, session.ErrSessionClosed):
		code = ipc.CodeDisconnected
	case errors.Is(cause, context.DeadlineExceeded):
		code = ipc.CodeTimeout
	default:
		return ipc.NormalizeError(err)
	}
	return ipc.WrapError(code, message, err)
}
