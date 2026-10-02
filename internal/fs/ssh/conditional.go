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
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	"github.com/pkg/sftp"
)

// The hooks let tests inject adversarial external mutations around the
// final check, including after it and immediately before the commit. They
// are nil in production.
var (
	conditionalPreVerifyHook func()
	conditionalPreCommitHook func()
)

// conditionalLink is the atomic no-clobber commit primitive. Tests wrap it
// to inject post-commit response loss and determinate server failures.
var conditionalLink = func(client *sftp.Client, oldname, newname string) error {
	return client.Link(oldname, newname)
}

// WriteFileVersion implements conditional.Writer over the SFTP protocol
// only, without shell commands.
//
// A conditional create stages the content in an exclusively created
// same-directory temporary file and publishes it through the
// hardlink@openssh.com extension: an atomic operation that fails instead of
// overwriting if the path appeared at any moment before the commit.
// Existence is established with Lstat alone, so any occupied path — file,
// directory, symlink, readable or not — is a version mismatch.
//
// An existing-file replacement cannot honour the contract: the protocol has
// neither a compare-and-commit operation nor file locks that exclude
// non-cooperating writers between the final snapshot and an unconditional
// rename, so it fails with base.ErrUnsupported before any mutation.
//
// A failed commit is reconciled against the resulting path state, never
// retried: an absent target is a determinate uncommitted failure, a target
// holding exactly the staged content yields *conditional.IndeterminateError
// (the link may have succeeded before its response was lost), and any other
// occupant is a version mismatch.
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
		return f.reconcileCreate(ctx, remotePath, data, expected, err)
	}
	committed = true
	_ = f.client.Remove(temporary)
	return nil
}

// reconcileCreate classifies a failed hard-link commit by inspecting the
// resulting path state rather than trusting error shapes, which the SFTP
// client normalises inconsistently. The link is atomic, so an absent target
// proves nothing was committed; a target holding exactly the staged content
// means the commit may have succeeded before the response was lost; any
// other occupant is a plain version mismatch. No retry is ever attempted.
func (f *FS) reconcileCreate(ctx context.Context, remotePath string, data []byte, expected conditional.Expectation, linkErr error) error {
	// Reconciliation must finish even if the caller's context was cancelled;
	// otherwise a cancelled read could misclassify the commit outcome.
	ctx = context.WithoutCancel(ctx)
	occupied, statErr := f.lstatOccupied(remotePath)
	if statErr != nil {
		return &conditional.IndeterminateError{Expected: expected, New: conditional.VersionOf(data), Cause: fmt.Errorf("SFTP conditional create %s: %w (reconciliation failed: %v)", remotePath, linkErr, statErr)}
	}
	if !occupied {
		return fmt.Errorf("SFTP conditional create %s (requires hardlink@openssh.com): %w", remotePath, linkErr)
	}
	if version, err := f.hashRemoteRegular(ctx, remotePath); err == nil && version == conditional.VersionOf(data) {
		return &conditional.IndeterminateError{Expected: expected, New: version, Cause: fmt.Errorf("SFTP conditional create %s: %w", remotePath, linkErr)}
	}
	return remoteAlreadyExists(expected)
}

// hashRemoteRegular measures a regular file for reconciliation. Any open or
// read failure is reported as an error and treated as foreign content.
func (f *FS) hashRemoteRegular(ctx context.Context, remotePath string) (conditional.Version, error) {
	info, err := f.client.Lstat(remotePath)
	if err != nil {
		return conditional.Version{}, err
	}
	if !info.Mode().IsRegular() {
		return conditional.Version{}, fmt.Errorf("not a regular file")
	}
	file, err := f.client.Open(remotePath)
	if err != nil {
		return conditional.Version{}, err
	}
	stop := context.AfterFunc(ctx, func() { _ = file.Close() })
	defer stop()
	version, hashErr := conditional.HashReader(ctx, file)
	closeErr := file.Close()
	if hashErr != nil {
		return conditional.Version{}, hashErr
	}
	return version, closeErr
}

// lstatOccupied reports whether anything exists at remotePath without
// opening it, so unreadable files still count as occupied.
func (f *FS) lstatOccupied(remotePath string) (bool, error) {
	_, err := f.client.Lstat(remotePath)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("SFTP lstat %s: %w", remotePath, err)
}

func remoteAlreadyExists(expected conditional.Expectation) error {
	return &conditional.MismatchError{Expected: expected, Actual: conditional.Version{Exists: true}, Reason: "file already exists"}
}

// uploadTemporary stages the replacement content in an exclusively created
// same-directory temporary file.
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
