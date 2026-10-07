package sshconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// DefaultSSHHome returns the default ~/.ssh directory, or "" when the home
// directory cannot be determined.
func DefaultSSHHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh")
}

// ScannedKey describes a supported private key file discovered in an SSH home
// directory. It never carries key material.
type ScannedKey struct {
	Path        string
	Name        string
	Fingerprint string
	KeyType     string
	Encrypted   bool
}

// homeNonKeyNames are well-known ~/.ssh entries that are never private keys
// and are skipped without a diagnostic.
var homeNonKeyNames = map[string]bool{
	"config":           true,
	"known_hosts":      true,
	"known_hosts.old":  true,
	"ssh_known_hosts":  true,
	"authorized_keys":  true,
	"authorized_keys2": true,
	"environment":      true,
	"rc":               true,
}

// ScanHome scans dir (normally ~/.ssh) for supported private keys. Only
// regular files directly inside dir are considered; symlinks are followed
// only when they resolve back inside dir. Public keys, well-known non-key
// files and anything that does not parse as a supported private key are
// skipped. File contents are read only to compute fingerprints and never
// appear in results or diagnostics.
func ScanHome(dir string, limits Limits) ([]ScannedKey, []Diagnostic, error) {
	limits = withDefaultLimits(limits)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var (
		keys         []ScannedKey
		diagnostics  []Diagnostic
		seenResolved = map[string]bool{}
		inspected    = 0
		truncated    = false
	)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".pub") || homeNonKeyNames[name] {
			continue
		}
		path := filepath.Join(dir, name)
		resolved, ok := resolveHomeEntry(dir, path, &diagnostics)
		if !ok {
			continue
		}
		if seenResolved[resolved] {
			continue
		}
		seenResolved[resolved] = true
		if inspected >= limits.MaxFiles {
			if !truncated {
				truncated = true
				diagnostics = append(diagnostics, Diagnostic{
					Code:    "limit-truncated",
					Source:  dir,
					Message: fmt.Sprintf("directory entry count exceeds %d", limits.MaxFiles),
				})
			}
			break
		}
		inspected++
		if len(keys) >= limits.MaxKeys {
			if !truncated {
				truncated = true
				diagnostics = append(diagnostics, Diagnostic{
					Code:    "limit-truncated",
					Source:  dir,
					Message: fmt.Sprintf("key count exceeds %d", limits.MaxKeys),
				})
			}
			break
		}
		key, code, message := inspectHomeKey(path, name)
		if code != "" {
			diagnostics = append(diagnostics, Diagnostic{Code: code, Source: path, Message: message})
			continue
		}
		keys = append(keys, key)
	}
	return keys, diagnostics, nil
}

// resolveHomeEntry applies the strict path checks for one directory entry and
// reports whether it should be inspected as a key candidate. resolved is the
// real path used for dedupe.
func resolveHomeEntry(dir, path string, diagnostics *[]Diagnostic) (resolved string, ok bool) {
	info, err := os.Lstat(path)
	if err != nil {
		*diagnostics = append(*diagnostics, Diagnostic{Code: "key-unreadable", Source: path, Message: "file cannot be inspected"})
		return "", false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			*diagnostics = append(*diagnostics, Diagnostic{Code: "symlink-unresolved", Source: path, Message: "symlink target cannot be resolved"})
			return "", false
		}
		if !pathWithin(dir, target) {
			*diagnostics = append(*diagnostics, Diagnostic{Code: "symlink-escape", Source: path, Message: "symlink points outside the scanned directory"})
			return "", false
		}
		resolved = target
	} else {
		if !info.Mode().IsRegular() {
			return "", false
		}
		resolved = path
	}
	target, err := os.Stat(resolved)
	if err != nil || !target.Mode().IsRegular() {
		return "", false
	}
	return resolved, true
}

// inspectHomeKey reads one candidate file and classifies it. A non-empty code
// means the file is skipped with that diagnostic.
func inspectHomeKey(path, name string) (ScannedKey, string, string) {
	data, err := readKeyFileBounded(path)
	if err != nil {
		return ScannedKey{}, "key-unreadable", "file cannot be read within the size limit"
	}
	signer, parseErr := ssh.ParsePrivateKey(data)
	if parseErr == nil {
		pub := signer.PublicKey()
		if !supportedHomeKeyType(pub.Type()) {
			return ScannedKey{}, "unsupported-key-type", fmt.Sprintf("key type %q is not supported for import", pub.Type())
		}
		return ScannedKey{Path: path, Name: name, Fingerprint: ssh.FingerprintSHA256(pub), KeyType: pub.Type()}, "", ""
	}
	var missing *ssh.PassphraseMissingError
	if errors.As(parseErr, &missing) {
		pub := missing.PublicKey
		if pub == nil {
			var err error
			pub, err = parsePublicKeySibling(path)
			if err != nil {
				return ScannedKey{}, "encrypted-key-unreadable", "encrypted private key has no readable public part or .pub sibling to fingerprint and is skipped"
			}
		}
		if !supportedHomeKeyType(pub.Type()) {
			return ScannedKey{}, "unsupported-key-type", fmt.Sprintf("key type %q is not supported for import", pub.Type())
		}
		return ScannedKey{Path: path, Name: name, Fingerprint: ssh.FingerprintSHA256(pub), KeyType: pub.Type(), Encrypted: true}, "", ""
	}
	return ScannedKey{}, "unsupported-file", "not a supported private key; skipped"
}

