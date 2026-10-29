package production

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sync"
	"time"

	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	sshfs "github.com/Hello-CTF/NexTerm/internal/fs/ssh"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const fsReadDefaultMaxBytes = 8 << 20

const (
	fsRangeReadDefaultMaxBytes = 256 << 10
	fsRangeReadMaxBytes        = 4 << 20
)

// StagedBlobResolver resolves the relative path (id/name) in a blob Stage/Reserve response to a server-side staged
// file path; satisfied by *server.BlobStore and injected only in the server-side assembly. On desktop it is nil,
// and localPath values of fs_upload/fs_download etc. are real local paths chosen by the user via system dialogs, used as-is.
type StagedBlobResolver interface {
	ResolveStagedPath(rel, userID string) (string, error)
}

// resolveStagedLocalPath resolves localPath as a staged reference to a server staged file in the server-side assembly.
// No real local paths exist on the browser side, so absolute paths are refused, keeping /rpc from becoming an arbitrary
// server file read/write channel; resolution failures are treated as nonexistent, never falling back to CWD-relative paths.
func resolveStagedLocalPath(ctx context.Context, staged StagedBlobResolver, path string) (string, error) {
	if staged == nil {
		return path, nil
	}
	if filepath.IsAbs(path) {
		return "", ipc.NewError(ipc.CodeBadParam, "参数错误: Web 模式下的本地路径必须是暂存引用")
	}
	userID, _ := ipc.UserIDFromContext(ctx)
	resolved, err := staged.ResolveStagedPath(path, userID)
	if err != nil {
		return "", ipc.NewError(ipc.CodeNotFound, "暂存文件不存在或已过期").WithDetail(map[string]any{
			"resource": "local_staging", "reason": "staged_not_found",
		})
	}
	return resolved, nil
}

type fsTransferAttemptState uint8

const (
	fsTransferAttemptRunning fsTransferAttemptState = iota
	fsTransferAttemptCancelPending
	fsTransferAttemptEnded
	fsTransferAttemptRetention = 30 * time.Minute
	fsTransferExpiryRecheck    = time.Minute
)

type fsTransferAttemptKey struct {
	owner     string
	taskID    string
	attemptID string
}

type fsTransferCancellation struct {
	state     fsTransferAttemptState
	cancel    context.CancelFunc
	updatedAt time.Time
	inFlight  int
	timer     *time.Timer
	onExpire  func()
}

type fsTransferRegistry struct {
	mu       sync.Mutex
	attempts map[fsTransferAttemptKey]*fsTransferCancellation
}

func newFSTransferRegistry() *fsTransferRegistry {
	return &fsTransferRegistry{attempts: make(map[fsTransferAttemptKey]*fsTransferCancellation)}
}

func (r *fsTransferRegistry) pruneLocked(now time.Time) []func() {
	var expired []func()
	for key, entry := range r.attempts {
		if now.Sub(entry.updatedAt) < fsTransferAttemptRetention || (entry.state == fsTransferAttemptRunning && entry.inFlight > 0) {
			continue
		}
		delete(r.attempts, key)
		if entry.timer != nil {
			entry.timer.Stop()
		}
		if entry.cancel != nil {
			entry.cancel()
		}
		if entry.onExpire != nil {
			expired = append(expired, entry.onExpire)
		}
	}
	return expired
}

func runFSExpirations(expired []func()) {
	for _, expire := range expired {
		expire()
	}
}

func (r *fsTransferRegistry) scheduleLocked(key fsTransferAttemptKey, entry *fsTransferCancellation) {
	if entry.timer != nil {
		entry.timer.Stop()
	}
	delay := time.Until(entry.updatedAt.Add(fsTransferAttemptRetention))
	if delay < 0 {
		delay = 0
	}
	entry.timer = time.AfterFunc(delay, func() { r.expire(key, entry) })
}

