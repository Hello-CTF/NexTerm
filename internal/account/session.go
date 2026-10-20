package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
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
	ID         string
	UserID     string
	DeviceID   string
	ClientName string
	Origin     string
	CreatedAt  int64
	TouchedAt  int64
	ExpiresAt  int64
	RevokedAt  int64
}

type SessionMetadata struct {
	ClientName string
	Origin     string
}

type SessionInfo struct {
	ID         string
	DeviceID   string
	DeviceName string
	ClientName string
	Origin     string
	CreatedAt  int64
	LastSeenAt int64
	ExpiresAt  int64
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
	return a.IssueSessionWithMetadata(ctx, userID, deviceID, SessionMetadata{})
}

func (a *Accounts) IssueSessionWithMetadata(ctx context.Context, userID, deviceID string, metadata SessionMetadata) (string, *Session, error) {
	raw := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: 会话令牌生成失败", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := a.now()
	session := &Session{
		ID: ids.New(), UserID: userID, DeviceID: deviceID, ClientName: metadata.ClientName, Origin: metadata.Origin, CreatedAt: now, TouchedAt: now,
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
				ClientName: session.ClientName, Origin: session.Origin,
				CreatedAt: session.CreatedAt, TouchedAt: session.TouchedAt, ExpiresAt: session.ExpiresAt,
			}
			if session.DeviceID != "" {
				row.DeviceID = &session.DeviceID
			}
			if err := query.Use(tx).UserSession.WithContext(ctx).Create(&row); err != nil {
				return err
			}
			if session.DeviceID != "" {
				return touchDeviceAt(ctx, tx, session.DeviceID, now)
			}
			return nil
		})
	})
	if err != nil {
		return "", nil, dbError(err)
	}
	return token, session, nil
}

func (a *Accounts) ListUserSessions(ctx context.Context, userID string) ([]SessionInfo, error) {
	now := a.now()
	userSession := query.Use(a.db).UserSession
	rows := []store.UserSession{}
	if err := userSession.WithContext(ctx).
		Where(userSession.UserID.Eq(userID), userSession.RevokedAt.IsNull(),
			userSession.ExpiresAt.Gt(now), userSession.CreatedAt.Gt(now-SessionAbsoluteTTL.Milliseconds())).
		Order(userSession.TouchedAt.Desc(), userSession.ID).
		Scan(&rows); err != nil {
		return nil, dbError(err)
	}
	devices, err := a.ListDevices(ctx, userID)
	if err != nil {
		return nil, err
	}
	deviceNames := make(map[string]string, len(devices))
	for _, device := range devices {
		deviceNames[device.ID] = device.Name
	}
	result := make([]SessionInfo, 0, len(rows))
	for _, row := range rows {
		info := SessionInfo{
			ID: row.ID, ClientName: row.ClientName, Origin: row.Origin,
			CreatedAt: row.CreatedAt, LastSeenAt: row.TouchedAt, ExpiresAt: row.ExpiresAt,
		}
		if row.DeviceID != nil {
			info.DeviceID = *row.DeviceID
			info.DeviceName = deviceNames[info.DeviceID]
		}
		result = append(result, info)
	}
	return result, nil
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
		updated, err := userSession.WithContext(ctx).
			Where(userSession.ID.Eq(session.ID), userSession.RevokedAt.IsNull(),
				userSession.TouchedAt.Lte(now-sessionSlidePersist.Milliseconds())).
			UpdateColumns(map[string]any{
				"touched_at": now, "expires_at": expiresAt,
			})
		if err != nil {
			return nil, dbError(err)
		}
		if updated.RowsAffected > 0 && session.DeviceID != "" {
			if err := touchDeviceAt(ctx, a.db, session.DeviceID, now); err != nil {
				return nil, err
			}
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

// accountSessionOwnerRead resolves the session's owner before revocation; tests
// replace it to inject owner-read failures.
var accountSessionOwnerRead = func(ctx context.Context, db *gorm.DB, sessionID string) (string, error) {
	userSession := query.Use(db).UserSession
	row, err := userSession.WithContext(ctx).Where(userSession.ID.Eq(sessionID)).First()
	if err != nil {
		return "", err
	}
	return row.UserID, nil
}

func (a *Accounts) RevokeSession(ctx context.Context, sessionID string) error {
	// Read the owner before revoking: a failed lookup aborts the revocation
	// (the session stays active and the caller retries) instead of silently
	// skipping the epoch bump after the revoke already committed.
	ownerID, err := accountSessionOwnerRead(ctx, a.db, sessionID)
	if err == gorm.ErrRecordNotFound {
		return ipc.NewError(ipc.CodeNotFound, "会话不存在")
	}
	if err != nil {
		return dbError(err)
	}
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
	a.epochs.bump(ownerID)
	return nil
}

func (a *Accounts) RevokeUserSession(ctx context.Context, userID, sessionID string) error {
	userSession := query.Use(a.db).UserSession
	info, err := userSession.WithContext(ctx).
		Where(userSession.ID.Eq(sessionID), userSession.UserID.Eq(userID), userSession.RevokedAt.IsNull()).
		UpdateColumns(map[string]any{"revoked_at": a.now()})
	if err != nil {
		return dbError(err)
	}
	if info.RowsAffected == 0 {
		return ipc.NewError(ipc.CodeNotFound, "会话不存在或已撤销")
	}
	a.epochs.bump(userID)
	return nil
}

func (a *Accounts) RevokeUserSessions(ctx context.Context, userID, exceptSessionID string) (int64, error) {
	sessionIDs, err := a.RevokeUserSessionsWithIDs(ctx, userID, exceptSessionID)
	return int64(len(sessionIDs)), err
}

// RevokeUserSessionsWithIDs revokes and returns the exact session set in one account write transaction.
func (a *Accounts) RevokeUserSessionsWithIDs(ctx context.Context, userID, exceptSessionID string, beforeCommit ...func() error) ([]string, error) {
	var sessionIDs []string
	err := a.withBusyRetry(ctx, func() error {
		sessionIDs = nil
		return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			userSession := query.Use(tx).UserSession
			q := userSession.WithContext(ctx).Where(userSession.UserID.Eq(userID), userSession.RevokedAt.IsNull())
			if exceptSessionID != "" {
				q = q.Where(userSession.ID.Neq(exceptSessionID))
			}
			var rows []store.UserSession
			if err := q.Select(userSession.ID).Scan(&rows); err != nil {
				return dbError(err)
			}
			ids := make([]string, 0, len(rows))
			for _, row := range rows {
				ids = append(ids, row.ID)
			}
			if len(ids) == 0 {
				return nil
			}
			for _, prepare := range beforeCommit {
				if prepare != nil {
					if err := prepare(); err != nil {
						return err
					}
				}
			}
			updateQ := userSession.WithContext(ctx).Where(userSession.UserID.Eq(userID), userSession.RevokedAt.IsNull())
			if exceptSessionID != "" {
				updateQ = updateQ.Where(userSession.ID.Neq(exceptSessionID))
			}
			info, err := updateQ.UpdateColumns(map[string]any{"revoked_at": a.now()})
			if err != nil {
				return dbError(err)
			}
			if info.RowsAffected != int64(len(ids)) {
				return dbError(fmt.Errorf("session revocation set changed during transaction"))
			}
			sessionIDs = ids
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	if len(sessionIDs) > 0 {
		a.epochs.bump(userID)
	}
	return sessionIDs, nil
}
