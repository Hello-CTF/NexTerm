package sync

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func (s *Service) vaultStatus() (initialized, unlocked bool) {
	if s.vault == nil {
		return false, false
	}
	status := s.vault.Status()
	return status.Initialized, status.Unlocked
}

// protectSettingSecret 写入侧一律升级为 enc:v1: 信封；凭据库未初始化时拒绝写入
// (口令不得明文落库)，已锁定时要求先解锁。
func (s *Service) protectSettingSecret(ctx context.Context, plaintext string) (string, error) {
	initialized, unlocked := s.vaultStatus()
	if !initialized {
		return "", ipc.NewError(ipc.CodeVaultNotInit, "凭据库尚未初始化，无法安全保存同步链接设置；请先完成凭据保护初始化")
	}
	if !unlocked {
		return "", ipc.NewError(ipc.CodeVaultLocked, "凭据库已锁定，请先解锁")
	}
	return s.vault.EncryptSecret(ctx, plaintext)
}

// revealSettingSecret 读取落盘的同步链接设置: 一律经凭据库解密, 非信封或无法解密
// 统一报解密失败, 由设置页重新保存。
func (s *Service) revealSettingSecret(ctx context.Context, stored string) (string, error) {
	if s.vault == nil {
		return "", ipc.NewError(ipc.CodeVaultLocked, "凭据库已锁定，请先解锁")
	}
	return s.vault.DecryptSecret(ctx, stored)
}
