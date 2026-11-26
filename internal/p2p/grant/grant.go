// Package grant issues and verifies the short-lived, server-signed grants that authorize a direct (P2P) session
// between one client process and one device agent. It depends only on the standard library so the agent and the
// mobile builds can link it without pulling in persistence code.
//
// A grant is "v1.<payload>.<signature>": payload is the base64url (no padding) JSON encoding of Claims and the
// signature is Ed25519 over a domain-separated copy of the exact payload bytes, so verification never depends on
// re-encoding JSON.
package grant

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

const (
	// Alg is the only signature algorithm grants use.
	Alg = "ed25519"
	// Version is the claims and wire format version.
	Version = 1
	// DefaultTTL is how long an issued grant stays valid.
	DefaultTTL = 5 * time.Minute
	// MaxTTL bounds the lifetime a verifier accepts, whatever the signer wrote.
	MaxTTL = 10 * time.Minute
	// ClockSkew is the tolerance for an issued_at slightly ahead of the verifier's clock.
	ClockSkew = 30 * time.Second

	tokenPrefix   = "v1."
	signingDomain = "nexterm-p2p-grant-v1\x00"
	nonceBytes    = 16
	maxTokenBytes = 4096
	maxScopes     = 16
	maxIDLength   = 191
)

var (
	ErrMalformed  = errors.New("grant: malformed")
	ErrUnknownKey = errors.New("grant: unknown signing key")
	ErrSignature  = errors.New("grant: bad signature")
	ErrExpired    = errors.New("grant: expired")
	ErrNotYet     = errors.New("grant: issued in the future")
	ErrDevice     = errors.New("grant: issued for another device")
	ErrClientKey  = errors.New("grant: client key mismatch")
	ErrAgentKey   = errors.New("grant: agent key mismatch")
	ErrScope      = errors.New("grant: purpose out of scope")
)

var encoding = base64.RawURLEncoding

// Claims are the signed fields of a grant. Keys are raw 32-byte Ed25519 public keys, encoded as base64url in JSON.
// IssuedAt and ExpiresAt are Unix milliseconds.
type Claims struct {
	Version   int      `json:"v"`
	KID       string   `json:"kid"`
	GrantID   string   `json:"grant_id"`
	DeviceID  string   `json:"device_id"`
	OwnerID   string   `json:"owner_id"`
	ClientPub Key      `json:"client_pub"`
	AgentPub  Key      `json:"agent_pub"`
	Scope     []string `json:"scope"`
	Nonce     string   `json:"nonce"`
	IssuedAt  int64    `json:"issued_at"`
	ExpiresAt int64    `json:"exp"`
}

// Key is a raw Ed25519 public key that encodes as unpadded base64url.
type Key []byte

func (k Key) MarshalJSON() ([]byte, error) {
	return json.Marshal(encoding.EncodeToString(k))
}

func (k *Key) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	decoded, err := ParseKey(text)
	if err != nil {
		return err
	}
	*k = decoded
	return nil
}

// String returns the base64url form of the key.
func (k Key) String() string { return encoding.EncodeToString(k) }

// ParseKey decodes a base64url (unpadded) Ed25519 public key and rejects any other length.
func ParseKey(text string) (Key, error) {
	decoded, err := encoding.Strict().DecodeString(text)
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: public key must be %d bytes of unpadded base64url", ErrMalformed, ed25519.PublicKeySize)
	}
	return Key(decoded), nil
}

// SigningKey is a server signing key identified by KID.
type SigningKey struct {
	KID     string
	Private ed25519.PrivateKey
}

// PublicKey is a published verification key.
type PublicKey struct {
	KID string `json:"kid"`
	Alg string `json:"alg"`
	Pub Key    `json:"pub"`
}

// Public returns the verification half of the signing key.
func (k SigningKey) Public() PublicKey {
	return PublicKey{KID: k.KID, Alg: Alg, Pub: Key(k.Private.Public().(ed25519.PublicKey))}
}

// GenerateSigningKey creates a signing key with a random KID; a nil reader uses crypto/rand.
func GenerateSigningKey(random io.Reader) (SigningKey, error) {
	if random == nil {
		random = rand.Reader
	}
	_, private, err := ed25519.GenerateKey(random)
	if err != nil {
		return SigningKey{}, err
	}
	kid := make([]byte, 9)
	if _, err := io.ReadFull(random, kid); err != nil {
		return SigningKey{}, err
	}
	return SigningKey{KID: "k" + encoding.EncodeToString(kid), Private: private}, nil
}

