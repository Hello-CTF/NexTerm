package account

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const (
	totpPeriodSeconds = 30
	totpDigits        = 6
	totpSecretBytes   = 20
	totpWindow        = 1
	totpNonceLength   = 12
	totpKeyBytes      = 32

	mfaRequiredSettingKey = "auth.mfa_required"

	recoveryCodeCount      = 8
	recoveryCodeBytes      = 10
	recoveryCodeEncodedLen = 16
)

var (
	totpSecretEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)
	totpSecretAAD      = []byte("nexterm/go/user-totp/v1")
)

func generateTOTPSecret() ([]byte, error) {
	secret := make([]byte, totpSecretBytes)
	if _, err := rand.Read(secret); err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: TOTP 密钥生成失败", err)
	}
	return secret, nil
}

func hotp(secret []byte, counter uint64) uint32 {
	mac := hmac.New(sha1.New, secret)
	var counterBytes [8]byte
	binary.BigEndian.PutUint64(counterBytes[:], counter)
	_, _ = mac.Write(counterBytes[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return value % 1_000_000
}

func verifyTOTPCode(secret []byte, code string, nowSeconds int64) bool {
	if len(code) != totpDigits {
		return false
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return false
		}
	}
	counter := uint64(nowSeconds / totpPeriodSeconds)
	for step := -totpWindow; step <= totpWindow; step++ {
		candidate := counter + uint64(step)
		if step < 0 && candidate > counter {
			continue
		}
		expected := fmt.Sprintf("%0*d", totpDigits, hotp(secret, candidate))
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

func totpAAD(userID string) []byte {
	aad := make([]byte, 0, len(totpSecretAAD)+1+len(userID))
	aad = append(aad, totpSecretAAD...)
	aad = append(aad, '|')
	aad = append(aad, userID...)
	return aad
}

func (a *Accounts) sealTOTPSecret(userID string, secret []byte) ([]byte, error) {
	key, err := a.totpKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: "+err.Error(), err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: "+err.Error(), err)
	}
	nonce := make([]byte, totpNonceLength)
	if _, err := rand.Read(nonce); err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: nonce 生成失败", err)
	}
	return aead.Seal(nonce, nonce, secret, totpAAD(userID)), nil
}

func (a *Accounts) openTOTPSecret(userID string, envelope []byte) ([]byte, error) {
	if len(envelope) <= totpNonceLength {
		return nil, ipc.NewError(ipc.CodeDecrypt, "TOTP 密钥信封长度不合法")
	}
	key, err := a.totpKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: "+err.Error(), err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: "+err.Error(), err)
	}
	plaintext, err := aead.Open(nil, envelope[:totpNonceLength], envelope[totpNonceLength:], totpAAD(userID))
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeDecrypt, "TOTP 密钥解密失败", err)
	}
	return plaintext, nil
}

func generateRecoveryCodes() ([]string, error) {
	codes := make([]string, 0, recoveryCodeCount)
	for i := 0; i < recoveryCodeCount; i++ {
		raw := make([]byte, recoveryCodeBytes)
		if _, err := rand.Read(raw); err != nil {
			return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: 恢复码生成失败", err)
		}
		codes = append(codes, totpSecretEncoding.EncodeToString(raw))
	}
	return codes, nil
}

func normalizeRecoveryCode(input string) string {
	replaced := strings.NewReplacer("-", "", " ", "", "\t", "").Replace(input)
	return strings.ToUpper(replaced)
}

func recoveryCodeHash(code string) string {
	digest := sha256.Sum256([]byte(normalizeRecoveryCode(code)))
	return hex.EncodeToString(digest[:])
}

func formatRecoveryCode(code string) string {
	var b strings.Builder
	for i := 0; i+4 <= len(code); i += 4 {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(code[i : i+4])
	}
	return b.String()
}

func totpAuthURI(username, secret string) string {
	label := url.PathEscape("NexTerm:" + username)
	return "otpauth://totp/" + label + "?secret=" + secret + "&issuer=NexTerm"
}

// TOTPEnabled 只反映已完成确认的绑定; 登录门禁用它决定是否进入 TOTP 一步。
func (a *Accounts) TOTPEnabled(ctx context.Context, userID string) (bool, error) {
	var envelope []byte
	err := a.db.QueryRowContext(ctx, "SELECT secret_envelope FROM user_totp WHERE user_id = ?", userID).Scan(&envelope)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, dbError(err)
	}
	return len(envelope) > 0, nil
}

// TOTPEnabledMap 返回全部已完成 TOTP 绑定的用户 id 集合, 供管理端列表一次取回。
func (a *Accounts) TOTPEnabledMap(ctx context.Context) (map[string]bool, error) {
	rows, err := a.db.QueryContext(ctx, "SELECT user_id FROM user_totp WHERE secret_envelope IS NOT NULL")
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	enabled := make(map[string]bool)
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, dbError(err)
		}
		enabled[userID] = true
	}
	return enabled, rows.Err()
}

