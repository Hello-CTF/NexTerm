package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

const initCodeSettingKey = "auth.init_code"

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
	if _, err := a.db.ExecContext(ctx, `INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)
ON CONFLICT(key) DO NOTHING`, initCodeSettingKey, string(encoded), a.now()); err != nil {
		return "", dbError(err)
	}
	var existing string
	if err := a.db.QueryRowContext(ctx, "SELECT value FROM setting WHERE key = ?", initCodeSettingKey).Scan(&existing); err != nil {
		return "", dbError(err)
	}
	if existing != string(encoded) {
		return "", ipc.NewError(ipc.CodeForbidden, "初始化码已生成，请使用服务器控制台输出的码")
	}
	return code, nil
}

func (a *Accounts) consumeInitCode(ctx context.Context, tx *sql.Tx, code string) error {
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT value FROM setting WHERE key = ?", initCodeSettingKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ipc.NewError(ipc.CodeForbidden, "初始化码不存在，请重启服务器生成")
	}
	if err != nil {
		return dbError(err)
	}
	var state initCodeState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
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
	result, err := tx.ExecContext(ctx, "UPDATE setting SET value = ?, updated_at = ? WHERE key = ? AND value = ?",
		string(consumed), a.now(), initCodeSettingKey, raw)
	if err != nil {
		return dbError(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return ipc.NewError(ipc.CodeForbidden, "初始化码已被使用，服务器已完成初始化，请直接登录")
	}
	return nil
}

func (a *Accounts) InitSuperadmin(ctx context.Context, code, username, password string) (*User, error) {
	return a.initSuperadmin(ctx, code, username, password, nil)
}

// InitSuperadminWithEnvelopes 在同一个事务里消费初始化码、创建超管并写入初始 DEK 信封;
// 信封写入失败时初始化码保持未消费, 可重试。
func (a *Accounts) InitSuperadminWithEnvelopes(ctx context.Context, code, username, password string, envelopes *vault.UserDEKEnvelopes) (*User, error) {
	if err := validateEnvelopes(envelopes); err != nil {
		return nil, err
	}
	return a.initSuperadmin(ctx, code, username, password, envelopes)
}

func (a *Accounts) initSuperadmin(ctx context.Context, code, username, password string, envelopes *vault.UserDEKEnvelopes) (*User, error) {
	if err := validateUsername(username); err != nil {
		return nil, err
	}
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var users int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+userTable).Scan(&users); err != nil {
		return nil, dbError(err)
	}
	if users > 0 {
		return nil, ipc.NewError(ipc.CodeForbidden, "服务器已完成初始化，不能重复初始化")
	}
	if err := a.consumeInitCode(ctx, tx, code); err != nil {
		return nil, err
	}
	now := a.now()
	user := &User{
		ID:           ids.New(),
		Username:     username,
		Role:         RoleSuperadmin,
		State:        StateActive,
		CreatedAt:    now,
		UpdatedAt:    now,
		passwordHash: hash,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+userTable+`(id, username, display_name, role, password_hash, state, must_change_password, created_at, updated_at)
VALUES(?,?,?,?,?,?,0,?,?)`, user.ID, user.Username, "", string(user.Role), hash, string(user.State), now, now); err != nil {
		return nil, translateUserWriteError(err)
	}
	if envelopes != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_dek(user_id, dek_envelope, kdf_salt, kdf_params, recovery_envelope, recovery_hash, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?)`, user.ID, envelopes.DEKEnvelope, envelopes.KDFSalt, envelopes.KDFParams,
			envelopes.RecoveryEnvelope, envelopes.RecoveryHash, now, now); err != nil {
			return nil, dbError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, dbError(err)
	}
	return user, nil
}
