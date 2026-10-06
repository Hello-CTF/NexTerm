package account

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
	"golang.org/x/crypto/argon2"
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
	argon2Time        = 3
	argon2Memory      = 64 * 1024
	argon2Threads     = 4
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
	key := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLength)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argon2Memory, argon2Time, argon2Threads,
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

// CreateUserWithEnvelopes 在同一个事务里创建用户并写入初始 DEK 信封, 不留半初始化账号。
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
		ID:           ids.New(),
		Username:     username,
		DisplayName:  displayName,
		Role:         RoleUser,
		State:        StateActive,
		CreatedAt:    now,
		UpdatedAt:    now,
		passwordHash: hash,
	}
	if envelopes == nil {
		if _, err := a.db.ExecContext(ctx, `INSERT INTO `+userTable+`(id, username, display_name, role, password_hash, state, must_change_password, created_at, updated_at)
VALUES(?,?,?,?,?,?,0,?,?)`, user.ID, user.Username, user.DisplayName, string(user.Role), hash, string(user.State), now, now); err != nil {
			return nil, translateUserWriteError(err)
		}
		return user, nil
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+userTable+`(id, username, display_name, role, password_hash, state, must_change_password, created_at, updated_at)
VALUES(?,?,?,?,?,?,0,?,?)`, user.ID, user.Username, user.DisplayName, string(user.Role), hash, string(user.State), now, now); err != nil {
		return nil, translateUserWriteError(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_dek(user_id, dek_envelope, kdf_salt, kdf_params, recovery_envelope, recovery_hash, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?)`, user.ID, envelopes.DEKEnvelope, envelopes.KDFSalt, envelopes.KDFParams,
		envelopes.RecoveryEnvelope, envelopes.RecoveryHash, now, now); err != nil {
		return nil, dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, dbError(err)
	}
	return user, nil
}

func (a *Accounts) GetUser(ctx context.Context, id string) (*User, error) {
	return scanUser(a.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM "+userTable+" WHERE id = ?", id))
}

func (a *Accounts) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	return scanUser(a.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM "+userTable+" WHERE username = ? COLLATE NOCASE", username))
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
	if _, err := a.db.ExecContext(ctx, "UPDATE "+userTable+" SET last_login_at = ? WHERE id = ?", now, user.ID); err != nil {
		return nil, dbError(err)
	}
	user.LastLoginAt = now
	return user, nil
}

const userColumns = "id, username, display_name, role, password_hash, state, must_change_password, created_at, updated_at, last_login_at"

func scanUser(row rowScanner) (*User, error) {
	var user User
	var role, state string
	var lastLoginAt sql.NullInt64
	if err := row.Scan(&user.ID, &user.Username, &user.DisplayName, &role, &user.passwordHash, &state,
		&user.MustChangePassword, &user.CreatedAt, &user.UpdatedAt, &lastLoginAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ipc.NewError(ipc.CodeNotFound, "未找到: 用户")
		}
		return nil, dbError(err)
	}
	user.Role = Role(role)
	user.State = State(state)
	if lastLoginAt.Valid {
		user.LastLoginAt = lastLoginAt.Int64
	}
	return &user, nil
}
