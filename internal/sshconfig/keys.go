package sshconfig

import (
	"fmt"
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
}

func InspectKeyFile(path string) (KeyInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return KeyInfo{}, err
	}
	if len(data) > maxKeyFileBytes {
		return KeyInfo{}, fmt.Errorf("sshconfig: key file %s exceeds size limit %d", path, maxKeyFileBytes)
	}
	info := KeyInfo{Path: path, Name: filepath.Base(path)}
	signer, parseErr := ssh.ParsePrivateKey(data)
	if parseErr == nil {
		pub := signer.PublicKey()
		info.Fingerprint = ssh.FingerprintSHA256(pub)
		info.KeyType = pub.Type()
		return info, nil
	}
	pubData, err := os.ReadFile(path + ".pub")
	if err != nil {
		return KeyInfo{}, fmt.Errorf("sshconfig: parse private key %s: %w", path, parseErr)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(pubData)
	if err != nil {
		return KeyInfo{}, fmt.Errorf("sshconfig: parse private key %s: %w", path, parseErr)
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
