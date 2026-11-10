package account

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const (
	csrfPurpose       = "nexterm-csrf-v2"
	csrfKeyDerivation = "nexterm/csrf-key/v1"
	csrfKeyBytes      = 32
)

// CSRFToken derives the stateless CSRF token of a login session. It is an
// HMAC of the session ID under a server secret, so a leaked session ID (shown
// in session lists and audit records) does not yield the token. The token
// expires with the session and needs no storage.
//
// The secret is derived from the out-of-database TOTP storage key file, so it
// survives restarts; without a key file it is random per process and clients
// re-read the token after a restart.
func (a *Accounts) CSRFToken(sessionID string) (string, error) {
	key, err := a.csrfKey()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(csrfMAC(key, sessionID)), nil
}

// CSRFValid reports whether presented is the CSRF token of the session.
func (a *Accounts) CSRFValid(sessionID, presented string) bool {
	key, err := a.csrfKey()
	if err != nil {
		return false
	}
	provided, err := hex.DecodeString(presented)
	if err != nil {
		return false
	}
	return hmac.Equal(csrfMAC(key, sessionID), provided)
}

func csrfMAC(key []byte, sessionID string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(csrfPurpose))
	mac.Write([]byte{0})
	mac.Write([]byte(sessionID))
	return mac.Sum(nil)
}

func (a *Accounts) csrfKey() ([]byte, error) {
	return a.serverKey(csrfKeyDerivation)
}

// serverKey derives a purpose-specific server secret from the out-of-database
// TOTP storage key file, or from a random per-process root without one. Keys
// are cached per label.
func (a *Accounts) serverKey(label string) ([]byte, error) {
	a.csrfMu.Lock()
	defer a.csrfMu.Unlock()
	if key, ok := a.serverKeys[label]; ok {
		return key, nil
	}
	if a.csrfSecret == nil {
		if a.totpKeyPath == "" {
			secret := make([]byte, csrfKeyBytes)
			if _, err := rand.Read(secret); err != nil {
				return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: CSRF 密钥生成失败", err)
			}
			a.csrfSecret = secret
		} else {
			root, err := a.totpKey()
			if err != nil {
				return nil, err
			}
			a.csrfSecret = root
		}
	}
	mac := hmac.New(sha256.New, a.csrfSecret)
	mac.Write([]byte(label))
	key := mac.Sum(nil)
	if a.serverKeys == nil {
		a.serverKeys = make(map[string][]byte)
	}
	a.serverKeys[label] = key
	return key, nil
}
