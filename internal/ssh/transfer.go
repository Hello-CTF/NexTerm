package ssh

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sshfs "github.com/Hello-CTF/NexTerm/internal/fs/ssh"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	transportssh "github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

var (
	ErrSFTPUnavailable = errors.New("SFTP subsystem unavailable")
	ErrNoExec          = errors.New("remote helper execution blocked")
	ErrProtocol        = errors.New("remote helper protocol failure")
)

type TransferStage string

const (
	TransferStageSFTP           TransferStage = "sftp"
	TransferStageOpen           TransferStage = "open"
	TransferStageUpload         TransferStage = "upload"
	TransferStageHalfClose      TransferStage = "half_close"
	TransferStageWait           TransferStage = "wait"
	TransferStagePrepare        TransferStage = "prepare"
	TransferStageChmod          TransferStage = "chmod"
	TransferStageChecksum       TransferStage = "checksum"
	TransferStageProbe          TransferStage = "probe"
	TransferStageRename         TransferStage = "rename"
	TransferStageCleanupPartial TransferStage = "cleanup_partial"
	TransferStageCleanupGzip    TransferStage = "cleanup_gzip"
	TransferStageCleanupLock    TransferStage = "cleanup_lock"
)

// TransferError retains the failing stage and its original cause.
type TransferError struct {
	Stage TransferStage
	Path  string
	Cause error
}

func (e *TransferError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("helper transfer %s (%s): %v", e.Stage, e.Path, e.Cause)
	}
	return fmt.Sprintf("helper transfer %s: %v", e.Stage, e.Cause)
}

func (e *TransferError) Unwrap() error { return e.Cause }

func transferError(stage TransferStage, path string, cause error) error {
	if cause == nil {
		return nil
	}
	return &TransferError{Stage: stage, Path: path, Cause: cause}
}

// IsSFTPUnavailable reports only explicit capability failures. Permission,
// execution, protocol, trust, cancellation and transport failures are terminal.
func IsSFTPUnavailable(err error) bool {
	if err == nil || errors.Is(err, fs.ErrPermission) || errors.Is(err, ErrNoExec) ||
		errors.Is(err, ErrProtocol) || errors.Is(err, ErrTrust) || errors.Is(err, ErrDigestMismatch) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, base.ErrDisconnected) || errors.Is(err, base.ErrClosed) ||
		errors.Is(err, base.ErrExitStatusMissing) {
		return false
	}
	if errors.Is(err, ErrSFTPUnavailable) || errors.Is(err, base.ErrUnsupported) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, hardFailure := range []string{"permission denied", "access denied", "noexec", "host key", "trust", "protocol"} {
		if strings.Contains(message, hardFailure) {
			return false
		}
	}
	return strings.Contains(message, "ssh: subsystem request failed") ||
		strings.Contains(message, "sftp subsystem") && strings.Contains(message, "unavailable")
}

// FallbackHost delegates ordinary SSH operations to Host and retries only
// filesystem operations whose primary failure proves SFTP is unavailable.
// A proven split between the SFTP and exec views makes the exec fallback sticky.
type FallbackHost struct {
	Host
	fallback    Host
	useFallback atomic.Bool
}

func NewFallbackHost(primary, fallback Host) *FallbackHost {
	return &FallbackHost{Host: primary, fallback: fallback}
}

func (h *FallbackHost) switchToFallback() bool {
	return h.useFallback.CompareAndSwap(false, true)
}

func (h *FallbackHost) fallbackError(primary error, fallback func() error) error {
	if !IsSFTPUnavailable(primary) {
		return transferError(TransferStageSFTP, "", primary)
	}
	if err := fallback(); err != nil {
		return errors.Join(transferError(TransferStageSFTP, "", primary), err)
	}
	return nil
}

func (h *FallbackHost) Exists(ctx context.Context, path string) (bool, error) {
	if h.useFallback.Load() {
		return h.fallback.Exists(ctx, path)
	}
	exists, err := h.Host.Exists(ctx, path)
	if err == nil {
		return exists, nil
	}
	if !IsSFTPUnavailable(err) {
		return false, transferError(TransferStageSFTP, path, err)
	}
	fallbackExists, fallbackErr := h.fallback.Exists(ctx, path)
	if fallbackErr != nil {
		return false, errors.Join(transferError(TransferStageSFTP, path, err), fallbackErr)
	}
	return fallbackExists, nil
}

