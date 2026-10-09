package vault

import (
	"context"
	"encoding/base64"
	"strings"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func (v *Vault) EncryptSecret(_ context.Context, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	dek, err := v.currentDEK()
	if err != nil {
		return "", err
	}
	defer dek.destroy()
	nonce, blob, err := seal(dek, []byte(plaintext), secretAAD)
	if err != nil {
		return "", err
	}
	return store.SecretEnvelopePrefix + base64.StdEncoding.EncodeToString(append(nonce, blob...)), nil
}

func (v *Vault) DecryptSecret(_ context.Context, envelope string) (string, error) {
	if !strings.HasPrefix(envelope, store.SecretEnvelopePrefix) {
		return "", ipc.NewError(ipc.CodeDecrypt, "凭据解密失败: 不是密文信封")
	}
	raw, err := base64.StdEncoding.DecodeString(envelope[len(store.SecretEnvelopePrefix):])
	if err != nil {
		return "", ipc.WrapError(ipc.CodeDecrypt, "凭据解密失败: 信封解码失败", err)
	}
	if len(raw) <= nonceLength {
		return "", ipc.NewError(ipc.CodeDecrypt, "凭据解密失败: 信封长度不合法")
	}
	dek, err := v.currentDEK()
	if err != nil {
		return "", err
	}
	defer dek.destroy()
	plaintext, err := open(dek, raw[:nonceLength], raw[nonceLength:], secretAAD)
	if err != nil {
		return "", err
	}
	result := string(plaintext)
	if !utf8.Valid(plaintext) {
		result = string([]rune(result))
	}
	for i := range plaintext {
		plaintext[i] = 0
	}
	return result, nil
}
