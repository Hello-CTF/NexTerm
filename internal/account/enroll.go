package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
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
	if _, err := a.db.ExecContext(ctx, `INSERT INTO device_enroll_code(id, user_id, code_hash, created_at, expires_at)
VALUES(?,?,?,?,?)`, ids.New(), userID, enrollCodeHash(code), now, now+EnrollCodeTTL.Milliseconds()); err != nil {
		return "", dbError(err)
	}
	return code, nil
}

func (a *Accounts) ConsumeEnrollCode(ctx context.Context, code string) (string, error) {
	var userID string
	err := a.db.QueryRowContext(ctx, `UPDATE device_enroll_code SET consumed_at = ?
WHERE code_hash = ? AND consumed_at IS NULL AND expires_at > ?
RETURNING user_id`, a.now(), enrollCodeHash(code), a.now()).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ipc.NewError(ipc.CodeForbidden, "设备注册码无效或已过期，请重新生成后再试")
	}
	if err != nil {
		return "", dbError(err)
	}
	return userID, nil
}
