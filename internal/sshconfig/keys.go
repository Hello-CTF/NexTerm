package sshconfig

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

const maxKeyFileBytes = 1 << 20

type KeyInfo struct {
	Path        string
	Name        string
	Fingerprint string
	KeyType     string
	Encrypted   bool
}

// errNoPublicKey marks encrypted key material whose format carries no
// unencrypted public key (legacy PEM encryption); callers can fall back to a
// .pub sibling.
var errNoPublicKey = errors.New("sshconfig: encrypted private key carries no public key")

// fingerprintKeyMaterial extracts the public fingerprint from private key
// material. Encrypted keys are fingerprinted via their embedded public key
// when the format carries one (OpenSSH); legacy PEM encryption reports
// errNoPublicKey with encrypted=true so callers can fall back to a .pub
// sibling.
func fingerprintKeyMaterial(data []byte) (fingerprint, keyType string, encrypted bool, err error) {
	signer, parseErr := ssh.ParsePrivateKey(data)
	if parseErr == nil {
		pub := signer.PublicKey()
		return ssh.FingerprintSHA256(pub), pub.Type(), false, nil
	}
	var missing *ssh.PassphraseMissingError
	if errors.As(parseErr, &missing) {
		if missing.PublicKey != nil {
			pub := missing.PublicKey
			return ssh.FingerprintSHA256(pub), pub.Type(), true, nil
		}
		return "", "", true, errNoPublicKey
	}
	return "", "", false, parseErr
}

func InspectKeyFile(path string) (KeyInfo, error) {
	data, err := readKeyFileBounded(path)
	if err != nil {
		return KeyInfo{}, err
	}
	info := KeyInfo{Path: path, Name: filepath.Base(path)}
	fingerprint, keyType, encrypted, ferr := fingerprintKeyMaterial(data)
	if ferr == nil {
		info.Fingerprint, info.KeyType, info.Encrypted = fingerprint, keyType, encrypted
		return info, nil
	}
	if encrypted {
		info.Encrypted = true
	}
	pubData, err := readKeyFileBounded(path + ".pub")
	if err != nil {
		return KeyInfo{}, fmt.Errorf("sshconfig: parse private key %s: %w", path, ferr)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(pubData)
	if err != nil {
		return KeyInfo{}, fmt.Errorf("sshconfig: parse private key %s: %w", path, ferr)
	}
	info.Fingerprint = ssh.FingerprintSHA256(pub)
	info.KeyType = pub.Type()
	return info, nil
}

func FingerprintPrivateKey(pemData []byte) (fingerprint, keyType string, err error) {
	signer, err := ssh.ParsePrivateKey(pemData)
	if err != nil {
		return "", "", err
	}
	pub := signer.PublicKey()
	return ssh.FingerprintSHA256(pub), pub.Type(), nil
}

func DedupeIdentityFiles(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		cleaned := filepath.Clean(path)
		if seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		out = append(out, cleaned)
	}
	return out
}

func readKeyFileBounded(path string) ([]byte, error) {
	if err := rejectNonRegular(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("sshconfig: key file %s is not a regular file", path)
	}
	if info.Size() > maxKeyFileBytes {
		return nil, fmt.Errorf("sshconfig: key file %s exceeds size limit %d", path, maxKeyFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxKeyFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxKeyFileBytes {
		return nil, fmt.Errorf("sshconfig: key file %s exceeds size limit %d", path, maxKeyFileBytes)
	}
	return data, nil
}

func rejectNonRegular(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	mode := info.Mode()
	if mode&os.ModeSymlink != 0 {
		target, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !target.Mode().IsRegular() {
			return fmt.Errorf("sshconfig: key file %s is not a regular file", path)
		}
		return nil
	}
	if !mode.IsRegular() {
		return fmt.Errorf("sshconfig: key file %s is not a regular file", path)
	}
	return nil
}
