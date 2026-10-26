package production

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// mkdirPrivate creates dir exclusively with owner-only permissions. Backends that implement base.PrivateDirCreator
// guarantee exclusivity atomically. Other backends (the device bridge) get a refuse-if-present check, a single-level
// Mkdir, Chmod 0700 and a verification through the parent listing; callers pass an unguessable name, so the window
// between the presence check and Mkdir cannot be targeted.
func mkdirPrivate(ctx context.Context, filesystem base.FileSystem, dir string) error {
	if dir == "" || !path.IsAbs(dir) || path.Clean(dir) != dir || dir == "/" {
		return ipc.BadParam(fmt.Errorf("私有目录路径必须是规范的绝对路径: %q", dir))
	}
	if creator, ok := filesystem.(base.PrivateDirCreator); ok {
		return creator.MkdirPrivate(ctx, dir)
	}
	exists, err := filesystem.Exists(ctx, dir)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%s: %w", dir, fs.ErrExist)
	}
	parent, name := path.Dir(dir), path.Base(dir)
	parentExists, err := filesystem.Exists(ctx, parent)
	if err != nil {
		return err
	}
	if !parentExists {
		return fmt.Errorf("%s: %w", parent, fs.ErrNotExist)
	}
	if err := filesystem.Mkdir(ctx, dir); err != nil {
		return err
	}
	if err := filesystem.Chmod(ctx, dir, 0o700); err != nil {
		return err
	}
	entries, err := filesystem.List(ctx, parent)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name != name {
			continue
		}
		if entry.Kind == base.FileDirectory && entry.Mode.Perm() == 0o700 {
			return nil
		}
		break
	}
	return fmt.Errorf("%s: %w", dir, base.ErrNotPrivateDir)
}

// privateDirIPCError keeps an occupied or unverifiable private directory visible as its own reason.
func privateDirIPCError(kind string, err error) error {
	if errors.Is(err, fs.ErrExist) {
		return ipc.WrapError(ipc.Code("conflict"), "临时目录已存在，已拒绝复用: "+err.Error(), err).WithDetail(map[string]any{
			"resource": "path", "reason": "exists",
		})
	}
	if errors.Is(err, base.ErrNotPrivateDir) {
		return ipc.WrapError(ipc.Code("conflict"), "临时目录不是仅限当前用户访问的真实目录: "+err.Error(), err).WithDetail(map[string]any{
			"resource": "path", "reason": "not_private",
		})
	}
	return fsIPCError(kind, err)
}
