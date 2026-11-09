package account

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
)

// The CSRF token is keyed by a server secret: it is not the session-ID-only
// HMAC of earlier versions, differs between servers with different key files,
// and stays stable across restarts that keep the key file.
func TestCSRFTokenKeyedByServerSecret(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "totp.key")
	first := New(nil, WithTOTPKeyFile(keyFile))
	restarted := New(nil, WithTOTPKeyFile(keyFile))
	other := New(nil, WithTOTPKeyFile(filepath.Join(t.TempDir(), "totp.key")))
	const sessionID = "session-1"

	token, err := first.CSRFToken(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	legacy := hmac.New(sha256.New, []byte(sessionID))
	legacy.Write([]byte("nexterm-csrf-v1"))
	if token == hex.EncodeToString(legacy.Sum(nil)) {
		t.Fatal("token is derivable from the session ID alone")
	}
	if again, err := restarted.CSRFToken(sessionID); err != nil || again != token {
		t.Fatalf("token after restart = %q, %v; want %q", again, err, token)
	}
	if foreign, err := other.CSRFToken(sessionID); err != nil || foreign == token {
		t.Fatalf("servers with different keys share a token: %q, %v", foreign, err)
	}
	if !first.CSRFValid(sessionID, token) || !restarted.CSRFValid(sessionID, token) {
		t.Fatal("valid token rejected")
	}
	if first.CSRFValid("session-2", token) || first.CSRFValid(sessionID, "") || first.CSRFValid(sessionID, "zz") || other.CSRFValid(sessionID, token) {
		t.Fatal("token accepted for another session, server or malformed input")
	}

	// Without a key file the secret is per process but still not derivable.
	ephemeral := New(nil)
	ephemeralToken, err := ephemeral.CSRFToken(sessionID)
	if err != nil || ephemeralToken == token || !ephemeral.CSRFValid(sessionID, ephemeralToken) {
		t.Fatalf("ephemeral token = %q, %v", ephemeralToken, err)
	}
}
