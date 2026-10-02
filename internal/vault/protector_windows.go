//go:build windows

package vault

import (
	"runtime"
	"unsafe"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"golang.org/x/sys/windows"
)

func systemProtect(data []byte) ([]byte, error) {
	input := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var output windows.DataBlob
	if err := windows.CryptProtectData(&input, nil, nil, 0, nil, 0, &output); err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: DPAPI protect 失败: "+err.Error(), err)
	}
	runtime.KeepAlive(data)
	defer func() { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data))) }()
	result := append([]byte(nil), unsafe.Slice(output.Data, output.Size)...)
	return result, nil
}

func systemUnprotect(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, ipc.NewError(ipc.CodeCrypto, "加密错误: DPAPI 信封为空")
	}
	input := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var output windows.DataBlob
	if err := windows.CryptUnprotectData(&input, nil, nil, 0, nil, 0, &output); err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: DPAPI unprotect 失败: "+err.Error(), err)
	}
	runtime.KeepAlive(data)
	defer func() { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data))) }()
	result := append([]byte(nil), unsafe.Slice(output.Data, output.Size)...)
	return result, nil
}
