package update

import "fmt"

func platformSupported(goos, goarch string) error {
	switch goos + "/" + goarch {
	case "windows/amd64", "windows/arm64", "darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64":
		return nil
	}
	return fmt.Errorf("平台 %s/%s 不支持应用内安装", goos, goarch)
}
