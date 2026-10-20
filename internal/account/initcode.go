package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"github.com/Hello-CTF/NexTerm/internal/vault"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const initCodeSettingKey = "auth.init_code"

// initialPasswordRandomBytes keeps a generated first-install password at 192
// bits of entropy, well above MinPasswordLength once base64-encoded.
const initialPasswordRandomBytes = 24

// InitialPasswordPolicy controls how the first-install superadmin password is
// provisioned.
type InitialPasswordPolicy int

const (
	// InitialPasswordSupplied provisions the caller-supplied password as-is and
	// does not force rotation. It is the policy behind InitSuperadmin and
	// InitSuperadminWithEnvelopes.
	InitialPasswordSupplied InitialPasswordPolicy = iota
	// InitialPasswordRandom generates a cryptographically random initial
	// password, hands the plaintext to the caller exactly once for the
	// installer handoff and requires a password change at first login. Only
	// the password hash is stored.
	InitialPasswordRandom
)

type initCodeState struct {
	Hash     string `json:"hash"`
	Consumed bool   `json:"consumed"`
}

func (a *Accounts) GenerateInitCode(ctx context.Context) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", ipc.WrapError(ipc.CodeCrypto, "加密错误: 初始化码生成失败", err)
	}
	code := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(code))
	encoded, err := json.Marshal(initCodeState{Hash: hex.EncodeToString(digest[:])})
	if err != nil {
		return "", ipc.WrapError(ipc.CodeInternal, "内部错误: 初始化码编码失败", err)
	}
	if err := a.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoNothing: true,
	}).Create(&store.Setting{Key: initCodeSettingKey, Value: string(encoded), UpdatedAt: a.now()}).Error; err != nil {
		return "", dbError(err)
	}
	setting := query.Use(a.db).Setting
	row, err := setting.WithContext(ctx).Where(setting.Key.Eq(initCodeSettingKey)).First()
	if err != nil {
		return "", dbError(err)
	}
	if row.Value != string(encoded) {
		return "", ipc.NewError(ipc.CodeForbidden, "初始化码已生成，请查看服务器控制台")
	}
	return code, nil
}

func (a *Accounts) consumeInitCode(ctx context.Context, tx *gorm.DB, code string) error {
	setting := query.Use(tx).Setting
	row, err := setting.WithContext(ctx).Where(setting.Key.Eq(initCodeSettingKey)).First()
	if err == gorm.ErrRecordNotFound {
		return ipc.NewError(ipc.CodeForbidden, "初始化码不存在，请重启服务器生成")
	}
	if err != nil {
		return dbError(err)
	}
	var state initCodeState
	if err := json.Unmarshal([]byte(row.Value), &state); err != nil {
		return ipc.WrapError(ipc.CodeInternal, "内部错误: 初始化码状态损坏", err)
	}
	if state.Consumed {
		return ipc.NewError(ipc.CodeForbidden, "初始化码已被使用，服务器已完成初始化，请直接登录")
	}
	digest := sha256.Sum256([]byte(code))
	if state.Hash != hex.EncodeToString(digest[:]) {
		return ipc.NewError(ipc.CodeForbidden, "初始化码错误")
	}
	consumed, err := json.Marshal(initCodeState{Hash: state.Hash, Consumed: true})
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "内部错误: 初始化码编码失败", err)
	}
	info, err := setting.WithContext(ctx).
		Where(setting.Key.Eq(initCodeSettingKey), setting.Value.Eq(row.Value)).
		UpdateColumns(map[string]any{"value": string(consumed), "updated_at": a.now()})
	if err != nil {
		return dbError(err)
	}
	if info.RowsAffected != 1 {
		return ipc.NewError(ipc.CodeForbidden, "初始化码已被使用，服务器已完成初始化，请直接登录")
	}
	return nil
}

func (a *Accounts) InitSuperadmin(ctx context.Context, code, username, password string) (*User, error) {
	user, _, err := a.initSuperadmin(ctx, code, username, password, nil, InitialPasswordSupplied)
	return user, err
}

func (a *Accounts) InitSuperadminWithEnvelopes(ctx context.Context, code, username, password string, envelopes *vault.UserDEKEnvelopes) (*User, error) {
	if err := validateEnvelopes(envelopes); err != nil {
		return nil, err
	}
	user, _, err := a.initSuperadmin(ctx, code, username, password, envelopes, InitialPasswordSupplied)
	return user, err
}

// InitSuperadminWithPolicy initializes the first superadmin under an explicit
// initial-password policy. The returned plaintext password is non-empty only
// for InitialPasswordRandom, where it is handed to the calling installer
// exactly once; it is never stored, logged or embedded in errors. A non-nil
// envelopes set is validated and written atomically with the account.
func (a *Accounts) InitSuperadminWithPolicy(ctx context.Context, code, username, password string, envelopes *vault.UserDEKEnvelopes, policy InitialPasswordPolicy) (*User, string, error) {
	if envelopes != nil {
		if err := validateEnvelopes(envelopes); err != nil {
			return nil, "", err
		}
	}
	return a.initSuperadmin(ctx, code, username, password, envelopes, policy)
}

func (a *Accounts) initSuperadmin(ctx context.Context, code, username, password string, envelopes *vault.UserDEKEnvelopes, policy InitialPasswordPolicy) (*User, string, error) {
	if err := validateUsername(username); err != nil {
		return nil, "", err
	}
	state := StateActive
	generated := ""
	switch policy {
	case InitialPasswordSupplied:
		if err := validatePassword(password); err != nil {
			return nil, "", err
		}
	case InitialPasswordRandom:
		var err error
		generated, err = generateInitialPassword()
		if err != nil {
			return nil, "", err
		}
		password = generated
		state = StateResetRequired
	default:
		return nil, "", ipc.BadParam(fmt.Errorf("未知的首装密码策略: %d", policy))
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, "", err
	}
	var user *User
	err = a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		appUser := query.Use(tx).AppUser
		users, err := appUser.WithContext(ctx).Count()
		if err != nil {
			return dbError(err)
		}
		if users > 0 {
			return ipc.NewError(ipc.CodeForbidden, "服务器已完成初始化，不能重复初始化")
		}
		if err := a.consumeInitCode(ctx, tx, code); err != nil {
			return err
		}
		now := a.now()
		user = &User{
			ID: ids.New(), Username: username, Role: RoleSuperadmin, State: state,
			MustChangePassword: state == StateResetRequired,
			CreatedAt:          now, UpdatedAt: now, passwordHash: hash,
		}
		row := store.AppUser{
			ID: user.ID, Username: user.Username, Role: string(user.Role), PasswordHash: hash,
			State: string(user.State), MustChangePassword: user.MustChangePassword, CreatedAt: now, UpdatedAt: now,
		}
		if err := appUser.WithContext(ctx).Create(&row); err != nil {
			return translateUserWriteError(err)
		}
		if envelopes != nil {
			dek := store.UserDEK{
				UserID: user.ID, DEKEnvelope: envelopes.DEKEnvelope, KDFSalt: envelopes.KDFSalt, KDFParams: envelopes.KDFParams,
				RecoveryEnvelope: envelopes.RecoveryEnvelope, RecoveryHash: envelopes.RecoveryHash, CreatedAt: now, UpdatedAt: now,
			}
			if err := query.Use(tx).UserDEK.WithContext(ctx).Create(&dek); err != nil {
				return dbError(err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, "", dbError(err)
	}
	return user, generated, nil
}

func generateInitialPassword() (string, error) {
	raw := make([]byte, initialPasswordRandomBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", ipc.WrapError(ipc.CodeCrypto, "加密错误: 初始密码生成失败", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
