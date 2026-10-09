package update

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

const (
	apiTimeout      = 20 * time.Second
	downloadTimeout = 10 * time.Minute
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
}

type Result struct {
	Installed         bool   `json:"installed"`
	Restarted         bool   `json:"restarted"`
	Version           string `json:"version"`
	UnavailableReason string `json:"unavailableReason"`
}

// Manager 的应用内更新入口; 所有检查失败一律降级为 Status.UnavailableReason,
// 不向调用方抛错。RestartFunc 由装配层注入 (桌面端重启 wails 应用), server 为 nil。
type Manager struct {
	CurrentVersion string
	GOOS           string
	GOARCH         string
	CanInstall     bool
	ExePath        string
	APIURL         string
	HTTPClient     *http.Client
	Emit           func(context.Context, Progress)
	RestartFunc    func(context.Context, string) error

	restartTarget string
	installing    atomic.Bool
}

func (m *Manager) releasesAPI() string {
	if m.APIURL != "" {
		return m.APIURL
	}
	return DefaultReleasesAPI
}

func (m *Manager) httpClient() *http.Client {
	if m.HTTPClient != nil {
		return m.HTTPClient
	}
	return http.DefaultClient
}

func (m *Manager) emit(ctx context.Context, progress Progress) {
	if m.Emit != nil {
		m.Emit(ctx, progress)
	}
}

func (m *Manager) Check(ctx context.Context) Status {
	status := Status{CurrentVersion: m.CurrentVersion, CanInstall: m.CanInstall}
	fail := func(reason string) Status {
		status.UnavailableReason = reason
		return status
	}
	if _, err := planFor(m.GOOS, m.GOARCH); err != nil {
		return fail(err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	releases, err := m.fetchReleases(ctx)
	if err != nil {
		return fail("检查更新失败: " + err.Error())
	}
	release, ok := selectRelease(releases, m.CurrentVersion)
	if !ok {
		return status
	}
	asset, ok := matchAsset(release, m.GOOS, m.GOARCH)
	if !ok {
		return fail("新版本 " + release.version() + " 没有适合当前平台的安装包")
	}
	sums, err := m.fetchChecksums(ctx, release)
	if err != nil {
		return fail("获取校验和失败: " + err.Error())
	}
	if _, ok := sums[asset.Name]; !ok {
		return fail(checksumsAssetName + " 缺少 " + asset.Name + " 的校验和")
	}
	status.Available = true
	status.Version = release.version()
	status.Notes = release.Body
	status.Prerelease = release.Prerelease
	status.AssetName = asset.Name
	status.AssetSize = asset.Size
	return status
}

func (m *Manager) Install(ctx context.Context, version string) (Result, error) {
	if !m.CanInstall {
		return Result{UnavailableReason: "server 模式不支持桌面应用内安装"}, nil
	}
	if !m.installing.CompareAndSwap(false, true) {
		return Result{}, fmt.Errorf("正在安装更新, 请稍候")
	}
	defer m.installing.Store(false)
	status := m.Check(ctx)
	if !status.Available {
		reason := status.UnavailableReason
		if reason == "" {
			reason = "当前已是最新版本"
		}
		return Result{UnavailableReason: reason}, nil
	}
	if version != "" && version != status.Version {
		return Result{}, fmt.Errorf("待安装版本 %s 与最新版本 %s 不一致", version, status.Version)
	}
	asset, sums, err := m.resolveAsset(ctx, status.Version)
	if err != nil {
		return Result{}, err
	}
	directory, err := os.MkdirTemp("", "nexterm-update-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(directory)
	archive := filepath.Join(directory, asset.Name)
	taskID := ids.New()
	if err := m.download(ctx, asset, archive, taskID); err != nil {
		m.emit(ctx, Progress{TaskID: taskID, Phase: PhaseDownload, Done: true, Error: err.Error()})
		return Result{}, err
	}
	if err := verifyChecksumFile(archive, asset.Name, sums); err != nil {
		_ = os.Remove(archive)
		m.emit(ctx, Progress{TaskID: taskID, Phase: PhaseDownload, Transferred: asset.Size, Total: asset.Size, Done: true, Error: err.Error()})
		return Result{}, err
	}
	m.emit(ctx, Progress{TaskID: taskID, Phase: PhaseInstall})
	if err := m.applyInstall(ctx, archive); err != nil {
		m.emit(ctx, Progress{TaskID: taskID, Phase: PhaseInstall, Done: true, Error: err.Error()})
		return Result{}, err
	}
	m.emit(ctx, Progress{TaskID: taskID, Phase: PhaseInstall, Done: true})
	result := Result{Installed: true, Version: status.Version}
	restart := m.Restart(ctx)
	result.Restarted = restart.Restarted
	result.UnavailableReason = restart.UnavailableReason
	return result, nil
}

func (m *Manager) resolveAsset(ctx context.Context, version string) (Asset, map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	releases, err := m.fetchReleases(ctx)
	if err != nil {
		return Asset{}, nil, fmt.Errorf("检查更新失败: %w", err)
	}
	acceptPre := acceptsPrerelease(m.CurrentVersion)
	for _, release := range releases {
		if release.Draft || release.version() != version {
			continue
		}
		if release.Prerelease && !acceptPre {
			continue
		}
		asset, ok := matchAsset(release, m.GOOS, m.GOARCH)
		if !ok {
			continue
		}
		sums, err := m.fetchChecksums(ctx, release)
		if err != nil {
			return Asset{}, nil, fmt.Errorf("获取校验和失败: %w", err)
		}
		return asset, sums, nil
	}
	return Asset{}, nil, fmt.Errorf("release %s 不存在或不可用", version)
}

func (m *Manager) download(ctx context.Context, asset Asset, dest, taskID string) error {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "NexTerm-updater")
	response, err := m.httpClient().Do(request)
	if err != nil {
		return fmt.Errorf("下载 %s 失败: %w", asset.Name, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s 返回 %s", asset.Name, response.Status)
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
	m.emit(ctx, Progress{TaskID: taskID, Phase: PhaseDownload, Total: total})
	for {
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
			if _, err := file.Write(buffer[:count]); err != nil {
				return fmt.Errorf("写入 %s 失败: %w", asset.Name, err)
			}
			transferred += int64(count)
			m.emit(ctx, Progress{TaskID: taskID, Phase: PhaseDownload, Transferred: transferred, Total: total})
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("下载 %s 失败: %w", asset.Name, readErr)
		}
	}
	m.emit(ctx, Progress{TaskID: taskID, Phase: PhaseDownload, Transferred: transferred, Total: total, Done: true})
	return nil
}

func (m *Manager) Restart(ctx context.Context) Result {
	if m.RestartFunc == nil {
		return Result{UnavailableReason: "server 模式不支持应用内重启"}
	}
	target := m.restartTarget
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
