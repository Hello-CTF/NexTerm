package agent

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ReleaseBaseURL is the only official release source for self-upgrade: the domain is hardcoded, the server only reports
// the version number in version_mismatch frames, and download URLs are never delivered by the server (supply-chain red line).
const ReleaseBaseURL = "https://github.com/Hello-CTF/NexTerm/releases/download"

const (
	checksumsFileName         = "SHA256SUMS"
	maxReleaseVersionLength   = 64
	upgradeDownloadTimeout    = 10 * time.Minute
	defaultUpgradeMaxAttempts = 3
	defaultUpgradeBackoff     = 5 * time.Second
	replaceScriptDelaySeconds = 3
)

// errUpgradeStaged means the new binary has been downloaded, verified, and a deferred replacement scheduled: after the process exits,
// the replacement script completes the binary switch and restarts the service, and the cli layer ends this process with ExitOK accordingly.
var errUpgradeStaged = errors.New("agent self-upgrade staged")

// Upgrader self-upgrades the running agent to the version reported by the server: it downloads the platform asset from the fixed official release
// source, verifies it against the same release's SHA256SUMS (same verification-chain semantics as install-device.sh),
// and schedules a deferred replacement after unpacking. Download/verify/staging failures are retried by Upgrade with exponential
// backoff; once retries are exhausted the old binary is kept exactly as-is.
type Upgrader struct {
	BaseURL     string
	Executable  string
	MaxAttempts int
	Backoff     time.Duration
	Logger      *slog.Logger
	HTTPClient  *http.Client

	sleep           func(ctx context.Context, d time.Duration) bool
	scheduleReplace func(newPath, targetPath string) error
}

func defaultUpgrader(executable string) *Upgrader {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	return &Upgrader{BaseURL: ReleaseBaseURL, Executable: executable}
}

// Upgrade downloads and stages the agent binary for targetVersion, schedules the deferred replacement
// script on success, and returns nil; once retries are exhausted it returns an error carrying the attempt count, and the old binary always stays runnable as-is.
func (u *Upgrader) Upgrade(ctx context.Context, targetVersion string) error {
	if err := validateReleaseVersion(targetVersion); err != nil {
		return err
	}
	if u.BaseURL == "" || u.Executable == "" {
		return errors.New("upgrader requires BaseURL and Executable")
	}
	maxAttempts := u.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultUpgradeMaxAttempts
	}
	backoff := u.Backoff
	if backoff <= 0 {
		backoff = defaultUpgradeBackoff
	}
	sleep := u.sleep
	if sleep == nil {
		sleep = sleepContext
	}
	logger := u.Logger
	if logger == nil {
		logger = slog.Default()
	}
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if err = u.attempt(ctx, targetVersion); err == nil {
			return nil
		}
		if attempt < maxAttempts {
			logger.Warn("self-upgrade attempt failed; retrying", "target_version", targetVersion, "attempt", attempt, "max_attempts", maxAttempts, "error", err, "delay", backoff)
			if !sleep(ctx, backoff) {
				return context.Cause(ctx)
			}
			backoff *= 2
		}
	}
	return fmt.Errorf("self-upgrade to %s failed after %d attempts: %w", targetVersion, maxAttempts, err)
}

func (u *Upgrader) attempt(ctx context.Context, targetVersion string) error {
	asset := releaseAssetName(targetVersion, runtime.GOOS, runtime.GOARCH)
	tag := "v" + targetVersion
	stageDir, err := os.MkdirTemp("", "nexterm-agent-upgrade-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stageDir) }()
	archivePath := filepath.Join(stageDir, asset)
	if err := u.download(ctx, tag+"/"+asset, archivePath); err != nil {
		return err
	}
	sumsPath := filepath.Join(stageDir, checksumsFileName)
	if err := u.download(ctx, tag+"/"+checksumsFileName, sumsPath); err != nil {
		return err
	}
	sumsContent, err := os.ReadFile(sumsPath)
	if err != nil {
		return err
	}
	expected := parseSHA256SUMS(sumsContent)[asset]
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("%s 中没有 %s 的合法校验和条目, 拒绝升级", checksumsFileName, asset)
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return fmt.Errorf("%s 中 %s 的校验和不是合法 sha256, 拒绝升级", checksumsFileName, asset)
	}
	actual, err := fileSHA256(archivePath)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("SHA256 校验失败 (%s): 期望 %s, 实际 %s, 拒绝升级", asset, expected, actual)
	}
	target := u.Executable
	staged := filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".nexterm-new")
	if err := extractAgentBinary(archivePath, staged, agentBinaryName(runtime.GOOS)); err != nil {
		return err
	}
	schedule := u.scheduleReplace
	if schedule == nil {
		schedule = scheduleDeferredReplace
	}
	if err := schedule(staged, target); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("schedule deferred replace: %w", err)
	}
	return nil
}

func (u *Upgrader) download(ctx context.Context, ref, dest string) error {
	client := u.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: upgradeDownloadTimeout}
	}
	url := strings.TrimRight(u.BaseURL, "/") + "/" + ref
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, response.Status)
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, response.Body)
	closeErr := out.Close()
	if copyErr != nil {
		return fmt.Errorf("write %s: %w", dest, copyErr)
	}
	return closeErr
}

func validateReleaseVersion(value string) error {
	if value == "" || len(value) > maxReleaseVersionLength {
		return fmt.Errorf("invalid release version %q", value)
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '.' || r == '-' {
			continue
		}
		return fmt.Errorf("invalid release version %q", value)
	}
	return nil
}

func releaseAssetName(version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	if goos == "darwin" {
		goos = "macos"
	}
	return "nexterm-agent_" + goos + "_" + goarch + "_" + version + ext
}