// Issue signs claims with key. It fills Version, KID and a random Nonce; IssuedAt and ExpiresAt must be set by the
// caller (ExpiresAt-IssuedAt is bounded by MaxTTL).
func Issue(key SigningKey, claims Claims) (string, error) {
	if len(key.Private) != ed25519.PrivateKeySize || key.KID == "" {
		return "", fmt.Errorf("%w: signing key is not usable", ErrMalformed)
	}
	claims.Version = Version
	claims.KID = key.KID
	nonce := make([]byte, nonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	claims.Nonce = encoding.EncodeToString(nonce)
	if err := claims.validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encodedPayload := encoding.EncodeToString(payload)
	signature := ed25519.Sign(key.Private, signedBytes(encodedPayload))
	return tokenPrefix + encodedPayload + "." + encoding.EncodeToString(signature), nil
}

// Verify checks a grant for the agent of deviceID: a trusted KID and valid signature, not expired at now, issued
// for deviceID and clientPub, and purpose inside the scope. An empty purpose means "terminal".
func Verify(token string, trusted []PublicKey, now time.Time, deviceID string, clientPub ed25519.PublicKey, purpose string) (*Claims, error) {
	claims, err := Parse(token, trusted)
	if err != nil {
		return nil, err
	}
	nowMS := now.UnixMilli()
	switch {
	case nowMS >= claims.ExpiresAt:
		return nil, ErrExpired
	case claims.IssuedAt > nowMS+ClockSkew.Milliseconds():
		return nil, ErrNotYet
	case claims.DeviceID != deviceID:
		return nil, ErrDevice
	case !bytes.Equal(claims.ClientPub, clientPub):
		return nil, ErrClientKey
	case !claims.Allows(purpose):
		return nil, ErrScope
	}
	return claims, nil
}

// VerifyAgent is Verify that also requires the grant to name agentPub, the verifying agent's own device key.
func VerifyAgent(token string, trusted []PublicKey, now time.Time, deviceID string, agentPub, clientPub ed25519.PublicKey, purpose string) (*Claims, error) {
	claims, err := Verify(token, trusted, now, deviceID, clientPub, purpose)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(claims.AgentPub, agentPub) {
		return nil, ErrAgentKey
	}
	return claims, nil
}

// Parse checks the encoding, KID and signature of a grant and returns its claims without any time, device, key
// or scope check. Servers use it to recognize their own grants in after-the-fact audit uploads.
func Parse(token string, trusted []PublicKey) (*Claims, error) {
	if len(token) > maxTokenBytes || !strings.HasPrefix(token, tokenPrefix) {
		return nil, ErrMalformed
	}
	encodedPayload, encodedSignature, ok := strings.Cut(strings.TrimPrefix(token, tokenPrefix), ".")
	if !ok || strings.Contains(encodedSignature, ".") {
		return nil, ErrMalformed
	}
	payload, err := encoding.Strict().DecodeString(encodedPayload)
	if err != nil {
		return nil, ErrMalformed
	}
	signature, err := encoding.Strict().DecodeString(encodedSignature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, ErrMalformed
	}
	var header struct {
		KID string `json:"kid"`
	}
	if err := json.Unmarshal(payload, &header); err != nil || header.KID == "" {
		return nil, ErrMalformed
	}
	index := slices.IndexFunc(trusted, func(key PublicKey) bool { return key.KID == header.KID })
	if index < 0 {
		return nil, ErrUnknownKey
	}
	key := trusted[index]
	if key.Alg != Alg || len(key.Pub) != ed25519.PublicKeySize {
		return nil, ErrUnknownKey
	}
	if !ed25519.Verify(ed25519.PublicKey(key.Pub), signedBytes(encodedPayload), signature) {
		return nil, ErrSignature
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var claims Claims
	if err := decoder.Decode(&claims); err != nil {
		return nil, ErrMalformed
	}
	if decoder.More() {
		return nil, ErrMalformed
	}
	if err := claims.validate(); err != nil {
		return nil, err
	}
	return &claims, nil
}

// Allows reports whether purpose is inside the grant scope; an empty purpose means "terminal".
func (c *Claims) Allows(purpose string) bool {
	if purpose == "" {
		purpose = "terminal"
	}
	return slices.Contains(c.Scope, purpose)
}

func (c *Claims) validate() error {
	switch {
	case c.Version != Version:
		return fmt.Errorf("%w: version %d", ErrMalformed, c.Version)
	case c.KID == "" || c.GrantID == "" || c.DeviceID == "" || c.OwnerID == "" || c.Nonce == "":
		return fmt.Errorf("%w: missing identifier", ErrMalformed)
	case len(c.KID) > maxIDLength || len(c.GrantID) > maxIDLength || len(c.DeviceID) > maxIDLength || len(c.OwnerID) > maxIDLength:
		return fmt.Errorf("%w: identifier too long", ErrMalformed)
	case len(c.ClientPub) != ed25519.PublicKeySize || len(c.AgentPub) != ed25519.PublicKeySize:
		return fmt.Errorf("%w: keys must be Ed25519 public keys", ErrMalformed)
	case len(c.Scope) == 0 || len(c.Scope) > maxScopes:
		return fmt.Errorf("%w: scope must name 1-%d purposes", ErrMalformed, maxScopes)
	case c.IssuedAt <= 0 || c.ExpiresAt <= c.IssuedAt || c.ExpiresAt-c.IssuedAt > MaxTTL.Milliseconds():
		return fmt.Errorf("%w: lifetime must be positive and at most %s", ErrMalformed, MaxTTL)
	}
	for _, purpose := range c.Scope {
		if purpose == "" || len(purpose) > 64 {
			return fmt.Errorf("%w: bad scope entry", ErrMalformed)
		}
	}
	return nil
}

func signedBytes(encodedPayload string) []byte {
	return append([]byte(signingDomain), encodedPayload...)
}
