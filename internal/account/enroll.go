package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
)

const (
	EnrollCodeTTL   = 15 * time.Minute
	enrollCodeBytes = 32
)

func enrollCodeHash(code string) string {
	digest := sha256.Sum256([]byte(code))
	return hex.EncodeToString(digest[:])
}

func (a *Accounts) IssueEnrollCode(ctx context.Context, userID string) (string, error) {
	if _, err := a.GetUser(ctx, userID); err != nil {
		return "", err
	}
	raw := make([]byte, enrollCodeBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", ipc.WrapError(ipc.CodeCrypto, "加密错误: 设备注册码生成失败", err)
	}
	code := base64.RawURLEncoding.EncodeToString(raw)
	now := a.now()
	row := query.Use(a.db).DeviceEnrollCode
	record := store.DeviceEnrollCode{ID: ids.New(), UserID: userID, CodeHash: enrollCodeHash(code), CreatedAt: now, ExpiresAt: now + EnrollCodeTTL.Milliseconds()}
	if err := row.WithContext(ctx).Create(&record); err != nil {
		return "", dbError(err)
	}
	return code, nil
}

func (a *Accounts) ConsumeEnrollCode(ctx context.Context, code string) (string, error) {
	var userID string
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		enrollCode := query.Use(tx).DeviceEnrollCode
		row, err := enrollCode.WithContext(ctx).
			Where(enrollCode.CodeHash.Eq(enrollCodeHash(code)), enrollCode.ConsumedAt.IsNull(), enrollCode.ExpiresAt.Gt(a.now())).
			First()
		if err == gorm.ErrRecordNotFound {
			return ipc.NewError(ipc.CodeForbidden, "设备注册码无效或已过期，请重新生成")
		}
		if err != nil {
			return dbError(err)
		}
		info, err := enrollCode.WithContext(ctx).
			Where(enrollCode.ID.Eq(row.ID), enrollCode.ConsumedAt.IsNull()).
			UpdateColumns(map[string]any{"consumed_at": a.now()})
		if err != nil {
			return dbError(err)
		}
		if info.RowsAffected == 0 {
			return ipc.NewError(ipc.CodeForbidden, "设备注册码无效或已过期，请重新生成")
		}
		userID = row.UserID
		return nil
	})
	if err != nil {
		return "", dbError(err)
	}
	return userID, nil
}
