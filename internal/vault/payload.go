package vault

import (
	"encoding/json"
	"strings"
	"unicode"
)

const KindPrivateKey = "private_key"

type PrivateKeyPayload struct {
	Key        *string `json:"key,omitempty"`
	File       *string `json:"ref,omitempty"`
	Passphrase *string `json:"passphrase,omitempty"`
}

func InlinePrivateKey(key string, passphrase *string) PrivateKeyPayload {
	return PrivateKeyPayload{Key: &key, Passphrase: normalizePassphrase(passphrase)}
}

func ReferencedPrivateKey(path string, passphrase *string) PrivateKeyPayload {
	return PrivateKeyPayload{File: &path, Passphrase: normalizePassphrase(passphrase)}
}

func normalizePassphrase(passphrase *string) *string {
	if passphrase == nil {
		return nil
	}
	normalized := strings.TrimSpace(*passphrase)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func (p PrivateKeyPayload) IsRef() bool {
	return p.File != nil
}

func (p PrivateKeyPayload) Encode() string {
	encoded, _ := json.Marshal(p)
	return string(encoded)
}

func ParsePrivateKeyPayload(raw string) PrivateKeyPayload {
	trimmed := strings.TrimLeftFunc(raw, unicode.IsSpace)
	if strings.HasPrefix(trimmed, "{") {
		var payload PrivateKeyPayload
		if json.Unmarshal([]byte(trimmed), &payload) == nil && (payload.Key != nil || payload.File != nil) {
			payload.Passphrase = normalizePassphrase(payload.Passphrase)
			return payload
		}
	}
	return PrivateKeyPayload{}
}
