// Package releasesigtest provides deterministic Minisign test keys for tests of release signature consumers.
package releasesigtest

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/blake2b"
)

// Key is a test-only Minisign key pair derived from a one-byte seed.
type Key struct {
	KeyID         [8]byte
	Private       ed25519.PrivateKey
	EncodedPublic string
}

// NewKey derives a deterministic key; different seeds give different keys.
func NewKey(seed byte) Key {
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	public := private.Public().(ed25519.PublicKey)
	keyID := [8]byte{seed, seed, seed, seed, seed, seed, seed, seed}
	encoded := make([]byte, 0, 42)
	encoded = append(encoded, 'E', 'd')
	encoded = append(encoded, keyID[:]...)
	encoded = append(encoded, public...)
	return Key{KeyID: keyID, Private: private, EncodedPublic: base64.StdEncoding.EncodeToString(encoded)}
}

// PublicKeyFile renders the key as a minisign.pub file.
func (key Key) PublicKeyFile() string {
	return "untrusted comment: test-only key\n" + key.EncodedPublic + "\n"
}

// Sign produces a prehashed Minisign signature file for message.
func (key Key) Sign(message []byte, trustedComment string) string {
	digest := blake2b.Sum512(message)
	signature := ed25519.Sign(key.Private, digest[:])
	encoded := make([]byte, 0, 74)
	encoded = append(encoded, 'E', 'D')
	encoded = append(encoded, key.KeyID[:]...)
	encoded = append(encoded, signature...)
	globalMessage := make([]byte, 0, len(signature)+len(trustedComment))
	globalMessage = append(globalMessage, signature...)
	globalMessage = append(globalMessage, trustedComment...)
	globalSignature := ed25519.Sign(key.Private, globalMessage)
	return fmt.Sprintf("untrusted comment: test-only signature\n%s\ntrusted comment: %s\n%s\n",
		base64.StdEncoding.EncodeToString(encoded), trustedComment, base64.StdEncoding.EncodeToString(globalSignature))
}
