package account

import (
	"context"
	"crypto/rand"
	"encoding/base64"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

const (
	PlatformOwnerUsername    = "owner"
	PlatformOwnerDisplayName = "平台所有者"
)

func (a *Accounts) EnsurePlatformOwner(ctx context.Context) (*User, error) {
	existing, err := a.GetUserByUsername(ctx, PlatformOwnerUsername)
	if err == nil {
		return existing, nil
	}
	if ipc.NormalizeError(err).Code != ipc.CodeNotFound {
		return nil, err
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: 平台所有者口令生成失败", err)
	}
	password, err := hashPassword("platform-owner!" + base64.RawURLEncoding.EncodeToString(raw))
	if err != nil {
		return nil, err
	}

	now := a.now()
	owner := &User{
		ID: ids.New(), Username: PlatformOwnerUsername, DisplayName: PlatformOwnerDisplayName,
		Role: RoleSuperadmin, State: StateActive, CreatedAt: now, UpdatedAt: now, passwordHash: password,
	}
	row := store.AppUser{
		ID: owner.ID, Username: owner.Username, DisplayName: owner.DisplayName, Role: string(owner.Role),
		PasswordHash: password, State: string(owner.State), CreatedAt: now, UpdatedAt: now,
	}
	if err := query.Use(a.db).AppUser.WithContext(ctx).Create(&row); err != nil && !store.IsUniqueErr(err) {
		return nil, translateUserWriteError(err)
	}
	return a.GetUserByUsername(ctx, PlatformOwnerUsername)
}
