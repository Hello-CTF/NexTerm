//go:build windows

package sshconfig

import (
	"errors"
	"strconv"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// readPuTTYRegistry reads every saved session below HKCU\Software\SimonTatham\PuTTY\Sessions. String and DWORD values
// are kept; other value types are not used by the import.
func readPuTTYRegistry() ([]PuTTYSession, error) {
	root, err := registry.OpenKey(registry.CURRENT_USER, PuTTYSessionsKey, registry.READ)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil, ErrNoClientSessions
		}
		return nil, err
	}
	defer root.Close()
	names, err := root.ReadSubKeyNames(-1)
	if err != nil {
		return nil, err
	}
	sessions := make([]PuTTYSession, 0, len(names))
	for _, name := range names {
		key, err := registry.OpenKey(root, name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		session := PuTTYSession{Key: name, Values: map[string]string{}, Where: `HKCU\` + PuTTYSessionsKey + `\` + name}
		valueNames, _ := key.ReadValueNames(-1)
		for _, valueName := range valueNames {
			_, valueType, err := key.GetValue(valueName, nil)
			if err != nil {
				continue
			}
			switch valueType {
			case registry.SZ, registry.EXPAND_SZ:
				if value, _, err := key.GetStringValue(valueName); err == nil {
					session.Values[strings.ToLower(valueName)] = value
				}
			case registry.DWORD, registry.QWORD:
				if value, _, err := key.GetIntegerValue(valueName); err == nil {
					session.Values[strings.ToLower(valueName)] = strconv.FormatUint(value, 10)
				}
			}
		}
		key.Close()
		sessions = append(sessions, session)
	}
	if len(sessions) == 0 {
		return nil, ErrNoClientSessions
	}
	return sessions, nil
}