// TOTPStatus 汇报用户 TOTP 绑定状态与剩余恢复码数量。
func (a *Accounts) TOTPStatus(ctx context.Context, userID string) (enabled bool, pending bool, recoveryLeft int, err error) {
	var secretEnvelope, pendingEnvelope []byte
	err = a.db.QueryRowContext(ctx, "SELECT secret_envelope, pending_secret_envelope FROM user_totp WHERE user_id = ?", userID).
		Scan(&secretEnvelope, &pendingEnvelope)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, 0, nil
	}
	if err != nil {
		return false, false, 0, dbError(err)
	}
	if err := a.db.QueryRowContext(ctx, "SELECT count(*) FROM user_totp_recovery_code WHERE user_id = ? AND used_at IS NULL", userID).
		Scan(&recoveryLeft); err != nil {
		return false, false, 0, dbError(err)
	}
	return len(secretEnvelope) > 0, len(pendingEnvelope) > 0, recoveryLeft, nil
}

// BeginTOTPSetup 生成新密钥并写入待确认信封; 已有绑定不受影响, 确认后才替换。
// 已绑定用户必须用 reverify(当前动态码、恢复码或登录密码)完成重验, 防止劫持的会话静默换绑。
func (a *Accounts) BeginTOTPSetup(ctx context.Context, userID, reverify string) (secret string, otpauthURI string, returnErr error) {
	enabled, err := a.TOTPEnabled(ctx, userID)
	if err != nil {
		return "", "", err
	}
	if enabled {
		if err := a.verifyTOTPRebind(ctx, userID, reverify); err != nil {
			return "", "", err
		}
	}
	var username string
	if err := a.db.QueryRowContext(ctx, "SELECT username FROM "+userTable+" WHERE id = ?", userID).Scan(&username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", ipc.NewError(ipc.CodeNotFound, "用户不存在")
		}
		return "", "", dbError(err)
	}
	secretBytes, err := generateTOTPSecret()
	if err != nil {
		return "", "", err
	}
	envelope, err := a.sealTOTPSecret(userID, secretBytes)
	if err != nil {
		return "", "", err
	}
	encoded := totpSecretEncoding.EncodeToString(secretBytes)
	now := a.now()
	if _, err := a.db.ExecContext(ctx, `INSERT INTO user_totp(user_id, secret_envelope, pending_secret_envelope, created_at, updated_at)
VALUES(?,NULL,?,?,?)
ON CONFLICT(user_id) DO UPDATE SET pending_secret_envelope = excluded.pending_secret_envelope, updated_at = excluded.updated_at`,
		userID, envelope, now, now); err != nil {
		return "", "", dbError(err)
	}
	return encoded, totpAuthURI(username, encoded), nil
}

// verifyTOTPRebind 校验换绑重验凭据: 当前动态码/恢复码优先, 其次登录密码。
func (a *Accounts) verifyTOTPRebind(ctx context.Context, userID, reverify string) error {
	if reverify == "" {
		return ipc.NewError(ipc.CodeForbidden, "已开启两步验证: 换绑前需要当前动态码、恢复码或登录密码")
	}
	if valid, err := a.VerifyTOTPLoginCode(ctx, userID, reverify); err != nil {
		return err
	} else if valid {
		return nil
	}
	var hash string
	if err := a.db.QueryRowContext(ctx, "SELECT password_hash FROM "+userTable+" WHERE id = ?", userID).Scan(&hash); err != nil {
		return dbError(err)
	}
	if verifyPassword(hash, reverify) {
		return nil
	}
	return ipc.NewError(ipc.CodeForbidden, "验证失败: 动态码、恢复码或密码错误")
}

