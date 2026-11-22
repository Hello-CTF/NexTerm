package update

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/idleread"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/netproxy"
	"github.com/Hello-CTF/NexTerm/internal/platform"
)

const apiTimeout = 20 * time.Second

// An update download fails once it receives no data for downloadIdleTimeout, and after downloadMaxDuration however
// steadily it progresses, so a slow link still completes; these are vars for tests.
var (
	downloadIdleTimeout = 60 * time.Second
	downloadMaxDuration = 2 * time.Hour
)

type Status struct {
	Available         bool   `json:"available"`
	CurrentVersion    string `json:"currentVersion"`
	Version           string `json:"version"`
	Notes             string `json:"notes"`
	Prerelease        bool   `json:"prerelease"`
	AssetName         string `json:"assetName"`
	AssetSize         int64  `json:"assetSize"`
	CanInstall        bool   `json:"canInstall"`
	UnavailableReason string `json:"unavailableReason"`
	// ReleaseURL is the release page of Version; DownloadURL is its package for this platform, for a manual install
	// when the app cannot install it itself.
	ReleaseURL  string `json:"releaseUrl,omitempty"`
	DownloadURL string `json:"downloadUrl,omitempty"`
	// PendingVersion names a newer release that is published without this platform's package or its SHA256SUMS
	// yet (a release still uploading). It is skipped rather than reported as a failed check.
	PendingVersion string `json:"pendingVersion,omitempty"`
	// Channel names how this platform receives updates when the app cannot install them itself (see
	// externalChannel). It is empty where in-app updates apply, and it is not a failure.
	Channel string `json:"channel"`
	// ReleaseChannel is the update channel in effect for this check (stable or rc); RunningPrerelease reports
	// whether the running build is itself a prerelease so the UI can explain the no-downgrade rule.
	ReleaseChannel    ReleaseChannel `json:"releaseChannel"`
	RunningPrerelease bool           `json:"runningPrerelease"`
}

// Result reports an install or a restart. An install never restarts the app: Installed means the new version is
// on disk and applies after Restart; only Restart sets Restarted.
type Result struct {
	Installed         bool   `json:"installed"`
	Restarted         bool   `json:"restarted"`
	Version           string `json:"version"`
	UnavailableReason string `json:"unavailableReason"`
}

// Install states reported by State besides the progress phases.
const (
	StateIdle      = "idle"
	StateInstalled = "installed"
	StateFailed    = "failed"
)

// InstallState is the progress of the current or last install, kept by the process so a reloaded interface can
// pick it up. Phase is idle, a progress phase while an install runs, installed or failed. InstalledVersion stays
// set once a version is installed and waits for a restart, even when a later attempt fails.
type InstallState struct {
	Phase            string `json:"phase"`
	Version          string `json:"version"`
	TaskID           string `json:"taskId"`
	Transferred      int64  `json:"transferred"`
	Total            int64  `json:"total"`
	Error            string `json:"error"`
	InstalledVersion string `json:"installedVersion"`
}

// progressInterval bounds how often intermediate download progress is emitted; phase changes, completion and
// errors are always emitted.
const progressInterval = 200 * time.Millisecond

// Manager is the in-app update entry point; every check failure degrades to Status.UnavailableReason
// instead of returning an error to the caller. RestartFunc is injected by the wiring layer (desktop restarts the wails app); it is nil on the server.
type Manager struct {
	CurrentVersion string
	GOOS           string
	GOARCH         string
	CanInstall     bool
	ExePath        string
	APIURL         string
	HTTPClient     *http.Client
	// MinisignPublicKey overrides the build-time trust root; configuring a key makes signed checksums mandatory.
	MinisignPublicKey string
	// RequireSignedChecksums also fails closed when no trusted public key is configured.
	RequireSignedChecksums bool
	Emit                   func(context.Context, Progress)
	RestartFunc            func(context.Context, string) error
	// ChannelProvider supplies the release channel on every Check and Install, so a change applies at once.
	// Nil means stable.
	ChannelProvider func(context.Context) ReleaseChannel

	installing atomic.Bool
	// now is the clock that throttles progress events; nil means time.Now.
	now func() time.Time

	mu            sync.Mutex
	state         InstallState
	restartTarget string
	lastEmit      time.Time
}

