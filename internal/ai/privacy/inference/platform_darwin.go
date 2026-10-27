//go:build darwin && !ios && cgo

package inference

/*
#cgo CFLAGS: -mmacosx-version-min=11.0
#cgo LDFLAGS: -framework Foundation
int ng_macos_supported(void);
*/
import "C"

func CheckPlatform() error {
	if C.ng_macos_supported() == 0 {
		return &PlatformError{Message: "本地 AI 脱敏需要 macOS 13.4 或更新版本"}
	}
	return nil
}
