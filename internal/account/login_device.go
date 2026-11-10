package account

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

const (
	loginDeviceKeyDerivation = "nexterm/login-device-key/v1"
	loginDevicePurpose       = "nexterm-login-device-v1"
	loginDeviceIDBytes       = 16
)

type loginDeviceClaims struct {
	UserID   string `json:"u"`
	Username string `json:"n"`
	DeviceID string `json:"d"`
	Expires  int64  `json:"e"`
}

// LoginDeviceToken issues a signed token for a browser or client that
// completed a full login to the account. The token only marks the client as
// known to the login throttle; it authenticates nothing. It binds the user ID,
// the lowercase username, a random device ID and an expiry under a server key
// separate from the CSRF key.
func (a *Accounts) LoginDeviceToken(user *User, ttl time.Duration) (string, error) {
	key, err := a.serverKey(loginDeviceKeyDerivation)
	if err != nil {
		return "", err
	}
	deviceID := make([]byte, loginDeviceIDBytes)
	if _, err := rand.Read(deviceID); err != nil {
		return "", err
	}
	payload, err := json.Marshal(loginDeviceClaims{
		UserID: user.ID, Username: strings.ToLower(user.Username),
		DeviceID: base64.RawURLEncoding.EncodeToString(deviceID),
		Expires:  time.UnixMilli(a.now()).Add(ttl).Unix(),
	})
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(loginDeviceMAC(key, encoded)), nil
}

// LoginDeviceKnown reports whether token is an unexpired login device token
// for username.
func (a *Accounts) LoginDeviceKnown(token, username string) bool {
	encoded, signature, ok := strings.Cut(token, ".")
	if !ok || encoded == "" {
		return false
	}
	key, err := a.serverKey(loginDeviceKeyDerivation)
	if err != nil {
		return false
	}
	provided, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(loginDeviceMAC(key, encoded), provided) {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	var claims loginDeviceClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return false
	}
	return claims.UserID != "" && claims.Username == strings.ToLower(username) &&
		time.UnixMilli(a.now()).Unix() < claims.Expires
}

func loginDeviceMAC(key []byte, encoded string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(loginDevicePurpose))
	mac.Write([]byte{0})
	mac.Write([]byte(encoded))
	return mac.Sum(nil)
}
