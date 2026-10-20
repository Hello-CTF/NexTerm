package production

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fs/chunked"
	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type fsChunkUploadRequest struct {
	SessionID     string `json:"sessionId"`
	UploadID      string `json:"uploadId"`
	RemotePath    string `json:"remotePath"`
	TaskID        string `json:"taskId"`
	AttemptID     string `json:"attemptId"`
	TotalSize     int64  `json:"totalSize"`
	ChunkSize     int    `json:"chunkSize"`
	Index         int64  `json:"index"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
	ContentBase64 string `json:"contentBase64"`
}

type fsChunkUploadBeginDTO struct {
	UploadID  string `json:"uploadId"`
	ChunkSize int    `json:"chunkSize"`
}

type fsChunkUploadProgressDTO struct {
	Received int64 `json:"received"`
	Total    int64 `json:"total"`
}

type fsChunkUploadKey struct {
	owner     string
	sessionID string
	uploadID  string
}

type fsChunkUploadEntry struct {
	sessionID   string
	taskID      string
	attemptID   string
	transferCtx context.Context
	upload      *chunked.Upload
	finish      func()
	touch       func()
	enter       func() func()
}

type fsChunkUploadRegistry struct {
	mu              sync.Mutex
	entries         map[fsChunkUploadKey]*fsChunkUploadEntry
	filesystem      fsFilesystemResolver
	transfers       *fsTransferRegistry
	logger          *slog.Logger
	closed          bool
	closingSessions map[string]int
	sessionVersions map[string]uint64
	beforeRegister  func()
}

func newFSChunkUploadRegistry(loggers ...*slog.Logger) *fsChunkUploadRegistry {
	var logger *slog.Logger
	if len(loggers) > 0 {
		logger = loggers[0]
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &fsChunkUploadRegistry{
		entries: make(map[fsChunkUploadKey]*fsChunkUploadEntry), logger: logger,
		closingSessions: make(map[string]int), sessionVersions: make(map[string]uint64),
	}
}

func (r *fsChunkUploadRegistry) begin(ctx context.Context, transfers *fsTransferRegistry, input fsChunkUploadRequest) (fsChunkUploadBeginDTO, error) {
	if input.TaskID == "" || input.AttemptID == "" {
		return fsChunkUploadBeginDTO{}, ipc.BadParam(fmt.Errorf("taskId 和 attemptId 不能为空"))
	}
	chunkSize := input.ChunkSize
	if chunkSize == 0 {
		chunkSize = chunked.DefaultChunkSize
	}
	uploadID := ids.New()
	upload, err := chunked.New(uploadID, input.RemotePath, input.TotalSize, chunkSize)
	if err != nil {
		return fsChunkUploadBeginDTO{}, ipc.BadParam(err)
	}
	r.mu.Lock()
	version := r.sessionVersions[input.SessionID]
	closed := r.closed || r.closingSessions[input.SessionID] > 0
	r.mu.Unlock()
	if closed {
		return fsChunkUploadBeginDTO{}, ipc.NewError(ipc.Code("conflict"), "上传会话或注册表正在关闭")
	}
	key := fsChunkUploadKey{owner: ipc.OwnerID(ctx), sessionID: input.SessionID, uploadID: uploadID}
	var entry *fsChunkUploadEntry
	transferCtx, _, finish, err := transfers.beginWithExpiry(context.WithoutCancel(ctx), input.TaskID, input.AttemptID, func() {
		r.expireEntry(key, entry)
	})
	if err != nil {
		return fsChunkUploadBeginDTO{}, err
	}
	registered := false
	defer func() {
		if registered {
			return
		}
		if entry != nil && entry.finish != nil {
			entry.finish()
			return
		}
		finish()
	}()
	entry = &fsChunkUploadEntry{sessionID: input.SessionID, taskID: input.TaskID, attemptID: input.AttemptID, transferCtx: transferCtx, upload: upload}
	var finishOnce sync.Once
	entry.finish = func() {
		finishOnce.Do(func() {
			r.mu.Lock()
			if r.entries[key] == entry {
				delete(r.entries, key)
			}
			r.mu.Unlock()
			finish()
		})
	}
	entry.touch = func() {
		transfers.touch(transferCtx, input.TaskID, input.AttemptID)
	}
	entry.enter = func() func() {
		return transfers.enter(transferCtx, input.TaskID, input.AttemptID)
	}
	if r.beforeRegister != nil {
		r.beforeRegister()
	}
	r.mu.Lock()
	if r.closed || r.closingSessions[input.SessionID] > 0 || r.sessionVersions[input.SessionID] != version || r.entries[key] != nil {
		r.mu.Unlock()
		return fsChunkUploadBeginDTO{}, ipc.NewError(ipc.Code("conflict"), "上传会话 ID 冲突或会话正在关闭")
	}
	r.entries[key] = entry
	r.mu.Unlock()
	transfers.leave(transferCtx, input.TaskID, input.AttemptID)
	registered = true
	return fsChunkUploadBeginDTO{UploadID: uploadID, ChunkSize: chunkSize}, nil
}

func (r *fsChunkUploadRegistry) lookup(ctx context.Context, input fsChunkUploadRequest) (*fsChunkUploadEntry, error) {
	key := fsChunkUploadKey{owner: ipc.OwnerID(ctx), sessionID: input.SessionID, uploadID: input.UploadID}
	r.mu.Lock()
	entry := r.entries[key]
	closed := r.closed
	r.mu.Unlock()
	if closed || entry == nil {
		return nil, ipc.NewError(ipc.CodeNotFound, "上传会话不存在或已结束")
	}
	if entry.taskID != input.TaskID || entry.attemptID != input.AttemptID {
		return nil, ipc.NewError(ipc.Code("conflict"), "上传会话任务标识不匹配")
	}
	return entry, nil
}

func (r *fsChunkUploadRegistry) cleanupEntry(key fsChunkUploadKey, entry *fsChunkUploadEntry) error {
	if r.filesystem == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(entry.transferCtx), 10*time.Second)
	defer cancel()
	remote, kind, err := r.filesystem(ctx, key.sessionID)
	remote = fsAtomicUploadFilesystem(remote, kind)
	if err != nil {
		return err
	}
	return entry.upload.Abort(ctx, remote)
}

func (r *fsChunkUploadRegistry) expireEntry(key fsChunkUploadKey, entry *fsChunkUploadEntry) {
	if entry == nil {
		return
	}
	r.mu.Lock()
	current := r.entries[key]
	r.mu.Unlock()
	if current != entry {
		return
	}
	entry.finish()
	if err := r.cleanupEntry(key, entry); err != nil {
		r.logger.Warn("expire chunked upload cleanup failed", "session", key.sessionID, "upload", key.uploadID, "error", err)
	}
}

func (r *fsChunkUploadRegistry) closeEntries(entries map[fsChunkUploadKey]*fsChunkUploadEntry) error {
	var errs []error
	for key, entry := range entries {
		entry.finish()
		if err := r.cleanupEntry(key, entry); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *fsChunkUploadRegistry) closeSession(sessionID string) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.sessionVersions[sessionID]++
	r.closingSessions[sessionID]++
	entries := make(map[fsChunkUploadKey]*fsChunkUploadEntry)
	for key, entry := range r.entries {
		if key.sessionID == sessionID {
			entries[key] = entry
		}
	}
	r.mu.Unlock()
	err := r.closeEntries(entries)
	r.mu.Lock()
	r.closingSessions[sessionID]--
	if r.closingSessions[sessionID] <= 0 {
		delete(r.closingSessions, sessionID)
	}
	r.mu.Unlock()
	return err
}

func (r *fsChunkUploadRegistry) Close(context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	entries := r.entries
	r.entries = make(map[fsChunkUploadKey]*fsChunkUploadEntry)
	r.mu.Unlock()
	err := r.closeEntries(entries)
	if r.transfers != nil {
		r.transfers.close()
	}
	return err
}

func (r *fsChunkUploadRegistry) Start(context.Context) error { return nil }

func (r *fsChunkUploadRegistry) Shutdown(ctx context.Context) error { return r.Close(ctx) }

// Device agents are Unix-only, so their local rename atomically replaces the destination.
type atomicDeviceRenameFS struct {
	base.FileSystem
}

func (f atomicDeviceRenameFS) AtomicRename(ctx context.Context, from, to string) error {
	return f.FileSystem.Rename(ctx, from, to)
}

func fsAtomicUploadFilesystem(remote base.FileSystem, kind string) base.FileSystem {
	if kind == "device" && remote != nil {
		return atomicDeviceRenameFS{FileSystem: remote}
	}
	return remote
}

type fsFilesystemResolver func(context.Context, string) (base.FileSystem, string, error)

func registerFSChunkedCommands(dispatcher *ipc.Dispatcher, filesystem fsFilesystemResolver, transfers *fsTransferRegistry, uploads *fsChunkUploadRegistry) error {
	if uploads == nil {
		uploads = newFSChunkUploadRegistry()
	}
	uploads.filesystem = filesystem
	uploads.transfers = transfers
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "fs_upload_begin", func(ctx context.Context, _ *ipc.Call, input fsChunkUploadRequest) (fsChunkUploadBeginDTO, error) {
				_, kind, err := filesystem(ctx, input.SessionID)
				if err != nil {
					return fsChunkUploadBeginDTO{}, fsIPCError(kind, err)
				}
				result, err := uploads.begin(ctx, transfers, input)
				return result, fsIPCError(kind, err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fs_upload_chunk", func(ctx context.Context, call *ipc.Call, input fsChunkUploadRequest) (fsChunkUploadProgressDTO, error) {
				entry, err := uploads.lookup(ctx, input)
				if err != nil {
					return fsChunkUploadProgressDTO{}, err
				}
				done := entry.enter()
				defer done()
				if input.Size < 0 || input.Size > chunked.MaxChunkSize || len(input.ContentBase64) > base64.StdEncoding.EncodedLen(chunked.MaxChunkSize) {
					return fsChunkUploadProgressDTO{}, ipc.BadParam(fmt.Errorf("块大小超过 %d 字节上限", chunked.MaxChunkSize))
				}
				data, err := base64.StdEncoding.DecodeString(input.ContentBase64)
				if err != nil {
					return fsChunkUploadProgressDTO{}, ipc.BadParam(fmt.Errorf("块内容不是有效的 Base64: %w", err))
				}
				remote, kind, err := filesystem(entry.transferCtx, input.SessionID)
				remote = fsAtomicUploadFilesystem(remote, kind)
				if err != nil {
					return fsChunkUploadProgressDTO{}, fsIPCError(kind, err)
				}
				received, err := entry.upload.WriteChunk(entry.transferCtx, remote, chunked.Chunk{
					Index: input.Index, Size: input.Size, SHA256: input.SHA256, Data: data,
				})
				entry.touch()
				progress := fslocal.Progress{TaskID: input.TaskID, Transferred: received, Total: entry.upload.Total(), Done: err != nil}
				if err != nil && entry.transferCtx.Err() == nil {
					progress.Error = err.Error()
				}
				if entry.transferCtx.Err() == nil {
					_ = ipc.Emit(entry.transferCtx, call.Events, ipc.TopicFSProgress, progress)
				}
				return fsChunkUploadProgressDTO{Received: received, Total: entry.upload.Total()}, fsIPCError(kind, err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fs_upload_commit", func(ctx context.Context, call *ipc.Call, input fsChunkUploadRequest) (int64, error) {
				entry, err := uploads.lookup(ctx, input)
				if err != nil {
					return 0, err
				}
				done := entry.enter()
				defer done()
				remote, kind, err := filesystem(entry.transferCtx, input.SessionID)
				remote = fsAtomicUploadFilesystem(remote, kind)
				if err != nil {
					return 0, fsIPCError(kind, err)
				}
				committed, commitErr := entry.upload.Commit(entry.transferCtx, remote, entry.touch)
				resultErr := commitErr
				if commitErr == nil {
					entry.finish()
				} else {
					cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
					abortErr := entry.upload.Abort(cleanupCtx, remote)
					cancel()
					if abortErr == nil {
						entry.finish()
					} else {
						resultErr = errors.Join(commitErr, fmt.Errorf("清理上传临时文件失败: %w", abortErr))
					}
				}
				progress := fslocal.Progress{TaskID: input.TaskID, Transferred: committed, Total: entry.upload.Total(), Done: true}
				if resultErr != nil && entry.transferCtx.Err() == nil {
					progress.Error = resultErr.Error()
				}
				if entry.transferCtx.Err() == nil {
					_ = ipc.Emit(entry.transferCtx, call.Events, ipc.TopicFSProgress, progress)
				}
				return committed, fsIPCError(kind, resultErr)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fs_upload_abort", func(ctx context.Context, _ *ipc.Call, input fsChunkUploadRequest) (any, error) {
				entry, err := uploads.lookup(ctx, input)
				if err != nil {
					if errors.Is(err, fs.ErrNotExist) {
						return nil, nil
					}
					var appErr *ipc.Error
					if errors.As(err, &appErr) && appErr.Code == ipc.CodeNotFound {
						return nil, nil
					}
					return nil, err
				}
				done := entry.enter()
				defer done()
				remote, kind, err := filesystem(ctx, input.SessionID)
				if err != nil {
					return nil, fsIPCError(kind, err)
				}
				abortErr := entry.upload.Abort(ctx, remote)
				entry.touch()
				if abortErr == nil {
					entry.finish()
				}
				return nil, fsIPCError(kind, abortErr)
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}
