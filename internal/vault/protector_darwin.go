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

// keychainAccount is the account prefix; every protected DEK lives in "<prefix>/<vault id>"
// (see keychain_envelope.go). Tests replace it to stay clear of real entries. The bare
// prefix is the v1 account, which is read for old envelopes but never written or deleted.
var keychainAccount = keychainLegacyAccount

func systemProtect(data []byte) ([]byte, error) {
	envelope, account, err := newKeychainEnvelope(keychainAccount)
	if err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	command := fmt.Sprintf("add-generic-password -U -a %s -s %s -w %s\n", account, keychainService, encoded)
	cmd := exec.Command("/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader(command)
	if err := cmd.Run(); err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: 钥匙串写入失败: "+err.Error(), err)
	}
	return envelope, nil
}

func systemEnvelopeIsLegacy(envelope []byte) bool {
	_, legacy, err := parseKeychainEnvelope(envelope, keychainAccount)
	return err == nil && legacy
}

func systemUnprotect(envelope []byte) ([]byte, error) {
	account, _, err := parseKeychainEnvelope(envelope, keychainAccount)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("/usr/bin/security", "find-generic-password", "-a", account, "-s", keychainService, "-w")
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

// systemDiscard deletes the entry behind a v2 envelope that was never persisted.
func systemDiscard(envelope []byte) error {
	account, err := keychainDiscardAccount(envelope, keychainAccount)
	if err != nil {
		return err
	}
	cmd := exec.Command("/usr/bin/security", "delete-generic-password", "-a", account, "-s", keychainService)
	if output, err := cmd.CombinedOutput(); err != nil && !bytes.Contains(output, []byte("could not be found")) {
		return fmt.Errorf("keychain discard: %w: %s", err, output)
	}
	return nil
}

// cleanupKeychainForTests deletes the (test-replaced) prefix account and the account of each
// given envelope. Real entries are only reachable when a test leaves keychainAccount alone.
func cleanupKeychainForTests(envelopes ...[]byte) error {
	accounts := []string{keychainAccount}
	for _, envelope := range envelopes {
		if account, _, err := parseKeychainEnvelope(envelope, keychainAccount); err == nil {
			accounts = append(accounts, account)
		}
	}
	for _, account := range accounts {
		cmd := exec.Command("/usr/bin/security", "delete-generic-password", "-a", account, "-s", keychainService)
		if output, err := cmd.CombinedOutput(); err != nil && !bytes.Contains(output, []byte("could not be found")) {
			return fmt.Errorf("keychain cleanup: %w: %s", err, output)
		}
	}
	return nil
}