func (h *FallbackHost) Mkdir(ctx context.Context, path string) error {
	if h.useFallback.Load() {
		return h.fallback.Mkdir(ctx, path)
	}
	if err := h.Host.Mkdir(ctx, path); err != nil {
		return h.fallbackError(err, func() error { return h.fallback.Mkdir(ctx, path) })
	}
	return nil
}

func (h *FallbackHost) Chmod(ctx context.Context, path string, mode fs.FileMode) error {
	if h.useFallback.Load() {
		return h.fallback.Chmod(ctx, path, mode)
	}
	if err := h.Host.Chmod(ctx, path, mode); err != nil {
		return h.fallbackError(err, func() error { return h.fallback.Chmod(ctx, path, mode) })
	}
	return nil
}

func uploadHostWithProgress(ctx context.Context, host Host, localPath, remotePath string, options sshfs.TransferOptions) (int64, error) {
	if progressHost, ok := host.(interface {
		UploadWithProgress(context.Context, string, string, sshfs.TransferOptions) (int64, error)
	}); ok {
		return progressHost.UploadWithProgress(ctx, localPath, remotePath, options)
	}
	transferred, err := host.Upload(ctx, localPath, remotePath)
	if options.Progress != nil {
		options.Progress(sshfs.Progress{Transferred: transferred, Total: transferred, Done: err == nil})
	}
	return transferred, err
}

func (h *FallbackHost) Upload(ctx context.Context, localPath, remotePath string) (int64, error) {
	return h.uploadWithProgress(ctx, localPath, remotePath, sshfs.TransferOptions{})
}

func (h *FallbackHost) UploadWithProgress(ctx context.Context, localPath, remotePath string, options sshfs.TransferOptions) (int64, error) {
	return h.uploadWithProgress(ctx, localPath, remotePath, options)
}

func (h *FallbackHost) uploadWithProgress(ctx context.Context, localPath, remotePath string, options sshfs.TransferOptions) (int64, error) {
	if h.useFallback.Load() {
		return uploadHostWithProgress(ctx, h.fallback, localPath, remotePath, options)
	}
	transferred, err := uploadHostWithProgress(ctx, h.Host, localPath, remotePath, options)
	if err == nil {
		exists, verifyErr := h.fallback.Exists(ctx, remotePath)
		if verifyErr != nil {
			return transferred, fmt.Errorf("verify uploaded file through exec %s: %w", remotePath, verifyErr)
		}
		if exists {
			return transferred, nil
		}
		if cleanupErr := h.Host.Delete(ctx, remotePath, false); cleanupErr != nil && !errors.Is(cleanupErr, fs.ErrNotExist) {
			return transferred, fmt.Errorf("remove SFTP upload invisible through exec %s: %w", remotePath, cleanupErr)
		}
		h.switchToFallback()
		return uploadHostWithProgress(ctx, h.fallback, localPath, remotePath, options)
	}
	if !IsSFTPUnavailable(err) {
		return transferred, transferError(TransferStageSFTP, remotePath, err)
	}
	fallbackTransferred, fallbackErr := uploadHostWithProgress(ctx, h.fallback, localPath, remotePath, options)
	if fallbackErr != nil {
		return fallbackTransferred, errors.Join(transferError(TransferStageSFTP, remotePath, err), fallbackErr)
	}
	return fallbackTransferred, nil
}

func (h *FallbackHost) Rename(ctx context.Context, from, to string) error {
	if h.useFallback.Load() {
		return h.fallback.Rename(ctx, from, to)
	}
	if err := h.Host.Rename(ctx, from, to); err != nil {
		return h.fallbackError(err, func() error { return h.fallback.Rename(ctx, from, to) })
	}
	return nil
}

func (h *FallbackHost) Delete(ctx context.Context, path string, directory bool) error {
	if h.useFallback.Load() {
		return h.fallback.Delete(ctx, path, directory)
	}
	if err := h.Host.Delete(ctx, path, directory); err != nil {
		return h.fallbackError(err, func() error { return h.fallback.Delete(ctx, path, directory) })
	}
	return nil
}

