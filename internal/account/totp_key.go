package account

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/atomicfile"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
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

// legacyTOTPKey 读取旧版本存放在 setting 表的全局密钥(仅迁移用途, 读出不删)。
func (a *Accounts) legacyTOTPKey(ctx context.Context) ([]byte, bool, error) {
	var value string
	err := a.db.QueryRowContext(ctx, "SELECT value FROM setting WHERE key = ?", totpKeySetting).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, dbError(err)
	}
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(key) != totpKeyBytes {
		return nil, false, ipc.NewError(ipc.CodeCrypto, "加密错误: TOTP 存储密钥损坏")
	}
	return key, true, nil
}

func (a *Accounts) deleteLegacyTOTPKey(ctx context.Context) error {
	if _, err := a.db.ExecContext(ctx, "DELETE FROM setting WHERE key = ?", totpKeySetting); err != nil {
		return dbError(err)
	}
	return nil
}

// MigrateTOTPStorageKey 在启动时把 setting 表里的旧版 TOTP 全局密钥迁移到库外密钥文件:
// 文件缺失时以库内密钥为准重建文件, 文件已存在时仅清理库内残留副本。
func (a *Accounts) MigrateTOTPStorageKey(ctx context.Context) error {
	if a.totpKeyPath == "" {
		return nil
	}
	legacy, found, err := a.legacyTOTPKey(ctx)
	if err != nil || !found {
		return err
	}
	if _, statErr := os.Stat(a.totpKeyPath); statErr != nil {
		if !errors.Is(statErr, os.ErrNotExist) {
			return ipc.WrapError(ipc.CodeCrypto, "加密错误: TOTP 存储密钥文件不可访问", statErr)
		}
		if err := writeTOTPKeyFile(a.totpKeyPath, legacy); err != nil {
			return err
		}
	}
	return a.deleteLegacyTOTPKey(ctx)
}

func (a *Accounts) totpKey(ctx context.Context) ([]byte, error) {
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
	if legacy, found, err := a.legacyTOTPKey(ctx); err != nil {
		return nil, err
	} else if found {
		if err := writeTOTPKeyFile(a.totpKeyPath, legacy); err != nil {
			return nil, err
		}
		if err := a.deleteLegacyTOTPKey(ctx); err != nil {
			return nil, err
		}
		return legacy, nil
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
