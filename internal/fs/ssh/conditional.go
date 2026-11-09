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
	stage, err := f.uploadTemporary(ctx, remotePath, data, 0o600)
	if err != nil {
		return err
	}
	defer f.discardStaged(stage)
	if f.conditionalPreVerifyHook != nil {
		f.conditionalPreVerifyHook()
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
	if f.conditionalPreCommitHook != nil {
		f.conditionalPreCommitHook()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.conditionalLink(f.client, stage.path, remotePath); err != nil {
		if !sftpFailureProvenByServer(err) {
			return &conditional.IndeterminateError{Expected: expected, New: conditional.VersionOf(data), Cause: fmt.Errorf("SFTP conditional create %s: %w", remotePath, err)}
		}
		if occupied, statErr := f.lstatOccupied(remotePath); statErr == nil && occupied {
			return remoteAlreadyExists(expected)
		}
		return fmt.Errorf("SFTP conditional create %s (requires hardlink@openssh.com): %w", remotePath, err)
	}
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

var (
	errStageCreate     = errors.New("SFTP create temporary file")
	errStageAttributes = errors.New("SFTP set temporary file attributes")
)

// staged is content written next to its target: path is the file, dir the private directory holding it, empty
// when the server allowed no directory and the file sits directly beside the target with mode 0600.
type staged struct {
	path string
	dir  string
}

// discardStaged removes whatever of the staging is still present; after a rename only the empty directory is.
func (f *FS) discardStaged(stage staged) {
	_ = f.client.Remove(stage.path)
	if stage.dir != "" {
		_ = f.client.RemoveDirectory(stage.dir)
	}
}

func (f *FS) uploadTemporary(ctx context.Context, remotePath string, data []byte, mode fs.FileMode) (staged, error) {
	return f.stageTemporary(ctx, remotePath, data, func(temporary string) error {
		return f.client.Chmod(temporary, mode)
	})
}

// stageTemporary writes data next to remotePath without ever exposing it to other accounts. pkg/sftp opens files
// without attributes, so a new file gets the server umask (typically 0644) and any account that opens it while the
// content streams in keeps its handle afterwards. The file is therefore created inside a fresh sibling directory
// verified as owner-only 0700; when the server refuses that directory, the file is created beside the target and
// restricted to 0600 before the first byte is written. attributes then applies the final mode before the file is
// synced and closed. A failure to create or restrict the file wraps errStageCreate, a failure of attributes wraps
// errStageAttributes, and every failure removes what was staged.
func (f *FS) stageTemporary(ctx context.Context, remotePath string, data []byte, attributes func(temporary string) error) (stage staged, err error) {
	var suffix [8]byte
	_, _ = rand.Read(suffix[:])
	name := "." + path.Base(remotePath) + ".nexterm-tmp-" + hex.EncodeToString(suffix[:])
	sibling := path.Join(path.Dir(remotePath), name)
	stage = staged{path: sibling}
	if f.mkdirStage(f.client, sibling) == nil {
		if verifyErr := f.verifyPrivateDir(sibling); verifyErr == nil {
			stage = staged{path: path.Join(sibling, "content"), dir: sibling}
		} else if info, lerr := f.client.Lstat(sibling); lerr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			_ = f.client.RemoveDirectory(sibling)
		}
	}
	file, err := f.client.OpenFile(stage.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		if stage.dir != "" {
			_ = f.client.RemoveDirectory(stage.dir)
		}
		return staged{}, fmt.Errorf("%w: %w", errStageCreate, err)
	}
	// Failure returns clear the named result before deferred calls run, so cleanup holds its own copy.
	cleanup := stage
	defer func() {
		if err != nil {
			_ = file.Close()
			f.discardStaged(cleanup)
		}
	}()
	if stage.dir == "" {
		if err = file.Chmod(0o600); err != nil {
			return staged{}, fmt.Errorf("%w: restrict before writing: %w", errStageCreate, err)
		}
	}
	stop := context.AfterFunc(ctx, func() { _ = file.Close() })
	defer stop()
	if err = writeAll(ctx, file, data); err != nil {
		return staged{}, fmt.Errorf("SFTP stage write: %w", err)
	}
	if f.stageWrittenHook != nil {
		f.stageWrittenHook(stage.path)
	}
	if err = ctx.Err(); err != nil {
		return staged{}, err
	}
	if err = attributes(stage.path); err != nil {
		return staged{}, fmt.Errorf("%w: %w", errStageAttributes, err)
	}
	if syncErr := file.Sync(); syncErr != nil {
		var status *sftp.StatusError
		if !errors.As(syncErr, &status) || status.FxCode() != sftp.ErrSSHFxOpUnsupported {
			err = syncErr
			return staged{}, fmt.Errorf("SFTP stage sync: %w", err)
		}
	}
	if err = file.Close(); err != nil {
		return staged{}, fmt.Errorf("SFTP stage close: %w", err)
	}
	return stage, nil
}

var (
	_ conditional.Writer = (*FS)(nil)
	_ base.FileCreator   = (*FS)(nil)
)
