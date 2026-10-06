package sync

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

const maxBundleFileBytes = 8 << 20

const bundlePlaintextWarning = "资产包以明文导出，获得文件的人都能直接读取其中的凭据，请妥善保管"

type BundleFileRequest struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Password string `json:"password,omitempty"`
}

type BundleReadOptions struct {
	Password string `json:"password,omitempty"`
}

type BundleWriteOptions struct {
	Password string `json:"password,omitempty"`
}

type BundleWriteResult struct {
	Encrypted bool   `json:"encrypted"`
	Warning   string `json:"warning,omitempty"`
}

func (s *Service) ReadBundleFileWithOptions(_ context.Context, path string, opts BundleReadOptions) (string, error) {
	if err := s.requireDesktop(); err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", ipc.NewError(ipc.CodeBadParam, "资产包路径为空")
	}
	data, err := os.ReadFile(trimmed)
	if err != nil {
		return "", ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("无法读取资产包文件: %v", err))
	}
	if len(data) > maxBundleFileBytes+bundleHeaderSize+bundleGCMTagSize {
		return "", ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("资产包文件超过 %d MiB，拒绝读取", maxBundleFileBytes>>20))
	}
	plaintext, err := decodeBundleContainer(data, opts.Password)
	if err != nil {
		return "", err
	}
	if len(plaintext) > maxBundleFileBytes {
		return "", ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("资产包内容超过 %d MiB，拒绝读取", maxBundleFileBytes>>20))
	}
	return string(plaintext), nil
}

func (s *Service) WriteBundleFileWithOptions(_ context.Context, path string, content string, opts BundleWriteOptions) (*BundleWriteResult, error) {
	if err := s.requireDesktop(); err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, ipc.NewError(ipc.CodeBadParam, "资产包路径为空")
	}
	if len(content) > maxBundleFileBytes {
		return nil, ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("资产包内容超过 %d MiB，拒绝写入", maxBundleFileBytes>>20))
	}
	data, err := encodeBundleContainer([]byte(content), opts.Password)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(trimmed, data, 0o600); err != nil {
		return nil, ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("无法写入资产包文件: %v", err))
	}
	if err := os.Chmod(trimmed, 0o600); err != nil {
		return nil, ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("无法设置资产包文件权限: %v", err))
	}
	result := &BundleWriteResult{Encrypted: opts.Password != ""}
	if !result.Encrypted {
		result.Warning = bundlePlaintextWarning
	}
	return result, nil
}
