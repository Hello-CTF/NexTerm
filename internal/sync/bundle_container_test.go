package sync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestBundleContainerRejectsInvalidHeader(t *testing.T) {
	encoded, err := encodeBundleContainer([]byte(`{"protocol":1,"groups":[],"assets":[],"creds":[]}`), "correct horse")
	if err != nil {
		t.Fatal(err)
	}

	badMagic := append([]byte(nil), encoded...)
	badMagic[0] = 'X'
	if _, err := decodeBundleContainer(badMagic, "correct horse"); err == nil {
		t.Fatal("bad magic accepted")
	}
	badVersion := append([]byte(nil), encoded...)
	badVersion[4] = 99
	if _, err := decodeBundleContainer(badVersion, "correct horse"); err == nil {
		t.Fatal("unknown version accepted")
	}
	if _, err := decodeBundleContainer([]byte("NXBM"), "correct horse"); err == nil {
		t.Fatal("truncated header accepted")
	}
	if _, err := decodeBundleContainer([]byte(`{"protocol":1,"groups":[],"assets":[],"creds":[]}`), ""); err == nil {
		t.Fatal("legacy plaintext JSON accepted as container")
	}
}

func TestBundleContainerEncryptedRoundTrip(t *testing.T) {
	plaintext := []byte(`{"protocol":1,"groups":[{"id":"g1"}],"assets":[],"creds":[]}`)
	encoded, err := encodeBundleContainer(plaintext, "s3cret-口令")
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) < bundleHeaderSize || encoded[5]&bundleFlagEncrypted == 0 {
		t.Fatal("container is not marked encrypted")
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

func TestBundleContainerEmptyPasswordStaysPlaintext(t *testing.T) {
	plaintext := []byte(`{"protocol":1,"groups":[],"assets":[],"creds":[]}`)
	encoded, err := encodeBundleContainer(plaintext, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != bundleHeaderSize+len(plaintext) || encoded[5]&bundleFlagEncrypted != 0 {
		t.Fatal("empty password must produce a plaintext container")
	}
	if string(encoded[bundleHeaderSize:]) != string(plaintext) {
		t.Fatal("plaintext container body mismatch")
	}
	decoded, err := decodeBundleContainer(encoded, "")
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

func TestBundleFileEncryptedWriteReadRoundTrip(t *testing.T) {
	desktop := newTestInstance(t, true)
	path := filepath.Join(t.TempDir(), "nexterm-assets.nxbm")

	content := `{"protocol":1,"groups":[],"assets":[],"creds":[]}`
	result, err := desktop.service.WriteBundleFileWithOptions(context.Background(), path, content, BundleWriteOptions{Password: "导出口令"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Encrypted {
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

func TestBundleFileEmptyPasswordWriteReadsWithoutPrompt(t *testing.T) {
	desktop := newTestInstance(t, true)
	path := filepath.Join(t.TempDir(), "nexterm-assets.nxbm")

	result, err := desktop.service.WriteBundleFileWithOptions(context.Background(), path, `{"protocol":1}`, BundleWriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Encrypted {
		t.Fatalf("empty-password write must stay plaintext, got %+v", result)
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

func TestBundleFileOverwriteTightensPermissions(t *testing.T) {
	desktop := newTestInstance(t, true)
	path := filepath.Join(t.TempDir(), "nexterm-assets.nxbm")

	if err := os.WriteFile(path, []byte(`{"protocol":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := desktop.service.WriteBundleFileWithOptions(context.Background(), path, `{"protocol":1}`, BundleWriteOptions{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("plaintext overwrite mode=%v, want 0600", info.Mode().Perm())
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := desktop.service.WriteBundleFileWithOptions(context.Background(), path, `{"protocol":1}`, BundleWriteOptions{Password: "导出口令"}); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("encrypted overwrite mode=%v, want 0600", info.Mode().Perm())
	}
}

func TestBundleFileRejectsLegacyJSON(t *testing.T) {
	desktop := newTestInstance(t, true)
	path := filepath.Join(t.TempDir(), "nexterm-assets.json")
	legacy := `{"protocol":1,"groups":[],"assets":[],"creds":[]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := desktop.service.ReadBundleFileWithOptions(context.Background(), path, BundleReadOptions{Password: "ignored"}); err == nil {
		t.Fatal("legacy JSON accepted")
	} else {
		requireCode(t, err, ipc.CodeBadParam)
	}

	dispatcher := ipc.NewDispatcher()
	if err := desktop.service.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	response := dispatchJSON(t, dispatcher, CommandBundleRead, `{"args":{"path":`+jsonString(path)+`}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("legacy JSON read through IPC response=%+v", response)
	}
}
