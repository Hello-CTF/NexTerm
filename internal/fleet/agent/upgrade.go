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
	"strings"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/idleread"
	"github.com/Hello-CTF/NexTerm/internal/platform/psquote"
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
	defaultUpgradeMaxAttempts = 3
	defaultUpgradeBackoff     = 5 * time.Second
	// replaceScriptDelaySeconds is the delay the update command announces before the swap; the replace script waits
	// for the upgrading process to exit rather than sleeping a fixed time.
	replaceScriptDelaySeconds = 3
	// replaceWaitTimeoutSeconds bounds how long the replace script waits for the upgrading process to exit.
	replaceWaitTimeoutSeconds = 60
	// replaceHealthTimeoutSeconds bounds how long the replace script waits for the restarted agent to report a
	// successful control-channel connection before it restores the previous binary.
	replaceHealthTimeoutSeconds = 120
	// upgradeLogName is the file under the data dir where the replace script records its restart outcome.
	upgradeLogName = "upgrade.log"
	// upgradeLogRestartExit prefixes the recorded exit code of the restart command in the upgrade log.
	upgradeLogRestartExit = "restart exit code"
)

// upgradeDownloadIdleTimeout fails a release download that receives no data for this long, and
// upgradeDownloadMaxDuration caps one download however steadily it progresses; these are vars for tests.
var (
	upgradeDownloadIdleTimeout = 60 * time.Second
	upgradeDownloadMaxDuration = 2 * time.Hour
	// stagedVersionTimeout bounds running the staged binary's version command.
	stagedVersionTimeout = 10 * time.Second
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
	// LogPath is the file the deferred replace script appends its outcome to; empty disables the script log.
	LogPath string

	sleep func(ctx context.Context, d time.Duration) bool
	// verifyStaged replaces checkStagedVersion in tests whose staged binary cannot run.
	verifyStaged    func(ctx context.Context, path, targetVersion string) error
	scheduleReplace func(newPath, targetPath string) error
	// scheduleReplaceDetached replaces scheduleReplace in tests that need to control whether the script left the caller's job.
	scheduleReplaceDetached func(newPath, targetPath string) (bool, error)
	replaceDetached         atomic.Bool
}

// LastReplaceDetached reports whether the most recently scheduled replace script runs outside the caller's Windows job
// object, so ending the scheduled task cannot terminate it. It is false before a replacement is scheduled.
func (u *Upgrader) LastReplaceDetached() bool { return u.replaceDetached.Load() }

// upgradeLogPathIn returns the upgrade log path under dir, or "" when dir is not an existing writable directory:
// a log that cannot be opened must not be handed to the replace script.
func upgradeLogPathIn(dir string) string {
	if dir == "" {
		return ""
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ""
	}
	if err := checkWritableDir(dir); err != nil {
		return ""
	}
	return filepath.Join(dir, upgradeLogName)
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
		return fmt.Errorf("SHA-256 校验失败 (%s): 期望 %s, 实际 %s, 拒绝升级", asset, expected, actual)
	}
	target := u.Executable
	staged := filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".nexterm-new")
	if err := extractAgentBinary(archivePath, staged, agentBinaryName(runtime.GOOS)); err != nil {
		return err
	}
	verify := u.verifyStaged
	if verify == nil {
		verify = checkStagedVersion
	}
	if err := verify(ctx, staged, targetVersion); err != nil {
		_ = os.Remove(staged)
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return err
	}
	schedule := u.scheduleReplaceDetached
	if schedule == nil {
		logPath := u.LogPath
		schedule = func(newPath, targetPath string) (bool, error) {
			if u.scheduleReplace != nil {
				return false, u.scheduleReplace(newPath, targetPath)
			}
			return scheduleDeferredReplace(newReplacePlan(newPath, targetPath, targetVersion, logPath, os.Getpid()))
		}
	}
	detached, err := schedule(staged, target)
	u.replaceDetached.Store(err == nil && detached)
	if err != nil {
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

// get requests one release file and hands its body to read. The request fails when no data arrives for
// upgradeDownloadIdleTimeout (waiting for the response headers included) or after upgradeDownloadMaxDuration overall,
// so a slow but steady link still completes while a stalled one fails promptly.
func (u *Upgrader) get(ctx context.Context, ref string, read func(body io.Reader) error) error {
	client := u.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	url := strings.TrimRight(u.BaseURL, "/") + "/" + ref
	capped, cancelCap := context.WithTimeout(ctx, upgradeDownloadMaxDuration)
	defer cancelCap()
	idleCtx, watchdog, cancelIdle := idleread.WithIdleTimeout(capped, upgradeDownloadIdleTimeout)
	defer cancelIdle()
	request, err := http.NewRequestWithContext(idleCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, idleread.Cause(idleCtx, err))
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return &downloadStatusError{url: url, status: response.StatusCode, text: response.Status}
	}
	watchdog.Touch()
	return idleread.Cause(idleCtx, read(watchdog.Reader(response.Body)))
}

// fetch reads a small release file into memory, failing when it exceeds limit bytes.
func (u *Upgrader) fetch(ctx context.Context, ref string, limit int64) ([]byte, error) {
	var content []byte
	err := u.get(ctx, ref, func(body io.Reader) error {
		var readErr error
		content, readErr = io.ReadAll(io.LimitReader(body, limit+1))
		if readErr != nil {
			return fmt.Errorf("read %s: %w", ref, readErr)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", ref, limit)
	}
	return content, nil
}

func (u *Upgrader) download(ctx context.Context, ref, dest string) error {
	return u.get(ctx, ref, func(body io.Reader) error {
		out, err := os.Create(dest)
		if err != nil {
			return err
		}
		written, copyErr := io.Copy(out, io.LimitReader(body, maxUpgradeDownloadBytes+1))
		if copyErr == nil && written > maxUpgradeDownloadBytes {
			copyErr = fmt.Errorf("response exceeds %d bytes", maxUpgradeDownloadBytes)
		}
		closeErr := out.Close()
		if copyErr != nil {
			_ = os.Remove(dest)
			return fmt.Errorf("write %s: %w", dest, copyErr)
		}
		return closeErr
	})
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
	return psquote.Quote(value)
}

// appendUpgradeLog best-effort appends one line to the upgrade log; logging must never fail an upgrade.
func appendUpgradeLog(path, line string) {
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(time.Now().Format(time.RFC3339) + " " + line + "\n")
	_ = f.Close()
}
