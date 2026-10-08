package vault

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"golang.org/x/crypto/argon2"
)

const (
	recoveryKeyBytes = 20
	maxKDFTime       = 16
	maxKDFMemoryKiB  = 2 * 1024 * 1024
	maxKDFThreads    = 16
	minKDFMemoryKiB  = 8 * 1024
)

var recoveryKeyEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

type UserDEKKDFParams struct {
	Time      uint32 `json:"t"`
	MemoryKiB uint32 `json:"m"`
	Threads   uint32 `json:"p"`
}

var userDEKKDFDefaults = UserDEKKDFParams{Time: 3, MemoryKiB: 64 * 1024, Threads: 4}

type UserDEKEnvelopes struct {
	DEKEnvelope      []byte
	KDFSalt          []byte
	KDFParams        string
	RecoveryEnvelope []byte
	RecoveryHash     string
}

func GenerateUserDEK() []byte {
	dek := make([]byte, DEKLength)
	randomBytes(dek)
	return dek
}

func GenerateUserDEKEnvelopes(password string) (dek []byte, envelopes *UserDEKEnvelopes, recoveryKey string, returnErr error) {
	dek = GenerateUserDEK()
	envelope, salt, params, err := WrapUserDEK(password, dek)
	if err != nil {
		return nil, nil, "", err
	}
	canonical, err := GenerateRecoveryKey()
	if err != nil {
		return nil, nil, "", err
	}
	recoveryEnvelope, err := WrapUserDEKWithRecovery(canonical, dek)
	if err != nil {
		return nil, nil, "", err
	}
	return dek, &UserDEKEnvelopes{
		DEKEnvelope:      envelope,
		KDFSalt:          salt,
		KDFParams:        params,
		RecoveryEnvelope: recoveryEnvelope,
		RecoveryHash:     RecoveryKeyHash(canonical),
	}, FormatRecoveryKey(canonical), nil
}

func WrapUserDEK(password string, dek []byte) (envelope, salt []byte, params string, returnErr error) {
	if len(dek) != DEKLength {
		return nil, nil, "", ipc.NewError(ipc.CodeCrypto, "加密错误: 密钥长度不合法")
	}
	salt = make([]byte, 16)
	randomBytes(salt)
	kek, err := deriveUserKEK(password, salt, userDEKKDFDefaults)
	if err != nil {
		return nil, nil, "", err
	}
	defer kek.destroy()
	nonce, ciphertext, err := seal(kek, dek, userDEKAAD)
	if err != nil {
		return nil, nil, "", err
	}
	encoded, err := json.Marshal(userDEKKDFDefaults)
	if err != nil {
		return nil, nil, "", ipc.WrapError(ipc.CodeCrypto, "加密错误: KDF 参数编码失败", err)
	}
	return append(nonce, ciphertext...), salt, string(encoded), nil
}

func UnwrapUserDEK(password string, envelope, salt []byte, params string) ([]byte, error) {
	kdf, err := parseUserDEKKDFParams(params)
	if err != nil {
		return nil, err
	}
	kek, err := deriveUserKEK(password, salt, *kdf)
	if err != nil {
		return nil, err
	}
	defer kek.destroy()
	if len(envelope) != nonceLength+DEKLength+16 {
		return nil, ipc.NewError(ipc.CodeDecrypt, "凭据解密失败: 账号加密密钥数据长度不合法")
	}
	plaintext, err := open(kek, envelope[:nonceLength], envelope[nonceLength:], userDEKAAD)
	if err != nil {
		return nil, err
	}
	dek := make([]byte, DEKLength)
	copy(dek, plaintext)
	for i := range plaintext {
		plaintext[i] = 0
	}
	return dek, nil
}

func WrapUserDEKWithRecovery(recoveryKey string, dek []byte) ([]byte, error) {
	if len(dek) != DEKLength {
		return nil, ipc.NewError(ipc.CodeCrypto, "加密错误: 密钥长度不合法")
	}
	if NormalizeRecoveryKey(recoveryKey) == "" {
		return nil, ipc.BadParam(errString("恢复密钥为空"))
	}
	key := recoveryKeyMaterial(recoveryKey)
	defer clear(key)
	kek := &secretKey{}
	copy(kek.bytes[:], key)
	defer kek.destroy()
	nonce, ciphertext, err := seal(kek, dek, userDEKRecoveryAAD)
	if err != nil {
		return nil, err
	}
	return append(nonce, ciphertext...), nil
}