func parsePublicKeySibling(path string) (ssh.PublicKey, error) {
	data, err := readKeyFileBounded(path + ".pub")
	if err != nil {
		return nil, err
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(data)
	return pub, err
}

func supportedHomeKeyType(keyType string) bool {
	switch keyType {
	case "ssh-rsa", "ssh-ed25519", "sk-ssh-ed25519@openssh.com":
		return true
	}
	return strings.HasPrefix(keyType, "ecdsa-sha2-") || strings.HasPrefix(keyType, "sk-ecdsa-sha2-")
}

func pathWithin(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// PreviewHome scans an SSH home directory (normally ~/.ssh) for private keys
// and, when a config file is present, maps Host/IdentityFile entries onto the
// scanned keys so hosts and keys share the same conflict and duplicate rules
// as the other import sources. Private key material is never retained.
func PreviewHome(dir string, existing []ExistingAsset, existingKeys []ExistingKey, limits Limits) (*ImportPreview, error) {
	limits = withDefaultLimits(limits)
	preview := &ImportPreview{Source: "ssh-home"}
	scanned, diagnostics, err := ScanHome(dir, limits)
	if err != nil {
		if os.IsNotExist(err) {
			preview.Diagnostics = append(preview.Diagnostics, Diagnostic{Code: "home-missing", Source: dir, Message: "SSH directory does not exist"})
			return preview, nil
		}
		return nil, fmt.Errorf("sshconfig: scan %s: %w", dir, err)
	}
	preview.Diagnostics = append(preview.Diagnostics, diagnostics...)

	var hosts []Host
	configPath := filepath.Join(dir, "config")
	if _, err := os.Stat(configPath); err == nil {
		result, err := Parse(configPath, limits)
		if err != nil {
			preview.Diagnostics = append(preview.Diagnostics, Diagnostic{Code: "config-unparsed", Source: configPath, Message: "SSH config could not be parsed; its hosts are not imported"})
		} else {
			hosts = result.Hosts
			preview.Diagnostics = append(preview.Diagnostics, result.Diagnostics...)
		}
	}

	existingByName, existingByEndpoint := indexExistingAssets(existing)
	batchNames := map[string]endpoint{}
	batchEndpoints := map[endpoint]string{}
	hostPreviews := make([]HostPreview, 0, len(hosts))
	for _, host := range hosts {
		item := HostPreview{
			Alias:         host.Alias,
			Hostname:      host.Hostname,
			Port:          host.Port,
			Username:      host.Username,
			IdentityFiles: DedupeIdentityFiles(host.IdentityFiles),
			ProxyJump:     host.ProxyJump,
			Source:        host.Source,
			Action:        PlanAdd,
		}
		if len(item.IdentityFiles) > 0 {
			item.AuthMethod = "key"
		}
		planHostAction(&item, existingByName, existingByEndpoint, batchNames, batchEndpoints)
		ep := endpointOf(item.Hostname, item.Port, item.Username)
		if _, ok := batchNames[strings.ToLower(item.Alias)]; !ok {
			batchNames[strings.ToLower(item.Alias)] = ep
		}
		if _, ok := batchEndpoints[ep]; !ok {
			batchEndpoints[ep] = item.Alias
		}
		hostPreviews = append(hostPreviews, item)
	}
	validateProxyJumps(hostPreviews, existing)
	preview.Hosts = hostPreviews

	existingKeyByName, existingFPNames := indexExistingKeys(existingKeys)
	seenFingerprints := map[string]bool{}
	batchKeyNames := map[string]bool{}
	keyPreviews := make([]KeyPreview, 0, len(scanned))
	for _, key := range scanned {
		item := KeyPreview{
			Aliases:     []string{key.Name},
			Fingerprint: key.Fingerprint,
			KeyType:     key.KeyType,
			Path:        key.Path,
			Source:      "ssh-home",
			Action:      PlanAdd,
		}
		if key.Encrypted {
			item.Warnings = append(item.Warnings, "private key is passphrase-protected; it will be imported as a file reference, bind a credential for the passphrase")
		}
		decideKeyAction(&item, seenFingerprints, existingKeyByName, existingFPNames, batchKeyNames)
		seenFingerprints[item.Fingerprint] = true
		batchKeyNames[strings.ToLower(key.Name)] = true
		keyPreviews = append(keyPreviews, item)
	}
	preview.Keys = keyPreviews
	return preview, nil
}
