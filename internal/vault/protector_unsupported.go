//go:build !windows && !darwin

package vault

import "github.com/ProbiusOfficial/NexTerm/internal/ipc"

func systemProtect([]byte) ([]byte, error) {
	return nil, ipc.NewError(ipc.CodeUnsupported, "不支持的操作: 系统级免密保护仅支持 Windows / macOS")
}

func systemUnprotect([]byte) ([]byte, error) {
	return nil, ipc.NewError(ipc.CodeUnsupported, "不支持的操作: 系统级免密保护仅支持 Windows / macOS")
}
