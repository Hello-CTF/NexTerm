//go:build android && cgo && (amd64 || arm64)

package inference

import (
	"reflect"
	"strconv"
	"unsafe"

	"github.com/ebitengine/purego"
)

func CheckPlatform() error {
	if androidAPILevel() < 24 {
		return &PlatformError{Message: "本地 AI 脱敏需要 Android 7（API 24）或更新版本"}
	}
	return nil
}

// androidAPILevel reads ro.build.version.sdk, or returns 0.
func androidAPILevel() int {
	libc, err := purego.Dlopen("libc.so", purego.RTLD_NOW)
	if err != nil {
		return 0
	}
	defer purego.Dlclose(libc)
	get, err := purego.Dlsym(libc, "__system_property_get")
	if err != nil {
		return 0
	}
	name := nulTerminated("ro.build.version.sdk")
	var value [92]byte // PROP_VALUE_MAX
	length, _, _ := purego.SyscallN(get, uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Pointer(&value[0])))
	if n := int(int32(length)); n > 0 && n < len(value) {
		level, err := strconv.Atoi(string(value[:n]))
		if err == nil {
			return level
		}
	}
	return 0
}

// selfLibraryPath returns the path of the shared library holding this
// package's code, found with dladdr, because Android's executable is
// app_process and the bundled runtime lives beside this library.
func selfLibraryPath() string {
	libdl, err := purego.Dlopen("libdl.so", purego.RTLD_NOW)
	if err != nil {
		return ""
	}
	defer purego.Dlclose(libdl)
	dladdr, err := purego.Dlsym(libdl, "dladdr")
	if err != nil {
		return ""
	}
	var info struct{ fname, fbase, sname, saddr unsafe.Pointer } // Dl_info
	address := reflect.ValueOf(selfLibraryPath).Pointer()
	if found, _, _ := purego.SyscallN(dladdr, address, uintptr(unsafe.Pointer(&info))); found == 0 {
		return ""
	}
	path, _ := cString(info.fname, 4096)
	return path
}
