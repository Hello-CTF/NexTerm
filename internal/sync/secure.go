package sync

import (
	"context"
	"database/sql"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
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

func (s *Service) persistAdminTokenTx(ctx context.Context, tx *sql.Tx, plaintext string) error {
	protected, err := s.protectSettingSecret(ctx, plaintext)
	if err != nil {
		return err
	}
	now := ids.NowMS()
	if err := txUpsertSetting(ctx, tx, settingToken, protected, now); err != nil {
		return err
	}
	return txUpsertSetting(ctx, tx, settingTokenBackup, plaintext, now)
}

func txUpsertSetting(ctx context.Context, tx *sql.Tx, key, value string, now int64) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, now); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return nil
}
