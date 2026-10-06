package sync

import (
	"context"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func (s *Service) vaultStatus() (initialized, unlocked bool) {
	if s.vault == nil {
		return false, false
	}
	status := s.vault.Status()
	return status.Initialized, status.Unlocked
}

// protectSettingSecret 写入侧一律升级为 enc:v1: 信封；凭据库不可用时按原样写回并保持双读兼容。
func (s *Service) protectSettingSecret(ctx context.Context, plaintext string) (string, error) {
	initialized, unlocked := s.vaultStatus()
	if !initialized {
		return plaintext, nil
	}
	if !unlocked {
		return "", ipc.NewError(ipc.CodeVaultLocked, "凭据库已锁定，请先解锁")
	}
	return s.vault.EncryptSecret(ctx, plaintext)
}

// revealSettingSecret 双读：enc:v1: 信封走凭据库解密，历史明文原样返回。
func (s *Service) revealSettingSecret(ctx context.Context, stored string) (string, error) {
	if !strings.HasPrefix(stored, store.SecretEnvelopePrefix) {
		return stored, nil
	}
	if s.vault == nil {
		return "", ipc.NewError(ipc.CodeVaultLocked, "凭据库已锁定，请先解锁")
	}
	return s.vault.DecryptSecret(ctx, stored)
}
