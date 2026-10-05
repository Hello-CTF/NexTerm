package sync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestDetectBundleFormatReadsHeaderOnly(t *testing.T) {
	legacy := []byte(`{"protocol":1,"groups":[],"assets":[],"creds":[]}`)
	if got := DetectBundleFormat(legacy); got != BundleFormatLegacyJSON {
		t.Fatalf("legacy json detected as %v", got)
	}

	plaintext, err := encodeBundleContainer(legacy, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := DetectBundleFormat(plaintext); got != BundleFormatPlaintext {
		t.Fatalf("plaintext container detected as %v", got)
	}

	encrypted, err := encodeBundleContainer(legacy, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if got := DetectBundleFormat(encrypted); got != BundleFormatEncrypted {
		t.Fatalf("encrypted container detected as %v", got)
	}

	headerOnly := encrypted[:bundleHeaderSize]
	if got := DetectBundleFormat(headerOnly); got != BundleFormatEncrypted {
		t.Fatalf("header-only detection failed: %v", got)
	}
	if got := DetectBundleFormat(plaintext[:bundleHeaderSize]); got != BundleFormatPlaintext {
		t.Fatalf("header-only detection failed: %v", got)
	}

	badMagic := append([]byte(nil), encrypted...)
	badMagic[0] = 'X'
	if got := DetectBundleFormat(badMagic); got != BundleFormatLegacyJSON {
		t.Fatalf("bad magic must fall back to legacy, got %v", got)
	}
	badVersion := append([]byte(nil), encrypted...)
	badVersion[4] = 99
	if got := DetectBundleFormat(badVersion); got != BundleFormatLegacyJSON {
		t.Fatalf("unknown version must fall back to legacy, got %v", got)
	}
	short := []byte("NXBM")
	if got := DetectBundleFormat(short); got != BundleFormatLegacyJSON {
		t.Fatalf("truncated header must fall back to legacy, got %v", got)
	}
	if got := DetectBundleFormat(nil); got != BundleFormatLegacyJSON {
		t.Fatalf("empty data must fall back to legacy, got %v", got)
	}
}

func TestBundleContainerEncryptedRoundTrip(t *testing.T) {
	plaintext := []byte(`{"protocol":1,"groups":[{"id":"g1"}],"assets":[],"creds":[]}`)
	encoded, err := encodeBundleContainer(plaintext, "s3cret-口令")
	if err != nil {
		t.Fatal(err)
	}
	if DetectBundleFormat(encoded) != BundleFormatEncrypted {
		t.Fatal("encrypted container not detected")
	}
	if strings.Contains(string(encoded), "g1") {
		t.Fatal("ciphertext leaks plaintext")
	}
	decoded, err := decodeBundleContainer(encoded, "s3cret-口令")
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(plaintext) {
		t.Fatalf("round trip mismatch: %q", decoded)
	}
}

func TestBundleContainerRejectsWrongPasswordAndTampering(t *testing.T) {
	encoded, err := encodeBundleContainer([]byte(`{"protocol":1}`), "right-password")
	if err != nil {
		t.Fatal(err)
	}

	_, err = decodeBundleContainer(encoded, "wrong-password")
	if err == nil {
		t.Fatal("wrong password accepted")
	}
	ipcErr, ok := err.(*ipc.Error)
	if !ok || ipcErr.Code != ipc.CodeDecrypt {
		t.Fatalf("wrong password error=%+v", err)
	}
	if strings.Contains(err.Error(), "right-password") {
		t.Fatal("error leaks password material")
	}

	_, err = decodeBundleContainer(encoded, "")
	if err == nil {
		t.Fatal("missing password accepted")
	}
	ipcErr, ok = err.(*ipc.Error)
	if !ok || ipcErr.Code != ipc.CodeBadParam {
		t.Fatalf("missing password error=%+v", err)
	}

	tamperedCiphertext := append([]byte(nil), encoded...)
	tamperedCiphertext[bundleHeaderSize+2] ^= 0x40
	if _, err = decodeBundleContainer(tamperedCiphertext, "right-password"); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}

	tamperedHeader := append([]byte(nil), encoded...)
	tamperedHeader[36] ^= 0x01
	if _, err = decodeBundleContainer(tamperedHeader, "right-password"); err == nil {
		t.Fatal("tampered header accepted")
	}

	oversizedKDF := append([]byte(nil), encoded...)
	oversizedKDF[10] = 0xFF
	if _, err = decodeBundleContainer(oversizedKDF, "right-password"); err == nil {
		t.Fatal("out-of-range KDF params accepted")
	}

	truncated := encoded[:len(encoded)-1]
	if _, err = decodeBundleContainer(truncated, "right-password"); err == nil {
		t.Fatal("truncated ciphertext accepted")
	}
}

func TestBundleContainerPlaintextRoundTrip(t *testing.T) {
	plaintext := []byte(`{"protocol":1}`)
	encoded, err := encodeBundleContainer(plaintext, "")
	if err != nil {
		t.Fatal(err)
	}
	if DetectBundleFormat(encoded) != BundleFormatPlaintext {
		t.Fatal("plaintext container not detected")
	}
	decoded, err := decodeBundleContainer(encoded, "")
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(plaintext) {
		t.Fatalf("round trip mismatch: %q", decoded)
	}
}

func TestBundleFileEncryptedWriteReadRoundTrip(t *testing.T) {
	desktop := newTestInstance(t, true)
	path := filepath.Join(t.TempDir(), "nexterm-assets.nxbm")

	content := `{"protocol":1,"groups":[],"assets":[],"creds":[]}`
	result, err := desktop.service.WriteBundleFileWithOptions(context.Background(), path, content, BundleWriteOptions{Password: "导出口令"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Encrypted || result.Warning != "" {
		t.Fatalf("write result=%+v", result)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("bundle file mode=%v, want 0600", info.Mode().Perm())
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "protocol") {
		t.Fatal("encrypted bundle file leaks plaintext")
	}

	if _, err := desktop.service.ReadBundleFileWithOptions(context.Background(), path, BundleReadOptions{}); err == nil {
		t.Fatal("read without password succeeded")
	}
	read, err := desktop.service.ReadBundleFileWithOptions(context.Background(), path, BundleReadOptions{Password: "导出口令"})
	if err != nil {
		t.Fatal(err)
	}
	if read != content {
		t.Fatalf("read mismatch: %q", read)
	}
	if _, err := desktop.service.ReadBundleFileWithOptions(context.Background(), path, BundleReadOptions{Password: "错误口令"}); err == nil {
		t.Fatal("read with wrong password succeeded")
	}
}

func TestBundleFilePlaintextWriteReturnsRiskWarning(t *testing.T) {
	desktop := newTestInstance(t, true)
	path := filepath.Join(t.TempDir(), "nexterm-assets.nxbm")

	result, err := desktop.service.WriteBundleFileWithOptions(context.Background(), path, `{"protocol":1}`, BundleWriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Encrypted || result.Warning == "" {
		t.Fatalf("plaintext write must carry a risk warning, got %+v", result)
	}
	read, err := desktop.service.ReadBundleFileWithOptions(context.Background(), path, BundleReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if read != `{"protocol":1}` {
		t.Fatalf("read mismatch: %q", read)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("bundle file mode=%v, want 0600", info.Mode().Perm())
	}
}

func TestBundleFileLegacyJSONStillReadable(t *testing.T) {
	desktop := newTestInstance(t, true)
	path := filepath.Join(t.TempDir(), "nexterm-assets.json")
	legacy := `{"protocol":1,"groups":[],"assets":[],"creds":[]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	read, err := desktop.service.ReadBundleFileWithOptions(context.Background(), path, BundleReadOptions{Password: "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if read != legacy {
		t.Fatalf("legacy read mismatch: %q", read)
	}
	read, err = desktop.service.ReadBundleFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if read != legacy {
		t.Fatalf("legacy wrapper read mismatch: %q", read)
	}
}
