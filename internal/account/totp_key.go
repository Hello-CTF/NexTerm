package account

import (
	"context"
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

// totpKeyMu 串行化同一进程内的「读不到则创建」路径, 避免并发首用写出两把不同的密钥。
var totpKeyMu sync.Mutex

// WithTOTPKeyFile 把 TOTP 全局存储密钥放到库外 0600 文件(与服务器主密钥文件同一保护级别)。
// path 为空时 TOTP 加解密不可用: 整库泄露不得顺带泄露 TOTP 密钥, 未配置必须 fail-closed。
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

// legacyTOTPKeyPresent 检测 setting 表是否残留旧版本存放的 TOTP 全局密钥行。
func (a *Accounts) legacyTOTPKeyPresent(ctx context.Context) bool {
	var count int
	err := a.db.QueryRowContext(ctx, "SELECT count(*) FROM setting WHERE key = ?", "auth.totp_key").Scan(&count)
	return err == nil && count > 0
}

func (a *Accounts) totpKey() ([]byte, error) {
	if a.totpKeyPath == "" {
		return nil, ipc.NewError(ipc.CodeCrypto, "加密错误: TOTP 存储密钥未配置(缺少库外密钥文件)")
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
