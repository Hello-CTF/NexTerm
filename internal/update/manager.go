package update

import (
	"context"
	"errors"
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
	// PendingVersion names a newer release published within pendingWindow without this platform's package or its
	// SHA256SUMS yet (a release still uploading). It is skipped rather than reported as a failed check.
	PendingVersion string `json:"pendingVersion,omitempty"`
	// IncompleteVersion names a newer release, published longer ago than pendingWindow (or at an unknown time),
	// that has no package or SHA256SUMS for this platform. It is skipped like a pending one.
	IncompleteVersion string `json:"incompleteVersion,omitempty"`
	// Channel names how this platform receives updates when the app cannot install them itself (see
	// externalChannel). It is empty where in-app updates apply, and it is not a failure.
	Channel string `json:"channel"`
	// ReleaseChannel is the update channel in effect for this check (stable or rc); RunningPrerelease reports
	// whether the running build is itself a prerelease so the UI can explain the no-downgrade rule.
	ReleaseChannel    ReleaseChannel `json:"releaseChannel"`
	RunningPrerelease bool           `json:"runningPrerelease"`
}

// Result reports an install or a restart. Install never touches the installed app: Ready means the package is
// downloaded, verified and staged, and it is applied by the next Restart. Restart sets Installed once it applied a
// staged package and Restarted once the app relaunches.
type Result struct {
	Ready bool `json:"ready"`
	// Failure tells a Restart that failed apart: FailureApply installed nothing, FailureRelaunch means the
	// package (if any) is installed and only the relaunch failed.
	Failure           string `json:"failure,omitempty"`
	Installed         bool   `json:"installed"`
	Restarted         bool   `json:"restarted"`
	Version           string `json:"version"`
	UnavailableReason string `json:"unavailableReason"`
}

// Restart failure kinds reported in Result.Failure.
const (
	FailureApply    = "apply"
	FailureRelaunch = "relaunch"
)

// Install states reported by State besides the progress phases.
const (
	StateIdle = "idle"
	// StateReady: a verified package is staged and waits for Restart to apply it.
	StateReady = "ready"
	// StateInstalled: Restart applied the package but could not relaunch the app.
	StateInstalled = "installed"
	StateFailed    = "failed"
)

// InstallState is the progress of the current or last install, kept by the process (and, for a staged package, on
// disk) so a reloaded interface or the next launch can pick it up. Phase is idle, a progress phase while a download
// or an apply runs, ready, installed or failed. ReadyVersion names the staged package, and stays set when a later
// attempt fails.
type InstallState struct {
	Phase        string `json:"phase"`
	Version      string `json:"version"`
	TaskID       string `json:"taskId"`
	Transferred  int64  `json:"transferred"`
	Total        int64  `json:"total"`
	Error        string `json:"error"`
	ReadyVersion string `json:"readyVersion"`
}

// progressInterval bounds how often intermediate download progress is emitted; phase changes, completion and
// errors are always emitted.
const progressInterval = 200 * time.Millisecond

// pendingWindow is how long after publishing a release missing this platform's assets counts as still uploading.
const pendingWindow = 6 * time.Hour

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
	// StageDir keeps the verified package between Install and the Restart that applies it, so it survives the
	// process; it must not be a directory removed on exit. Install refuses to run without one.
	StageDir string

	installing atomic.Bool
	stageOnce  sync.Once
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

// State returns the current or last install, so a reloaded interface can resume showing it; a package staged by an
// earlier run that was never restarted is reported as ready.
func (m *Manager) State() InstallState {
	m.loadStaged()
	m.dropOffChannel(context.Background())
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
	asset     Asset
	sums      map[string]string
	checksums checksums
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
			// Releases are published from a complete draft, so only a release published moments ago can still be
			// uploading; an older one stays without this platform's package.
			if status.PendingVersion == "" && status.IncompleteVersion == "" {
				if !release.PublishedAt.IsZero() && m.clock().Sub(release.PublishedAt) < pendingWindow {
					status.PendingVersion = release.version()
				} else {
					status.IncompleteVersion = release.version()
				}
			}
			continue
		}
		fetched, err := m.fetchChecksums(ctx, release)
		if err != nil {
			return fail(fmt.Errorf("获取校验和失败: %w", err).Error())
		}
		if _, ok := fetched.sums[asset.Name]; !ok {
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
		return status, &resolved{asset: asset, sums: fetched.sums, checksums: fetched}
	}
	return status, nil
}

