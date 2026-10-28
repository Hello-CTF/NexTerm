package production

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// privateDirParent is the host temp root under which fs_mkdir_private may create directories. Tests point it at a
// scratch directory.
var privateDirParent = "/tmp"

// privateDirPrefix is the only name prefix fs_mkdir_private accepts, so the command cannot create arbitrary paths.
const privateDirPrefix = "nexterm-paste-"

// validPrivateDir accepts only a canonical direct child of the temp root named nexterm-paste-<suffix>.
func validPrivateDir(dir string) bool {
	if dir == "" || !path.IsAbs(dir) || path.Clean(dir) != dir {
		return false
	}
	name := path.Base(dir)
	return path.Dir(dir) == privateDirParent && strings.HasPrefix(name, privateDirPrefix) && len(name) > len(privateDirPrefix)
}

// mkdirPrivate creates dir exclusively with owner-only permissions. Backends that implement base.PrivateDirCreator
// guarantee exclusivity atomically. Other backends (the device bridge) get a refuse-if-present check, a single-level
// Mkdir, a real-directory check before Chmod 0700 and a mode verification through the parent listing; callers pass an
// unguessable name, so the window between the presence check and Mkdir cannot be targeted.
func mkdirPrivate(ctx context.Context, filesystem base.FileSystem, dir string) error {
	if !validPrivateDir(dir) {
		return ipc.BadParam(fmt.Errorf("私有目录必须是 %s 下以 %s 开头的直接子目录: %q", privateDirParent, privateDirPrefix, dir))
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
	if err := verifyListedPrivateDir(ctx, filesystem, dir, parent, name); err != nil {
		// Undo only a real directory, non-recursively: a non-empty or foreign directory is left alone.
		if entry, lerr := listedEntry(ctx, filesystem, parent, name); lerr == nil && entry != nil && entry.Kind == base.FileDirectory {
			_ = filesystem.Delete(ctx, dir, false)
		}
		return err
	}
	return nil
}

func verifyListedPrivateDir(ctx context.Context, filesystem base.FileSystem, dir, parent, name string) error {
	entry, err := listedEntry(ctx, filesystem, parent, name)
	if err != nil {
		return err
	}
	if entry == nil || entry.Kind != base.FileDirectory {
		return fmt.Errorf("%s: %w", dir, base.ErrNotPrivateDir)
	}
	if err := filesystem.Chmod(ctx, dir, 0o700); err != nil {
		return err
	}
	entry, err = listedEntry(ctx, filesystem, parent, name)
	if err != nil {
		return err
	}
	if entry == nil || entry.Kind != base.FileDirectory || entry.Mode.Perm() != 0o700 {
		return fmt.Errorf("%s: %w", dir, base.ErrNotPrivateDir)
	}
	return nil
}

// listedEntry returns the parent listing's entry for name without following symlinks, or nil when it is absent.
func listedEntry(ctx context.Context, filesystem base.FileSystem, parent, name string) (*base.FileEntry, error) {
	entries, err := filesystem.List(ctx, parent)
	if err != nil {
		return nil, err
	}
	for index := range entries {
		if entries[index].Name == name {
			return &entries[index], nil
		}
	}
	return nil, nil
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
