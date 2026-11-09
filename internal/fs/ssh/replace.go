package ssh

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/pkg/sftp"
)

const (
	maxSymlinkHops = 40
	preservedModes = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky
)

// replaceFile commits data to remotePath without ever truncating the original: the content goes to an O_EXCL sibling
// that takes the original's mode (and owner where the server allows chown), is synced and closed, and then replaces
// the target with one rename. A cancel or disconnect before the rename leaves the original untouched.
//
// A symlink is followed to its final target, which is replaced so the link itself survives. SFTP v3 reports no link
// count, so a hard-linked target is replaced like any other file and its other names keep the previous content.
// The write falls back to rewriting the file in place when the replacement cannot preserve what the original has:
// a target that is not a regular file, a symlink that cannot be resolved lexically, a directory where no temporary
// file can be created, or a server that refuses to apply the original's mode.
func (f *FS) replaceFile(ctx context.Context, remotePath string, data []byte) error {
	target, info, ok := f.replaceTarget(remotePath)
	if !ok || (info != nil && !info.Mode().IsRegular()) {
		return f.writeInPlace(ctx, remotePath, data)
	}
	mode := fs.FileMode(0o600)
	if info != nil {
		mode = info.Mode() & preservedModes
	}
	temporary, err := f.stageTemporary(ctx, target, data, func(temporary string) error {
		f.preserveOwner(temporary, info)
		return f.client.Chmod(temporary, mode)
	})
	if errors.Is(err, errStageCreate) || errors.Is(err, errStageAttributes) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return f.writeInPlace(ctx, remotePath, data)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("SFTP write %s: %w", remotePath, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = f.client.Remove(temporary)
		}
	}()
	if f.replacePreCommitHook != nil {
		f.replacePreCommitHook()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.commitReplace(temporary, target, info != nil); err != nil {
		return fmt.Errorf("SFTP replace %s: %w", target, err)
	}
	committed = true
	return nil
}

// replaceTarget follows symlinks from remotePath to the file a write lands on. info is nil when that file does not
// exist yet. ok is false when a link cannot be resolved without the server's help: a readlink failure, a link that
// climbs with "..", whose lexical join can differ from the kernel's walk through a symlinked directory, or a loop.
func (f *FS) replaceTarget(remotePath string) (target string, info os.FileInfo, ok bool) {
	target = remotePath
	for range maxSymlinkHops {
		stat, err := f.client.Lstat(target)
		if errors.Is(err, fs.ErrNotExist) {
			return target, nil, true
		}
		if err != nil {
			return "", nil, false
		}
		if stat.Mode()&fs.ModeSymlink == 0 {
			return target, stat, true
		}
		link, err := f.client.ReadLink(target)
		if err != nil || link == "" || containsParentSegment(link) {
			return "", nil, false
		}
		if !path.IsAbs(link) {
			link = path.Join(path.Dir(target), link)
		}
		target = link
	}
	return "", nil, false
}

func containsParentSegment(link string) bool {
	for _, segment := range strings.Split(link, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

// preserveOwner hands the temporary file the original's owner and group. A non-root account usually cannot chown to
// another user, so a failure is ignored: the replacement then belongs to the writing account with the original mode.
func (f *FS) preserveOwner(temporary string, original os.FileInfo) {
	if original == nil {
		return
	}
	want, ok := original.Sys().(*sftp.FileStat)
	if !ok {
		return
	}
	current, err := f.client.Lstat(temporary)
	if err != nil {
		return
	}
	if have, ok := current.Sys().(*sftp.FileStat); ok && have.UID == want.UID && have.GID == want.GID {
		return
	}
	_ = f.client.Chown(temporary, int(want.UID), int(want.GID))
}

// commitReplace moves temporary over target. posix-rename@openssh.com replaces atomically. Without it, SFTP v3
// rename refuses an existing target, so the original is first moved aside and is moved back if the second rename
// fails; the target name is briefly absent but its content is never lost.
func (f *FS) commitReplace(temporary, target string, existed bool) error {
	if f.posixRenameSupported(f.client) {
		return f.client.PosixRename(temporary, target)
	}
	if !existed {
		return f.client.Rename(temporary, target)
	}
	var suffix [8]byte
	_, _ = rand.Read(suffix[:])
	aside := path.Join(path.Dir(target), "."+path.Base(target)+".nexterm-old-"+hex.EncodeToString(suffix[:]))
	if err := f.client.Rename(target, aside); err != nil {
		return fmt.Errorf("move original aside: %w", err)
	}
	if err := f.client.Rename(temporary, target); err != nil {
		if restoreErr := f.client.Rename(aside, target); restoreErr != nil {
			return fmt.Errorf("%w; the original content is kept at %s (restore failed: %v)", err, aside, restoreErr)
		}
		return err
	}
	_ = f.client.Remove(aside)
	return nil
}

// writeInPlace truncates and rewrites remotePath through its existing inode.
func (f *FS) writeInPlace(ctx context.Context, remotePath string, data []byte) error {
	writer, err := f.OpenWrite(ctx, remotePath, false)
	if err != nil {
		return err
	}
	defer writer.Close()
	if err := writeAll(ctx, writer, data); err != nil {
		return fmt.Errorf("SFTP write %s: %w", remotePath, err)
	}
	if err := writer.Sync(); err != nil {
		return fmt.Errorf("SFTP sync %s: %w", remotePath, err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("SFTP close %s: %w", remotePath, err)
	}
	return nil
}

// CreateFile creates an empty file with O_EXCL and the owner-only mode WriteFile gives a new file. SFTP v3 has no
// "already exists" status, so an occupied path is confirmed with Lstat and reported as fs.ErrExist.
func (f *FS) CreateFile(ctx context.Context, remotePath string) error {
	remotePath, err := f.real(ctx, remotePath)
	if err != nil {
		return err
	}
	file, err := f.client.OpenFile(remotePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		if !errors.Is(err, fs.ErrExist) {
			if _, lerr := f.client.Lstat(remotePath); lerr == nil {
				return fmt.Errorf("SFTP create %s: %w: %w", remotePath, fs.ErrExist, err)
			}
		}
		return fmt.Errorf("SFTP create %s: %w", remotePath, err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = f.client.Remove(remotePath)
		return fmt.Errorf("SFTP restrict new file %s: %w", remotePath, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("SFTP close %s: %w", remotePath, err)
	}
	return nil
}
