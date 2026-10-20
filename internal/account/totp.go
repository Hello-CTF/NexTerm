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
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

func (a *Accounts) TOTPEnabled(ctx context.Context, userID string) (bool, error) {
	userTOTP := query.Use(a.db).UserTOTP
	row, err := userTOTP.WithContext(ctx).Where(userTOTP.UserID.Eq(userID)).First()
	if err == gorm.ErrRecordNotFound {
		return false, nil
	}
	if err != nil {
		return false, dbError(err)
	}
	return len(row.SecretEnvelope) > 0, nil
}

func (a *Accounts) TOTPEnabledMap(ctx context.Context) (map[string]bool, error) {
	userTOTP := query.Use(a.db).UserTOTP
	rows := []store.UserTOTP{}
	if err := userTOTP.WithContext(ctx).Where(userTOTP.SecretEnvelope.IsNotNull()).Scan(&rows); err != nil {
		return nil, dbError(err)
	}
	enabled := make(map[string]bool, len(rows))
	for _, row := range rows {
		enabled[row.UserID] = true
	}
	return enabled, nil
}

func (a *Accounts) TOTPStatus(ctx context.Context, userID string) (enabled bool, pending bool, recoveryLeft int, err error) {
	userTOTP := query.Use(a.db).UserTOTP
	row, err := userTOTP.WithContext(ctx).Where(userTOTP.UserID.Eq(userID)).First()
	if err == gorm.ErrRecordNotFound {
		return false, false, 0, nil
	}
	if err != nil {
		return false, false, 0, dbError(err)
	}
	recoveryCode := query.Use(a.db).UserTOTPRecoveryCode
	count, err := recoveryCode.WithContext(ctx).Where(recoveryCode.UserID.Eq(userID), recoveryCode.UsedAt.IsNull()).Count()
	if err != nil {
		return false, false, 0, dbError(err)
	}
	return len(row.SecretEnvelope) > 0, len(row.PendingSecretEnvelope) > 0, int(count), nil
}

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
	appUser := query.Use(a.db).AppUser
	user, err := appUser.WithContext(ctx).Where(appUser.ID.Eq(userID)).First()
	if err == gorm.ErrRecordNotFound {
		return "", "", ipc.NewError(ipc.CodeNotFound, "用户不存在")
	}
	if err != nil {
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
	if err := a.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"pending_secret_envelope", "updated_at"}),
	}).Create(&store.UserTOTP{UserID: userID, PendingSecretEnvelope: envelope, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		return "", "", dbError(err)
	}
	a.epochs.notePending(userID)
	return encoded, totpAuthURI(user.Username, encoded), nil
}

func (a *Accounts) verifyTOTPRebind(ctx context.Context, userID, reverify string) error {
	if reverify == "" {
		return ipc.NewError(ipc.CodeForbidden, "已开启两步验证：更换绑定前需验证当前动态码、恢复码或登录密码")
	}
	if valid, err := a.VerifyTOTPLoginCode(ctx, userID, reverify); err != nil {
		return err
	} else if valid {
		return nil
	}
	appUser := query.Use(a.db).AppUser
	user, err := appUser.WithContext(ctx).Where(appUser.ID.Eq(userID)).First()
	if err != nil {
		return dbError(err)
	}
	if verifyPassword(user.PasswordHash, reverify) {
		return nil
	}
	return ipc.NewError(ipc.CodeForbidden, "验证失败: 动态码、恢复码或密码错误")
}