func (r *fsTransferRegistry) expire(key fsTransferAttemptKey, entry *fsTransferCancellation) {
	r.mu.Lock()
	if r.attempts[key] != entry {
		r.mu.Unlock()
		return
	}
	if time.Since(entry.updatedAt) < fsTransferAttemptRetention {
		r.scheduleLocked(key, entry)
		r.mu.Unlock()
		return
	}
	if entry.state == fsTransferAttemptRunning && entry.inFlight > 0 {
		if entry.timer != nil {
			entry.timer.Stop()
		}
		entry.timer = time.AfterFunc(fsTransferExpiryRecheck, func() { r.expire(key, entry) })
		r.mu.Unlock()
		return
	}
	delete(r.attempts, key)
	if entry.timer != nil {
		entry.timer.Stop()
	}
	onExpire := entry.onExpire
	if entry.cancel != nil {
		entry.cancel()
	}
	r.mu.Unlock()
	if onExpire != nil {
		onExpire()
	}
}

func (r *fsTransferRegistry) touch(ctx context.Context, taskID, attemptID string) {
	key := fsTransferAttemptKey{owner: ipc.OwnerID(ctx), taskID: taskID, attemptID: attemptID}
	r.mu.Lock()
	if entry := r.attempts[key]; entry != nil && entry.state == fsTransferAttemptRunning {
		entry.updatedAt = time.Now()
		r.scheduleLocked(key, entry)
	}
	r.mu.Unlock()
}

func (r *fsTransferRegistry) enter(ctx context.Context, taskID, attemptID string) func() {
	key := fsTransferAttemptKey{owner: ipc.OwnerID(ctx), taskID: taskID, attemptID: attemptID}
	r.mu.Lock()
	if entry := r.attempts[key]; entry != nil && entry.state == fsTransferAttemptRunning {
		entry.inFlight++
		entry.updatedAt = time.Now()
		r.scheduleLocked(key, entry)
	}
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() { r.leave(ctx, taskID, attemptID) })
	}
}

func (r *fsTransferRegistry) leave(ctx context.Context, taskID, attemptID string) {
	key := fsTransferAttemptKey{owner: ipc.OwnerID(ctx), taskID: taskID, attemptID: attemptID}
	r.mu.Lock()
	if entry := r.attempts[key]; entry != nil && entry.state == fsTransferAttemptRunning && entry.inFlight > 0 {
		entry.inFlight--
		entry.updatedAt = time.Now()
		r.scheduleLocked(key, entry)
	}
	r.mu.Unlock()
}

func (r *fsTransferRegistry) close() {
	r.mu.Lock()
	for key, entry := range r.attempts {
		delete(r.attempts, key)
		if entry.timer != nil {
			entry.timer.Stop()
		}
		if entry.cancel != nil {
			entry.cancel()
		}
	}
	r.mu.Unlock()
}

func (r *fsTransferRegistry) begin(ctx context.Context, taskID, attemptID string) (context.Context, string, func(), error) {
	return r.beginWithExpiry(ctx, taskID, attemptID, nil)
}

func (r *fsTransferRegistry) beginWithExpiry(ctx context.Context, taskID, attemptID string, onExpire func()) (context.Context, string, func(), error) {
	if taskID == "" {
		taskID = ids.New()
	}
	if attemptID == "" {
		attemptID = taskID
	}
	key := fsTransferAttemptKey{owner: ipc.OwnerID(ctx), taskID: taskID, attemptID: attemptID}
	now := time.Now()
	r.mu.Lock()
	expired := r.pruneLocked(now)
	defer func() {
		r.mu.Unlock()
		runFSExpirations(expired)
	}()
	if existing := r.attempts[key]; existing != nil {
		switch existing.state {
		case fsTransferAttemptCancelPending:
			existing.state = fsTransferAttemptEnded
			existing.updatedAt = now
			r.scheduleLocked(key, existing)
			return nil, "", nil, context.Canceled
		case fsTransferAttemptRunning:
			return nil, "", nil, ipc.NewError(ipc.Code("conflict"), "文件传输任务 attempt 正在运行")
		default:
			return nil, "", nil, ipc.NewError(ipc.Code("conflict"), "文件传输任务 attempt 已结束")
		}
	}
	transferCtx, cancel := context.WithCancel(ctx)
	entry := &fsTransferCancellation{state: fsTransferAttemptRunning, cancel: cancel, updatedAt: now, onExpire: onExpire, inFlight: 1}
	r.attempts[key] = entry
	r.scheduleLocked(key, entry)
	finish := func() {
		r.mu.Lock()
		if current := r.attempts[key]; current == entry && current.state == fsTransferAttemptRunning {
			current.state = fsTransferAttemptEnded
			current.inFlight = 0
			current.updatedAt = time.Now()
			r.scheduleLocked(key, current)
		}
		r.mu.Unlock()
		cancel()
	}
	return transferCtx, taskID, finish, nil
}