// releaseHasChecksums reports whether the release lists SHA256SUMS. A missing signature is not treated as a
// release still being published: when signatures are required it fails the check.
func releaseHasChecksums(release Release) bool {
	asset, ok := findReleaseAsset(release, checksumsAssetName)
	return ok && asset.BrowserDownloadURL != ""
}

// Install downloads and verifies the newest release and stages it for the next Restart; nothing installed is
// touched, so the running app never runs against new files. The download runs to completion even when the caller
// goes away (a reloaded interface), and State reports it meanwhile. A package already staged for that version is
// verified again and reused instead of downloaded; one for another version is discarded once the new one is staged.
func (m *Manager) Install(ctx context.Context, version string) (Result, error) {
	if !m.CanInstall {
		return Result{UnavailableReason: "服务端模式不支持桌面应用内安装"}, nil
	}
	if m.StageDir == "" {
		return Result{UnavailableReason: "未配置更新暂存目录，无法在应用内安装"}, nil
	}
	if !m.installing.CompareAndSwap(false, true) {
		return Result{}, userErrorf(UserErrorBadParam, "正在安装更新，请稍候")
	}
	defer m.installing.Store(false)
	m.loadStaged()
	ctx = context.WithoutCancel(ctx)
	// The install is reported active from the start, before the release check, so an interface reloaded during
	// the check finds it running; a check that ends the attempt puts the previous state back.
	previous := m.State()
	taskID := ids.New()
	m.updateState(func(state *InstallState) {
		state.Phase = PhaseDownload
		state.Version = version
		state.TaskID = taskID
		state.Transferred = 0
		state.Total = 0
		state.Error = ""
	})
	restore := func() {
		m.mu.Lock()
		m.state = previous
		m.mu.Unlock()
	}
	channel := m.releaseChannel(ctx)
	status, found := m.check(ctx, channel)
	if !status.Available {
		restore()
		reason := status.UnavailableReason
		if reason == "" {
			reason = "当前已是最新版本"
		}
		return Result{UnavailableReason: reason}, nil
	}
	if !status.CanInstall {
		restore()
		return Result{UnavailableReason: status.UnavailableReason}, nil
	}
	if version != "" && version != status.Version {
		restore()
		return Result{}, userErrorf(UserErrorBadParam, "待安装版本 %s 与最新版本 %s 不一致", version, status.Version)
	}
	asset := found.asset
	// Only a package named exactly as the release build names it can be staged: the restart accepts no other.
	if !(stagedPackage{Version: status.Version, AssetName: asset.Name}).validFor(m.GOOS, m.GOARCH) {
		restore()
		return Result{}, userErrorf(UserErrorIO, "安装包 %s 的名称与版本 %s 不符", asset.Name, status.Version)
	}
	m.dropOffChannel(ctx)
	if staged, ok := m.stagedPackage(); ok && staged.Version == status.Version && staged.AssetName == asset.Name {
		if digest, err := m.stagedDigest(staged); err == nil && digest == found.sums[asset.Name] &&
			hashMatches(staged.path(m.stageDir()), digest) == nil {
			m.markReady(staged.Version)
			return Result{Ready: true, Version: staged.Version}, nil
		}
	}
	m.updateState(func(state *InstallState) {
		state.Version = status.Version
	})
	failed := func(phase string, transferred, total int64, err error) (Result, error) {
		m.report(ctx, Progress{TaskID: taskID, Phase: phase, Transferred: transferred, Total: total, Done: true, Error: err.Error()}, false)
		return Result{}, err
	}
	m.report(ctx, Progress{TaskID: taskID, Phase: PhaseDownload, Total: asset.Size}, false)
	stageDir := m.stageDir()
	if err := prepareStageDir(stageDir); err != nil {
		return failed(PhaseDownload, 0, asset.Size, err)
	}
	directory, err := os.MkdirTemp(stageDir, ".download-")
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
		return failed(PhaseVerify, asset.Size, asset.Size, err)
	}
	if err := m.checkPackage(ctx, archive); err != nil {
		return failed(PhaseVerify, asset.Size, asset.Size, err)
	}
	staged := stagedPackage{Version: status.Version, AssetName: asset.Name}
	if err := stagePackage(stageDir, archive, staged, found.checksums); err != nil {
		// Staging removed the previous package first, so nothing is ready any more.
		m.discardStaged()
		return failed(PhaseVerify, asset.Size, asset.Size, userErrorf(UserErrorIO, "保存更新包失败: %w", err))
	}
	m.mu.Lock()
	m.state = InstallState{Phase: StateReady, Version: status.Version, TaskID: taskID, ReadyVersion: status.Version}
	m.mu.Unlock()
	m.emit(ctx, Progress{TaskID: taskID, Phase: PhaseVerify, Transferred: asset.Size, Total: asset.Size, Done: true})
	return Result{Ready: true, Version: status.Version}, nil
}

