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

	"github.com/Hello-CTF/NexTerm/internal/releasesig"
)

// ReleaseBaseURL is the only official release source for self-upgrade: the domain is hardcoded, the server only reports
// the version number in version_mismatch frames, and download URLs are never delivered by the server (supply-chain red line).
const ReleaseBaseURL = "https://github.com/Hello-CTF/NexTerm/releases/download"

const (
	checksumsFileName         = "SHA256SUMS"
	checksumSignatureSuffix   = ".minisig"
	maxChecksumsBytes         = 1 << 20
	maxChecksumSignatureBytes = 16 << 10
	maxReleaseVersionLength   = 64
	upgradeDownloadTimeout    = 10 * time.Minute
	defaultUpgradeMaxAttempts = 3
	defaultUpgradeBackoff     = 5 * time.Second
	replaceScriptDelaySeconds = 3
)

// maxUpgradeDownloadBytes caps one release download (archive or checksum list) so a misbehaving host cannot fill the disk; it is a var for tests.
var maxUpgradeDownloadBytes int64 = 512 << 20

// errUpgradeStaged means the new binary has been downloaded, verified, and a deferred replacement scheduled: after the process exits,
// the replacement script completes the binary switch and restarts the service, and the cli layer ends this process with ExitOK accordingly.
var errUpgradeStaged = errors.New("agent self-upgrade staged")

// errUpgradeRefused marks permanent refusals (no trusted signing key, missing or invalid release signature): Upgrade returns them
// at once instead of retrying, and the old binary is left as-is.
var errUpgradeRefused = errors.New("self-upgrade refused")

// Upgrader self-upgrades the running agent to the version reported by the server: it downloads the platform asset from the fixed official release
// source, verifies the release's SHA256SUMS Minisign signature against the trusted key, verifies the archive against that
// SHA256SUMS, and schedules a deferred replacement after unpacking. Download/verify/staging failures are retried by Upgrade with exponential
// backoff; once retries are exhausted the old binary is kept exactly as-is.
type Upgrader struct {
	BaseURL    string
	Executable string
	// PublicKey is the trusted Minisign public key text; empty falls back to releasesig.EmbeddedPublicKey.
	PublicKey   string
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
		if errors.Is(err, errUpgradeRefused) {
			return err
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

func (u *Upgrader) trustedKey() (releasesig.PublicKey, error) {
	encoded := u.PublicKey
	if encoded == "" {
		encoded = releasesig.EmbeddedPublicKey
	}
	if encoded == "" {
		return releasesig.PublicKey{}, fmt.Errorf("%w: 此构建未内置发布签名公钥（开发构建），已拒绝自升级；请用 install-device.sh 或官方发行版重新安装", errUpgradeRefused)
	}
	key, err := releasesig.ParsePublicKey(encoded)
	if err != nil {
		return releasesig.PublicKey{}, fmt.Errorf("%w: 受信发布签名公钥无效: %v", errUpgradeRefused, err)
	}
	return key, nil
}

// verifiedChecksums fetches SHA256SUMS and its Minisign signature and returns the manifest only when the signature verifies
// for exactly this release version.
func (u *Upgrader) verifiedChecksums(ctx context.Context, tag, targetVersion string, key releasesig.PublicKey) ([]byte, error) {
	sums, err := u.fetch(ctx, tag+"/"+checksumsFileName, maxChecksumsBytes)
	if err != nil {
		return nil, err
	}
	signature, err := u.fetch(ctx, tag+"/"+checksumsFileName+checksumSignatureSuffix, maxChecksumSignatureBytes)
	var statusErr *downloadStatusError
	if errors.As(err, &statusErr) && statusErr.status == http.StatusNotFound {
		return nil, fmt.Errorf("%w: release 缺少 %s%s: %v", errUpgradeRefused, checksumsFileName, checksumSignatureSuffix, err)
	}
	if err != nil {
		return nil, err
	}
	if err := releasesig.Verify(sums, signature, key, releasesig.ChecksumsTrustedComment(targetVersion)); err != nil {
		return nil, fmt.Errorf("%w: %s 的 minisign 验证失败: %v", errUpgradeRefused, checksumsFileName, err)
	}
	return sums, nil
}

func (u *Upgrader) attempt(ctx context.Context, targetVersion string) error {
	key, err := u.trustedKey()
	if err != nil {
		return err
	}
	asset := releaseAssetName(targetVersion, runtime.GOOS, runtime.GOARCH)
	tag := "v" + targetVersion
	sumsContent, err := u.verifiedChecksums(ctx, tag, targetVersion, key)
	if err != nil {
		return err
	}
	stageDir, err := os.MkdirTemp("", "nexterm-agent-upgrade-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stageDir) }()
	expected := parseSHA256SUMS(sumsContent)[asset]
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("%s 中没有 %s 的合法校验和条目, 拒绝升级", checksumsFileName, asset)
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return fmt.Errorf("%s 中 %s 的校验和不是合法 sha256, 拒绝升级", checksumsFileName, asset)
	}
	archivePath := filepath.Join(stageDir, asset)
	if err := u.download(ctx, tag+"/"+asset, archivePath); err != nil {
		return err
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

type downloadStatusError struct {
	url    string
	status int
	text   string
}

func (e *downloadStatusError) Error() string { return fmt.Sprintf("download %s: %s", e.url, e.text) }

func (u *Upgrader) open(ctx context.Context, ref string) (*http.Response, error) {
	client := u.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: upgradeDownloadTimeout}
	}
	url := strings.TrimRight(u.BaseURL, "/") + "/" + ref
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, &downloadStatusError{url: url, status: response.StatusCode, text: response.Status}
	}
	return response, nil
}

// fetch reads a small release file into memory, failing when it exceeds limit bytes.
func (u *Upgrader) fetch(ctx context.Context, ref string, limit int64) ([]byte, error) {
	response, err := u.open(ctx, ref)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	content, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", ref, err)
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", ref, limit)
	}
	return content, nil
}

