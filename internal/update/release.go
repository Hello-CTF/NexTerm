package update

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/releasesig"
)

const DefaultReleasesAPI = "https://api.github.com/repos/Hello-CTF/NexTerm/releases"

const releasesPageSize = 100

const (
	checksumsAssetName         = "SHA256SUMS"
	checksumSignatureAssetName = checksumsAssetName + ".minisig"
	maxChecksumManifestSize    = 1 << 20
	maxMinisignSignatureSize   = 16 << 10
)

type Release struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	// PublishedAt is zero for drafts and when the API omits it.
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	Assets      []Asset   `json:"assets"`
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
	endpoint, err := url.Parse(m.releasesAPI())
	if err != nil {
		return nil, err
	}
	// GitHub returns 30 releases per page by default; a run of release candidates must not push stable releases
	// off the page, so ask for the maximum.
	values := endpoint.Query()
	values.Set("per_page", strconv.Itoa(releasesPageSize))
	endpoint.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
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

// checksums is a release's SHA256SUMS as fetched: the exact manifest and signature bytes are kept so a staged
// package can be verified against them again, offline, before it is applied.
type checksums struct {
	sums      map[string]string
	manifest  []byte
	signature []byte
}

func (m *Manager) fetchChecksums(ctx context.Context, release Release) (checksums, error) {
	trust, required, err := m.minisignTrust()
	if err != nil {
		return checksums{}, err
	}
	checksumAsset, ok := findReleaseAsset(release, checksumsAssetName)
	if !ok || checksumAsset.BrowserDownloadURL == "" {
		return checksums{}, fmt.Errorf("release %s 缺少 %s", release.TagName, checksumsAssetName)
	}
	var signatureAsset Asset
	if required {
		signatureAsset, ok = findReleaseAsset(release, checksumSignatureAssetName)
		if !ok || signatureAsset.BrowserDownloadURL == "" {
			return checksums{}, fmt.Errorf("release %s 缺少 %s，签名验证已启用", release.TagName, checksumSignatureAssetName)
		}
	}
	contents, err := m.fetchReleaseAssetBytes(ctx, checksumAsset, maxChecksumManifestSize)
	if err != nil {
		return checksums{}, err
	}
	var signature []byte
	if required {
		signature, err = m.fetchReleaseAssetBytes(ctx, signatureAsset, maxMinisignSignatureSize)
		if err != nil {
			return checksums{}, err
		}
	}
	sums, err := verifyChecksums(contents, signature, trust, required, release.version())
	if err != nil {
		return checksums{}, err
	}
	return checksums{sums: sums, manifest: contents, signature: signature}, nil
}

// verifyChecksums checks the manifest's minisign signature when signatures are required, binding it to version
// through the trusted comment so the signed sums of another release cannot be replayed, then parses it.
func verifyChecksums(manifest, signature []byte, trust releasesig.PublicKey, required bool, version string) (map[string]string, error) {
	if required {
		if len(signature) == 0 {
			return nil, fmt.Errorf("缺少 %s，签名验证已启用", checksumSignatureAssetName)
		}
		if err := releasesig.Verify(manifest, signature, trust, releasesig.ChecksumsTrustedComment(version)); err != nil {
			return nil, fmt.Errorf("%s 的 minisign 验证失败: %w", checksumsAssetName, err)
		}
	}
	sums, err := parseSHA256SUMS(bytes.NewReader(manifest))
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

// selectRelease picks the latest release visible on the channel that is newer than the current version. The
// running build never influences the channel: a stable channel on an RC build waits for a newer stable release
// and never downgrades. A current version that cannot be parsed (dev builds) accepts any visible release.
func selectRelease(releases []Release, current string, channel ReleaseChannel) (Release, bool) {
	candidates := candidateReleases(releases, current, channel)
	if len(candidates) == 0 {
		return Release{}, false
	}
	return candidates[0], true
}

// candidateReleases lists the releases visible on the channel that are newer than the current version, newest
// first; releases that compare equal keep the API order.
func candidateReleases(releases []Release, current string, channel ReleaseChannel) []Release {
	var candidates []Release
	for _, release := range releases {
		if releaseVisibleOn(release, channel) && isNewerVersion(release.version(), current) {
			candidates = append(candidates, release)
		}
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		return compareMustParse(candidates[left].version(), candidates[right].version()) > 0
	})
	return candidates
}

func compareMustParse(left, right string) int {
	leftVersion, leftOK := parseVersion(left)
	rightVersion, rightOK := parseVersion(right)
	if !leftOK || !rightOK {
		return 0
	}
	return compareVersions(leftVersion, rightVersion)
}
