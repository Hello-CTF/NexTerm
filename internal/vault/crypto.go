package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"runtime"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"golang.org/x/crypto/argon2"
)

const (
	DEKLength   = 32
	nonceLength = 12
)

var (
	dekAAD             = []byte("nexterm/go/dek/v1")
	userDEKAAD         = []byte("nexterm/go/user-dek/v1")
	userDEKRecoveryAAD = []byte("nexterm/go/user-dek-recovery/v1")
	credentialAAD      = []byte("nexterm/go/credential/v1")
	secretAAD          = []byte("nexterm/go/secret/v1")
)

type secretKey struct {
	bytes [DEKLength]byte
}

func randomBytes(buffer []byte) {
	_, _ = rand.Read(buffer)
}

func generateKey() *secretKey {
	key := &secretKey{}
	randomBytes(key.bytes[:])
	return key
}

func (k *secretKey) clone() *secretKey {
	if k == nil {
		return nil
	}
	clone := &secretKey{}
	copy(clone.bytes[:], k.bytes[:])
	return clone
}

func (k *secretKey) destroy() {
	if k == nil {
		return
	}
	for i := range k.bytes {
		k.bytes[i] = 0
	}
	runtime.KeepAlive(k)
}

func deriveMasterKey(password string, salt []byte) (*secretKey, error) {
	if len(salt) < 16 {
		return nil, ipc.NewError(ipc.CodeCrypto, "加密错误: salt 过短")
	}
	key := &secretKey{}
	copy(key.bytes[:], argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, DEKLength))
	return key, nil
}

func newAEAD(key *secretKey) (cipher.AEAD, error) {
	if key == nil {
		return nil, ipc.NewError(ipc.CodeCrypto, "加密错误: 密钥不存在")
	}
	block, err := aes.NewCipher(key.bytes[:])
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: "+err.Error(), err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: "+err.Error(), err)
	}
	return aead, nil
}

func seal(key *secretKey, plaintext, aad []byte) ([]byte, []byte, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, nonceLength)
	_, _ = rand.Read(nonce)
	return nonce, aead.Seal(nil, nonce, plaintext, aad), nil
}

func open(key *secretKey, nonce, ciphertext, aad []byte) ([]byte, error) {
	if len(nonce) != nonceLength {
		return nil, ipc.NewError(ipc.CodeDecrypt, "凭据解密失败: nonce 长度不合法")
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeDecrypt, "凭据解密失败（密钥不匹配或数据损坏）", err)
	}
	return plaintext, nil
}

func sealDEK(kek, dek *secretKey) ([]byte, error) {
	nonce, ciphertext, err := seal(kek, dek.bytes[:], dekAAD)
	if err != nil {
		return nil, err
	}
	return append(nonce, ciphertext...), nil
}

func openDEK(kek *secretKey, envelope []byte) (*secretKey, error) {
	if len(envelope) != nonceLength+DEKLength+16 {
		return nil, ipc.NewError(ipc.CodeDecrypt, "凭据解密失败: DEK 信封长度不合法")
	}
	plaintext, err := open(kek, envelope[:nonceLength], envelope[nonceLength:], dekAAD)
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeBadMasterPass, "主密码错误", err)
	}
	dek := &secretKey{}
	copy(dek.bytes[:], plaintext)
	for i := range plaintext {
		plaintext[i] = 0
	}
	return dek, nil
}

func sealCredential(dek *secretKey, plaintext []byte) ([]byte, []byte, error) {
	return seal(dek, plaintext, credentialAAD)
}

func openCredential(dek *secretKey, nonce, blob []byte) ([]byte, error) {
	plaintext, err := open(dek, nonce, blob, credentialAAD)
	if err != nil {
		return nil, err
	}
	if len(plaintext) == 0 {
		return []byte{}, nil
	}
	return plaintext, nil
}

func decodeDEK(plaintext []byte) (*secretKey, error) {
	if len(plaintext) != DEKLength {
		return nil, ipc.NewError(ipc.CodeDecrypt, fmt.Sprintf("凭据解密失败: DEK 长度为 %d，应为 %d", len(plaintext), DEKLength))
	}
	dek := &secretKey{}
	copy(dek.bytes[:], plaintext)
	return dek, nil
}
