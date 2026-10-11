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
	UserID     string
	SessionID  string
	Role       Role
	State      State
	DeviceID   string
	MFAEnabled bool
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
		ID: ids.New(), UserID: userID, DeviceID: deviceID, CreatedAt: now, TouchedAt: now,
		ExpiresAt: now + SessionSlidingTTL.Milliseconds(),
	}
	err := a.withBusyRetry(ctx, func() error {
		return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			appUser := query.Use(tx).AppUser
			user, err := appUser.WithContext(ctx).Where(appUser.ID.Eq(userID)).First()
			if err == gorm.ErrRecordNotFound {
				return ipc.NewError(ipc.CodeNotFound, "用户不存在")
			}
			if err != nil {
				return dbError(err)
			}
			if user.State == string(StateDisabled) {
				return ipc.NewError(ipc.CodeForbidden, "账号已禁用")
			}
			if deviceID != "" {
				userDevice := query.Use(tx).UserDevice
				device, err := userDevice.WithContext(ctx).Where(userDevice.ID.Eq(deviceID)).First()
				if err == gorm.ErrRecordNotFound {
					return ipc.NewError(ipc.CodeNotFound, "设备不存在")
				}
				if err != nil {
					return dbError(err)
				}
				if device.UserID != userID {
					return ipc.NewError(ipc.CodeForbidden, "设备不属于该用户")
				}
				if device.RevokedAt != nil {
					return ipc.NewError(ipc.CodeForbidden, "设备已吊销")
				}
			}
			row := store.UserSession{
				ID: session.ID, UserID: session.UserID, TokenHash: sessionTokenHash(token),
				CreatedAt: session.CreatedAt, TouchedAt: session.TouchedAt, ExpiresAt: session.ExpiresAt,
			}
			if session.DeviceID != "" {
				row.DeviceID = &session.DeviceID
			}
			return query.Use(tx).UserSession.WithContext(ctx).Create(&row)
		})
	})
	if err != nil {
		return "", nil, dbError(err)
	}
	return token, session, nil
}

func (a *Accounts) ValidateSession(ctx context.Context, token string) (*Identity, error) {
	userSession := query.Use(a.db).UserSession
	row, err := userSession.WithContext(ctx).Where(userSession.TokenHash.Eq(sessionTokenHash(token))).First()
	if err == gorm.ErrRecordNotFound {
		return nil, ipc.NewError(ipc.CodeForbidden, "会话无效，请重新登录")
	}
	if err != nil {
		return nil, dbError(err)
	}
	session := Session{
		ID: row.ID, UserID: row.UserID, CreatedAt: row.CreatedAt, TouchedAt: row.TouchedAt, ExpiresAt: row.ExpiresAt,
	}
	if row.DeviceID != nil {
		session.DeviceID = *row.DeviceID
	}
	if row.RevokedAt != nil {
		session.RevokedAt = *row.RevokedAt
	}
	appUser := query.Use(a.db).AppUser
	user, err := appUser.WithContext(ctx).Where(appUser.ID.Eq(session.UserID)).First()
	if err != nil {
		return nil, dbError(err)
	}
	deviceRevoked := false
	if session.DeviceID != "" {
		userDevice := query.Use(a.db).UserDevice
		device, err := userDevice.WithContext(ctx).Where(userDevice.ID.Eq(session.DeviceID)).First()
		if err != nil {
			return nil, dbError(err)
		}
		deviceRevoked = device.RevokedAt != nil
	}
	now := a.now()
	switch {
	case session.RevokedAt != 0:
		return nil, ipc.NewError(ipc.CodeForbidden, "会话已吊销，请重新登录")
	case deviceRevoked:
		return nil, ipc.NewError(ipc.CodeForbidden, "设备已吊销")
	case now >= session.ExpiresAt:
		return nil, ipc.NewError(ipc.CodeForbidden, "会话已过期，请重新登录")
	case now >= session.CreatedAt+SessionAbsoluteTTL.Milliseconds():
		return nil, ipc.NewError(ipc.CodeForbidden, "会话已过期，请重新登录")
	case user.State == string(StateDisabled):
		return nil, ipc.NewError(ipc.CodeForbidden, "账号已禁用")
	}
	if now-session.TouchedAt >= sessionSlidePersist.Milliseconds() {
		expiresAt := now + SessionSlidingTTL.Milliseconds()
		if absolute := session.CreatedAt + SessionAbsoluteTTL.Milliseconds(); expiresAt > absolute {
			expiresAt = absolute
		}
		if _, err := userSession.WithContext(ctx).Where(userSession.ID.Eq(session.ID)).UpdateColumns(map[string]any{
			"touched_at": now, "expires_at": expiresAt,
		}); err != nil {
			return nil, dbError(err)
		}
	}
	mfaEnabled := false
	userTOTP := query.Use(a.db).UserTOTP
	if _, err := userTOTP.WithContext(ctx).Where(userTOTP.UserID.Eq(session.UserID), userTOTP.SecretEnvelope.IsNotNull()).First(); err == nil {
		mfaEnabled = true
	} else if err != gorm.ErrRecordNotFound {
		return nil, dbError(err)
	}
	return &Identity{
		UserID: session.UserID, SessionID: session.ID, Role: Role(user.Role), State: State(user.State),
		DeviceID: session.DeviceID, MFAEnabled: mfaEnabled,
	}, nil
}

func (a *Accounts) RevokeSession(ctx context.Context, sessionID string) error {
	userSession := query.Use(a.db).UserSession
	info, err := userSession.WithContext(ctx).
		Where(userSession.ID.Eq(sessionID), userSession.RevokedAt.IsNull()).
		UpdateColumns(map[string]any{"revoked_at": a.now()})
	if err != nil {
		return dbError(err)
	}
	if info.RowsAffected == 0 {
		return ipc.NewError(ipc.CodeNotFound, "会话不存在")
	}
	return nil
}

func (a *Accounts) RevokeUserSessions(ctx context.Context, userID, exceptSessionID string) (int64, error) {
	userSession := query.Use(a.db).UserSession
	q := userSession.WithContext(ctx).Where(userSession.UserID.Eq(userID), userSession.RevokedAt.IsNull())
	if exceptSessionID != "" {
		q = q.Where(userSession.ID.Neq(exceptSessionID))
	}
	info, err := q.UpdateColumns(map[string]any{"revoked_at": a.now()})
	if err != nil {
		return 0, dbError(err)
	}
	return info.RowsAffected, nil
}
