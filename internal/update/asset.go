package update

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// matchAsset selects the desktop package named nexterm-desktop_<os>_<arch>_<version>.<ext>;
// the version is matched by prefix plus suffix so pre-release versions need no parsing.
func matchAsset(release Release, goos, goarch string) (Asset, bool) {
	var platform, suffix string
	switch goos {
	case "windows":
		platform, suffix = "windows", ".exe"
	case "darwin":
		platform, suffix = "macos", ".dmg"
	case "linux":
		platform, suffix = "linux", ".deb"
	default:
		return Asset{}, false
	}
	if goarch != "amd64" && goarch != "arm64" {
		return Asset{}, false
	}
	prefix := "nexterm-desktop_" + platform + "_" + goarch + "_"
	for _, asset := range release.Assets {
		if strings.HasPrefix(asset.Name, prefix) && strings.HasSuffix(asset.Name, suffix) {
			return asset, true
		}
	}
	return Asset{}, false
}

func parseSHA256SUMS(reader io.Reader) (map[string]string, error) {
	sums := make(map[string]string)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("非法行 %q", line)
		}
		digest := strings.ToLower(fields[0])
		if len(digest) != sha256.Size*2 {
			return nil, fmt.Errorf("非法摘要 %q", fields[0])
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return nil, fmt.Errorf("非法摘要 %q", fields[0])
		}
		sums[strings.TrimPrefix(fields[1], "*")] = digest
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(sums) == 0 {
		return nil, fmt.Errorf("%s 为空", checksumsAssetName)
	}
	return sums, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// verifyChecksumFile verifies the downloaded file against the release's SHA256SUMS; a missing entry or digest mismatch
// is always an error, and the caller must not proceed with the install.
func verifyChecksumFile(path, assetName string, sums map[string]string) error {
	expected, ok := sums[assetName]
	if !ok {
		return userErrorf(UserErrorIO, "%s 缺少 %s 的校验和", checksumsAssetName, assetName)
	}
	actual, err := fileSHA256(path)
	if err != nil {
		return userErrorf(UserErrorIO, "计算 %s 摘要失败: %w", assetName, err)
	}
	if actual != expected {
		return userErrorf(UserErrorIO, "%s 的 SHA256 校验失败：期望 %s，实际 %s", assetName, expected, actual)
	}
	return nil
}
