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

// ReleaseBaseURL 是自升级唯一的官方发布源: 域名硬编码, server 只在
// version_mismatch 帧里上报版本号, 下载地址绝不由 server 下发 (供应链红线)。
const ReleaseBaseURL = "https://github.com/Hello-CTF/NexTerm/releases/download"

const (
	checksumsFileName         = "SHA256SUMS"
	maxReleaseVersionLength   = 64
	upgradeDownloadTimeout    = 10 * time.Minute
	defaultUpgradeMaxAttempts = 3
	defaultUpgradeBackoff     = 5 * time.Second
	replaceScriptDelaySeconds = 3
)

// errUpgradeStaged 表示新二进制已下载校验并安排了延迟替换: 进程退出后由
// 替换脚本完成二进制切换并拉起服务, cli 层据此以 ExitOK 结束本进程。
var errUpgradeStaged = errors.New("agent self-upgrade staged")

// Upgrader 把运行中的 agent 自升级到 server 上报的版本: 从固定官方 release
// 源下载对应平台资产, 用同 release 的 SHA256SUMS 校验 (与 install-device.sh
// 同一校验链语义), 解包后安排延迟替换。下载/校验/暂存失败由 Upgrade 按指数
// 退避重试, 重试耗尽快照原样保留旧二进制。
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

// Upgrade 下载并暂存 targetVersion 对应的 agent 二进制, 成功后安排延迟替换
// 脚本并返回 nil; 重试耗尽返回带尝试次数的错误, 旧二进制始终原样可运行。
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
	return "NexTerm-agent_" + version + "_" + goos + "_" + goarch + ext
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

// extractAgentBinary 从发布包解出根目录唯一的 agent 二进制到 destPath
// (0755): tar.gz 对应 linux/darwin 资产, zip 对应 windows 资产, 与
// scripts/pack-agent.sh 的产物布局一致。
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

// unixRestartCommand 是替换成功后由脚本显式拉起服务的命令 (darwin=launchctl
// kickstart, linux=systemctl --user restart); 回滚路径不拉起, 停留在待人工
// 状态, 服务管理器不会形成重启循环。未装服务时命令静默失败, 下次手动启动即
// 新二进制。
func unixRestartCommand(goos string) string {
	if goos == "darwin" {
		return `launchctl kickstart "gui/$(id -u)/` + launchdLabel + `" 2>/dev/null || true`
	}
	return "systemctl --user restart " + systemdUnitName + " 2>/dev/null || true"
}

// renderReplaceScriptUnix 生成延迟替换脚本: 运行中的进程不能覆盖自身, 脚本
// 在进程退出后 (sleep) 把新二进制换入目标路径。target->backup 与 new->target
// 都是同目录 rename; 换入失败即用 backup 回滚, 任何路径都不留无二进制状态。
// restartCmd 只在换入成功后执行 (见 unixRestartCommand)。
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

// renderReplaceScriptWindows 是 renderReplaceScriptUnix 的 windows 对应物:
// powershell 延迟替换, 失败回滚旧二进制; 替换成功后若存在 NexTermAgent 服务
// 则 sc.exe start 拉起 (服务未装或启动失败均静默, 不构成循环)。
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
