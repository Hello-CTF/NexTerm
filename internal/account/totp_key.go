package account

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/atomicfile"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// totpKeyMu serializes the "read-or-create" path within a process, so concurrent first use never writes two different keys.
var totpKeyMu sync.Mutex

// WithTOTPKeyFile stores the TOTP global storage key in an out-of-database 0600 file (same protection level as the server master key file).
// When path is empty TOTP encryption is unavailable: a full database leak must not leak the TOTP key, so unconfigured fails closed.
func WithTOTPKeyFile(path string) Option {
	return func(a *Accounts) {
		a.totpKeyPath = path
	}
}

func readTOTPKeyFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(key) != totpKeyBytes {
		return nil, ipc.NewError(ipc.CodeCrypto, "加密错误: TOTP 存储密钥文件损坏")
	}
	return key, nil
}

func writeTOTPKeyFile(path string, key []byte) error {
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := atomicfile.Write(path, 0o600, func(writer io.Writer) error {
		_, err := io.WriteString(writer, encoded)
		return err
	}); err != nil {
		return ipc.WrapError(ipc.CodeCrypto, "加密错误: TOTP 存储密钥文件写入失败", err)
	}
	return nil
}

func (a *Accounts) totpKey() ([]byte, error) {
	if a.totpKeyPath == "" {
		return nil, ipc.NewError(ipc.CodeCrypto, "加密错误：TOTP 存储密钥未配置（缺少密钥文件）")
	}
	totpKeyMu.Lock()
	defer totpKeyMu.Unlock()
	key, err := readTOTPKeyFile(a.totpKeyPath)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key = make([]byte, totpKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: TOTP 存储密钥生成失败", err)
	}
	if err := writeTOTPKeyFile(a.totpKeyPath, key); err != nil {
		return nil, err
	}
	return key, nil
}
