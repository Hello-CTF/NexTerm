//go:build windows && (amd64 || arm64)

package inference

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// loadLibrary opens path so that the ONNX Runtime DLL's bundled VC runtime
// resolves beside the DLL, not from the application directory or PATH.
func loadLibrary(path string) (uintptr, error) {
	handle, err := windows.LoadLibraryEx(path, 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_DEFAULT_DIRS)
	if err != nil {
		return 0, loaderError(err)
	}
	return uintptr(handle), nil
}

func librarySymbol(handle uintptr, name string) (uintptr, error) {
	symbol, err := windows.GetProcAddress(windows.Handle(handle), name)
	if err != nil {
		return 0, loaderError(err)
	}
	return symbol, nil
}

func unloadLibrary(handle uintptr) { _ = windows.FreeLibrary(windows.Handle(handle)) }

// loaderError describes the last loader error, such as a missing Visual C++
// runtime, with its GetLastError code and system message.
func loaderError(err error) error {
	var code syscall.Errno
	if errors.As(err, &code) {
		return fmt.Errorf("Windows error %d: %s", uint32(code), strings.TrimRight(code.Error(), "\r\n "))
	}
	return err
}

// nativePath returns a NUL-terminated ORTCHAR_T path, which is UTF-16 here.
func nativePath(path string) (unsafe.Pointer, error) {
	value, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	return unsafe.Pointer(value), nil
}
