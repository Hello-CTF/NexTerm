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

	"github.com/Hello-CTF/NexTerm/internal/fs/conditional"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/pkg/sftp"
)

var (
	conditionalPreVerifyHook func()
	conditionalPreCommitHook func()
)

var conditionalLink = func(client *sftp.Client, oldname, newname string) error {
	return client.Link(oldname, newname)
}

func (f *FS) WriteFileVersion(ctx context.Context, remotePath string, data []byte, backup bool, expected conditional.Expectation) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	remotePath, err := f.real(ctx, remotePath)
	if err != nil {
		return err
	}
	if expected.Exists {
		return fmt.Errorf("SFTP conditional replace %s: %w: the protocol has no compare-and-commit or file-lock primitive against non-cooperating writers", remotePath, base.ErrUnsupported)
	}
	return f.createFileVersion(ctx, remotePath, data, expected)
}

func (f *FS) createFileVersion(ctx context.Context, remotePath string, data []byte, expected conditional.Expectation) error {
	occupied, err := f.lstatOccupied(remotePath)
	if err != nil {
		return err
	}
	if occupied {
		return remoteAlreadyExists(expected)
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
	if conditionalPreVerifyHook != nil {
		conditionalPreVerifyHook()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	occupied, err = f.lstatOccupied(remotePath)
	if err != nil {
		return err
	}
	if occupied {
		return remoteAlreadyExists(expected)
	}
	if conditionalPreCommitHook != nil {
		conditionalPreCommitHook()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := conditionalLink(f.client, temporary, remotePath); err != nil {
		if !sftpFailureProvenByServer(err) {
			return &conditional.IndeterminateError{Expected: expected, New: conditional.VersionOf(data), Cause: fmt.Errorf("SFTP conditional create %s: %w", remotePath, err)}
		}
		if occupied, statErr := f.lstatOccupied(remotePath); statErr == nil && occupied {
			return remoteAlreadyExists(expected)
		}
		return fmt.Errorf("SFTP conditional create %s (requires hardlink@openssh.com): %w", remotePath, err)
	}
	committed = true
	_ = f.client.Remove(temporary)
	return nil
}

func sftpFailureProvenByServer(err error) bool {
	var status *sftp.StatusError
	if errors.As(err, &status) {
		return true
	}
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission)
}

func (f *FS) lstatOccupied(remotePath string) (bool, error) {
	_, err := f.client.Lstat(remotePath)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("SFTP lstat %s: %w", remotePath, err)
}

func remoteAlreadyExists(expected conditional.Expectation) error {
	return &conditional.MismatchError{Expected: expected, Actual: conditional.Version{Exists: true}, Reason: "file already exists"}
}

func (f *FS) uploadTemporary(ctx context.Context, remotePath string, data []byte, mode fs.FileMode) (temporary string, err error) {
	var suffix [8]byte
	_, _ = rand.Read(suffix[:])
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
