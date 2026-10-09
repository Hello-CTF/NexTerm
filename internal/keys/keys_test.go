package keys

import (
	"crypto/ed25519"
	"crypto/rsa"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	gossh "golang.org/x/crypto/ssh"
)

func parseAuthorized(t *testing.T, line string) gossh.PublicKey {
	t.Helper()
	pub, _, _, rest, err := gossh.ParseAuthorizedKey([]byte(line + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(string(rest))) != 0 {
		t.Fatal("unexpected trailing data after authorized_keys line")
	}
	return pub
}

func assertPairConsistent(t *testing.T, pair *KeyPair, wantType string) gossh.PublicKey {
	t.Helper()
	signer, err := gossh.ParsePrivateKey(pair.PrivateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pub := parseAuthorized(t, pair.PublicKeyLine)
	if pub.Type() != wantType {
		t.Fatalf("public key type = %q, want %q", pub.Type(), wantType)
	}
	if string(signer.PublicKey().Marshal()) != string(pub.Marshal()) {
		t.Fatal("authorized_keys line does not match private key")
	}
	if pair.Fingerprint != gossh.FingerprintSHA256(pub) {
		t.Fatal("fingerprint does not match public key")
	}
	return pub
}

func TestGenerateAlgorithms(t *testing.T) {
	cases := []struct {
		name     string
		opts     Options
		wantType string
	}{
		{"default ed25519", Options{}, "ssh-ed25519"},
		{"explicit ed25519", Options{Algorithm: AlgorithmEd25519}, "ssh-ed25519"},
		{"default rsa bits", Options{Algorithm: AlgorithmRSA}, "ssh-rsa"},
		{"explicit rsa 4096", Options{Algorithm: AlgorithmRSA, RSABits: DefaultRSABits}, "ssh-rsa"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pair, err := Generate(tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			assertPairConsistent(t, pair, tc.wantType)
			raw, err := gossh.ParseRawPrivateKey(pair.PrivateKeyPEM)
			if err != nil {
				t.Fatal(err)
			}
			switch tc.wantType {
			case "ssh-ed25519":
				if _, ok := raw.(*ed25519.PrivateKey); !ok {
					t.Fatalf("raw key type = %T, want *ed25519.PrivateKey", raw)
				}
			case "ssh-rsa":
				rsaKey, ok := raw.(*rsa.PrivateKey)
				if !ok {
					t.Fatalf("raw key type = %T, want *rsa.PrivateKey", raw)
				}
				if rsaKey.N.BitLen() != DefaultRSABits {
					t.Fatalf("RSA modulus bits = %d, want %d", rsaKey.N.BitLen(), DefaultRSABits)
				}
			}
		})
	}
}

func TestGeneratePassphraseRoundtrip(t *testing.T) {
	const passphrase = "correct horse battery staple"
	pair, err := Generate(Options{Algorithm: AlgorithmEd25519, Passphrase: passphrase})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gossh.ParsePrivateKey(pair.PrivateKeyPEM); err == nil {
		t.Fatal("unencrypted parse of passphrase-protected key unexpectedly succeeded")
	}
	if _, err := gossh.ParsePrivateKeyWithPassphrase(pair.PrivateKeyPEM, []byte("wrong passphrase")); err == nil {
		t.Fatal("wrong passphrase unexpectedly succeeded")
	}
	signer, err := gossh.ParsePrivateKeyWithPassphrase(pair.PrivateKeyPEM, []byte(passphrase))
	if err != nil {
		t.Fatal(err)
	}
	pub := parseAuthorized(t, pair.PublicKeyLine)
	if string(signer.PublicKey().Marshal()) != string(pub.Marshal()) {
		t.Fatal("authorized_keys line does not match decrypted private key")
	}
	if pair.Fingerprint != gossh.FingerprintSHA256(pub) {
		t.Fatal("fingerprint does not match public key")
	}
}

func TestGenerateInvalidOptions(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{"unknown algorithm", Options{Algorithm: "dsa"}},
		{"rsa 2048", Options{Algorithm: AlgorithmRSA, RSABits: 2048}},
		{"rsa 8192", Options{Algorithm: AlgorithmRSA, RSABits: 8192}},
		{"negative rsa bits", Options{Algorithm: AlgorithmRSA, RSABits: -1}},
		{"ed25519 with rsa bits", Options{Algorithm: AlgorithmEd25519, RSABits: DefaultRSABits}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pair, err := Generate(tc.opts)
			if err == nil {
				t.Fatal("invalid options unexpectedly succeeded")
			}
			if pair != nil {
				t.Fatal("key pair returned alongside error")
			}
			var appErr *ipc.Error
			if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam {
				t.Fatalf("error = %v, want ipc bad_param error", err)
			}
		})
	}
}

func TestGenerateNoFileArtifacts(t *testing.T) {
	tmp := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatal(err)
		}
	}()
	variants := []Options{
		{},
		{Algorithm: AlgorithmRSA},
		{Algorithm: AlgorithmEd25519, Passphrase: "synthetic test passphrase"},
	}
	for _, opts := range variants {
		if _, err := Generate(opts); err != nil {
			t.Fatal(err)
		}
	}
	var files []string
	err = filepath.WalkDir(tmp, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("Generate created file-system artifacts: %v", files)
	}
}