func (a *Accounts) ConfirmTOTPSetup(ctx context.Context, userID, code string) ([]string, error) {
	userTOTP := query.Use(a.db).UserTOTP
	row, err := userTOTP.WithContext(ctx).Where(userTOTP.UserID.Eq(userID)).First()
	if err == gorm.ErrRecordNotFound || (err == nil && len(row.PendingSecretEnvelope) == 0) {
		return nil, ipc.NewError(ipc.CodeBadParam, "参数错误: 尚未开始 TOTP 绑定")
	}
	if err != nil {
		return nil, dbError(err)
	}
	// The pending binding belongs to the account generation that started it; a
	// revocation, reset, or disable in between invalidates the confirm.
	if !a.epochs.checkPending(userID) {
		return nil, ipc.NewError(ipc.CodeBadParam, "参数错误：绑定已失效，请重新开始")
	}
	pending := row.PendingSecretEnvelope
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
	err = a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		totp := query.Use(tx).UserTOTP
		current, err := totp.WithContext(ctx).Where(totp.UserID.Eq(userID)).First()
		if err == gorm.ErrRecordNotFound || (err == nil && !bytes.Equal(current.PendingSecretEnvelope, pending)) {
			return ipc.NewError(ipc.CodeBadParam, "参数错误：绑定已确认或已失效，请重新开始")
		}
		if err != nil {
			return dbError(err)
		}
		info, err := totp.WithContext(ctx).
			Where(totp.UserID.Eq(userID), totp.PendingSecretEnvelope.Eq(pending)).
			UpdateColumns(map[string]any{"secret_envelope": pending, "pending_secret_envelope": nil, "updated_at": now})
		if err != nil {
			return dbError(err)
		}
		if info.RowsAffected == 0 {
			return ipc.NewError(ipc.CodeBadParam, "参数错误：绑定已确认或已失效，请重新开始")
		}
		recoveryCode := query.Use(tx).UserTOTPRecoveryCode
		if _, err := recoveryCode.WithContext(ctx).Where(recoveryCode.UserID.Eq(userID)).Delete(); err != nil {
			return dbError(err)
		}
		for _, code := range codes {
			if err := recoveryCode.WithContext(ctx).Create(&store.UserTOTPRecoveryCode{
				ID: ids.New(), UserID: userID, CodeHash: recoveryCodeHash(code), CreatedAt: now,
			}); err != nil {
				return dbError(err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, dbError(err)
	}
	formatted := make([]string, 0, len(codes))
	for _, code := range codes {
		formatted = append(formatted, formatRecoveryCode(code))
	}
	a.epochs.clearPending(userID)
	return formatted, nil
}

func (a *Accounts) VerifyTOTPLoginCode(ctx context.Context, userID, code string) (bool, error) {
	userTOTP := query.Use(a.db).UserTOTP
	row, err := userTOTP.WithContext(ctx).Where(userTOTP.UserID.Eq(userID)).First()
	if err == gorm.ErrRecordNotFound {
		return false, nil
	}
	if err != nil {
		return false, dbError(err)
	}
	if len(row.SecretEnvelope) == 0 {
		return false, nil
	}
	secret, err := a.openTOTPSecret(userID, row.SecretEnvelope)
	if err != nil {
		return false, err
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
	recoveryCode := query.Use(a.db).UserTOTPRecoveryCode
	row, err := recoveryCode.WithContext(ctx).
		Where(recoveryCode.UserID.Eq(userID), recoveryCode.CodeHash.Eq(recoveryCodeHash(code)), recoveryCode.UsedAt.IsNull()).
		First()
	if err == gorm.ErrRecordNotFound {
		return false, nil
	}
	if err != nil {
		return false, dbError(err)
	}
	info, err := recoveryCode.WithContext(ctx).
		Where(recoveryCode.ID.Eq(row.ID), recoveryCode.UsedAt.IsNull()).
		UpdateColumns(map[string]any{"used_at": a.now()})
	if err != nil {
		return false, dbError(err)
	}
	return info.RowsAffected > 0, nil
}

func (a *Accounts) DisableTOTP(ctx context.Context, userID, code string) error {
	valid, err := a.VerifyTOTPLoginCode(ctx, userID, code)
	if err != nil {
		return err
	}
	if !valid {
		return ipc.NewError(ipc.CodeForbidden, "验证码错误")
	}
	err = a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := query.Use(tx).UserTOTP.WithContext(ctx).Where(query.Use(tx).UserTOTP.UserID.Eq(userID)).Delete(); err != nil {
			return dbError(err)
		}
		if _, err := query.Use(tx).UserTOTPRecoveryCode.WithContext(ctx).Where(query.Use(tx).UserTOTPRecoveryCode.UserID.Eq(userID)).Delete(); err != nil {
			return dbError(err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.epochs.clearPending(userID)
	return nil
}

func (a *Accounts) MFARequired(ctx context.Context) (bool, error) {
	setting := query.Use(a.db).Setting
	row, err := setting.WithContext(ctx).Where(setting.Key.Eq(mfaRequiredSettingKey)).First()
	if err == gorm.ErrRecordNotFound {
		return false, nil
	}
	if err != nil {
		return false, dbError(err)
	}
	return row.Value == "true", nil
}

func (a *Accounts) SetMFARequired(ctx context.Context, required bool) error {
	value := "false"
	if required {
		value = "true"
	}
	if err := a.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&store.Setting{Key: mfaRequiredSettingKey, Value: value, UpdatedAt: a.now()}).Error; err != nil {
		return dbError(err)
	}
	return nil
}