func (m *Manager) releasesAPI() string {
	if m.APIURL != "" {
		return m.APIURL
	}
	return DefaultReleasesAPI
}

// defaultClient checks and downloads updates through the environment's
// proxy, or the system's on macOS where an app started from the Dock has no
// proxy variables.
var defaultClient = &http.Client{Transport: netproxy.Transport()}

func (m *Manager) httpClient() *http.Client {
	if m.HTTPClient != nil {
		return m.HTTPClient
	}
	return defaultClient
}

func (m *Manager) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

func (m *Manager) emit(ctx context.Context, progress Progress) {
	if m.Emit != nil {
		m.Emit(ctx, progress)
	}
}

// State returns the current or last install, so a reloaded interface can resume showing it.
func (m *Manager) State() InstallState {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.state
	if state.Phase == "" {
		state.Phase = StateIdle
	}
	return state
}

func (m *Manager) updateState(change func(*InstallState)) {
	m.mu.Lock()
	change(&m.state)
	m.mu.Unlock()
}

// report records progress in the state and emits it; intermediate download ticks are throttled to
// progressInterval, every other event is emitted.
func (m *Manager) report(ctx context.Context, progress Progress, throttle bool) {
	m.mu.Lock()
	m.state.Phase = progress.Phase
	m.state.TaskID = progress.TaskID
	m.state.Transferred = progress.Transferred
	m.state.Total = progress.Total
	if progress.Error != "" {
		m.state.Phase = StateFailed
		m.state.Error = progress.Error
	}
	now := m.clock()
	if throttle && !m.lastEmit.IsZero() && now.Sub(m.lastEmit) < progressInterval {
		m.mu.Unlock()
		return
	}
	m.lastEmit = now
	m.mu.Unlock()
	m.emit(ctx, progress)
}

func (m *Manager) releaseChannel(ctx context.Context) ReleaseChannel {
	if m.ChannelProvider == nil {
		return ChannelStable
	}
	if channel, ok := ParseReleaseChannel(string(m.ChannelProvider(ctx))); ok {
		return channel
	}
	return ChannelStable
}

func (m *Manager) Check(ctx context.Context) Status {
	status, _ := m.check(ctx, m.releaseChannel(ctx))
	return status
}

// resolved is the release a check settled on, with the package and the verified checksums an install uses.
type resolved struct {
	asset Asset
	sums  map[string]string
}

