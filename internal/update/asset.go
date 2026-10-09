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

// matchAsset 按 GOOS/GOARCH 选择 release 资产; 版本号段不固定, 用前缀加后缀
// 锚定 (Linux 桌面包前缀 NexTerm-desktop_, 避免误匹配 NexTerm-server_ 包)。
func matchAsset(release Release, goos, goarch string) (Asset, bool) {
	var prefix, suffix string
	switch goos + "/" + goarch {
	case "windows/amd64":
		prefix, suffix = "NexTerm_", "_x64-setup.exe"
	case "windows/arm64":
		prefix, suffix = "NexTerm_", "_arm64-setup.exe"
	case "darwin/amd64":
		prefix, suffix = "NexTerm_", "_x86_64.dmg"
	case "darwin/arm64":
		prefix, suffix = "NexTerm_", "_aarch64.dmg"
	case "linux/amd64":
		prefix, suffix = "NexTerm-desktop_", "_linux_amd64.tar.gz"
	case "linux/arm64":
		prefix, suffix = "NexTerm-desktop_", "_linux_arm64.tar.gz"
	default:
		return Asset{}, false
	}
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

// verifyChecksumFile 用 release 的 SHA256SUMS 校验下载文件; 缺失条目或摘要不符
// 一律报错, 调用方不得继续安装。
func verifyChecksumFile(path, assetName string, sums map[string]string) error {
	expected, ok := sums[assetName]
	if !ok {
		return fmt.Errorf("%s 缺少 %s 的校验和", checksumsAssetName, assetName)
	}
	actual, err := fileSHA256(path)
	if err != nil {
		return fmt.Errorf("计算 %s 摘要失败: %w", assetName, err)
	}
	if actual != expected {
		return fmt.Errorf("%s 的 SHA256 校验失败: 期望 %s, 实际 %s", assetName, expected, actual)
	}
	return nil
}