func (u *Upgrader) download(ctx context.Context, ref, dest string) error {
	response, err := u.open(ctx, ref)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(out, io.LimitReader(response.Body, maxUpgradeDownloadBytes+1))
	if copyErr == nil && written > maxUpgradeDownloadBytes {
		copyErr = fmt.Errorf("response exceeds %d bytes", maxUpgradeDownloadBytes)
	}
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dest)
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

// windowsRestartCommand restarts the scheduled task after a successful swap-in: the agent exits with ExitOK, which the task's
// RestartOnFailure policy does not restart. A failing schtasks must not abort the script, so errors are swallowed.
func windowsRestartCommand() string {
	return "try { & schtasks.exe /Run /TN " + psQuote(windowsTaskName) + " *> $null } catch {}"
}

// renderReplaceScriptWindows is the windows counterpart of renderReplaceScriptUnix:
// powershell deferred replacement, rolling back to the old binary on failure. restartCmd runs only after a successful swap-in.
func renderReplaceScriptWindows(newPath, targetPath string, delaySeconds int, restartCmd string) string {
	var b strings.Builder
	b.WriteString("Start-Sleep -Seconds " + strconv.Itoa(delaySeconds) + "\n")
	b.WriteString("$nxTarget = " + psQuote(filepath.Clean(targetPath)) + "\n")
	b.WriteString("$nxNew = " + psQuote(filepath.Clean(newPath)) + "\n")
	b.WriteString("$nxBak = " + psQuote(filepath.Clean(targetPath)+".nexterm-old") + "\n")
	b.WriteString("try { Move-Item -LiteralPath $nxTarget -Destination $nxBak -Force -ErrorAction Stop } catch {}\n")
	b.WriteString("try {\n")
	b.WriteString("  Move-Item -LiteralPath $nxNew -Destination $nxTarget -Force -ErrorAction Stop\n")
	b.WriteString("  Remove-Item -LiteralPath $nxBak -Force -ErrorAction SilentlyContinue\n")
	b.WriteString("  " + restartCmd + "\n")
	b.WriteString("} catch {\n")
	b.WriteString("  try { Move-Item -LiteralPath $nxBak -Destination $nxTarget -Force -ErrorAction Stop } catch {}\n")
	b.WriteString("}\n")
	b.WriteString("Remove-Item -LiteralPath $PSCommandPath -Force -ErrorAction SilentlyContinue\n")
	return b.String()
}
