//go:build (linux || darwin) && (amd64 || arm64) && (cgo || !(android || ios))

package inference

import (
	"errors"
	"runtime"
	"strings"
	"unsafe"

	"github.com/ebitengine/purego"
)

// loadLibrary opens path with dlopen. On failure the error text is the
// dynamic loader diagnostic, such as a missing libstdc++.
func loadLibrary(path string) (uintptr, error) {
	// dlerror is thread-local: keep dlopen and dlerror on one thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	handle, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return 0, loaderError(err)
	}
	return handle, nil
}

func librarySymbol(handle uintptr, name string) (uintptr, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	symbol, err := purego.Dlsym(handle, name)
	if err != nil {
		return 0, loaderError(err)
	}
	return symbol, nil
}

func unloadLibrary(handle uintptr) { _ = purego.Dlclose(handle) }

func loaderError(err error) error {
	if message := strings.TrimSpace(err.Error()); message != "" {
		return errors.New(message)
	}
	return errors.New("unknown loader error")
}

// nativePath returns a NUL-terminated ORTCHAR_T path, which is char here.
func nativePath(path string) (unsafe.Pointer, error) {
	value := append([]byte(path), 0)
	return unsafe.Pointer(&value[0]), nil
}
