package account

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"github.com/Hello-CTF/NexTerm/internal/vault"
	"golang.org/x/crypto/argon2"
	"gorm.io/gorm"
)

type Role string

const (
	RoleSuperadmin Role = "superadmin"
	RoleUser       Role = "user"
)

type State string

const (
	StateActive        State = "active"
	StateDisabled      State = "disabled"
	StateResetRequired State = "reset_required"
)

const (
	passwordSaltBytes = 16
	argon2KeyLength   = 32
	MinPasswordLength = 8
	maxUsernameLength = 64
	maxDisplayNameLen = 128
)

type User struct {
	ID                 string
	Username           string
	DisplayName        string
	Role               Role
	State              State
	MustChangePassword bool
	CreatedAt          int64
	UpdatedAt          int64
	LastLoginAt        int64
	passwordHash       string
}

func (u *User) PasswordHash() string {
	return u.passwordHash
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", ipc.WrapError(ipc.CodeCrypto, "加密错误: 密码盐生成失败", err)
	}
	params := vault.PasswordArgon2()
	key := argon2.IDKey([]byte(password), salt, params.Time, params.MemoryKiB, params.Threads, argon2KeyLength)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		params.MemoryKiB, params.Time, params.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false
	}
	var memory, timeParam, threads uint64
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeParam, &threads); err != nil {
		return false
	}
	if timeParam == 0 || timeParam > 16 || memory < 8*1024 || memory > 2*1024*1024 || threads == 0 || threads > 16 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	key := argon2.IDKey([]byte(password), salt, uint32(timeParam), uint32(memory), uint8(threads), uint32(len(expected)))
	return subtle.ConstantTimeCompare(key, expected) == 1
}

func validateUsername(username string) error {
	if username == "" || len(username) > maxUsernameLength {
		return ipc.BadParam(fmt.Errorf("用户名长度需在 1-%d 之间", maxUsernameLength))
	}
	for _, r := range username {
		if r <= ' ' || r == 0x7f {
			return ipc.BadParam(fmt.Errorf("用户名不能包含空白或控制字符"))
		}
	}
	return nil
}

func validatePassword(password string) error {
	if len(password) < MinPasswordLength {
		return ipc.BadParam(fmt.Errorf("密码至少 %d 位", MinPasswordLength))
	}
	return nil
}

func (a *Accounts) CreateUser(ctx context.Context, username, displayName, password string) (*User, error) {
	return a.createUser(ctx, username, displayName, password, nil)
}

func (a *Accounts) CreateUserWithEnvelopes(ctx context.Context, username, displayName, password string, envelopes *vault.UserDEKEnvelopes) (*User, error) {
	if err := validateEnvelopes(envelopes); err != nil {
		return nil, err
	}
	return a.createUser(ctx, username, displayName, password, envelopes)
}

func (a *Accounts) createUser(ctx context.Context, username, displayName, password string, envelopes *vault.UserDEKEnvelopes) (*User, error) {
	if err := validateUsername(username); err != nil {
		return nil, err
	}
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	if len(displayName) > maxDisplayNameLen {
		return nil, ipc.BadParam(fmt.Errorf("显示名长度需在 0-%d 之间", maxDisplayNameLen))
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	now := a.now()
	user := &User{
		ID: ids.New(), Username: username, DisplayName: displayName, Role: RoleUser, State: StateActive,
		CreatedAt: now, UpdatedAt: now, passwordHash: hash,
	}
	row := store.AppUser{
		ID: user.ID, Username: user.Username, DisplayName: user.DisplayName, Role: string(user.Role),
		PasswordHash: hash, State: string(user.State), CreatedAt: now, UpdatedAt: now,
	}
	if envelopes == nil {
		if err := query.Use(a.db).AppUser.WithContext(ctx).Create(&row); err != nil {
			return nil, translateUserWriteError(err)
		}
		return user, nil
	}
	err = a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := query.Use(tx).AppUser.WithContext(ctx).Create(&row); err != nil {
			return translateUserWriteError(err)
		}
		dek := store.UserDEK{
			UserID: user.ID, DEKEnvelope: envelopes.DEKEnvelope, KDFSalt: envelopes.KDFSalt, KDFParams: envelopes.KDFParams,
			RecoveryEnvelope: envelopes.RecoveryEnvelope, RecoveryHash: envelopes.RecoveryHash, CreatedAt: now, UpdatedAt: now,
		}
		return query.Use(tx).UserDEK.WithContext(ctx).Create(&dek)
	})
	if err != nil {
		return nil, dbError(err)
	}
	return user, nil
}

func (a *Accounts) GetUser(ctx context.Context, id string) (*User, error) {
	appUser := query.Use(a.db).AppUser
	row, err := appUser.WithContext(ctx).Where(appUser.ID.Eq(id)).First()
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ipc.NewError(ipc.CodeNotFound, "用户不存在")
		}
		return nil, dbError(err)
	}
	return userFromModel(row), nil
}

func (a *Accounts) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	appUser := query.Use(a.db).AppUser
	row, err := appUser.WithContext(ctx).Where(appUser.Username.Lower().Eq(strings.ToLower(username))).First()
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ipc.NewError(ipc.CodeNotFound, "用户不存在")
		}
		return nil, dbError(err)
	}
	return userFromModel(row), nil
}

func (a *Accounts) Authenticate(ctx context.Context, username, password string) (*User, error) {
	user, err := a.GetUserByUsername(ctx, username)
	if err != nil {
		return nil, ipc.NewError(ipc.CodeForbidden, "用户名或密码错误")
	}
	if !verifyPassword(user.passwordHash, password) {
		return nil, ipc.NewError(ipc.CodeForbidden, "用户名或密码错误")
	}
	if user.State == StateDisabled {
		return nil, ipc.NewError(ipc.CodeForbidden, "账号已禁用")
	}
	now := a.now()
	appUser := query.Use(a.db).AppUser
	if _, err := appUser.WithContext(ctx).Where(appUser.ID.Eq(user.ID)).UpdateColumns(map[string]any{"last_login_at": now}); err != nil {
		return nil, dbError(err)
	}
	user.LastLoginAt = now
	return user, nil
}
