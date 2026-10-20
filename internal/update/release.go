package update

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const DefaultReleasesAPI = "https://api.github.com/repos/Hello-CTF/NexTerm/releases"

const (
	checksumsAssetName         = "SHA256SUMS"
	checksumSignatureAssetName = checksumsAssetName + ".minisig"
	maxChecksumManifestSize    = 1 << 20
	maxMinisignSignatureSize   = 16 << 10
)

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
	trust, required, err := m.minisignTrust()
	if err != nil {
		return nil, err
	}
	checksumAsset, ok := findReleaseAsset(release, checksumsAssetName)
	if !ok || checksumAsset.BrowserDownloadURL == "" {
		return nil, fmt.Errorf("release %s 缺少 %s", release.TagName, checksumsAssetName)
	}
	var signatureAsset Asset
	if required {
		signatureAsset, ok = findReleaseAsset(release, checksumSignatureAssetName)
		if !ok || signatureAsset.BrowserDownloadURL == "" {
			return nil, fmt.Errorf("release %s 缺少 %s，签名验证已启用", release.TagName, checksumSignatureAssetName)
		}
	}

	response, err := m.fetchReleaseAsset(ctx, checksumAsset)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if !required {
		sums, err := parseSHA256SUMS(response.Body)
		if err != nil {
			return nil, fmt.Errorf("解析 %s 失败: %w", checksumsAssetName, err)
		}
		return sums, nil
	}

	contents, err := readReleaseAsset(response.Body, checksumAsset.Name, maxChecksumManifestSize)
	if err != nil {
		return nil, err
	}
	signature, err := m.fetchReleaseAssetBytes(ctx, signatureAsset, maxMinisignSignatureSize)
	if err != nil {
		return nil, err
	}
	expectedComment := fmt.Sprintf("NexTerm %s %s", release.version(), checksumsAssetName)
	if err := verifyMinisign(contents, signature, trust, expectedComment); err != nil {
		return nil, fmt.Errorf("%s 的 minisign 验证失败: %w", checksumsAssetName, err)
	}
	sums, err := parseSHA256SUMS(bytes.NewReader(contents))
	if err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", checksumsAssetName, err)
	}
	return sums, nil
}

func findReleaseAsset(release Release, name string) (Asset, bool) {
	for _, asset := range release.Assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return Asset{}, false
}

func (m *Manager) fetchReleaseAsset(ctx context.Context, asset Asset) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "NexTerm-updater")
	response, err := m.httpClient().Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("下载 %s 返回 %s", asset.Name, response.Status)
	}
	return response, nil
}

func (m *Manager) fetchReleaseAssetBytes(ctx context.Context, asset Asset, limit int64) ([]byte, error) {
	response, err := m.fetchReleaseAsset(ctx, asset)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	return readReleaseAsset(response.Body, asset.Name, limit)
}

func readReleaseAsset(reader io.Reader, name string, limit int64) ([]byte, error) {
	contents, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("下载 %s 失败: %w", name, err)
	}
	if int64(len(contents)) > limit {
		return nil, fmt.Errorf("%s 超过 %d 字节大小限制", name, limit)
	}
	return contents, nil
}

// selectRelease picks the latest non-draft release above the current version; whether
// prereleases are accepted is decided by the current version channel (see acceptsPrerelease).
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
