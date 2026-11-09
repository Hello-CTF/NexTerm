package ssh

import (
	"context"
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

// replaceFile commits data to remotePath without ever truncating the original: the content is staged privately
// (see stageTemporary), takes the original's mode and owner, is synced and closed, and then replaces the target with
// one rename. A cancel or disconnect before the rename leaves the original untouched.
//
// A symlink is followed to its final target, which is replaced so the link itself survives. The rename keeps the
// content, mode (setuid and setgid included) and owner, but not ACLs, extended attributes or SELinux labels, and
// SFTP v3 reports no link count, so a hard-linked target is detached from its other names. A crash between staging
// and cleanup can leave a .nexterm-tmp- or .nexterm-replace- sibling behind.
//
// The write falls back to rewriting the file in place when the replacement cannot preserve what the original has:
// a target that is not a regular file, a symlink that cannot be resolved lexically, a directory where nothing can be
// staged, a server that refuses the original's mode or owner, or a server that refuses the rename itself (a
// bind-mounted file such as /etc/hosts in a container, a sticky directory, a locked Windows file).
func (f *FS) replaceFile(ctx context.Context, remotePath string, data []byte) error {
	target, info, ok := f.replaceTarget(remotePath)
	if !ok || (info != nil && !info.Mode().IsRegular()) {
		return f.writeInPlace(ctx, remotePath, data)
	}
	mode := fs.FileMode(0o600)
	if info != nil {
		mode = info.Mode() & preservedModes
	}
	stage, err := f.stageTemporary(ctx, target, data, func(temporary string) error {
		if err := f.preserveOwner(temporary, info); err != nil {
			return err
		}
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
	defer f.discardStaged(stage)
	if f.replacePreCommitHook != nil {
		f.replacePreCommitHook()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.commitReplace(ctx, stage.path, target, info != nil); err != nil {
		if f.renameRefusedWithTargetIntact(err, target, info != nil) {
			f.discardStaged(stage)
			return f.writeInPlace(ctx, remotePath, data)
		}
		return fmt.Errorf("SFTP replace %s: %w", target, err)
	}
	return nil
}

// renameRefusedWithTargetIntact reports a rename the server answered itself (so it did not happen) while the target
// is still where it was; a lost response proves nothing, and a failed restore of a moved-aside original leaves the
// target missing, so both surface instead.
func (f *FS) renameRefusedWithTargetIntact(err error, target string, existed bool) bool {
	if !sftpFailureProvenByServer(err) {
		return false
	}
	_, lerr := f.client.Lstat(target)
	if existed {
		return lerr == nil
	}
	return errors.Is(lerr, fs.ErrNotExist)
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

// preserveOwner hands the temporary file the original's owner and group. A non-root account can usually only change
// the group to one it belongs to; when the owner or group still differs afterwards, replacing would hand the file to
// the writing account, so the error wraps errStageAttributes and the save rewrites the original in place.
func (f *FS) preserveOwner(temporary string, original os.FileInfo) error {
	if original == nil {
		return nil
	}
	want, ok := original.Sys().(*sftp.FileStat)
	if !ok {
		return nil
	}
	matches := func() (bool, error) {
		current, err := f.client.Lstat(temporary)
		if err != nil {
			return false, err
		}
		have, ok := current.Sys().(*sftp.FileStat)
		return ok && have.UID == want.UID && have.GID == want.GID, nil
	}
	if same, err := matches(); err != nil || same {
		return err
	}
	chownErr := f.chown(f.client, temporary, int(want.UID), int(want.GID))
	if same, err := matches(); err != nil || same {
		return err
	}
	return fmt.Errorf("keep owner %d:%d: %v", want.UID, want.GID, chownErr)
}

// commitReplace moves temporary over target: posix-rename replaces atomically, and a server without it gets
// replaceAside, which keeps the original recoverable. A missing target needs only a plain rename.
func (f *FS) commitReplace(ctx context.Context, temporary, target string, existed bool) error {
	if !existed {
		return f.client.Rename(temporary, target)
	}
	err := f.posixRename(f.client, temporary, target)
	if posixRenameUnsupported(err) {
		return f.replaceAside(ctx, temporary, target)
	}
	return err
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
