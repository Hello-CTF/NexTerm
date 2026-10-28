package shellenv

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
)

// excludedKeys are per-process values a login shell computes for itself and that must not leak into
// sessions as inherited state.
var excludedKeys = map[string]bool{
	"_": true, "SHLVL": true, "PWD": true, "OLDPWD": true, "TERM": true,
	ResolvingEnv: true, "COLUMNS": true, "LINES": true,
}

func newMarker() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "NEXTERM_ENV_" + hex.EncodeToString(raw[:]), nil
}

// probeScript prints the environment between two markers; env -0 is preferred and plain env covers
// systems (macOS) whose env lacks -0.
func probeScript(marker string) string {
	return "echo " + marker + "; env -0 2>/dev/null || env; echo " + marker
}

// parseMarked extracts the KEY=VALUE pairs printed between the first two occurrences of marker, ignoring
// everything an rc file prints before or after.
func parseMarked(output []byte, marker string) (map[string]string, error) {
	open := []byte(marker + "\n")
	start := bytes.Index(output, open)
	if start < 0 {
		return nil, errors.New("start marker not found in shell output")
	}
	rest := output[start+len(open):]
	end := bytes.Index(rest, []byte(marker))
	if end < 0 {
		return nil, errors.New("end marker not found in shell output")
	}
	section := rest[:end]
	sep := []byte{'\n'}
	if bytes.IndexByte(section, 0) >= 0 {
		sep = []byte{0}
	}
	env := map[string]string{}
	for _, entry := range bytes.Split(section, sep) {
		key, value, ok := strings.Cut(string(entry), "=")
		if !ok || !validKey(key) {
			continue // continuation lines of multi-line values and stray text
		}
		env[key] = value
	}
	if len(env) == 0 {
		return nil, errors.New("no environment between markers")
	}
	return env, nil
}

func validKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// validatePATH keeps the absolute entries of path and rejects values with line breaks or no usable entry.
func validatePATH(path string) (string, error) {
	if path == "" {
		return "", errors.New("shell reported no PATH")
	}
	if strings.ContainsAny(path, "\n\r\x00") {
		return "", errors.New("shell PATH contains a line break")
	}
	var kept []string
	for _, entry := range strings.Split(path, ":") {
		if filepath.IsAbs(entry) {
			kept = append(kept, entry)
		}
	}
	if len(kept) == 0 {
		return "", errors.New("shell PATH has no absolute entry")
	}
	return strings.Join(kept, ":"), nil
}

// MergePATH returns first followed by the entries of second that first lacks. Only absolute entries are
// kept, duplicates are dropped and order is preserved.
func MergePATH(first, second string) string {
	seen := map[string]bool{}
	var merged []string
	for _, list := range []string{first, second} {
		for _, entry := range strings.Split(list, ":") {
			if !strings.HasPrefix(entry, "/") || seen[entry] {
				continue
			}
			seen[entry] = true
			merged = append(merged, entry)
		}
	}
	return strings.Join(merged, ":")
}

// overlay returns base with the resolved variables applied. The resolved PATH is merged in front of the
// process PATH, SHELL is the chosen shell, and excluded keys keep their process value.
func overlay(base []string, resolved map[string]string, path, shell string) []string {
	out := make([]string, 0, len(base)+len(resolved))
	index := map[string]int{}
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if i, dup := index[key]; dup {
			out[i] = entry
			continue
		}
		index[key] = len(out)
		out = append(out, entry)
	}
	set := func(key, value string) {
		entry := key + "=" + value
		if i, ok := index[key]; ok {
			out[i] = entry
			return
		}
		index[key] = len(out)
		out = append(out, entry)
	}
	for key, value := range resolved {
		if excludedKeys[key] || key == "PATH" || key == "SHELL" || strings.ContainsRune(value, 0) {
			continue
		}
		set(key, value)
	}
	processPath := ""
	if i, ok := index["PATH"]; ok {
		_, processPath, _ = strings.Cut(out[i], "=")
	}
	set("PATH", MergePATH(path, processPath))
	set("SHELL", shell)
	return out
}

// EnvValue returns the last value of key in env, or "".
func EnvValue(env []string, key string) string {
	value := ""
	for _, entry := range env {
		if k, v, ok := strings.Cut(entry, "="); ok && k == key {
			value = v
		}
	}
	return value
}

var minimalPATH = map[string]bool{
	"/usr/bin": true, "/bin": true, "/usr/sbin": true, "/sbin": true, "/usr/local/bin": true, "/usr/local/sbin": true,
}

// pathMinimal reports whether path is empty or holds only the system directories a service manager
// supplies, which is when an inherited environment cannot be trusted to be the user's.
func pathMinimal(path string) bool {
	for _, entry := range strings.Split(path, ":") {
		if entry != "" && !minimalPATH[entry] {
			return false
		}
	}
	return true
}
