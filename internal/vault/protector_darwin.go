//go:build darwin

package vault

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const (
	keychainService           = "com.nexterm.desktop"
	systemProtectionAvailable = true
)

var (
	keychainAccount = "vault-dek-go"
	keychainMarker  = []byte("nexterm-go:keychain:v1")
)

func systemProtect(data []byte) ([]byte, error) {
	encoded := base64.StdEncoding.EncodeToString(data)
	command := fmt.Sprintf("add-generic-password -U -a %s -s %s -w %s\n", keychainAccount, keychainService, encoded)
	cmd := exec.Command("/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader(command)
	if err := cmd.Run(); err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: 钥匙串写入失败: "+err.Error(), err)
	}
	return append([]byte(nil), keychainMarker...), nil
}

func systemUnprotect(envelope []byte) ([]byte, error) {
	if !bytes.Equal(envelope, keychainMarker) {
		return nil, ipc.NewError(ipc.CodeCrypto, "加密错误: 信封不是 Go 钥匙串托管标记")
	}
	cmd := exec.Command("/usr/bin/security", "find-generic-password", "-a", keychainAccount, "-s", keychainService, "-w")
	output, err := cmd.Output()
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: 钥匙串读取失败", err)
	}
	plaintext, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(output)))
	for i := range output {
		output[i] = 0
	}
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: 钥匙串数据损坏", err)
	}
	return plaintext, nil
}

func cleanupKeychainForTests() error {
	cmd := exec.Command("/usr/bin/security", "delete-generic-password", "-a", keychainAccount, "-s", keychainService)
	if output, err := cmd.CombinedOutput(); err != nil && !bytes.Contains(output, []byte("could not be found")) {
		return fmt.Errorf("keychain cleanup: %w: %s", err, output)
	}
	return nil
}
