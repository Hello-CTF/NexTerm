package account

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"github.com/Hello-CTF/NexTerm/internal/vault"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func validateEnvelopes(envelopes *vault.UserDEKEnvelopes) error {
	if envelopes == nil ||
		len(envelopes.DEKEnvelope) == 0 || len(envelopes.KDFSalt) == 0 || envelopes.KDFParams == "" ||
		len(envelopes.RecoveryEnvelope) == 0 || envelopes.RecoveryHash == "" {
		return ipc.BadParam(fmt.Errorf("数据密钥封装字段不完整"))
	}
	return nil
}

func (a *Accounts) SetUserDEKEnvelopes(ctx context.Context, userID string, envelopes *vault.UserDEKEnvelopes) error {
	if err := validateEnvelopes(envelopes); err != nil {
		return err
	}
	now := a.now()
	if err := a.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"dek_envelope", "kdf_salt", "kdf_params", "recovery_envelope", "recovery_hash", "updated_at"}),
	}).Create(&store.UserDEK{
		UserID: userID, DEKEnvelope: envelopes.DEKEnvelope, KDFSalt: envelopes.KDFSalt, KDFParams: envelopes.KDFParams,
		RecoveryEnvelope: envelopes.RecoveryEnvelope, RecoveryHash: envelopes.RecoveryHash, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		return dbError(err)
	}
	return nil
}

var ErrDEKEnvelopesExist = errors.New("数据密钥已存在")

func (a *Accounts) InsertUserDEKEnvelopes(ctx context.Context, userID string, envelopes *vault.UserDEKEnvelopes) error {
	if err := validateEnvelopes(envelopes); err != nil {
		return err
	}
	now := a.now()
	result := a.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoNothing: true,
	}).Create(&store.UserDEK{
		UserID: userID, DEKEnvelope: envelopes.DEKEnvelope, KDFSalt: envelopes.KDFSalt, KDFParams: envelopes.KDFParams,
		RecoveryEnvelope: envelopes.RecoveryEnvelope, RecoveryHash: envelopes.RecoveryHash, CreatedAt: now, UpdatedAt: now,
	})
	if result.Error != nil {
		return dbError(result.Error)
	}
	if result.RowsAffected == 0 {
		return ipc.WrapError(ipc.CodeBadParam, ErrDEKEnvelopesExist.Error(), ErrDEKEnvelopesExist)
	}
	return nil
}

func (a *Accounts) GetUserDEKEnvelopes(ctx context.Context, userID string) (*vault.UserDEKEnvelopes, error) {
	userDEK := query.Use(a.db).UserDEK
	row, err := userDEK.WithContext(ctx).Where(userDEK.UserID.Eq(userID)).First()
	if err == gorm.ErrRecordNotFound {
		return nil, ipc.NewError(ipc.CodeNotFound, "数据密钥不存在")
	}
	if err != nil {
		return nil, dbError(err)
	}
	return &vault.UserDEKEnvelopes{
		DEKEnvelope: row.DEKEnvelope, KDFSalt: row.KDFSalt, KDFParams: row.KDFParams,
		RecoveryEnvelope: row.RecoveryEnvelope, RecoveryHash: row.RecoveryHash,
	}, nil
}

func (a *Accounts) ChangePassword(ctx context.Context, userID, oldPassword, newPassword string, envelopes *vault.UserDEKEnvelopes, keepSessionID string) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	if err := validateEnvelopes(envelopes); err != nil {
		return err
	}
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		appUser := query.Use(tx).AppUser
		user, err := appUser.WithContext(ctx).Where(appUser.ID.Eq(userID)).First()
		if err == gorm.ErrRecordNotFound {
			return ipc.NewError(ipc.CodeNotFound, "用户不存在")
		}
		if err != nil {
			return dbError(err)
		}
		if !verifyPassword(user.PasswordHash, oldPassword) {
			return ipc.NewError(ipc.CodeForbidden, "当前密码错误")
		}
		newHash, err := hashPassword(newPassword)
		if err != nil {
			return err
		}
		now := a.now()
		state := user.State
		if state == string(StateResetRequired) {
			state = string(StateActive)
		}
		info, err := appUser.WithContext(ctx).
			Where(appUser.ID.Eq(userID), appUser.PasswordHash.Eq(user.PasswordHash)).
			UpdateColumns(map[string]any{"password_hash": newHash, "must_change_password": false, "state": state, "updated_at": now})
		if err != nil {
			return dbError(err)
		}
		if info.RowsAffected == 0 {
			return ipc.NewError(ipc.CodeForbidden, "当前密码错误")
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"dek_envelope", "kdf_salt", "kdf_params", "recovery_envelope", "recovery_hash", "updated_at"}),
		}).Create(&store.UserDEK{
			UserID: userID, DEKEnvelope: envelopes.DEKEnvelope, KDFSalt: envelopes.KDFSalt, KDFParams: envelopes.KDFParams,
			RecoveryEnvelope: envelopes.RecoveryEnvelope, RecoveryHash: envelopes.RecoveryHash, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			return dbError(err)
		}
		userSession := query.Use(tx).UserSession
		q := userSession.WithContext(ctx).Where(userSession.UserID.Eq(userID), userSession.RevokedAt.IsNull())
		if keepSessionID != "" {
			q = q.Where(userSession.ID.Neq(keepSessionID))
		}
		if _, err := q.UpdateColumns(map[string]any{"revoked_at": now}); err != nil {
			return dbError(err)
		}
		return nil
	})
}

