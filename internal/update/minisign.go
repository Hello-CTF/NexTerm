package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

const minisignTrustedCommentPrefix = "trusted comment: "

// defaultMinisignPublicKey is an ldflags -X injection point for release builds; empty preserves the legacy update path.
var defaultMinisignPublicKey string

type minisignPublicKey struct {
	keyID [8]byte
	key   [ed25519.PublicKeySize]byte
}

func (m *Manager) minisignTrust() (minisignPublicKey, bool, error) {
	encoded := m.MinisignPublicKey
	if encoded == "" {
		encoded = defaultMinisignPublicKey
	}
	if encoded == "" {
		if m.RequireSignedChecksums {
			return minisignPublicKey{}, false, fmt.Errorf("已启用发布签名验证，但未配置受信 minisign 公钥")
		}
		return minisignPublicKey{}, false, nil
	}
	key, err := parseMinisignPublicKey(encoded)
	if err != nil {
		return minisignPublicKey{}, false, fmt.Errorf("受信 minisign 公钥无效: %w", err)
	}
	return key, true, nil
}

func parseMinisignPublicKey(encoded string) (minisignPublicKey, error) {
	var publicKey minisignPublicKey
	text := strings.TrimSpace(encoded)
	lines := strings.Split(text, "\n")
	encodedKey := text
	if len(lines) > 1 {
		if !strings.HasPrefix(lines[0], "untrusted comment:") {
			return publicKey, fmt.Errorf("公钥缺少 untrusted comment 行")
		}
		encodedKey = strings.TrimSpace(lines[1])
	}
	decoded, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		return publicKey, fmt.Errorf("公钥 Base64 解码失败: %w", err)
	}
	if len(decoded) != 42 {
		return publicKey, fmt.Errorf("公钥长度为 %d，期望 42", len(decoded))
	}
	if string(decoded[:2]) != "Ed" {
		return publicKey, fmt.Errorf("不支持的公钥算法 %q", decoded[:2])
	}
	copy(publicKey.keyID[:], decoded[2:10])
	copy(publicKey.key[:], decoded[10:42])
	return publicKey, nil
}

func verifyMinisign(message, encodedSignature []byte, publicKey minisignPublicKey, expectedTrustedComment string) error {
	lines := strings.Split(strings.TrimSpace(string(encodedSignature)), "\n")
	if len(lines) != 4 {
		return fmt.Errorf("签名应包含 4 行，实际为 %d", len(lines))
	}
	for index := range lines {
		lines[index] = strings.TrimSuffix(lines[index], "\r")
	}
	if !strings.HasPrefix(lines[0], "untrusted comment:") {
		return fmt.Errorf("签名缺少 untrusted comment 行")
	}
	signature, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil {
		return fmt.Errorf("签名 Base64 解码失败: %w", err)
	}
	if len(signature) != 74 {
		return fmt.Errorf("签名长度为 %d，期望 74", len(signature))
	}
	if string(signature[:2]) != "ED" {
		return fmt.Errorf("仅支持预哈希 minisign 签名")
	}
	if string(signature[2:10]) != string(publicKey.keyID[:]) {
		return fmt.Errorf("签名 key ID 与受信公钥不匹配")
	}
	if !strings.HasPrefix(lines[2], minisignTrustedCommentPrefix) {
		return fmt.Errorf("签名缺少 trusted comment 行")
	}
	globalSignature, err := base64.StdEncoding.DecodeString(lines[3])
	if err != nil {
		return fmt.Errorf("全局签名 Base64 解码失败: %w", err)
	}
	if len(globalSignature) != ed25519.SignatureSize {
		return fmt.Errorf("全局签名长度为 %d，期望 %d", len(globalSignature), ed25519.SignatureSize)
	}

	digest := blake2b.Sum512(message)
	if !ed25519.Verify(publicKey.key[:], digest[:], signature[10:74]) {
		return fmt.Errorf("文件签名无效")
	}
	trustedComment := lines[2][len(minisignTrustedCommentPrefix):]
	globalMessage := make([]byte, 0, ed25519.SignatureSize+len(trustedComment))
	globalMessage = append(globalMessage, signature[10:74]...)
	globalMessage = append(globalMessage, trustedComment...)
	if !ed25519.Verify(publicKey.key[:], globalMessage, globalSignature) {
		return fmt.Errorf("可信注释的全局签名无效")
	}
	if trustedComment != expectedTrustedComment {
		return fmt.Errorf("可信注释 %q 与目标发布 %q 不匹配", trustedComment, expectedTrustedComment)
	}
	return nil
}
