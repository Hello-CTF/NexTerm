// syncv2-helper 为 scripts/e2e-sync-local.py 提供 v2 同步协议的真实密码学原语,
// 使 Python 验收脚本无需第三方依赖即可完成账号 DEK 信封与对象信封的加解密。
package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/Hello-CTF/NexTerm/internal/vault"
)

const (
	objectAADPrefix = "nexterm/go/sync-object/v1"
	objectNonceSize = 12
)

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "syncv2-helper:", err)
	os.Exit(1)
}

func genDEK(password string) {
	dek, envelopes, _, err := vault.GenerateUserDEKEnvelopes(password)
	if err != nil {
		fatal(err)
	}
	out := map[string]string{
		"dek":               hex.EncodeToString(dek),
		"dek_envelope":      base64.StdEncoding.EncodeToString(envelopes.DEKEnvelope),
		"kdf_salt":          base64.StdEncoding.EncodeToString(envelopes.KDFSalt),
		"kdf_params":        envelopes.KDFParams,
		"recovery_envelope": base64.StdEncoding.EncodeToString(envelopes.RecoveryEnvelope),
		"recovery_hash":     envelopes.RecoveryHash,
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		fatal(err)
	}
	fmt.Println(string(encoded))
}

func objectAAD(id, kind string) []byte {
	return []byte(objectAADPrefix + "\x00" + id + "\x00" + kind)
}

func seal(dekHex, id, kind string) {
	dek, err := hex.DecodeString(dekHex)
	if err != nil || len(dek) != 32 {
		fatal(fmt.Errorf("DEK 必须是 32 字节十六进制"))
	}
	plaintext, err := io.ReadAll(os.Stdin)
	if err != nil {
		fatal(err)
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		fatal(err)
	}
	nonce := make([]byte, objectNonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		fatal(err)
	}
	blob := aead.Seal(nonce, nonce, plaintext, objectAAD(id, kind))
	fmt.Println(base64.StdEncoding.EncodeToString(blob))
}

func open(dekHex, id, kind string) {
	dek, err := hex.DecodeString(dekHex)
	if err != nil || len(dek) != 32 {
		fatal(fmt.Errorf("DEK 必须是 32 字节十六进制"))
	}
	encoded, err := io.ReadAll(os.Stdin)
	if err != nil {
		fatal(err)
	}
	blob, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil {
		fatal(err)
	}
	if len(blob) <= objectNonceSize {
		fatal(fmt.Errorf("blob 长度不合法"))
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		fatal(err)
	}
	plaintext, err := aead.Open(nil, blob[:objectNonceSize], blob[objectNonceSize:], objectAAD(id, kind))
	if err != nil {
		fatal(err)
	}
	fmt.Print(string(plaintext))
}

func main() {
	if len(os.Args) < 2 {
		fatal(fmt.Errorf("用法: syncv2-helper <gen-dek|seal|open> ..."))
	}
	switch os.Args[1] {
	case "gen-dek":
		if len(os.Args) != 3 {
			fatal(fmt.Errorf("用法: syncv2-helper gen-dek <password>"))
		}
		genDEK(os.Args[2])
	case "seal":
		if len(os.Args) != 5 {
			fatal(fmt.Errorf("用法: syncv2-helper seal <dek-hex> <id> <kind>"))
		}
		seal(os.Args[2], os.Args[3], os.Args[4])
	case "open":
		if len(os.Args) != 5 {
			fatal(fmt.Errorf("用法: syncv2-helper open <dek-hex> <id> <kind>"))
		}
		open(os.Args[2], os.Args[3], os.Args[4])
	default:
		fatal(fmt.Errorf("未知子命令 %s", os.Args[1]))
	}
}