func (a *Accounts) ResetPasswordWithRecovery(ctx context.Context, username, recoveryKey, newPassword string, envelopes *vault.UserDEKEnvelopes) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	if err := validateEnvelopes(envelopes); err != nil {
		return err
	}
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		appUser := query.Use(tx).AppUser
		user, err := appUser.WithContext(ctx).Where(appUser.Username.Lower().Eq(strings.ToLower(username))).First()
		if err == gorm.ErrRecordNotFound {
			return ipc.NewError(ipc.CodeForbidden, "用户名或恢复密钥错误")
		}
		if err != nil {
			return dbError(err)
		}
		if user.State == string(StateDisabled) {
			return ipc.NewError(ipc.CodeForbidden, "账号已禁用")
		}
		userDEK := query.Use(tx).UserDEK
		dek, err := userDEK.WithContext(ctx).Where(userDEK.UserID.Eq(user.ID)).First()
		if err == gorm.ErrRecordNotFound {
			return ipc.NewError(ipc.CodeForbidden, "账号未设置数据密钥，不能使用恢复密钥")
		}
		if err != nil {
			return dbError(err)
		}
		if !vault.VerifyRecoveryKey(recoveryKey, dek.RecoveryHash) {
			return ipc.NewError(ipc.CodeForbidden, "用户名或恢复密钥错误")
		}
		newHash, err := hashPassword(newPassword)
		if err != nil {
			return err
		}
		now := a.now()
		state := user.State
		if state == string(StateResetRequired) {
			state = string(StateActive)
		}
		if _, err := appUser.WithContext(ctx).Where(appUser.ID.Eq(user.ID)).UpdateColumns(map[string]any{
			"password_hash": newHash, "must_change_password": false, "state": state, "updated_at": now,
		}); err != nil {
			return dbError(err)
		}
		info, err := userDEK.WithContext(ctx).
			Where(userDEK.UserID.Eq(user.ID), userDEK.RecoveryHash.Eq(dek.RecoveryHash)).
			UpdateColumns(map[string]any{
				"dek_envelope": envelopes.DEKEnvelope, "kdf_salt": envelopes.KDFSalt, "kdf_params": envelopes.KDFParams,
				"recovery_envelope": envelopes.RecoveryEnvelope, "recovery_hash": envelopes.RecoveryHash, "updated_at": now,
			})
		if err != nil {
			return dbError(err)
		}
		if info.RowsAffected == 0 {
			return ipc.NewError(ipc.CodeForbidden, "用户名或恢复密钥错误")
		}
		userSession := query.Use(tx).UserSession
		if _, err := userSession.WithContext(ctx).
			Where(userSession.UserID.Eq(user.ID), userSession.RevokedAt.IsNull()).
			UpdateColumns(map[string]any{"revoked_at": now}); err != nil {
			return dbError(err)
		}
		return nil
	})
}

func (a *Accounts) AdminResetUser(ctx context.Context, userID string) error {
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		appUser := query.Use(tx).AppUser
		info, err := appUser.WithContext(ctx).
			Where(appUser.ID.Eq(userID)).
			UpdateColumns(map[string]any{"state": string(StateResetRequired), "must_change_password": true, "updated_at": a.now()})
		if err != nil {
			return dbError(err)
		}
		if info.RowsAffected == 0 {
			return ipc.NewError(ipc.CodeNotFound, "用户不存在")
		}
		if _, err := query.Use(tx).UserDEK.WithContext(ctx).Where(query.Use(tx).UserDEK.UserID.Eq(userID)).Delete(); err != nil {
			return dbError(err)
		}
		if _, err := query.Use(tx).UserTOTP.WithContext(ctx).Where(query.Use(tx).UserTOTP.UserID.Eq(userID)).Delete(); err != nil {
			return dbError(err)
		}
		if _, err := query.Use(tx).UserTOTPRecoveryCode.WithContext(ctx).Where(query.Use(tx).UserTOTPRecoveryCode.UserID.Eq(userID)).Delete(); err != nil {
			return dbError(err)
		}
		if _, err := query.Use(tx).UserSyncObject.WithContext(ctx).Where(query.Use(tx).UserSyncObject.UserID.Eq(userID)).Delete(); err != nil {
			return dbError(err)
		}
		if _, err := query.Use(tx).UserSyncHead.WithContext(ctx).Where(query.Use(tx).UserSyncHead.UserID.Eq(userID)).Delete(); err != nil {
			return dbError(err)
		}
		if _, err := query.Use(tx).SyncState.WithContext(ctx).Where(query.Use(tx).SyncState.UserID.Eq(userID)).Delete(); err != nil {
			return dbError(err)
		}
		if _, err := query.Use(tx).CredentialTombstone.WithContext(ctx).Where(query.Use(tx).CredentialTombstone.OwnerID.Eq(userID)).Delete(); err != nil {
			return dbError(err)
		}
		if _, err := query.Use(tx).SyncTombstone.WithContext(ctx).Where(query.Use(tx).SyncTombstone.OwnerID.Eq(userID)).Delete(); err != nil {
			return dbError(err)
		}
		userSession := query.Use(tx).UserSession
		if _, err := userSession.WithContext(ctx).
			Where(userSession.UserID.Eq(userID), userSession.RevokedAt.IsNull()).
			UpdateColumns(map[string]any{"revoked_at": a.now()}); err != nil {
			return dbError(err)
		}
		return nil
	})
}
