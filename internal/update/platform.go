package update

import "fmt"

// externalChannel returns how a platform without in-app installation gets its updates: "app-store" for iOS and
// iPadOS (TestFlight or the App Store) and "release" for Android (a new package from the Releases page).
func externalChannel(goos string) string {
	switch goos {
	case "ios":
		return "app-store"
	case "android":
		return "release"
	}
	return ""
}

func platformSupported(goos, goarch string) error {
	switch goos + "/" + goarch {
	case "windows/amd64", "windows/arm64", "darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64":
		return nil
	}
	return fmt.Errorf("平台 %s/%s 不支持应用内安装", goos, goarch)
}
