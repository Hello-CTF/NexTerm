package sync

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

const maxBundleFileBytes = 8 << 20

type BundleFileRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (s *Service) ReadBundleFile(_ context.Context, path string) (string, error) {
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
	if len(data) > maxBundleFileBytes {
		return "", ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("资产包文件超过 %d MiB，拒绝读取", maxBundleFileBytes>>20))
	}
	return string(data), nil
}

func (s *Service) WriteBundleFile(_ context.Context, path string, content string) error {
	if err := s.requireDesktop(); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ipc.NewError(ipc.CodeBadParam, "资产包路径为空")
	}
	if len(content) > maxBundleFileBytes {
		return ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("资产包内容超过 %d MiB，拒绝写入", maxBundleFileBytes>>20))
	}
	if err := os.WriteFile(trimmed, []byte(content), 0o600); err != nil {
		return ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("无法写入资产包文件: %v", err))
	}
	return nil
}
