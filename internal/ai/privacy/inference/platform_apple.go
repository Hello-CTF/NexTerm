//go:build darwin && (amd64 || arm64) && (cgo || !ios)

package inference

import (
	"runtime"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

func selfLibraryPath() string { return "" }

func CheckPlatform() error {
	version := appleVersion()
	if runtime.GOOS == "ios" {
		if version.Major < 15 || (version.Major == 15 && version.Minor < 1) {
			return &PlatformError{Message: "本地 AI 脱敏需要 iOS / iPadOS 15.1 或更新版本"}
		}
		return nil
	}
	if version.Major < 13 || (version.Major == 13 && version.Minor < 4) {
		return &PlatformError{Message: "本地 AI 脱敏需要 macOS 13.4 或更新版本"}
	}
	return nil
}

// operatingSystemVersion mirrors NSOperatingSystemVersion's three NSIntegers.
type operatingSystemVersion struct{ Major, Minor, Patch int }

// appleVersion reads -[NSProcessInfo operatingSystemVersion], or returns
// the zero version when Foundation is unavailable.
var appleVersion = sync.OnceValue(func() operatingSystemVersion {
	if _, err := purego.Dlopen("/System/Library/Frameworks/Foundation.framework/Foundation", purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
		return operatingSystemVersion{}
	}
	class := objc.GetClass("NSProcessInfo")
	if class == 0 {
		return operatingSystemVersion{}
	}
	info := objc.ID(class).Send(objc.RegisterName("processInfo"))
	if info == 0 {
		return operatingSystemVersion{}
	}
	return objc.Send[operatingSystemVersion](info, objc.RegisterName("operatingSystemVersion"))
})
