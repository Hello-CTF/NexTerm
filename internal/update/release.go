package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

const DefaultReleasesAPI = "https://api.github.com/repos/Hello-CTF/NexTerm/releases"

const checksumsAssetName = "SHA256SUMS"

type Release struct {
	TagName    string  `json:"tag_name"`
	Name       string  `json:"name"`
	Body       string  `json:"body"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

type Asset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func (release Release) version() string {
	return trimTagPrefix(release.TagName)
}

func trimTagPrefix(tag string) string {
	if len(tag) > 1 && tag[0] == 'v' {
		return tag[1:]
	}
	return tag
}

func (m *Manager) fetchReleases(ctx context.Context) ([]Release, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.releasesAPI(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "NexTerm-updater")
	response, err := m.httpClient().Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub Releases API 返回 %s", response.Status)
	}
	var releases []Release
	if err := json.NewDecoder(response.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("解析 releases 响应失败: %w", err)
	}
	return releases, nil
}

func (m *Manager) fetchChecksums(ctx context.Context, release Release) (map[string]string, error) {
	var url string
	for _, asset := range release.Assets {
		if asset.Name == checksumsAssetName {
			url = asset.BrowserDownloadURL
			break
		}
	}
	if url == "" {
		return nil, fmt.Errorf("release %s 缺少 %s", release.TagName, checksumsAssetName)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "NexTerm-updater")
	response, err := m.httpClient().Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载 %s 返回 %s", checksumsAssetName, response.Status)
	}
	sums, err := parseSHA256SUMS(response.Body)
	if err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", checksumsAssetName, err)
	}
	return sums, nil
}

// selectRelease 在非 draft release 中选出高于当前版本的最新一个; 是否接受
// prerelease 由当前版本通道决定 (见 acceptsPrerelease)。
func selectRelease(releases []Release, current string) (Release, bool) {
	acceptPre := acceptsPrerelease(current)
	var best Release
	found := false
	for _, release := range releases {
		if release.Draft {
			continue
		}
		if release.Prerelease && !acceptPre {
			continue
		}
		if !isNewerVersion(release.version(), current) {
			continue
		}
		if found && compareMustParse(release.version(), best.version()) <= 0 {
			continue
		}
		best = release
		found = true
	}
	return best, found
}

func compareMustParse(left, right string) int {
	leftVersion, leftOK := parseVersion(left)
	rightVersion, rightOK := parseVersion(right)
	if !leftOK || !rightOK {
		return 0
	}
	return compareVersions(leftVersion, rightVersion)
}
