package account

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

func validateEnvelopes(envelopes *vault.UserDEKEnvelopes) error {
	if envelopes == nil ||
		len(envelopes.DEKEnvelope) == 0 || len(envelopes.KDFSalt) == 0 || envelopes.KDFParams == "" ||
		len(envelopes.RecoveryEnvelope) == 0 || envelopes.RecoveryHash == "" {
		return ipc.BadParam(fmt.Errorf("DEK 信封字段不完整"))
	}
	return nil
}

func (a *Accounts) SetUserDEKEnvelopes(ctx context.Context, userID string, envelopes *vault.UserDEKEnvelopes) error {
	if err := validateEnvelopes(envelopes); err != nil {
		return err
	}
	now := a.now()
	if _, err := a.db.ExecContext(ctx, `INSERT INTO user_dek(user_id, dek_envelope, kdf_salt, kdf_params, recovery_envelope, recovery_hash, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(user_id) DO UPDATE SET dek_envelope = excluded.dek_envelope, kdf_salt = excluded.kdf_salt,
	kdf_params = excluded.kdf_params, recovery_envelope = excluded.recovery_envelope,
	recovery_hash = excluded.recovery_hash, updated_at = excluded.updated_at`,
		userID, envelopes.DEKEnvelope, envelopes.KDFSalt, envelopes.KDFParams,
		envelopes.RecoveryEnvelope, envelopes.RecoveryHash, now, now); err != nil {
		return dbError(err)
	}
	return nil
}

func (a *Accounts) GetUserDEKEnvelopes(ctx context.Context, userID string) (*vault.UserDEKEnvelopes, error) {
	var envelopes vault.UserDEKEnvelopes
	err := a.db.QueryRowContext(ctx, `SELECT dek_envelope, kdf_salt, kdf_params, recovery_envelope, recovery_hash
FROM user_dek WHERE user_id = ?`, userID).
		Scan(&envelopes.DEKEnvelope, &envelopes.KDFSalt, &envelopes.KDFParams, &envelopes.RecoveryEnvelope, &envelopes.RecoveryHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ipc.NewError(ipc.CodeNotFound, "未找到: DEK 信封")
	}
	if err != nil {
		return nil, dbError(err)
	}
	return &envelopes, nil
}

func (a *Accounts) ChangePassword(ctx context.Context, userID, oldPassword, newPassword string, envelopes *vault.UserDEKEnvelopes, keepSessionID string) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	if err := validateEnvelopes(envelopes); err != nil {
		return err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var hash string
	if err := tx.QueryRowContext(ctx, "SELECT password_hash FROM user WHERE id = ?", userID).Scan(&hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ipc.NewError(ipc.CodeNotFound, "未找到: 用户")
		}
		return dbError(err)
	}
	if !verifyPassword(hash, oldPassword) {
		return ipc.NewError(ipc.CodeForbidden, "原密码错误")
	}
	newHash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	now := a.now()
	if _, err := tx.ExecContext(ctx, `UPDATE user SET password_hash = ?, must_change_password = 0,
	state = CASE WHEN state = ? THEN ? ELSE state END, updated_at = ? WHERE id = ?`,
		newHash, string(StateResetRequired), string(StateActive), now, userID); err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_dek(user_id, dek_envelope, kdf_salt, kdf_params, recovery_envelope, recovery_hash, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(user_id) DO UPDATE SET dek_envelope = excluded.dek_envelope, kdf_salt = excluded.kdf_salt,
	kdf_params = excluded.kdf_params, recovery_envelope = excluded.recovery_envelope,
	recovery_hash = excluded.recovery_hash, updated_at = excluded.updated_at`,
		userID, envelopes.DEKEnvelope, envelopes.KDFSalt, envelopes.KDFParams,
		envelopes.RecoveryEnvelope, envelopes.RecoveryHash, now, now); err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE user_session SET revoked_at = ?
WHERE user_id = ? AND revoked_at IS NULL AND (? = '' OR id != ?)`, now, userID, keepSessionID, keepSessionID); err != nil {
		return dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}

func (a *Accounts) ResetPasswordWithRecovery(ctx context.Context, username, recoveryKey, newPassword string, envelopes *vault.UserDEKEnvelopes) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	if err := validateEnvelopes(envelopes); err != nil {
		return err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var userID, state string
	if err := tx.QueryRowContext(ctx, "SELECT id, state FROM user WHERE username = ? COLLATE NOCASE", username).Scan(&userID, &state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ipc.NewError(ipc.CodeForbidden, "用户名或恢复密钥错误")
		}
		return dbError(err)
	}
	if State(state) == StateDisabled {
		return ipc.NewError(ipc.CodeForbidden, "账号已禁用")
	}
	var recoveryHash string
	if err := tx.QueryRowContext(ctx, "SELECT recovery_hash FROM user_dek WHERE user_id = ?", userID).Scan(&recoveryHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ipc.NewError(ipc.CodeForbidden, "账号未设置 DEK，不能使用恢复密钥")
		}
		return dbError(err)
	}
	if !vault.VerifyRecoveryKey(recoveryKey, recoveryHash) {
		return ipc.NewError(ipc.CodeForbidden, "用户名或恢复密钥错误")
	}
	newHash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	now := a.now()
	if _, err := tx.ExecContext(ctx, `UPDATE user SET password_hash = ?, must_change_password = 0,
	state = CASE WHEN state = ? THEN ? ELSE state END, updated_at = ? WHERE id = ?`,
		newHash, string(StateResetRequired), string(StateActive), now, userID); err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE user_dek SET dek_envelope = ?, kdf_salt = ?, kdf_params = ?,
	recovery_envelope = ?, recovery_hash = ?, updated_at = ? WHERE user_id = ?`,
		envelopes.DEKEnvelope, envelopes.KDFSalt, envelopes.KDFParams,
		envelopes.RecoveryEnvelope, envelopes.RecoveryHash, now, userID); err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE user_session SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL", now, userID); err != nil {
		return dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}

func (a *Accounts) AdminResetUser(ctx context.Context, userID string) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE user SET state = ?, must_change_password = 1, updated_at = ? WHERE id = ?`,
		string(StateResetRequired), a.now(), userID)
	if err != nil {
		return dbError(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected == 0 {
		return ipc.NewError(ipc.CodeNotFound, "未找到: 用户")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_dek WHERE user_id = ?", userID); err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE user_session SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL", a.now(), userID); err != nil {
		return dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}