func (m *Manager) markReady(version string) {
	m.mu.Lock()
	m.state = InstallState{Phase: StateReady, Version: version, ReadyVersion: version}
	m.mu.Unlock()
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

// Restart applies a staged package, then relaunches the app into it: the same order as an install followed by a
// restart, done only when the user chose to restart. The package is verified again against the signed checksum
// manifest staged with it, bound to the staged version, and copied into a private directory while it is hashed;
// the installer runs on that copy. A package that fails verification, is not newer than the running build, does
// not belong to this platform or is a release candidate off the selected channel is refused and discarded. A
// failed apply leaves the app as it was and keeps the package; a successful one removes it.
func (m *Manager) Restart(ctx context.Context) Result {
	if m.RestartFunc == nil {
		return Result{UnavailableReason: "服务端模式不支持应用内重启"}
	}
	ctx = context.WithoutCancel(ctx)
	if !m.installing.CompareAndSwap(false, true) {
		return Result{UnavailableReason: "正在安装更新，请等待安装完成后再重启"}
	}
	defer m.installing.Store(false)
	m.loadStaged()
	applyFailed := func(reason string) Result {
		m.updateState(func(state *InstallState) {
			state.Phase = StateFailed
			state.Error = reason
		})
		return Result{Failure: FailureApply, UnavailableReason: reason}
	}
	m.mu.Lock()
	expected := m.state.ReadyVersion
	m.mu.Unlock()
	staged, ok := m.stagedPackage()
	if ok {
		if problem := m.stagedProblem(ctx, staged); problem != "" {
			m.discardStaged()
			return applyFailed(problem)
		}
	} else if expected != "" {
		// The interface offered an install that is no longer staged: relaunching would report an update that
		// never happened.
		m.discardStaged()
		return applyFailed("已下载的更新包不见了，请重新下载")
	}
	result := Result{}
	if ok {
		target, err := m.applyStaged(ctx, staged)
		if err != nil {
			return applyFailed(err.Error())
		}
		m.mu.Lock()
		m.restartTarget = target
		m.state = InstallState{Phase: StateInstalled, Version: staged.Version}
		m.mu.Unlock()
		result.Installed = true
		result.Version = staged.Version
	}
	m.mu.Lock()
	target := m.restartTarget
	m.mu.Unlock()
	if target == "" {
		target = m.ExePath
	}
	if target == "" {
		result.Failure = FailureRelaunch
		result.UnavailableReason = "无法确定当前可执行文件路径"
		return result
	}
	if err := m.RestartFunc(ctx, target); err != nil {
		result.Failure = FailureRelaunch
		result.UnavailableReason = "重启失败: " + err.Error()
		return result
	}
	result.Restarted = true
	return result
}

func (m *Manager) applyStaged(ctx context.Context, staged stagedPackage) (string, error) {
	directory := m.stageDir()
	digest, err := m.stagedDigest(staged)
	if err != nil {
		m.discardStaged()
		return "", userErrorf(UserErrorIO, "已下载的更新包校验失败，已丢弃，请重新下载: %w", err)
	}
	archive, cleanup, err := copyVerified(staged.path(directory), directory, digest)
	if err != nil {
		if errors.Is(err, errStagedTampered) {
			m.discardStaged()
			return "", userErrorf(UserErrorIO, "已下载的更新包校验失败，已丢弃，请重新下载: %w", err)
		}
		return "", userErrorf(UserErrorIO, "读取已下载的更新包失败: %w", err)
	}
	defer cleanup()
	taskID := ids.New()
	m.report(ctx, Progress{TaskID: taskID, Phase: PhaseInstall}, false)
	target, err := m.applyInstall(ctx, archive)
	if err != nil {
		m.report(ctx, Progress{TaskID: taskID, Phase: PhaseInstall, Done: true, Error: err.Error()}, false)
		return "", err
	}
	m.emit(ctx, Progress{TaskID: taskID, Phase: PhaseInstall, Done: true})
	m.discardStaged()
	return target, nil
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
