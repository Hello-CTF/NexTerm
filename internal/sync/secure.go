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

// protectSettingSecret always upgrades the write side to an enc:v1: envelope; it refuses to write when the vault is uninitialized
// (passwords must never be persisted in plaintext) and requires unlocking first when locked.
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

// revealSettingSecret reads the persisted sync link setting, an enc:v1: envelope decrypted via the vault.
func (s *Service) revealSettingSecret(ctx context.Context, stored string) (string, error) {
	if s.vault == nil {
		return "", ipc.NewError(ipc.CodeVaultLocked, "凭据库已锁定，请先解锁")
	}
	return s.vault.DecryptSecret(ctx, stored)
}