func (r *fsTransferRegistry) cancelLegacyLocked(owner, taskID string, now time.Time) (bool, error) {
	found := false
	ended := false
	for key, entry := range r.attempts {
		if key.owner != owner || key.taskID != taskID {
			continue
		}
		switch entry.state {
		case fsTransferAttemptRunning:
			if entry.cancel != nil {
				entry.cancel()
			}
			found = true
		case fsTransferAttemptCancelPending:
			found = true
		default:
			ended = true
		}
	}
	if found {
		return true, nil
	}
	if ended {
		return false, nil
	}
	for other := range r.attempts {
		if other.taskID == taskID {
			return false, ipc.NewError(ipc.CodeForbidden, "不能取消其他账号的文件传输")
		}
	}
	key := fsTransferAttemptKey{owner: owner, taskID: taskID, attemptID: taskID}
	r.attempts[key] = &fsTransferCancellation{state: fsTransferAttemptCancelPending, updatedAt: now}
	r.scheduleLocked(key, r.attempts[key])
	return true, nil
}

func (r *fsTransferRegistry) cancel(ctx context.Context, taskID, attemptID string) (bool, error) {
	if taskID == "" {
		return false, ipc.BadParam(errors.New("taskId 不能为空"))
	}
	owner := ipc.OwnerID(ctx)
	now := time.Now()
	r.mu.Lock()
	expired := r.pruneLocked(now)
	defer func() {
		r.mu.Unlock()
		runFSExpirations(expired)
	}()
	if attemptID == "" {
		return r.cancelLegacyLocked(owner, taskID, now)
	}
	key := fsTransferAttemptKey{owner: owner, taskID: taskID, attemptID: attemptID}
	entry := r.attempts[key]
	if entry != nil {
		switch entry.state {
		case fsTransferAttemptRunning:
			if entry.cancel != nil {
				entry.cancel()
			}
			return true, nil
		case fsTransferAttemptCancelPending:
			return true, nil
		default:
			return false, nil
		}
	}
	for other := range r.attempts {
		if other.taskID == taskID && other.attemptID == attemptID {
			return false, ipc.NewError(ipc.CodeForbidden, "不能取消其他账号的文件传输")
		}
	}
	// A pending entry is a tombstone for a cancel RPC that wins the race with begin.
	r.attempts[key] = &fsTransferCancellation{state: fsTransferAttemptCancelPending, updatedAt: now}
	r.scheduleLocked(key, r.attempts[key])
	return true, nil
}

type fsRequest struct {
	SessionID     string `json:"sessionId"`
	Path          string `json:"path"`
	MaxBytes      int64  `json:"maxBytes"`
	Offset        int64  `json:"offset"`
	ContentBase64 string `json:"contentBase64"`
	Backup        bool   `json:"backup"`
	From          string `json:"from"`
	To            string `json:"to"`
	IsDir         bool   `json:"isDir"`
	Mode          uint32 `json:"mode"`
	Algo          string `json:"algo"`
	LocalPath     string `json:"localPath"`
	RemotePath    string `json:"remotePath"`
	TaskID        string `json:"taskId"`
	AttemptID     string `json:"attemptId"`
	Resume        bool   `json:"resume"`
}

type fsCancelRequest struct {
	TaskID    string `json:"taskId"`
	AttemptID string `json:"attemptId,omitempty"`
}

type fsEntryDTO struct {
	Name          string  `json:"name"`
	Path          string  `json:"path"`
	Kind          string  `json:"kind"`
	Size          int64   `json:"size"`
	Mode          string  `json:"mode"`
	Owner         *string `json:"owner"`
	Group         *string `json:"group"`
	Mtime         int64   `json:"mtime"`
	SymlinkTarget *string `json:"symlinkTarget"`
}