func (h *FallbackHost) ChecksumSHA256(ctx context.Context, path string) (string, error) {
	if h.useFallback.Load() {
		return h.fallback.ChecksumSHA256(ctx, path)
	}
	digest, err := h.Host.ChecksumSHA256(ctx, path)
	if err == nil {
		return digest, nil
	}
	if !IsSFTPUnavailable(err) {
		return "", transferError(TransferStageSFTP, path, err)
	}
	fallbackDigest, fallbackErr := h.fallback.ChecksumSHA256(ctx, path)
	if fallbackErr != nil {
		return "", errors.Join(transferError(TransferStageSFTP, path, err), fallbackErr)
	}
	return fallbackDigest, nil
}

// ExecOpener opens a non-PTY SSH exec channel.
type ExecOpener interface {
	OpenExec(context.Context, string, base.ExecOptions) (base.Channel, error)
}

// ExecHost implements the helper filesystem through SSH exec only.
type ExecHost struct {
	*clientHost
	target Target
}

func NewExecHost(client *transportssh.Client, target Target) (*ExecHost, error) {
	if client == nil {
		return nil, fmt.Errorf("exec host requires an SSH client")
	}
	if err := target.validatePlatform(); err != nil {
		return nil, err
	}
	return &ExecHost{clientHost: &clientHost{client: client}, target: target}, nil
}

func (h *ExecHost) run(ctx context.Context, command string) (string, error) {
	result, err := h.client.Exec(ctx, command, base.ExecOptions{
		Timeout: probeTimeout,
		Limits:  base.OutputLimits{Stdout: 1 << 20, Stderr: 64 << 10},
	})
	if err != nil {
		return "", err
	}
	if result.ExitCode == nil {
		return "", base.ErrExitStatusMissing
	}
	if *result.ExitCode != 0 {
		return "", remoteCommandFailure(*result.ExitCode, result.Signal, result.Stderr+result.Stdout)
	}
	return result.Stdout, nil
}

func remoteCommandFailure(exitCode int, signal, output string) error {
	exitErr := &base.ExitError{Code: exitCode, Signal: signal}
	lower := strings.ToLower(output)
	var cause error = exitErr
	switch {
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "access denied"):
		cause = errors.Join(exitErr, fs.ErrPermission)
	case strings.Contains(lower, "noexec"), exitCode == 126:
		cause = errors.Join(exitErr, ErrNoExec)
	}
	if trimmed := strings.TrimSpace(output); trimmed != "" {
		return fmt.Errorf("remote command exited with %d: %s: %w", exitCode, trimmed, cause)
	}
	return cause
}

func remoteExitCodeIs(err error, code int) bool {
	actual, ok := base.ExitCode(err)
	return ok && actual == code
}

func remoteCommandNotFound(err error) bool {
	if err == nil || errors.Is(err, fs.ErrPermission) || errors.Is(err, ErrNoExec) {
		return false
	}
	var exitErr *base.ExitError
	return errors.As(err, &exitErr) && exitErr.Code == 1
}

func remoteStatNotFoundDiagnostic(err error, path string) bool {
	if !remoteCommandNotFound(err) {
		return false
	}
	suffix := ": No such file or directory"
	for _, diagnostic := range []string{
		"stat: " + path + suffix,
		"stat: " + path + ": stat" + suffix,
		"stat: cannot stat '" + path + "'" + suffix,
		"stat: cannot statx '" + path + "'" + suffix,
	} {
		if strings.Contains(err.Error(), diagnostic) {
			return true
		}
	}
	return false
}

func (h *ExecHost) Exists(ctx context.Context, path string) (bool, error) {
	quoted := quoteShell(path)
	stat := "LC_ALL=C stat -- "
	if h.target.GOOS == "darwin" {
		stat = "LC_ALL=C stat -f '%N' -- "
	}
	command := "if [ -e " + quoted + " ] || [ -L " + quoted + " ]; then printf 1; else " + stat + quoted + " >/dev/null && printf 1; fi"
	if h.target.GOOS == "windows" {
		script := "$ErrorActionPreference='Stop'; try { Get-Item -LiteralPath " + quotePowerShell(path) + " -Force -ErrorAction Stop | Out-Null; [Console]::Out.Write('1') } " +
			"catch [System.Management.Automation.ItemNotFoundException],[System.IO.FileNotFoundException],[System.IO.DirectoryNotFoundException] { [Console]::Out.Write('0') } " +
			"catch { [Console]::Error.Write($_.Exception.Message); exit 73 }"
		command = powerShellCommand("powershell.exe", script)
	}
	stdout, err := h.run(ctx, command)
	if err != nil {
		if h.target.GOOS != "windows" && remoteStatNotFoundDiagnostic(err, path) {
			return false, nil
		}
		return false, err
	}
	return parseExecExistence(h.target, path, stdout)
}

