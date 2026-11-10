package sshconfig

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// PuTTYSessionsKey is where PuTTY keeps its saved sessions, relative to HKEY_CURRENT_USER.
const PuTTYSessionsKey = `Software\SimonTatham\PuTTY\Sessions`

// PuTTYSession is one saved session: the registry subkey name as PuTTY escapes it and its values, with DWORDs in
// decimal.
type PuTTYSession struct {
	Key    string
	Values map[string]string
	Where  string
}

// ErrPuTTYRegistryUnsupported reports that PuTTY's registry sessions can only be read on Windows; elsewhere the user
// imports a regedit export instead.
var ErrPuTTYRegistryUnsupported = errors.New("sshconfig: the PuTTY registry is only available on Windows")

var puttyProxyMethods = map[string]string{"1": "SOCKS 4", "2": "SOCKS 5", "3": "HTTP", "4": "Telnet", "5": "本地命令"}

// PreviewPuTTY reads PuTTY sessions from a regedit export at path or, with an empty path, from the current user's
// registry (Windows only).
func PreviewPuTTY(path string, existing []ExistingAsset, existingKeys []ExistingKey, limits Limits) (*ImportPreview, error) {
	var sessions []PuTTYSession
	if strings.TrimSpace(path) == "" {
		var err error
		if sessions, err = readPuTTYRegistry(); err != nil {
			return nil, err
		}
	} else {
		data, err := readClientFile(path)
		if err != nil {
			return nil, err
		}
		if sessions, err = ParsePuTTYReg(decodeClientText(data), path); err != nil {
			return nil, err
		}
	}
	return previewClientResult(SourcePuTTY, PuTTYResult(sessions, limits), existing, existingKeys, limits), nil
}

// ParsePuTTYReg extracts the PuTTY sessions of a regedit export (.reg, version 5 or REGEDIT4).
func ParsePuTTYReg(text, where string) ([]PuTTYSession, error) {
	var sessions []PuTTYSession
	var current *PuTTYSession
	marker := `\` + strings.ToLower(PuTTYSessionsKey) + `\`
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		for strings.HasSuffix(line, `\`) && i+1 < len(lines) {
			i++
			line = strings.TrimSuffix(line, `\`) + strings.TrimSpace(lines[i])
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = nil
			key := line[1 : len(line)-1]
			if strings.HasPrefix(key, "-") {
				continue
			}
			idx := strings.Index(strings.ToLower(key), marker)
			if idx < 0 {
				continue
			}
			name := key[idx+len(marker):]
			if name == "" || strings.Contains(name, `\`) {
				continue
			}
			sessions = append(sessions, PuTTYSession{Key: name, Values: map[string]string{}, Where: where})
			current = &sessions[len(sessions)-1]
			continue
		}
		if current == nil || !strings.HasPrefix(line, `"`) {
			continue
		}
		name, rest, ok := regQuoted(line)
		if !ok || !strings.HasPrefix(rest, "=") {
			continue
		}
		rest = strings.TrimSpace(rest[1:])
		switch {
		case strings.HasPrefix(rest, `"`):
			if value, _, ok := regQuoted(rest); ok {
				current.Values[strings.ToLower(name)] = value
			}
		case strings.HasPrefix(strings.ToLower(rest), "dword:"):
			if value, err := strconv.ParseUint(strings.TrimSpace(rest[len("dword:"):]), 16, 32); err == nil {
				current.Values[strings.ToLower(name)] = strconv.FormatUint(value, 10)
			}
		}
	}
	if len(sessions) == 0 {
		return nil, fmt.Errorf("%w: no %s keys in %s", ErrNoClientSessions, PuTTYSessionsKey, where)
	}
	return sessions, nil
}

// regQuoted reads a regedit quoted string with its \\ and \" escapes and returns the text after it.
func regQuoted(s string) (string, string, bool) {
	if !strings.HasPrefix(s, `"`) {
		return "", s, false
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
		case '"':
			return b.String(), s[i+1:], true
		default:
			b.WriteByte(s[i])
		}
	}
	return "", s, false
}

// PuTTYResult maps PuTTY sessions to hosts. PuTTY never stores passwords; its private keys are .ppk files that
// NexTerm cannot load, so they are reported instead of imported.
func PuTTYResult(sessions []PuTTYSession, limits Limits) *Result {
	c := newClientCollector("PuTTY", limits)
	for _, session := range sessions {
		name := session.Key
		if decoded, err := url.PathUnescape(name); err == nil {
			name = decoded
		}
		where := session.Where
		if where == "" {
			where = `HKCU\` + PuTTYSessionsKey + `\` + session.Key
		}
		values := session.Values
		get := func(key string) string { return values[strings.ToLower(key)] }
		if name == "Default Settings" {
			c.diagnose("template-skipped", where, "%s 中的 %q 是新会话的默认模板，不作为主机导入", "PuTTY", name)
			continue
		}
		protocol := get("Protocol")
		if protocol == "" {
			protocol = "ssh"
		}
		item := clientSession{
			name:     name,
			protocol: protocol,
			host:     get("HostName"),
			port:     get("PortNumber"),
			username: get("UserName"),
			keyFile:  get("PublicKeyFile"),
			where:    where,
		}
		if method := get("ProxyMethod"); method != "" && method != "0" {
			kind := puttyProxyMethods[method]
			if kind == "" {
				kind = method
			}
			proxy := get("ProxyHost")
			if port := get("ProxyPort"); proxy != "" && port != "" {
				proxy += ":" + port
			}
			item.warnings = append(item.warnings, fmt.Sprintf("会话配置的 %s 代理 %s 不会导入；如需代理，请在资产中重新设置", kind, proxy))
		}
		c.add(item)
	}
	return c.finish("PuTTY")
}