type fsRootDTO struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

type fsRootsDTO struct {
	PathStyle string      `json:"pathStyle"`
	Roots     []fsRootDTO `json:"roots"`
	HomePath  *string     `json:"homePath"`
	Supported bool        `json:"supported"`
}

type fsReadDTO struct {
	Path          string `json:"path"`
	Size          int    `json:"size"`
	ContentBase64 string `json:"contentBase64"`
}

type fsRangeReadDTO struct {
	Path          string `json:"path"`
	Offset        int64  `json:"offset"`
	Size          int64  `json:"size"`
	ContentBase64 string `json:"contentBase64"`
	Truncated     bool   `json:"truncated"`
}

type rangeReadProvider interface {
	ReadRange(context.Context, string, int64, int64) ([]byte, int64, error)
}

type sshPackDownloader interface {
	PackDownload(context.Context, string, string, sshfs.TransferOptions) (int64, error)
}

type sshExtractor interface {
	Extract(context.Context, string) (string, error)
}

func registerFSCommands(dispatcher *ipc.Dispatcher, sessions *session.Manager, staged StagedBlobResolver, uploadRegistries ...*fsChunkUploadRegistry) error {
	return registerFSCommandsWithAssets(dispatcher, sessions, staged, nil, uploadRegistries...)
}

// currentAssetOptions returns the stored options of an asset as they are now, which can differ from the snapshot a
// connected session carries when the asset was edited after connecting.
type currentAssetOptions func(ctx context.Context, assetID string) (any, bool)

