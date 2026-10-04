package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
)

func stateDigestFor(stateDir string) (string, error) {
	if stateDir == "" {
		return "", fmt.Errorf("%w: state directory is required", ErrInvalidInput)
	}
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return "", fmt.Errorf("%w: state directory: %v", ErrInvalidInput, err)
	}
	identity, err := currentUserIdentity()
	if err != nil {
		return "", fmt.Errorf("current user identity: %w", err)
	}
	sum := sha256.Sum256([]byte(identity + "\x00" + absolute))
	return hex.EncodeToString(sum[:]), nil
}
