package sync

import (
	"context"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
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

// errLegacyPlaintextLink rejects a sync link setting persisted before 1.3.0 as plaintext JSON. It is never
// read or merged; the user re-enters the credentials, which are then saved as an enc:v1: envelope.
var errLegacyPlaintextLink = ipc.NewError(ipc.CodeDecrypt,
	"凭据解密失败：同步链接设置为 1.3.0 之前的明文格式，已不再读取；请在同步设置中重新填写用户名和密码并保存")

// revealSettingSecret reads the persisted sync link setting: only enc:v1: envelopes are decrypted via the vault;
// a pre-envelope plaintext value is rejected with errLegacyPlaintextLink, and the settings page re-saves.
func (s *Service) revealSettingSecret(ctx context.Context, stored string) (string, error) {
	if !strings.HasPrefix(strings.TrimSpace(stored), store.SecretEnvelopePrefix) {
		return "", errLegacyPlaintextLink
	}
	if s.vault == nil {
		return "", ipc.NewError(ipc.CodeVaultLocked, "凭据库已锁定，请先解锁")
	}
	return s.vault.DecryptSecret(ctx, stored)
}