func parseExecExistence(target Target, path, stdout string) (bool, error) {
	switch strings.TrimSpace(stdout) {
	case "1", "True":
		return true, nil
	case "0", "False":
		return false, nil
	default:
		if target.GOOS != "windows" && remoteStatNotFoundDiagnostic(remoteCommandFailure(1, "", stdout), path) {
			return false, nil
		}
		return false, fmt.Errorf("%w: unexpected existence response %q", ErrProtocol, stdout)
	}
}

func (h *ExecHost) Mkdir(ctx context.Context, path string) error {
	command := "mkdir " + quoteShell(path) + "; status=$?; if [ $status -ne 0 ] && { [ -e " + quoteShell(path) + " ] || [ -L " + quoteShell(path) + " ]; }; then exit 17; fi; exit $status"
	if h.target.GOOS == "windows" {
		script := "$ErrorActionPreference='Stop'; if (Test-Path -LiteralPath " + quotePowerShell(path) + ") { exit 17 }; New-Item -ItemType Directory -Path " + quotePowerShell(path) + " | Out-Null"
		command = powerShellCommand("powershell.exe", script)
	}
	_, err := h.run(ctx, command)
	if remoteExitCodeIs(err, 17) {
		return fs.ErrExist
	}
	return err
}

func (h *ExecHost) Chmod(ctx context.Context, path string, mode fs.FileMode) error {
	command := "chmod " + strconv.FormatUint(uint64(mode.Perm()), 8) + " " + quoteShell(path)
	if h.target.GOOS == "windows" {
		command = powerShellCommand("powershell.exe", "if (-not (Test-Path -LiteralPath "+quotePowerShell(path)+")) { exit 3 }")
	}
	_, err := h.run(ctx, command)
	if remoteExitCodeIs(err, 3) {
		return fs.ErrNotExist
	}
	return err
}

func (h *ExecHost) Upload(ctx context.Context, localPath, remotePath string) (int64, error) {
	return UploadExec(ctx, h.client, h.target, localPath, remotePath, sshfs.TransferOptions{})
}

func (h *ExecHost) UploadWithProgress(ctx context.Context, localPath, remotePath string, options sshfs.TransferOptions) (int64, error) {
	return UploadExec(ctx, h.client, h.target, localPath, remotePath, options)
}

func (h *ExecHost) Rename(ctx context.Context, from, to string) error {
	command := "mv " + quoteShell(from) + " " + quoteShell(to)
	if h.target.GOOS == "windows" {
		command = powerShellCommand("powershell.exe", "$ErrorActionPreference='Stop'; Move-Item -LiteralPath "+quotePowerShell(from)+" -Destination "+quotePowerShell(to)+" -Force")
	}
	_, err := h.run(ctx, command)
	return err
}

func (h *ExecHost) Delete(ctx context.Context, path string, directory bool) error {
	missing := "if [ ! -e " + quoteShell(path) + " ] && [ ! -L " + quoteShell(path) + " ]; then exit 3; fi; "
	command := missing + "rm " + quoteShell(path)
	if directory {
		command = missing + "rm -r " + quoteShell(path)
	}
	if h.target.GOOS == "windows" {
		recurse := ""
		if directory {
			recurse = " -Recurse"
		}
		command = powerShellCommand("powershell.exe", "$ErrorActionPreference='Stop'; if (-not (Test-Path -LiteralPath "+quotePowerShell(path)+")) { exit 3 }; Remove-Item -LiteralPath "+quotePowerShell(path)+recurse)
	}
	_, err := h.run(ctx, command)
	if remoteExitCodeIs(err, 3) {
		return fs.ErrNotExist
	}
	return err
}