func agentBinaryName(goos string) string {
	if goos == "windows" {
		return "nexterm-agent.exe"
	}
	return "nexterm-agent"
}

func parseSHA256SUMS(content []byte) map[string]string {
	out := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		name := filepath.Base(strings.TrimPrefix(parts[len(parts)-1], "*"))
		out[name] = strings.ToLower(parts[0])
	}
	return out
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractAgentBinary extracts the single agent binary at the archive root to destPath
// (0755): tar.gz corresponds to linux/darwin assets, zip to windows assets, matching the
// output layout of scripts/pack-agent.sh.
func extractAgentBinary(archivePath, destPath, innerName string) error {
	switch {
	case strings.HasSuffix(archivePath, ".zip"):
		return extractZipBinary(archivePath, destPath, innerName)
	case strings.HasSuffix(archivePath, ".tar.gz"):
		return extractTarGzBinary(archivePath, destPath, innerName)
	default:
		return fmt.Errorf("unknown release archive layout: %s", archivePath)
	}
}

func writeBinary(destPath string, source io.Reader) error {
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, source); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(destPath, 0o755)
}

func extractTarGzBinary(archivePath, destPath, innerName string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != innerName {
			continue
		}
		return writeBinary(destPath, reader)
	}
	return fmt.Errorf("%s not found in %s", innerName, archivePath)
}

func extractZipBinary(archivePath, destPath, innerName string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	for _, entry := range reader.File {
		if entry.Mode().IsDir() || filepath.Base(entry.Name) != innerName {
			continue
		}
		source, err := entry.Open()
		if err != nil {
			return err
		}
		defer func() { _ = source.Close() }()
		return writeBinary(destPath, source)
	}
	return fmt.Errorf("%s not found in %s", innerName, archivePath)
}

func shQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func psQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// unixRestartCommand is the command the script runs explicitly to restart the service after a successful replacement (darwin=launchctl
// kickstart, linux=systemctl --user restart); the rollback path does not restart, staying in the pending-manual state,
// so the service manager never forms a restart loop. When the service is not installed the command fails silently, and the next manual start is the
// new binary.
func unixRestartCommand(goos string) string {
	if goos == "darwin" {
		return `launchctl kickstart "gui/$(id -u)/` + launchdLabel + `" 2>/dev/null || true`
	}
	return "systemctl --user restart " + systemdUnitName + " 2>/dev/null || true"
}

// renderReplaceScriptUnix generates the deferred replacement script: a running process cannot overwrite itself, so the script
// swaps the new binary into the target path after the process exits (sleep). target->backup and new->target
// are both same-directory renames; if the swap-in fails it rolls back from backup, and no path is ever left without a binary.
// restartCmd runs only after a successful swap-in (see unixRestartCommand).
func renderReplaceScriptUnix(newPath, targetPath string, delaySeconds int, restartCmd string) string {
	backup := targetPath + ".nexterm-old"
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("sleep " + strconv.Itoa(delaySeconds) + "\n")
	b.WriteString("mv -f " + shQuote(targetPath) + " " + shQuote(backup) + " 2>/dev/null || true\n")
	b.WriteString("if mv -f " + shQuote(newPath) + " " + shQuote(targetPath) + "; then\n")
	b.WriteString("  chmod 0755 " + shQuote(targetPath) + "\n")
	b.WriteString("  rm -f " + shQuote(backup) + "\n")
	b.WriteString("  " + restartCmd + "\n")
	b.WriteString("else\n")
	b.WriteString("  mv -f " + shQuote(backup) + " " + shQuote(targetPath) + " 2>/dev/null || true\n")
	b.WriteString("fi\n")
	b.WriteString("rm -f \"$0\"\n")
	return b.String()
}

// renderReplaceScriptWindows is the windows counterpart of renderReplaceScriptUnix:
// powershell deferred replacement, rolling back to the old binary on failure; after a successful replacement, if the NexTermAgent service
// exists it is started via sc.exe start (service missing or start failure are both silent, forming no loop).
func renderReplaceScriptWindows(newPath, targetPath string, delaySeconds int) string {
	var b strings.Builder
	b.WriteString("Start-Sleep -Seconds " + strconv.Itoa(delaySeconds) + "\n")
	b.WriteString("$nxTarget = " + psQuote(filepath.Clean(targetPath)) + "\n")
	b.WriteString("$nxNew = " + psQuote(filepath.Clean(newPath)) + "\n")
	b.WriteString("$nxBak = " + psQuote(filepath.Clean(targetPath)+".nexterm-old") + "\n")
	b.WriteString("try { Move-Item -LiteralPath $nxTarget -Destination $nxBak -Force -ErrorAction Stop } catch {}\n")
	b.WriteString("try {\n")
	b.WriteString("  Move-Item -LiteralPath $nxNew -Destination $nxTarget -Force -ErrorAction Stop\n")
	b.WriteString("  Remove-Item -LiteralPath $nxBak -Force -ErrorAction SilentlyContinue\n")
	b.WriteString("  & sc.exe query NexTermAgent *>$null\n")
	b.WriteString("  if ($LASTEXITCODE -eq 0) { & sc.exe start NexTermAgent *>$null }\n")
	b.WriteString("} catch {\n")
	b.WriteString("  try { Move-Item -LiteralPath $nxBak -Destination $nxTarget -Force -ErrorAction Stop } catch {}\n")
	b.WriteString("}\n")
	b.WriteString("Remove-Item -LiteralPath $PSCommandPath -Force -ErrorAction SilentlyContinue\n")
	return b.String()
}