func UnwrapUserDEKWithRecovery(recoveryKey string, envelope []byte) ([]byte, error) {
	if NormalizeRecoveryKey(recoveryKey) == "" {
		return nil, ipc.BadParam(errString("恢复密钥为空"))
	}
	if len(envelope) != nonceLength+DEKLength+16 {
		return nil, ipc.NewError(ipc.CodeDecrypt, "凭据解密失败: 恢复密钥数据长度不合法")
	}
	key := recoveryKeyMaterial(recoveryKey)
	defer clear(key)
	kek := &secretKey{}
	copy(kek.bytes[:], key)
	defer kek.destroy()
	plaintext, err := open(kek, envelope[:nonceLength], envelope[nonceLength:], userDEKRecoveryAAD)
	if err != nil {
		return nil, err
	}
	dek := make([]byte, DEKLength)
	copy(dek, plaintext)
	for i := range plaintext {
		plaintext[i] = 0
	}
	return dek, nil
}

func GenerateRecoveryKey() (string, error) {
	raw := make([]byte, recoveryKeyBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", ipc.WrapError(ipc.CodeCrypto, "加密错误: 恢复密钥生成失败", err)
	}
	return recoveryKeyEncoding.EncodeToString(raw), nil
}

func FormatRecoveryKey(canonical string) string {
	canonical = NormalizeRecoveryKey(canonical)
	groups := make([]string, 0, len(canonical)/4)
	for i := 0; i+4 <= len(canonical); i += 4 {
		groups = append(groups, canonical[i:i+4])
	}
	return strings.Join(groups, "-")
}

func NormalizeRecoveryKey(input string) string {
	replaced := strings.NewReplacer("-", "", " ", "", "\t", "").Replace(input)
	return strings.ToUpper(replaced)
}

func RecoveryKeyHash(recoveryKey string) string {
	digest := sha256.Sum256([]byte(NormalizeRecoveryKey(recoveryKey)))
	return hex.EncodeToString(digest[:])
}

func VerifyRecoveryKey(recoveryKey, hash string) bool {
	computed, err := hex.DecodeString(RecoveryKeyHash(recoveryKey))
	if err != nil {
		return false
	}
	stored, err := hex.DecodeString(hash)
	if err != nil || len(stored) != sha256.Size {
		return false
	}
	return subtle.ConstantTimeCompare(computed, stored) == 1
}

func deriveUserKEK(password string, salt []byte, params UserDEKKDFParams) (*secretKey, error) {
	if len(salt) < 16 {
		return nil, ipc.NewError(ipc.CodeCrypto, "加密错误: salt 过短")
	}
	key := &secretKey{}
	copy(key.bytes[:], argon2.IDKey([]byte(password), salt, params.Time, params.MemoryKiB, uint8(params.Threads), DEKLength))
	return key, nil
}

func parseUserDEKKDFParams(raw string) (*UserDEKKDFParams, error) {
	var params UserDEKKDFParams
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, ipc.WrapError(ipc.CodeBadParam, "参数错误: KDF 参数不是合法 JSON", err)
	}
	if params.Time == 0 || params.Time > maxKDFTime ||
		params.MemoryKiB < minKDFMemoryKiB || params.MemoryKiB > maxKDFMemoryKiB ||
		params.Threads == 0 || params.Threads > maxKDFThreads {
		return nil, ipc.NewError(ipc.CodeBadParam, "参数错误: KDF 参数越界")
	}
	return &params, nil
}

func recoveryKeyMaterial(recoveryKey string) []byte {
	canonical := NormalizeRecoveryKey(recoveryKey)
	decoded, err := recoveryKeyEncoding.DecodeString(canonical)
	if err != nil || len(decoded) != recoveryKeyBytes {
		return []byte(canonical)
	}
	return decoded
}
