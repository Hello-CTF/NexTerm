// Package releasesig verifies Minisign signatures over release checksum manifests. It is shared by the desktop updater and
// the device agent, so it must depend on the standard library and x/crypto only (the agent is a fully static build).
package releasesig

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

const trustedCommentPrefix = "trusted comment: "

// EmbeddedPublicKey is the only ldflags injection point for the release signing key: release builds of the desktop and the
// agent set it with -ldflags -X github.com/Hello-CTF/NexTerm/internal/releasesig.EmbeddedPublicKey=<key>. Development builds leave it empty.
var EmbeddedPublicKey string

// PublicKey is a parsed Minisign Ed25519 public key.
type PublicKey struct {
	keyID [8]byte
	key   [ed25519.PublicKeySize]byte
}

// KeyID returns the 8-byte Minisign key identifier.
func (k PublicKey) KeyID() [8]byte { return k.keyID }

// ChecksumsTrustedComment is the trusted comment the release workflow signs SHA256SUMS with.
func ChecksumsTrustedComment(version string) string {
	return "NexTerm " + version + " SHA256SUMS"
}

// ParsePublicKey parses a bare base64 key or a full minisign.pub file.
func ParsePublicKey(encoded string) (PublicKey, error) {
	var publicKey PublicKey
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

// Verify checks a prehashed Minisign signature over message and requires its trusted comment to equal expectedTrustedComment.
func Verify(message, encodedSignature []byte, publicKey PublicKey, expectedTrustedComment string) error {
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
	if !strings.HasPrefix(lines[2], trustedCommentPrefix) {
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
	trustedComment := lines[2][len(trustedCommentPrefix):]
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