// check finds the newest release on the channel that is newer than the running version and complete for this
// platform. A newer release whose asset list lacks this platform's package or SHA256SUMS (one still being
// published) is skipped and named in PendingVersion; any failure to fetch or verify the checksums of the chosen
// release, a missing signature included, is a failed check, never a reason to fall back to an older release.
func (m *Manager) check(ctx context.Context, channel ReleaseChannel) (Status, *resolved) {
	status := Status{
		CurrentVersion: m.CurrentVersion, CanInstall: m.CanInstall,
		ReleaseChannel: channel, RunningPrerelease: runningPrerelease(m.CurrentVersion),
	}
	fail := func(reason string) (Status, *resolved) {
		status.UnavailableReason = reason
		return status, nil
	}
	if err := platformSupported(m.GOOS, m.GOARCH); err != nil {
		// Platforms that update through a store or a manual download are not a failed check, and there is
		// nothing to ask the release API about.
		if channel := externalChannel(m.GOOS); channel != "" {
			status.Channel = channel
			return status, nil
		}
		return fail(err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	releases, err := m.fetchReleases(ctx)
	if err != nil {
		return fail(fmt.Errorf("检查更新失败: %w", err).Error())
	}
	for _, release := range candidateReleases(releases, m.CurrentVersion, channel) {
		asset, ok := matchAsset(release, m.GOOS, m.GOARCH)
		if !ok || !releaseHasChecksums(release) {
			if status.PendingVersion == "" {
				status.PendingVersion = release.version()
			}
			continue
		}
		sums, err := m.fetchChecksums(ctx, release)
		if err != nil {
			return fail(fmt.Errorf("获取校验和失败: %w", err).Error())
		}
		if _, ok := sums[asset.Name]; !ok {
			return fail(fmt.Sprintf("%s 缺少 %s 的校验和", checksumsAssetName, asset.Name))
		}
		status.Available = true
		status.Version = release.version()
		status.Notes = release.Body
		status.Prerelease = release.Prerelease
		status.AssetName = asset.Name
		status.AssetSize = asset.Size
		status.ReleaseURL = release.HTMLURL
		status.DownloadURL = asset.BrowserDownloadURL
		if status.CanInstall {
			if reason := m.installBlocker(); reason != "" {
				status.CanInstall = false
				status.UnavailableReason = reason
			}
		}
		return status, &resolved{asset: asset, sums: sums}
	}
	return status, nil
}

// releaseHasChecksums reports whether the release lists SHA256SUMS. A missing signature is not treated as a
// release still being published: when signatures are required it fails the check.
func releaseHasChecksums(release Release) bool {
	asset, ok := findReleaseAsset(release, checksumsAssetName)
	return ok && asset.BrowserDownloadURL != ""
}

// Install downloads, verifies and installs the newest release; it does not restart the app. The install runs to
// completion even when the caller goes away (a reloaded interface), and State reports it meanwhile. An install of
// the version already installed and waiting for a restart returns at once.
func (m *Manager) Install(ctx context.Context, version string) (Result, error) {
	if !m.CanInstall {
		return Result{UnavailableReason: "服务端模式不支持桌面应用内安装"}, nil
	}
	if !m.installing.CompareAndSwap(false, true) {
		return Result{}, userErrorf(UserErrorBadParam, "正在安装更新，请稍候")
	}
	defer m.installing.Store(false)
	ctx = context.WithoutCancel(ctx)
	channel := m.releaseChannel(ctx)
	status, found := m.check(ctx, channel)
	if !status.Available {
		reason := status.UnavailableReason
		if reason == "" {
			reason = "当前已是最新版本"
		}
		return Result{UnavailableReason: reason}, nil
	}
	if !status.CanInstall {
		return Result{UnavailableReason: status.UnavailableReason}, nil
	}
	if version != "" && version != status.Version {
		return Result{}, userErrorf(UserErrorBadParam, "待安装版本 %s 与最新版本 %s 不一致", version, status.Version)
	}
	if m.State().InstalledVersion == status.Version {
		return Result{Installed: true, Version: status.Version}, nil
	}
	asset := found.asset
	taskID := ids.New()
	m.updateState(func(state *InstallState) {
		state.Version = status.Version
		state.Error = ""
	})
	failed := func(phase string, transferred, total int64, err error) (Result, error) {
		m.report(ctx, Progress{TaskID: taskID, Phase: phase, Transferred: transferred, Total: total, Done: true, Error: err.Error()}, false)
		return Result{}, err
	}
	m.report(ctx, Progress{TaskID: taskID, Phase: PhaseDownload, Total: asset.Size}, false)
	directory, err := os.MkdirTemp(platform.TempDir(), "nexterm-update-")
	if err != nil {
		return failed(PhaseDownload, 0, asset.Size, err)
	}
	defer os.RemoveAll(directory)
	archive := filepath.Join(directory, asset.Name)
	if err := m.download(ctx, asset, archive, taskID); err != nil {
		return failed(PhaseDownload, 0, asset.Size, err)
	}
	m.report(ctx, Progress{TaskID: taskID, Phase: PhaseVerify, Transferred: asset.Size, Total: asset.Size}, false)
	if err := verifyChecksumFile(archive, asset.Name, found.sums); err != nil {
		_ = os.Remove(archive)
		return failed(PhaseVerify, asset.Size, asset.Size, err)
	}
	m.report(ctx, Progress{TaskID: taskID, Phase: PhaseInstall}, false)
	target, err := m.applyInstall(ctx, archive)
	if err != nil {
		return failed(PhaseInstall, 0, 0, err)
	}
	m.mu.Lock()
	m.restartTarget = target
	m.state = InstallState{Phase: StateInstalled, Version: status.Version, TaskID: taskID, InstalledVersion: status.Version}
	m.mu.Unlock()
	m.emit(ctx, Progress{TaskID: taskID, Phase: PhaseInstall, Done: true})
	return Result{Installed: true, Version: status.Version}, nil
}

func (m *Manager) download(ctx context.Context, asset Asset, dest, taskID string) error {
	capped, cancelCap := context.WithTimeout(ctx, downloadMaxDuration)
	defer cancelCap()
	ctx, watchdog, cancel := idleread.WithIdleTimeout(capped, downloadIdleTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "NexTerm-updater")
	response, err := m.httpClient().Do(request)
	if err != nil {
		return userErrorf(UserErrorIO, "下载 %s 失败: %w", asset.Name, idleread.Cause(ctx, err))
	}
	watchdog.Touch()
	body := watchdog.Reader(response.Body)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return userErrorf(UserErrorIO, "下载 %s 返回 %s", asset.Name, response.Status)
	}
	total := asset.Size
	if response.ContentLength > 0 {
		total = response.ContentLength
	}
	file, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	buffer := make([]byte, 256<<10)
	var transferred int64
	m.report(ctx, Progress{TaskID: taskID, Phase: PhaseDownload, Total: total}, false)
	for {
		count, readErr := body.Read(buffer)
		if count > 0 {
			if _, err := file.Write(buffer[:count]); err != nil {
				return userErrorf(UserErrorIO, "写入 %s 失败: %w", asset.Name, err)
			}
			transferred += int64(count)
			m.report(ctx, Progress{TaskID: taskID, Phase: PhaseDownload, Transferred: transferred, Total: total}, true)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return userErrorf(UserErrorIO, "下载 %s 失败: %w", asset.Name, idleread.Cause(ctx, readErr))
		}
	}
	m.report(ctx, Progress{TaskID: taskID, Phase: PhaseDownload, Transferred: transferred, Total: total, Done: true}, false)
	return nil
}

func (m *Manager) Restart(ctx context.Context) Result {
	if m.RestartFunc == nil {
		return Result{UnavailableReason: "服务端模式不支持应用内重启"}
	}
	if m.installing.Load() {
		return Result{UnavailableReason: "正在安装更新，请等待安装完成后再重启"}
	}
	m.mu.Lock()
	target := m.restartTarget
	m.mu.Unlock()
	if target == "" {
		target = m.ExePath
	}
	if target == "" {
		return Result{UnavailableReason: "无法确定当前可执行文件路径"}
	}
	if err := m.RestartFunc(ctx, target); err != nil {
		return Result{UnavailableReason: "重启失败: " + err.Error()}
	}
	return Result{Restarted: true}
}

// installTargetFor decides where the new exe lands after install: the registry-registered install target wins,
// otherwise the NSIS default directory; inPlace true means the currently running exe is the install target itself
// and can be replaced by renaming; false means it is a loose copy, left alone, with a restart into the installed version after install.
func installTargetFor(exePath, registered, fallbackDir, exeName string) (target string, inPlace bool) {
	target = registered
	if target == "" && fallbackDir != "" {
		target = filepath.Join(fallbackDir, exeName)
	}
	if target == "" {
		return exePath, true
	}
	target = filepath.Clean(target)
	return target, strings.EqualFold(target, filepath.Clean(exePath))
}