func registerFSCommandsWithAssets(dispatcher *ipc.Dispatcher, sessions *session.Manager, staged StagedBlobResolver, assetOptions currentAssetOptions, uploadRegistries ...*fsChunkUploadRegistry) error {
	filesystem := func(ctx context.Context, sessionID string) (base.FileSystem, string, error) {
		transport, err := sessions.TransportFor(productionSessionRequestContext(ctx), sessionID)
		if err != nil {
			return nil, "", err
		}
		provider, ok := transport.(base.FileTransport)
		if !ok {
			return nil, transport.Kind(), base.ErrUnsupported
		}
		remote, err := provider.FileSystem(ctx)
		if err != nil {
			return nil, transport.Kind(), err
		}
		return remote, transport.Kind(), nil
	}
	transfers := newFSTransferRegistry()
	var uploads *fsChunkUploadRegistry
	if len(uploadRegistries) > 0 {
		uploads = uploadRegistries[0]
	}
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "fs_roots", func(ctx context.Context, _ *ipc.Call, input fsRequest) (fsRootsDTO, error) {
				result := fsRootsDTO{PathStyle: string(base.PathStyleUnknown), Roots: make([]fsRootDTO, 0)}
				filesystem, kind, err := filesystem(ctx, input.SessionID)
				if err != nil {
					return fsRootsDTO{}, fsIPCError(kind, err)
				}
				provider, ok := filesystem.(base.RootsProvider)
				if !ok {
					return result, nil
				}
				roots, err := provider.Roots(ctx)
				if errors.Is(err, base.ErrUnsupported) {
					return result, nil
				}
				if err != nil {
					return fsRootsDTO{}, fsIPCError(kind, err)
				}
				result.PathStyle = string(roots.PathStyle)
				if result.PathStyle == "" {
					result.PathStyle = string(base.PathStyleUnknown)
				}
				result.HomePath = nullableProductionString(roots.HomePath)
				result.Supported = roots.Supported
				result.Roots = make([]fsRootDTO, len(roots.Roots))
				for index, root := range roots.Roots {
					result.Roots[index] = fsRootDTO{Path: root.Path, Label: root.Label}
				}
				return result, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fs_list", func(ctx context.Context, _ *ipc.Call, input fsRequest) ([]fsEntryDTO, error) {
				filesystem, kind, err := filesystem(ctx, input.SessionID)
				if err != nil {
					return nil, fsIPCError(kind, err)
				}
				entries, err := filesystem.List(ctx, input.Path)
				result := make([]fsEntryDTO, len(entries))
				for index, entry := range entries {
					result[index] = productionFSEntry(entry)
				}
				return result, fsIPCError(kind, err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fs_read", func(ctx context.Context, _ *ipc.Call, input fsRequest) (fsReadDTO, error) {
				filesystem, kind, err := filesystem(ctx, input.SessionID)
				if err != nil {
					return fsReadDTO{}, fsIPCError(kind, err)
				}
				maxBytes := input.MaxBytes
				if maxBytes == 0 {
					maxBytes = fsReadDefaultMaxBytes
				}
				data, err := filesystem.ReadFile(ctx, input.Path, maxBytes)
				return fsReadDTO{Path: input.Path, Size: len(data), ContentBase64: base64.StdEncoding.EncodeToString(data)}, fsIPCError(kind, err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fs_read_range", func(ctx context.Context, _ *ipc.Call, input fsRequest) (fsRangeReadDTO, error) {
				filesystem, kind, err := filesystem(ctx, input.SessionID)
				if err != nil {
					return fsRangeReadDTO{}, fsIPCError(kind, err)
				}
				provider, ok := filesystem.(rangeReadProvider)
				if !ok {
					return fsRangeReadDTO{}, terminalIPCError(base.ErrUnsupported)
				}
				if input.Offset < 0 {
					return fsRangeReadDTO{}, ipc.BadParam(fmt.Errorf("offset 不能为负数"))
				}
				maxBytes := input.MaxBytes
				if maxBytes == 0 {
					maxBytes = fsRangeReadDefaultMaxBytes
				}
				if maxBytes < 0 || maxBytes > fsRangeReadMaxBytes {
					return fsRangeReadDTO{}, ipc.BadParam(fmt.Errorf("maxBytes 必须在 1 到 %d 字节之间", fsRangeReadMaxBytes))
				}
				data, size, err := provider.ReadRange(ctx, input.Path, input.Offset, maxBytes)
				return fsRangeReadDTO{
					Path:          input.Path,
					Offset:        input.Offset,
					Size:          size,
					ContentBase64: base64.StdEncoding.EncodeToString(data),
					Truncated:     input.Offset+int64(len(data)) < size,
				}, fsIPCError(kind, err)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "fs_write", func(ctx context.Context, _ *ipc.Call, input fsRequest) (any, error) {
				data, err := base64.StdEncoding.DecodeString(input.ContentBase64)
				if err != nil {
					return nil, ipc.BadParam(fmt.Errorf("文件内容不是有效的 Base64: %w", err))
				}
				transferCtx, _, finish, err := transfers.begin(ctx, input.TaskID, input.AttemptID)
				if err != nil {
					return nil, fsIPCError("", err)
				}
				defer finish()
				filesystem, kind, err := filesystem(transferCtx, input.SessionID)
				if err != nil {
					return nil, fsIPCError(kind, err)
				}
				return nil, fsIPCError(kind, filesystem.WriteFile(transferCtx, input.Path, data, input.Backup))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fs_mkdir", func(ctx context.Context, _ *ipc.Call, input fsRequest) (any, error) {
				filesystem, kind, err := filesystem(ctx, input.SessionID)
				if err == nil {
					err = fslocal.EnsureRemoteDir(ctx, filesystem, input.Path)
				}
				return nil, fsIPCError(kind, err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fs_mkdir_private", func(ctx context.Context, _ *ipc.Call, input fsRequest) (any, error) {
				connected, err := sessions.SessionFor(productionSessionRequestContext(ctx), input.SessionID)
				if err != nil {
					return nil, fsIPCError("", err)
				}
				filesystem, kind, err := filesystem(ctx, input.SessionID)
				if err != nil {
					return nil, fsIPCError(kind, err)
				}
				options := connected.Asset().Options
				if assetOptions != nil {
					if current, ok := assetOptions(ctx, connected.Asset().ID); ok {
						options = current
					}
				}
				return nil, privateDirIPCError(kind, mkdirPrivate(ctx, filesystem, input.Path, privateDirParentFor(options)))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fs_rename", func(ctx context.Context, _ *ipc.Call, input fsRequest) (any, error) {
				filesystem, kind, err := filesystem(ctx, input.SessionID)
				if err == nil {
					err = filesystem.Rename(ctx, input.From, input.To)
				}
				return nil, fsIPCError(kind, err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fs_delete", func(ctx context.Context, _ *ipc.Call, input fsRequest) (any, error) {
				filesystem, kind, err := filesystem(ctx, input.SessionID)
				if err == nil {
					err = filesystem.Delete(ctx, input.Path, input.IsDir)
				}
				return nil, fsIPCError(kind, err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fs_chmod", func(ctx context.Context, _ *ipc.Call, input fsRequest) (any, error) {
				filesystem, kind, err := filesystem(ctx, input.SessionID)
				if err == nil {
					err = filesystem.Chmod(ctx, input.Path, fs.FileMode(input.Mode))
				}
				return nil, fsIPCError(kind, err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fs_checksum", func(ctx context.Context, _ *ipc.Call, input fsRequest) (string, error) {
				filesystem, kind, err := filesystem(ctx, input.SessionID)
				if err != nil {
					return "", fsIPCError(kind, err)
				}
				value, err := filesystem.Checksum(ctx, input.Path, input.Algo)
				return value, fsIPCError(kind, err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fs_cancel", func(ctx context.Context, _ *ipc.Call, input fsCancelRequest) (bool, error) {
				return transfers.cancel(ctx, input.TaskID, input.AttemptID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fs_upload", func(ctx context.Context, call *ipc.Call, input fsRequest) (int64, error) {
				transferCtx, taskID, finish, err := transfers.begin(ctx, input.TaskID, input.AttemptID)
				if err != nil {
					return 0, fsIPCError("", err)
				}
				defer finish()
				localPath, err := resolveStagedLocalPath(transferCtx, staged, input.LocalPath)
				if err != nil {
					return 0, err
				}
				filesystem, kind, err := filesystem(transferCtx, input.SessionID)
				if err != nil {
					return 0, fsIPCError(kind, err)
				}
				value, err := fslocal.Upload(transferCtx, localPath, fsAtomicUploadFilesystem(filesystem, kind), input.RemotePath, productionTransferOptions(transferCtx, call, taskID, input.Resume, func() {
					transfers.touch(transferCtx, taskID, transferAttemptID(taskID, input.AttemptID))
				}))
				return value, fsIPCError(kind, err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fs_download", func(ctx context.Context, call *ipc.Call, input fsRequest) (int64, error) {
				transferCtx, taskID, finish, err := transfers.begin(ctx, input.TaskID, input.AttemptID)
				if err != nil {
					return 0, fsIPCError("", err)
				}
				defer finish()
				localPath, err := resolveStagedLocalPath(transferCtx, staged, input.LocalPath)
				if err != nil {
					return 0, err
				}
				filesystem, kind, err := filesystem(transferCtx, input.SessionID)
				if err != nil {
					return 0, fsIPCError(kind, err)
				}
				value, err := fslocal.Download(transferCtx, filesystem, input.RemotePath, localPath, productionTransferOptions(transferCtx, call, taskID, input.Resume, func() {
					transfers.touch(transferCtx, taskID, transferAttemptID(taskID, input.AttemptID))
				}))
				return value, fsIPCError(kind, err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fs_pack_download", func(ctx context.Context, call *ipc.Call, input fsRequest) (int64, error) {
				transferCtx, taskID, finish, err := transfers.begin(ctx, input.TaskID, input.AttemptID)
				if err != nil {
					return 0, fsIPCError("", err)
				}
				defer finish()
				localPath, err := resolveStagedLocalPath(transferCtx, staged, input.LocalPath)
				if err != nil {
					return 0, err
				}
				filesystem, kind, err := filesystem(transferCtx, input.SessionID)
				if err != nil {
					return 0, fsIPCError(kind, err)
				}
				provider, ok := filesystem.(sshPackDownloader)
				if !ok {
					return 0, terminalIPCError(base.ErrUnsupported)
				}
				var last sshfs.Progress
				value, err := provider.PackDownload(transferCtx, input.RemotePath, localPath, sshfs.TransferOptions{Progress: func(progress sshfs.Progress) {
					last = progress
					_ = ipc.Emit(transferCtx, call.Events, ipc.TopicFSProgress, fslocal.Progress{TaskID: taskID, Transferred: progress.Transferred, Total: progress.Total, Done: progress.Done})
				}})
				if err != nil && transferCtx.Err() == nil {
					_ = ipc.Emit(transferCtx, call.Events, ipc.TopicFSProgress, fslocal.Progress{TaskID: taskID, Transferred: last.Transferred, Total: last.Total, Done: true, Error: err.Error()})
				}
				return value, fsIPCError(kind, err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fs_extract", func(ctx context.Context, _ *ipc.Call, input fsRequest) (string, error) {
				filesystem, kind, err := filesystem(ctx, input.SessionID)
				if err != nil {
					return "", fsIPCError(kind, err)
				}
				provider, ok := filesystem.(sshExtractor)
				if !ok {
					return "", terminalIPCError(base.ErrUnsupported)
				}
				value, err := provider.Extract(ctx, input.Path)
				return value, fsIPCError(kind, err)
			})
		},
		func() error {
			return registerFSChunkedCommands(dispatcher, filesystem, transfers, uploads)
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

func fsIPCError(transportKind string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return ipc.WrapError(ipc.Code("cancelled"), "文件传输已取消", err)
	}
	if errors.Is(err, fslocal.ErrResumeMismatch) {
		return ipc.WrapError(ipc.Code("conflict"), err.Error(), err).WithDetail(map[string]any{
			"resource": "partial", "reason": "resume_mismatch",
		})
	}
	if errors.Is(err, base.ErrNoMedia) {
		return ipc.WrapError(ipc.CodeIO, err.Error(), err).WithDetail(map[string]any{"resource": "path", "reason": "no_media"})
	}
	if errors.Is(err, fs.ErrNotExist) {
		return ipc.WrapError(ipc.CodeNotFound, err.Error(), err).WithDetail(map[string]any{"resource": "path"})
	}
	if errors.Is(err, fs.ErrPermission) {
		return ipc.WrapError(ipc.CodeForbidden, err.Error(), err).WithDetail(map[string]any{"resource": "path", "reason": "permission_denied"})
	}
	classified := terminalIPCError(err)
	if errors.Is(err, session.ErrSessionNotFound) || errors.Is(err, session.ErrSessionClosed) || errors.Is(err, session.ErrDisconnected) || errors.Is(err, base.ErrDisconnected) {
		var appErr *ipc.Error
		if errors.As(classified, &appErr) {
			return appErr.WithDetail(map[string]any{"resource": "session"})
		}
	}
	var appErr *ipc.Error
	if !errors.As(classified, &appErr) || appErr.Code != ipc.CodeInternal {
		return classified
	}
	switch transportKind {
	case "ssh":
		return ipc.WrapError(ipc.CodeSFTP, err.Error(), err)
	case "winrm":
		return ipc.WrapError(ipc.CodeWinRM, err.Error(), err)
	case "device":
		return ipc.WrapError(ipc.CodeIO, err.Error(), err)
	}
	return classified
}

func transferAttemptID(taskID, attemptID string) string {
	if attemptID == "" {
		return taskID
	}
	return attemptID
}

func productionTransferOptions(ctx context.Context, call *ipc.Call, taskID string, resume bool, touch ...func()) fslocal.TransferOptions {
	if taskID == "" {
		taskID = ids.New()
	}
	return fslocal.TransferOptions{Resume: resume, TaskID: taskID, Progress: func(progress fslocal.Progress) {
		if len(touch) > 0 && touch[0] != nil {
			touch[0]()
		}
		_ = ipc.Emit(ctx, call.Events, ipc.TopicFSProgress, progress)
	}}
}

func productionFSEntry(entry base.FileEntry) fsEntryDTO {
	return fsEntryDTO{
		Name: entry.Name, Path: entry.Path, Kind: string(entry.Kind), Size: entry.Size, Mode: entry.Mode.String(),
		Owner: nullableProductionString(entry.Owner), Group: nullableProductionString(entry.Group), Mtime: entry.ModTime.UnixMilli(),
		SymlinkTarget: nullableProductionString(entry.SymlinkTarget),
	}
}
