package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

func TestPrivateKeyContentFileAndPassphrase(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	plainBlock, err := gossh.MarshalPrivateKey(private, "test")
	if err != nil {
		t.Fatal(err)
	}
	plain := pem.EncodeToMemory(plainBlock)
	assertSigner := func(cfg AuthConfig) {
		t.Helper()
		signer, err := privateKey(cfg)
		if err != nil {
			t.Fatal(err)
		}
		expected, _ := gossh.NewSignerFromKey(private)
		if string(signer.PublicKey().Marshal()) != string(expected.PublicKey().Marshal()) {
			t.Fatal("loaded key does not match generated key")
		}
	}
	assertSigner(AuthConfig{Method: AuthKey, KeyPEM: plain})
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	assertSigner(AuthConfig{Method: AuthKey, KeyPath: keyPath})

	encryptedBlock, err := gossh.MarshalPrivateKeyWithPassphrase(private, "test", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	encrypted := pem.EncodeToMemory(encryptedBlock)
	assertSigner(AuthConfig{Method: AuthKey, KeyPEM: encrypted, Passphrase: "secret"})
	if _, err := privateKey(AuthConfig{Method: AuthKey, KeyPEM: encrypted, Passphrase: "wrong"}); err == nil {
		t.Fatal("wrong passphrase unexpectedly succeeded")
	}
	if _, err := privateKey(AuthConfig{Method: AuthKey, KeyPEM: encrypted}); err == nil {
		t.Fatal("missing passphrase unexpectedly succeeded")
	}
}

func TestAuthenticationEmptyPasswordKeepsPasswordMethod(t *testing.T) {
	for _, method := range []AuthMethod{AuthPassword, AuthKeyboardInteractive} {
		auth, closers, err := authentication(context.Background(), AuthConfig{Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if auth == nil {
			t.Fatalf("%s with an empty password dropped the auth method, want it kept for PermitEmptyPasswords servers", method)
		}
		if len(closers) != 0 {
			t.Fatalf("%s with an empty password returned %d closers", method, len(closers))
		}
	}
}

func TestConnectEmptyPasswordSmoke(t *testing.T) {
	server := newTestSSHServerConfig(t, nil, func(config *gossh.ServerConfig) {
		config.NoClientAuth = true
	})
	connectTestClient(t, server, AuthConfig{Method: AuthPassword})
	connectTestClient(t, server, AuthConfig{Method: AuthKeyboardInteractive})
}