// ConfirmTOTPSetup 用待确认密钥校验动态码, 通过后转正并签发一次性恢复码(明文只在返回值里出现这一次)。
func (a *Accounts) ConfirmTOTPSetup(ctx context.Context, userID, code string) ([]string, error) {
	var pending []byte
	err := a.db.QueryRowContext(ctx, "SELECT pending_secret_envelope FROM user_totp WHERE user_id = ?", userID).Scan(&pending)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && len(pending) == 0) {
		return nil, ipc.NewError(ipc.CodeBadParam, "参数错误: 尚未开始 TOTP 绑定")
	}
	if err != nil {
		return nil, dbError(err)
	}
	secret, err := a.openTOTPSecret(userID, pending)
	if err != nil {
		return nil, err
	}
	if !verifyTOTPCode(secret, code, a.now()/1000) {
		return nil, ipc.NewError(ipc.CodeForbidden, "验证码错误")
	}
	codes, err := generateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	now := a.now()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var current []byte
	err = tx.QueryRowContext(ctx, "SELECT pending_secret_envelope FROM user_totp WHERE user_id = ?", userID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !bytes.Equal(current, pending)) {
		return nil, ipc.NewError(ipc.CodeBadParam, "参数错误: 绑定已确认或已失效,请重新开始")
	}
	if err != nil {
		return nil, dbError(err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE user_totp SET secret_envelope = ?, pending_secret_envelope = NULL, updated_at = ?
WHERE user_id = ? AND pending_secret_envelope = ?`, pending, now, userID, pending)
	if err != nil {
		return nil, dbError(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected == 0 {
		return nil, ipc.NewError(ipc.CodeBadParam, "参数错误: 绑定已确认或已失效,请重新开始")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_totp_recovery_code WHERE user_id = ?", userID); err != nil {
		return nil, dbError(err)
	}
	for _, code := range codes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_totp_recovery_code(id, user_id, code_hash, created_at)
VALUES(?,?,?,?)`, ids.New(), userID, recoveryCodeHash(code), now); err != nil {
			return nil, dbError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, dbError(err)
	}
	formatted := make([]string, 0, len(codes))
	for _, code := range codes {
		formatted = append(formatted, formatRecoveryCode(code))
	}
	return formatted, nil
}

// VerifyTOTPLoginCode 校验第二因子: 6 位动态码或一次性恢复码; 恢复码命中即作废。
func (a *Accounts) VerifyTOTPLoginCode(ctx context.Context, userID, code string) (bool, error) {
	var envelope []byte
	err := a.db.QueryRowContext(ctx, "SELECT secret_envelope FROM user_totp WHERE user_id = ?", userID).Scan(&envelope)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, dbError(err)
	}
	if len(envelope) == 0 {
		return false, nil
	}
	secret, err := a.openTOTPSecret(userID, envelope)
	if err != nil {
		if !a.legacyTOTPKeyPresent(ctx) {
			return false, err
		}
		// 旧库存储密钥已不再迁移, 动态码无法校验; 恢复码独立存储仍可用于登录后重绑。
		if valid, recErr := a.consumeRecoveryCode(ctx, userID, code); recErr != nil {
			return false, recErr
		} else if valid {
			return true, nil
		}
		return false, ipc.NewError(ipc.CodeCrypto, "加密错误: 两步验证密钥无法解密(存储密钥已更换), 请用恢复码登录后重新绑定 TOTP")
	}
	if verifyTOTPCode(secret, code, a.now()/1000) {
		return true, nil
	}
	return a.consumeRecoveryCode(ctx, userID, code)
}

func (a *Accounts) consumeRecoveryCode(ctx context.Context, userID, code string) (bool, error) {
	if len(normalizeRecoveryCode(code)) != recoveryCodeEncodedLen {
		return false, nil
	}
	var id string
	err := a.db.QueryRowContext(ctx, "SELECT id FROM user_totp_recovery_code WHERE user_id = ? AND code_hash = ? AND used_at IS NULL",
		userID, recoveryCodeHash(code)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, dbError(err)
	}
	result, err := a.db.ExecContext(ctx, "UPDATE user_totp_recovery_code SET used_at = ? WHERE id = ? AND used_at IS NULL", a.now(), id)
	if err != nil {
		return false, dbError(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected == 0 {
		return false, nil
	}
	return true, nil
}

// DisableTOTP 用当前动态码或恢复码解绑; 解绑同时清除全部恢复码。
func (a *Accounts) DisableTOTP(ctx context.Context, userID, code string) error {
	valid, err := a.VerifyTOTPLoginCode(ctx, userID, code)
	if err != nil {
		return err
	}
	if !valid {
		return ipc.NewError(ipc.CodeForbidden, "验证码错误")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_totp WHERE user_id = ?", userID); err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_totp_recovery_code WHERE user_id = ?", userID); err != nil {
		return dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}

// MFARequired 读取「强制要求 MFA」策略开关; 缺省关闭。开启后未绑定用户的会话被锁到只能完成 TOTP 绑定。
func (a *Accounts) MFARequired(ctx context.Context) (bool, error) {
	var value string
	err := a.db.QueryRowContext(ctx, "SELECT value FROM setting WHERE key = ?", mfaRequiredSettingKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, dbError(err)
	}
	return value == "true", nil
}

func (a *Accounts) SetMFARequired(ctx context.Context, required bool) error {
	value := "false"
	if required {
		value = "true"
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		mfaRequiredSettingKey, value, a.now()); err != nil {
		return dbError(err)
	}
	return nil
}
