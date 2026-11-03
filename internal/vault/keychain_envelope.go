package vault

import (
	"bytes"
	"encoding/hex"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// The macOS Keychain envelope names the Keychain account that holds the DEK.
// v1 used one fixed account for every data directory, so a second data directory
// overwrote the first one's DEK; v2 gives every protected DEK its own account.
// This file is platform independent so the parsing can be tested everywhere.
const (
	keychainLegacyAccount  = "vault-dek-go"
	keychainVaultIDBytes   = 16
	keychainEnvelopePrefix = "nexterm-go:keychain:v2:"
)

var keychainLegacyMarker = []byte("nexterm-go:keychain:v1")

var errKeychainEnvelope = errors.New("invalid keychain envelope")

// newKeychainEnvelope returns a fresh v2 envelope and the account it refers to.
func newKeychainEnvelope(accountBase string) (envelope []byte, account string, err error) {
	raw := make([]byte, keychainVaultIDBytes)
	if err := randomBytes(raw); err != nil {
		return nil, "", err
	}
	id := hex.EncodeToString(raw)
	return []byte(keychainEnvelopePrefix + id), accountBase + "/" + id, nil
}

// parseKeychainEnvelope maps an envelope to its Keychain account. The id ends up in
// a `security -i` command line, so anything but the exact v1 marker or a v2 prefix
// followed by 32 lowercase hex characters is rejected.
func parseKeychainEnvelope(envelope []byte, accountBase string) (account string, legacy bool, err error) {
	if bytes.Equal(envelope, keychainLegacyMarker) {
		return accountBase, true, nil
	}
	if !bytes.HasPrefix(envelope, []byte(keychainEnvelopePrefix)) {
		return "", false, ipc.WrapError(ipc.CodeCrypto, "加密错误：钥匙串数据格式无效", errKeychainEnvelope)
	}
	id := envelope[len(keychainEnvelopePrefix):]
	if len(id) != keychainVaultIDBytes*2 {
		return "", false, ipc.WrapError(ipc.CodeCrypto, "加密错误：钥匙串数据格式无效", errKeychainEnvelope)
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", false, ipc.WrapError(ipc.CodeCrypto, "加密错误：钥匙串数据格式无效", errKeychainEnvelope)
		}
	}
	return accountBase + "/" + string(id), false, nil
}

// keychainDiscardAccount returns the account behind an envelope that may be deleted.
// Only v2 envelopes qualify: the v1 account is shared by every data directory.
func keychainDiscardAccount(envelope []byte, accountBase string) (string, error) {
	account, legacy, err := parseKeychainEnvelope(envelope, accountBase)
	if err != nil {
		return "", err
	}
	if legacy {
		return "", ipc.WrapError(ipc.CodeCrypto, "加密错误：旧版钥匙串条目可能被其他数据目录共用，拒绝删除", errKeychainEnvelope)
	}
	return account, nil
}
