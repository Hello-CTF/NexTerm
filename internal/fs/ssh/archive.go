package ssh

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

const archiveExecTimeout = 10 * time.Minute

func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func PackCommand(remotePath, outputPath string) (string, error) {
	parent, name, err := splitParentName(remotePath)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("umask 077 && tar -czf %s -C %s -- %s", ShellQuote(outputPath), ShellQuote(parent), ShellQuote(shellOperand(name))), nil
}

func ExtractCommand(remotePath string) (string, string, error) {
	parent, name, err := splitParentName(remotePath)
	if err != nil {
		return "", "", err
	}
	baseName := StripArchiveExtension(name)
	if baseName == "" || baseName == "." || baseName == ".." {
		return "", "", fmt.Errorf("archive name does not produce a safe target directory")
	}
	target := path.Join(parent, baseName)
	file := ShellQuote(shellOperand(remotePath))
	quotedTarget := ShellQuote(shellOperand(target))
	lower := strings.ToLower(name)
	var command string
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		command = fmt.Sprintf("tar -xzf %s -C %s", file, quotedTarget)
	case strings.HasSuffix(lower, ".tar.bz2"), strings.HasSuffix(lower, ".tbz2"), strings.HasSuffix(lower, ".tbz"):
		command = fmt.Sprintf("tar -xjf %s -C %s", file, quotedTarget)
	case strings.HasSuffix(lower, ".tar.xz"), strings.HasSuffix(lower, ".txz"):
		command = fmt.Sprintf("tar -xJf %s -C %s", file, quotedTarget)
	case strings.HasSuffix(lower, ".tar"):
		command = fmt.Sprintf("tar -xf %s -C %s", file, quotedTarget)
	case strings.HasSuffix(lower, ".zip"):
		command = fmt.Sprintf("command -v unzip >/dev/null 2>&1 || { echo 'remote unzip is required' >&2; exit 127; }; unzip -o %s -d %s", file, quotedTarget)
	default:
		return "", "", fmt.Errorf("unsupported archive format %q", name)
	}
	return fmt.Sprintf("mkdir -p %s && %s", quotedTarget, command), target, nil
}

func StripArchiveExtension(name string) string {
	lower := strings.ToLower(name)
	for _, extension := range []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tgz", ".tbz2", ".tbz", ".txz", ".tar", ".zip"} {
		if strings.HasSuffix(lower, extension) {
			return name[:len(name)-len(extension)]
		}
	}
	return name
}

func (f *FS) PackDownload(ctx context.Context, remotePath, localPath string, options TransferOptions) (int64, error) {
	if f.executor == nil {
		return 0, base.ErrUnsupported
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return 0, err
	}
	temporary := "/tmp/nexterm-pack-" + hex.EncodeToString(random[:]) + ".tar.gz"
	command, err := PackCommand(remotePath, temporary)
	if err != nil {
		return 0, err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = f.Delete(cleanupCtx, temporary, false)
	}()
	result, err := f.executor.Exec(ctx, command, base.ExecOptions{Timeout: archiveExecTimeout})
	if err != nil {
		return 0, fmt.Errorf("create remote archive: %w", err)
	}
	if err := commandResult(result); err != nil {
		return 0, fmt.Errorf("create remote archive: %w", err)
	}
	options.Resume = false
	return f.Download(ctx, temporary, localPath, options)
}

func (f *FS) Extract(ctx context.Context, remotePath string) (string, error) {
	if f.executor == nil {
		return "", base.ErrUnsupported
	}
	command, target, err := ExtractCommand(remotePath)
	if err != nil {
		return "", err
	}
	result, err := f.executor.Exec(ctx, command, base.ExecOptions{Timeout: archiveExecTimeout})
	if err != nil {
		return "", fmt.Errorf("extract remote archive: %w", err)
	}
	if err := commandResult(result); err != nil {
		return "", fmt.Errorf("extract remote archive: %w", err)
	}
	return target, nil
}

func commandResult(result base.ExecResult) error {
	if result.ExitCode == nil {
		return base.ErrExitStatusMissing
	}
	if *result.ExitCode != 0 {
		message := strings.TrimSpace(result.Stderr + result.Stdout)
		return fmt.Errorf("remote command exited %d: %s", *result.ExitCode, message)
	}
	if result.Truncated {
		return fmt.Errorf("remote archive command output exceeded safety limit")
	}
	return nil
}

func splitParentName(remotePath string) (string, string, error) {
	if remotePath == "" || strings.ContainsRune(remotePath, 0) {
		return "", "", fmt.Errorf("remote path is empty or contains NUL")
	}
	cleaned := path.Clean(remotePath)
	if cleaned == "/" {
		return "/", ".", nil
	}
	parent, name := path.Split(cleaned)
	if parent == "" {
		parent = "."
	} else {
		parent = strings.TrimSuffix(parent, "/")
		if parent == "" {
			parent = "/"
		}
	}
	if name == "" {
		return "", "", fmt.Errorf("remote path has no file name")
	}
	return parent, name, nil
}

func shellOperand(value string) string {
	if strings.HasPrefix(value, "-") {
		return "./" + value
	}
	return value
}
