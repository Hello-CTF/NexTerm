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
	return StateDigest(absolute, identity)
}

// StateDigest computes the hello state digest for a state directory owned by
// the given user identity. Remote callers use it with the daemon's identity
// and absolute path as seen on the daemon host.
func StateDigest(stateDir, identity string) (string, error) {
	if stateDir == "" || identity == "" {
		return "", fmt.Errorf("%w: state directory and identity are required", ErrInvalidInput)
	}
	sum := sha256.Sum256([]byte(identity + "\x00" + stateDir))
	return hex.EncodeToString(sum[:]), nil
}
