//go:build ios && cgo

package inference

/*
#cgo LDFLAGS: -framework Foundation
int ng_ios_supported(void);
*/
import "C"

func CheckPlatform() error {
	if C.ng_ios_supported() == 0 {
		return &PlatformError{Message: "本地 AI 脱敏需要 iOS / iPadOS 15.1 或更新版本"}
	}
	return nil
}
