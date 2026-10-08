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
	SessionSlidingTTL   = 12 * time.Hour
	SessionAbsoluteTTL  = 7 * 24 * time.Hour
	sessionSlidePersist = 5 * time.Minute
	sessionTokenBytes   = 32
)

type Session struct {
	ID        string
	UserID    string
	DeviceID  string
	CreatedAt int64
	TouchedAt int64
	ExpiresAt int64
	RevokedAt int64
}

type Identity struct {
	UserID    string
	SessionID string
	Role      Role
	State     State
	DeviceID  string
}

func sessionTokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func (a *Accounts) IssueSession(ctx context.Context, userID, deviceID string) (string, *Session, error) {
	raw := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: 会话令牌生成失败", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := a.now()
	session := &Session{
		ID:        ids.New(),
		UserID:    userID,
		DeviceID:  deviceID,
		CreatedAt: now,
		TouchedAt: now,
		ExpiresAt: now + SessionSlidingTTL.Milliseconds(),
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var state string
	if err := tx.QueryRowContext(ctx, "SELECT state FROM "+userTable+" WHERE id = ?", userID).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil, ipc.NewError(ipc.CodeNotFound, "用户不存在")
		}
		return "", nil, dbError(err)
	}
	if State(state) == StateDisabled {
		return "", nil, ipc.NewError(ipc.CodeForbidden, "账号已禁用")
	}
	if deviceID != "" {
		var owner string
		var revokedAt sql.NullInt64
		err := tx.QueryRowContext(ctx, "SELECT user_id, revoked_at FROM user_device WHERE id = ?", deviceID).Scan(&owner, &revokedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil, ipc.NewError(ipc.CodeNotFound, "设备不存在")
		}
		if err != nil {
			return "", nil, dbError(err)
		}
		if owner != userID {
			return "", nil, ipc.NewError(ipc.CodeForbidden, "设备不属于该用户")
		}
		if revokedAt.Valid {
			return "", nil, ipc.NewError(ipc.CodeForbidden, "设备已吊销")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_session(id, user_id, device_id, token_hash, created_at, touched_at, expires_at)
VALUES(?,?,?,?,?,?,?)`, session.ID, session.UserID, nullableID(session.DeviceID), sessionTokenHash(token),
		session.CreatedAt, session.TouchedAt, session.ExpiresAt); err != nil {
		return "", nil, dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return "", nil, dbError(err)
	}
	return token, session, nil
}

func (a *Accounts) ValidateSession(ctx context.Context, token string) (*Identity, error) {
	row := a.db.QueryRowContext(ctx, `SELECT s.id, s.user_id, s.device_id, s.created_at, s.touched_at, s.expires_at, s.revoked_at, u.role, u.state, d.revoked_at
FROM user_session s JOIN `+userTable+` u ON u.id = s.user_id LEFT JOIN user_device d ON d.id = s.device_id
WHERE s.token_hash = ?`, sessionTokenHash(token))
	var session Session
	var role, state string
	var deviceID sql.NullString
	var revokedAt, deviceRevokedAt sql.NullInt64
	if err := row.Scan(&session.ID, &session.UserID, &deviceID, &session.CreatedAt, &session.TouchedAt,
		&session.ExpiresAt, &revokedAt, &role, &state, &deviceRevokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ipc.NewError(ipc.CodeForbidden, "会话无效，请重新登录")
		}
		return nil, dbError(err)
	}
	if deviceID.Valid {
		session.DeviceID = deviceID.String
	}
	if revokedAt.Valid {
		session.RevokedAt = revokedAt.Int64
	}
	now := a.now()
	switch {
	case session.RevokedAt != 0:
		return nil, ipc.NewError(ipc.CodeForbidden, "会话已吊销，请重新登录")
	case deviceRevokedAt.Valid:
		return nil, ipc.NewError(ipc.CodeForbidden, "设备已吊销")
	case now >= session.ExpiresAt:
		return nil, ipc.NewError(ipc.CodeForbidden, "会话已过期，请重新登录")
	case now >= session.CreatedAt+SessionAbsoluteTTL.Milliseconds():
		return nil, ipc.NewError(ipc.CodeForbidden, "会话已过期，请重新登录")
	case State(state) == StateDisabled:
		return nil, ipc.NewError(ipc.CodeForbidden, "账号已禁用")
	}
	if now-session.TouchedAt >= sessionSlidePersist.Milliseconds() {
		expiresAt := now + SessionSlidingTTL.Milliseconds()
		if absolute := session.CreatedAt + SessionAbsoluteTTL.Milliseconds(); expiresAt > absolute {
			expiresAt = absolute
		}
		if _, err := a.db.ExecContext(ctx, "UPDATE user_session SET touched_at = ?, expires_at = ? WHERE id = ?",
			now, expiresAt, session.ID); err != nil {
			return nil, dbError(err)
		}
	}
	return &Identity{UserID: session.UserID, SessionID: session.ID, Role: Role(role), State: State(state), DeviceID: session.DeviceID}, nil
}

func (a *Accounts) RevokeSession(ctx context.Context, sessionID string) error {
	result, err := a.db.ExecContext(ctx, "UPDATE user_session SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", a.now(), sessionID)
	if err != nil {
		return dbError(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected == 0 {
		return ipc.NewError(ipc.CodeNotFound, "会话不存在")
	}
	return nil
}

func (a *Accounts) RevokeUserSessions(ctx context.Context, userID, exceptSessionID string) (int64, error) {
	result, err := a.db.ExecContext(ctx, `UPDATE user_session SET revoked_at = ?
WHERE user_id = ? AND revoked_at IS NULL AND (? = '' OR id != ?)`, a.now(), userID, exceptSessionID, exceptSessionID)
	if err != nil {
		return 0, dbError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, dbError(err)
	}
	return affected, nil
}

func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}
