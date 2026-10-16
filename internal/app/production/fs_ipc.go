package production

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

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
		return "", ipc.NewError(ipc.CodeNotFound, "暂存文件不存在或已过期")
	}
	return resolved, nil
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
	Resume        bool   `json:"resume"`
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

func registerFSCommands(dispatcher *ipc.Dispatcher, sessions *session.Manager, staged StagedBlobResolver) error {
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
				filesystem, kind, err := filesystem(ctx, input.SessionID)
				if err != nil {
					return nil, fsIPCError(kind, err)
				}
				data, err := base64.StdEncoding.DecodeString(input.ContentBase64)
				if err != nil {
					return nil, ipc.BadParam(fmt.Errorf("文件内容不是有效的 Base64: %w", err))
				}
				return nil, fsIPCError(kind, filesystem.WriteFile(ctx, input.Path, data, input.Backup))
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
			return ipc.Register(dispatcher, "fs_upload", func(ctx context.Context, call *ipc.Call, input fsRequest) (int64, error) {
				localPath, err := resolveStagedLocalPath(ctx, staged, input.LocalPath)
				if err != nil {
					return 0, err
				}
				filesystem, kind, err := filesystem(ctx, input.SessionID)
				if err != nil {
					return 0, fsIPCError(kind, err)
				}
				value, err := fslocal.Upload(ctx, localPath, filesystem, input.RemotePath, productionTransferOptions(ctx, call, input.Resume))
				return value, fsIPCError(kind, err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fs_download", func(ctx context.Context, call *ipc.Call, input fsRequest) (int64, error) {
				localPath, err := resolveStagedLocalPath(ctx, staged, input.LocalPath)
				if err != nil {
					return 0, err
				}
				filesystem, kind, err := filesystem(ctx, input.SessionID)
				if err != nil {
					return 0, fsIPCError(kind, err)
				}
				value, err := fslocal.Download(ctx, filesystem, input.RemotePath, localPath, productionTransferOptions(ctx, call, false))
				return value, fsIPCError(kind, err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fs_pack_download", func(ctx context.Context, call *ipc.Call, input fsRequest) (int64, error) {
				localPath, err := resolveStagedLocalPath(ctx, staged, input.LocalPath)
				if err != nil {
					return 0, err
				}
				filesystem, kind, err := filesystem(ctx, input.SessionID)
				if err != nil {
					return 0, fsIPCError(kind, err)
				}
				provider, ok := filesystem.(sshPackDownloader)
				if !ok {
					return 0, terminalIPCError(base.ErrUnsupported)
				}
				taskID := ids.New()
				var last sshfs.Progress
				value, err := provider.PackDownload(ctx, input.RemotePath, localPath, sshfs.TransferOptions{Progress: func(progress sshfs.Progress) {
					last = progress
					_ = ipc.Emit(ctx, call.Events, ipc.TopicFSProgress, fslocal.Progress{TaskID: taskID, Transferred: progress.Transferred, Total: progress.Total, Done: progress.Done})
				}})
				if err != nil && ctx.Err() == nil {
					_ = ipc.Emit(ctx, call.Events, ipc.TopicFSProgress, fslocal.Progress{TaskID: taskID, Transferred: last.Transferred, Total: last.Total, Done: true, Error: err.Error()})
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

func productionTransferOptions(ctx context.Context, call *ipc.Call, resume bool) fslocal.TransferOptions {
	taskID := ids.New()
	return fslocal.TransferOptions{Resume: resume, TaskID: taskID, Progress: func(progress fslocal.Progress) {
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