func (h *ExecHost) ChecksumSHA256(ctx context.Context, path string) (string, error) {
	quoted := quoteShell(path)
	command := "if command -v sha256sum >/dev/null 2>&1; then sha256sum " + quoted + "; elif command -v shasum >/dev/null 2>&1; then shasum -a 256 " + quoted + "; elif command -v openssl >/dev/null 2>&1; then openssl dgst -sha256 -r " + quoted + "; else exit 127; fi"
	if h.target.GOOS == "windows" {
		command = powerShellCommand("powershell.exe", "$ErrorActionPreference='Stop'; (Get-FileHash -Algorithm SHA256 -LiteralPath "+quotePowerShell(path)+").Hash")
	}
	stdout, err := h.run(ctx, command)
	if err != nil {
		return "", err
	}
	digest, ok := parseSHA256Output(stdout)
	if !ok {
		return "", fmt.Errorf("%w: malformed remote SHA256 output %q", ErrProtocol, stdout)
	}
	return digest, nil
}

// UploadExec streams a local regular file to a remote path over a non-PTY SSH
// exec stdin, half-closes stdin, then waits for the remote exit status.
func UploadExec(ctx context.Context, opener ExecOpener, target Target, localPath, remotePath string, options sshfs.TransferOptions) (transferred int64, retErr error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := target.validatePlatform(); err != nil {
		return 0, err
	}
	if options.Resume {
		return 0, transferError(TransferStageUpload, remotePath, fmt.Errorf("%w: exec stdin resume requires a staged restart", base.ErrUnsupported))
	}
	if options.BufferSize < 0 || options.ProgressInterval < 0 {
		return 0, transferError(TransferStageUpload, remotePath, fmt.Errorf("invalid transfer options"))
	}
	if options.BufferSize == 0 {
		options.BufferSize = 256 << 10
	}
	if options.ProgressInterval == 0 {
		options.ProgressInterval = 200 * time.Millisecond
	}
	local, err := os.Open(localPath)
	if err != nil {
		return 0, transferError(TransferStageOpen, localPath, err)
	}
	defer local.Close()
	info, err := local.Stat()
	if err != nil {
		return 0, transferError(TransferStageOpen, localPath, err)
	}
	if !info.Mode().IsRegular() {
		return 0, transferError(TransferStageOpen, localPath, fmt.Errorf("upload source is not a regular file"))
	}
	command, err := execUploadCommand(target, remotePath)
	if err != nil {
		return 0, err
	}
	channel, err := opener.OpenExec(ctx, command, base.ExecOptions{})
	if err != nil {
		return 0, transferError(TransferStageOpen, remotePath, err)
	}
	defer channel.Close()

	stderr := &cappedTransferBuffer{maximum: 64 << 10}
	copyErrors := make(chan error, 2)
	var copies sync.WaitGroup
	copies.Add(2)
	go func() {
		defer copies.Done()
		_, err := io.Copy(io.Discard, channel)
		copyErrors <- err
	}()
	go func() {
		defer copies.Done()
		_, err := io.Copy(stderr, channel.Stderr())
		copyErrors <- err
	}()

	transferred, copyErr := copyExecStream(ctx, sshfs.PaceWriter(ctx, channel, options.MaxBytesPerSecond), local, info.Size(), options)
	closeWriteErr := channel.CloseWrite()
	waitErr := channel.Wait(ctx)
	_ = channel.Close()
	copies.Wait()
	close(copyErrors)
	if copyErr != nil {
		retErr = errors.Join(retErr, transferError(TransferStageUpload, remotePath, copyErr))
	}
	if closeWriteErr != nil {
		retErr = errors.Join(retErr, transferError(TransferStageHalfClose, remotePath, closeWriteErr))
	}
	if waitErr != nil {
		if code, ok := base.ExitCode(waitErr); ok {
			var exitErr *base.ExitError
			if errors.As(waitErr, &exitErr) {
				waitErr = remoteCommandFailure(code, exitErr.Signal, stderr.String())
			}
		}
		retErr = errors.Join(retErr, transferError(TransferStageWait, remotePath, waitErr))
	}
	for err := range copyErrors {
		if err != nil && !errors.Is(err, base.ErrClosed) && !errors.Is(err, io.EOF) {
			retErr = errors.Join(retErr, transferError(TransferStageWait, remotePath, err))
		}
	}
	if retErr == nil && options.Progress != nil {
		options.Progress(sshfs.Progress{Transferred: transferred, Total: info.Size(), Done: true})
	}
	return transferred, retErr
}

