//go:build darwin

package termiusdb

import (
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
)

// PlatformKeySource retrieves the Termius local encryption key from the
// macOS Keychain, matching the eTerm reference behavior.
func PlatformKeySource() (KeySource, error) {
	return func() ([]byte, error) {
		out, err := exec.Command("security", "find-generic-password", "-s", "Termius", "-a", "localKey", "-w").CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("termiusdb: macOS keychain access denied or key not found: %s", strings.TrimSpace(string(out)))
		}
		keyB64 := strings.TrimSpace(string(out))
		if keyB64 == "" {
			return nil, fmt.Errorf("termiusdb: empty key retrieved from keychain")
		}
		key, err := base64.StdEncoding.DecodeString(keyB64)
		if err != nil {
			return nil, fmt.Errorf("termiusdb: decode key from base64: %w", err)
		}
		return key, nil
	}, nil
}
