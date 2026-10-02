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

	"github.com/ProbiusOfficial/NexTerm/internal/fs/conditional"
	"github.com/pkg/sftp"
)

// conditionalTestHook lets tests inject an external mutation after the
// replacement content is staged and before the final verification.
var conditionalTestHook func()

// WriteFileVersion implements conditional.Writer over the SFTP protocol
// only, without shell commands. The target version is measured before and
// after the replacement content is staged in a same-directory temporary
// file, and the commit is a single atomic protocol operation: an
// overwriting posix-rename for replacements, or a hard link that refuses to
// clobber a path that appeared meanwhile for conditional creates. Any
// external change made before the final verification rejects the commit and
// leaves the remote target, and its backup, untouched by this call.
func (f *FS) WriteFileVersion(ctx context.Context, remotePath string, data []byte, backup bool, expected conditional.Expectation) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	remotePath, err := f.real(ctx, remotePath)
	if err != nil {
		return err
	}
	if expected.Exists {
		return f.replaceFileVersion(ctx, remotePath, data, backup, expected)
	}
	return f.createFileVersion(ctx, remotePath, data, expected)
}

type remoteSnapshot struct {
	version conditional.Version
	mode    fs.FileMode
	regular bool
}

// snapshot measures the remote target without following a final symlink:
// Lstat classifies the path, and only regular files are opened and hashed.
func (f *FS) snapshot(ctx context.Context, remotePath string) (remoteSnapshot, error) {
	info, err := f.client.Lstat(remotePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
			return remoteSnapshot{}, nil
		}
		return remoteSnapshot{}, fmt.Errorf("SFTP lstat %s: %w", remotePath, err)
	}
	snap := remoteSnapshot{version: conditional.Version{Exists: true}, mode: info.Mode(), regular: info.Mode().IsRegular()}
	if !snap.regular {
		return snap, nil
	}
	file, err := f.client.Open(remotePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
			return remoteSnapshot{}, nil
		}
		return remoteSnapshot{}, fmt.Errorf("SFTP open %s: %w", remotePath, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = file.Close() })
	defer stop()
	version, hashErr := conditional.HashReader(ctx, file)
	closeErr := file.Close()
	if hashErr != nil {
		return remoteSnapshot{}, fmt.Errorf("SFTP verify %s: %w", remotePath, hashErr)
	}
	if closeErr != nil {
		return remoteSnapshot{}, fmt.Errorf("SFTP close %s: %w", remotePath, closeErr)
	}
	snap.version = version
	return snap, nil
}

func checkSnapshot(expected conditional.Expectation, snap remoteSnapshot) error {
	if expected.Exists && snap.version.Exists && !snap.regular {
		return &conditional.MismatchError{Expected: expected, Actual: snap.version, Reason: "target is not a regular file"}
	}
	return expected.Check(snap.version)
}

func (f *FS) replaceFileVersion(ctx context.Context, remotePath string, data []byte, backup bool, expected conditional.Expectation) error {
	initial, err := f.snapshot(ctx, remotePath)
	if err != nil {
		return err
	}
	if err := checkSnapshot(expected, initial); err != nil {
		return err
	}
	temporary, err := f.uploadTemporary(ctx, remotePath, data, initial.mode.Perm())
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = f.client.Remove(temporary)
		}
	}()
	if conditionalTestHook != nil {
		conditionalTestHook()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	final, err := f.snapshot(ctx, remotePath)
	if err != nil {
		return err
	}
	if err := checkSnapshot(expected, final); err != nil {
		return err
	}
	if backup {
		if err := f.backup(ctx, remotePath, final.mode); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.client.PosixRename(temporary, remotePath); err != nil {
		return fmt.Errorf("SFTP conditional replace %s (requires posix-rename@openssh.com): %w", remotePath, err)
	}
	committed = true
	return nil
}

func (f *FS) createFileVersion(ctx context.Context, remotePath string, data []byte, expected conditional.Expectation) error {
	initial, err := f.snapshot(ctx, remotePath)
	if err != nil {
		return err
	}
	if err := checkSnapshot(expected, initial); err != nil {
		return err
	}
	temporary, err := f.uploadTemporary(ctx, remotePath, data, 0o600)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = f.client.Remove(temporary)
		}
	}()
	if conditionalTestHook != nil {
		conditionalTestHook()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	final, err := f.snapshot(ctx, remotePath)
	if err != nil {
		return err
	}
	if err := checkSnapshot(expected, final); err != nil {
		return err
	}
	if err := f.client.Link(temporary, remotePath); err != nil {
		if _, statErr := f.client.Lstat(remotePath); statErr == nil {
			return &conditional.MismatchError{Expected: expected, Actual: conditional.Version{Exists: true}, Reason: "file already exists"}
		}
		return fmt.Errorf("SFTP conditional create %s (requires hardlink@openssh.com): %w", remotePath, err)
	}
	committed = true
	_ = f.client.Remove(temporary)
	return nil
}

// uploadTemporary stages the replacement content in an exclusively created
// same-directory temporary file, preserving the target's permission bits.
func (f *FS) uploadTemporary(ctx context.Context, remotePath string, data []byte, mode fs.FileMode) (temporary string, err error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	temporary = path.Join(path.Dir(remotePath), "."+path.Base(remotePath)+".nexterm-tmp-"+hex.EncodeToString(suffix[:]))
	file, err := f.client.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return "", fmt.Errorf("SFTP create temporary file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = file.Close()
			_ = f.client.Remove(temporary)
		}
	}()
	stop := context.AfterFunc(ctx, func() { _ = file.Close() })
	defer stop()
	if err = writeAll(ctx, file, data); err != nil {
		return "", fmt.Errorf("SFTP stage conditional write: %w", err)
	}
	if err = f.client.Chmod(temporary, mode); err != nil {
		return "", fmt.Errorf("SFTP stage conditional mode: %w", err)
	}
	if syncErr := file.Sync(); syncErr != nil {
		var status *sftp.StatusError
		if !errors.As(syncErr, &status) || status.FxCode() != sftp.ErrSSHFxOpUnsupported {
			err = syncErr
			return "", fmt.Errorf("SFTP stage conditional sync: %w", err)
		}
	}
	if err = file.Close(); err != nil {
		return "", fmt.Errorf("SFTP stage conditional close: %w", err)
	}
	return temporary, nil
}

var _ conditional.Writer = (*FS)(nil)