func execUploadCommand(target Target, remotePath string) (string, error) {
	if remotePath == "" || strings.ContainsRune(remotePath, '\x00') {
		return "", transferError(TransferStageUpload, remotePath, fmt.Errorf("remote upload path is required"))
	}
	if target.GOOS != "windows" {
		return "umask 077 && cat > " + quoteShell(remotePath), nil
	}
	script := "$ErrorActionPreference='Stop'; $inputStream=[Console]::OpenStandardInput(); $outputStream=[System.IO.File]::Create(" + quotePowerShell(remotePath) + "); " +
		"try { $inputStream.CopyTo($outputStream); $outputStream.Flush($true) } finally { $outputStream.Dispose(); $inputStream.Dispose() }"
	return powerShellCommand("powershell.exe", script), nil
}

func copyExecStream(ctx context.Context, destination io.Writer, source io.Reader, total int64, options sshfs.TransferOptions) (int64, error) {
	buffer := make([]byte, options.BufferSize)
	var transferred int64
	lastProgress := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return transferred, err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			written, writeErr := destination.Write(buffer[:count])
			transferred += int64(written)
			if options.Progress != nil && time.Since(lastProgress) >= options.ProgressInterval {
				options.Progress(sshfs.Progress{Transferred: transferred, Total: total})
				lastProgress = time.Now()
			}
			if writeErr != nil {
				return transferred, writeErr
			}
			if written != count {
				return transferred, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return transferred, nil
		}
		if readErr != nil {
			return transferred, readErr
		}
	}
}

type cappedTransferBuffer struct {
	buffer  bytes.Buffer
	maximum int
}

func (b *cappedTransferBuffer) Write(p []byte) (int, error) {
	original := len(p)
	if remaining := b.maximum - b.buffer.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buffer.Write(p)
	}
	return original, nil
}

func (b *cappedTransferBuffer) String() string { return b.buffer.String() }

// StageUpload installs a standalone helper with lock, staged digest, executable
// mode, probe, rename and visible cleanup failures.
func StageUpload(ctx context.Context, host Host, target Target, source UploadSource, probe bool, progress func(context.Context, HelperProgress)) (result string, retErr error) {
	return StageUploadLimited(ctx, host, target, source, probe, progress, 0)
}

// StageUploadLimited is StageUpload with an optional upload rate cap (bytes per second, zero for none).
func StageUploadLimited(ctx context.Context, host Host, target Target, source UploadSource, probe bool, progress func(context.Context, HelperProgress), maxBytesPerSecond int64) (result string, retErr error) {
	if host == nil {
		return "", fmt.Errorf("staged upload requires a host")
	}
	if source.Path == "" {
		return "", transferError(TransferStageOpen, "", fmt.Errorf("local helper path is required"))
	}
	if err := source.ValidateForTarget(target); err != nil {
		return "", err
	}
	paths, err := target.Paths()
	if err != nil {
		return "", err
	}
	for _, directory := range []string{paths.Root, paths.BinDir} {
		if err := ensureStageDirectory(ctx, host, directory); err != nil {
			return "", transferError(TransferStagePrepare, directory, err)
		}
	}
	targetPath, err := paths.Helper(source.Digest)
	if err != nil {
		return "", err
	}
	lock, err := paths.installLock(source.Digest)
	if err != nil {
		return "", err
	}
	for {
		err = host.Mkdir(ctx, lock)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", transferError(TransferStagePrepare, lock, err)
		}
		exists, existsErr := host.Exists(ctx, lock)
		if existsErr != nil {
			return "", transferError(TransferStagePrepare, lock, existsErr)
		}
		if !exists {
			continue
		}
		select {
		case <-ctx.Done():
			return "", transferError(TransferStagePrepare, lock, context.Cause(ctx))
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		if err := host.Delete(cleanupCtx, lock, true); err != nil && !errors.Is(err, fs.ErrNotExist) {
			retErr = errors.Join(retErr, transferError(TransferStageCleanupLock, lock, err))
		}
	}()

	exists, err := host.Exists(ctx, targetPath)
	if err != nil {
		return "", transferError(TransferStagePrepare, targetPath, err)
	}
	if exists {
		digest, err := host.ChecksumSHA256(ctx, targetPath)
		if err != nil {
			return "", transferError(TransferStageChecksum, targetPath, err)
		}
		if !strings.EqualFold(digest, source.Digest) {
			return "", transferError(TransferStageChecksum, targetPath, fmt.Errorf("expected %s, got %s: %w", source.Digest, digest, ErrDigestMismatch))
		}
		if probe {
			if err := probeStagedHelper(ctx, host, target, targetPath); err != nil {
				return "", err
			}
		}
		return targetPath, nil
	}

	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", transferError(TransferStagePrepare, targetPath, err)
	}
	staging, err := paths.staging(source.Digest, hex.EncodeToString(nonce[:]))
	if err != nil {
		return "", err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		if err := host.Delete(cleanupCtx, staging, false); err != nil && !errors.Is(err, fs.ErrNotExist) {
			retErr = errors.Join(retErr, transferError(TransferStageCleanupPartial, staging, err))
		}
	}()

	progressFn := func(event sshfs.Progress) {
		if progress != nil {
			resolver := Resolver{Progress: progress}
			resolver.reportProgress(ctx, HelperProgress{Step: 4, Steps: helperProgressSteps, Stage: "upload", Transferred: event.Transferred, Total: event.Total})
		}
	}
	if progress != nil {
		resolver := Resolver{Progress: progress}
		resolver.reportProgress(ctx, HelperProgress{Step: 4, Stage: "upload", Transport: "sftp/exec", Path: staging})
	}
	if _, err := uploadHostWithProgress(ctx, host, source.Path, staging, sshfs.TransferOptions{Progress: progressFn, MaxBytesPerSecond: maxBytesPerSecond}); err != nil {
		return "", transferError(TransferStageUpload, staging, err)
	}
	if err := host.Chmod(ctx, staging, 0o700); err != nil {
		return "", transferError(TransferStageChmod, staging, err)
	}
	if progress != nil {
		resolver := Resolver{Progress: progress}
		resolver.reportProgress(ctx, HelperProgress{Step: 5, Stage: "verify", Path: staging})
	}
	digest, err := host.ChecksumSHA256(ctx, staging)
	if err != nil {
		return "", transferError(TransferStageChecksum, staging, err)
	}
	if !strings.EqualFold(digest, source.Digest) {
		return "", transferError(TransferStageChecksum, staging, fmt.Errorf("expected %s, got %s: %w", source.Digest, digest, ErrDigestMismatch))
	}
	if probe {
		if err := probeStagedHelper(ctx, host, target, staging); err != nil {
			return "", err
		}
	}
	if err := host.Rename(ctx, staging, targetPath); err != nil {
		return "", transferError(TransferStageRename, targetPath, err)
	}
	committed = true
	digest, err = host.ChecksumSHA256(ctx, targetPath)
	if err != nil {
		return targetPath, transferError(TransferStageChecksum, targetPath, err)
	}
	if !strings.EqualFold(digest, source.Digest) {
		return targetPath, transferError(TransferStageChecksum, targetPath, fmt.Errorf("expected %s, got %s: %w", source.Digest, digest, ErrDigestMismatch))
	}
	return targetPath, nil
}

func ensureStageDirectory(ctx context.Context, host Host, directory string) error {
	if err := host.Mkdir(ctx, directory); err != nil {
		exists, existsErr := host.Exists(ctx, directory)
		if existsErr != nil {
			return errors.Join(err, existsErr)
		}
		if !exists {
			return err
		}
	}
	return host.Chmod(ctx, directory, 0o700)
}

func probeStagedHelper(ctx context.Context, host Host, target Target, binary string) error {
	command, err := target.probeCommand(binary)
	if err != nil {
		return err
	}
	stdout, exitCode, err := host.Run(ctx, command, probeTimeout)
	if err != nil {
		return transferError(TransferStageProbe, binary, err)
	}
	if exitCode != 0 {
		return transferError(TransferStageProbe, binary, remoteCommandFailure(exitCode, "", stdout))
	}
	if actual := strings.TrimSpace(stdout); actual != strconv.Itoa(supervisor.ProtocolVersion) {
		return transferError(TransferStageProbe, binary, fmt.Errorf("%w: helper reported protocol %q, want %d", ErrProtocol, actual, supervisor.ProtocolVersion))
	}
	return nil
}
