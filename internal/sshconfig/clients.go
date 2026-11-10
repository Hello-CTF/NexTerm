package sshconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Session files of other clients are read whole; a session directory of a few
// thousand hosts stays far below these bounds.
const (
	maxClientFileBytes = 16 << 20
	maxClientFiles     = 10000
)

// Import sources for the session stores of other SSH clients.
const (
	SourcePuTTY     = "putty"
	SourceMobaXterm = "mobaxterm"
	SourceXshell    = "xshell"
	SourceSecureCRT = "securecrt"
)

// ErrNoClientSessions reports a file or folder that holds no sessions of the selected client.
var ErrNoClientSessions = errors.New("sshconfig: no sessions of the selected client")

// clientSession is one saved session of another client before it is planned.
type clientSession struct {
	name     string
	group    string
	protocol string
	host     string
	port     string
	username string
	keyFile  string
	// keyWarning replaces the key file when the session names a key NexTerm cannot read directly.
	keyWarning string
	warnings   []string
	where      string
}

// clientCollector turns sessions of one client into parser hosts and diagnostics.
type clientCollector struct {
	client string
	limits Limits
	result Result
}

func newClientCollector(client string, limits Limits) *clientCollector {
	return &clientCollector{client: client, limits: withDefaultLimits(limits)}
}

func (c *clientCollector) diagnose(code, where, format string, args ...any) {
	c.result.Diagnostics = append(c.result.Diagnostics, Diagnostic{Code: code, Source: where, Message: fmt.Sprintf(format, args...)})
}

func (c *clientCollector) add(session clientSession) {
	name := sanitizeValue(session.name)
	if name == "" {
		name = sanitizeValue(session.host)
	}
	if protocol := strings.ToLower(strings.TrimSpace(session.protocol)); protocol != "ssh" {
		c.diagnose("session-protocol-skipped", session.where, "会话 %q 使用 %s 协议，NexTerm 目前只导入 SSH 会话，已跳过", name, session.protocol)
		return
	}
	host := sanitizeValue(session.host)
	username := sanitizeValue(session.username)
	if at := strings.LastIndex(host, "@"); at >= 0 {
		if username == "" {
			username = host[:at]
		}
		host = host[at+1:]
	}
	if host == "" {
		c.diagnose("session-host-missing", session.where, "会话 %q 未设置主机地址，已跳过", name)
		return
	}
	port := 22
	if raw := strings.TrimSpace(session.port); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 65535 {
			c.diagnose("invalid-port", session.where, "会话 %q 的端口 %q 无效，已跳过", name, raw)
			return
		}
		port = value
	}
	if len(c.result.Hosts) >= c.limits.MaxHosts {
		if !c.result.Truncated {
			c.result.Truncated = true
			c.diagnose("limit-truncated", session.where, "主机数量超过上限 %d", c.limits.MaxHosts)
		}
		return
	}
	item := Host{
		Alias:      name,
		Hostname:   host,
		Port:       port,
		Username:   username,
		Group:      cleanClientGroup(session.group),
		AuthMethod: "password",
		Source:     session.where,
		Warnings:   session.warnings,
	}
	keyFile := sanitizeValue(session.keyFile)
	switch {
	case session.keyWarning != "":
		item.AuthMethod = ""
		item.Warnings = append(item.Warnings, session.keyWarning)
	case strings.EqualFold(filepath.Ext(strings.ReplaceAll(keyFile, `\`, "/")), ".ppk"):
		item.AuthMethod = ""
		item.Warnings = append(item.Warnings, fmt.Sprintf("私钥 %s 是 PuTTY 格式（.ppk），NexTerm 无法直接使用；请用 PuTTYgen 导出为 OpenSSH 格式后导入密钥并绑定到该主机", keyFile))
	case keyFile != "":
		item.IdentityFiles = []string{expandPath(keyFile)}
		item.AuthMethod = ""
	}
	if username == "" {
		item.Warnings = append(item.Warnings, "会话未保存用户名；请在导入后填写，否则连接时使用 root")
	}
	c.result.Hosts = append(c.result.Hosts, item)
}

// finish disambiguates sessions that share a name in different folders and reports that no password was imported.
func (c *clientCollector) finish(where string) *Result {
	groupsByName := map[string]map[string]bool{}
	for _, host := range c.result.Hosts {
		lower := strings.ToLower(host.Alias)
		if groupsByName[lower] == nil {
			groupsByName[lower] = map[string]bool{}
		}
		groupsByName[lower][strings.ToLower(host.Group)] = true
	}
	for i := range c.result.Hosts {
		host := &c.result.Hosts[i]
		if host.Group != "" && len(groupsByName[strings.ToLower(host.Alias)]) > 1 {
			host.Alias = host.Group + "/" + host.Alias
		}
	}
	if len(c.result.Hosts) > 0 {
		c.diagnose("password-not-imported", where, "%s 保存的密码经过加密或混淆，不会导入；使用密码认证的主机需在连接时重新输入或绑定密码", c.client)
	}
	return &c.result
}

// cleanClientGroup normalizes a client folder path to slash-separated, non-empty segments.
func cleanClientGroup(group string) string {
	group = strings.ReplaceAll(group, `\`, "/")
	segments := strings.Split(group, "/")
	out := segments[:0]
	for _, segment := range segments {
		if segment = sanitizeValue(segment); segment != "" && segment != "." && segment != ".." {
			out = append(out, segment)
		}
	}
	return strings.Join(out, "/")
}

// readClientFile reads one bounded session file, following a symlink to a regular file.
func readClientFile(name string) ([]byte, error) {
	info, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("sshconfig: %s is not a regular file", name)
	}
	if info.Size() > maxClientFileBytes {
		return nil, fmt.Errorf("sshconfig: %s exceeds size limit %d", name, maxClientFileBytes)
	}
	file, err := openSharedDelete(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxClientFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxClientFileBytes {
		return nil, fmt.Errorf("sshconfig: %s exceeds size limit %d", name, maxClientFileBytes)
	}
	return data, nil
}

// decodeClientText decodes the text encodings Windows clients write: UTF-8 with or without BOM, and UTF-16 (regedit
// exports and Xshell sessions) with a BOM or, little-endian, without one. Line endings become "\n".
func decodeClientText(data []byte) string {
	var text string
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		text = string(data[3:])
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		text = decodeUTF16(data[2:], false)
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		text = decodeUTF16(data[2:], true)
	case len(data) >= 4 && data[0] != 0 && data[1] == 0 && data[2] != 0 && data[3] == 0:
		text = decodeUTF16(data, false)
	default:
		text = string(data)
	}
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

func decodeUTF16(data []byte, bigEndian bool) string {
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		if bigEndian {
			units = append(units, uint16(data[i])<<8|uint16(data[i+1]))
		} else {
			units = append(units, uint16(data[i])|uint16(data[i+1])<<8)
		}
	}
	return string(utf16.Decode(units))
}

type iniSection struct {
	name   string
	values map[string]string
	keys   []string
}

// parseINI splits Windows INI text into sections; keys are matched case-insensitively and keep file order.
func parseINI(text string) []*iniSection {
	var sections []*iniSection
	current := &iniSection{values: map[string]string{}}
	sections = append(sections, current)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = &iniSection{name: strings.TrimSpace(line[1 : len(line)-1]), values: map[string]string{}}
			sections = append(sections, current)
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		lower := strings.ToLower(key)
		if _, seen := current.values[lower]; !seen {
			current.keys = append(current.keys, key)
		}
		current.values[lower] = strings.TrimSpace(value)
	}
	return sections
}

func (s *iniSection) get(key string) string { return s.values[strings.ToLower(key)] }

// walkClientSessions visits the session files with the given extension below dir; the relative directory of each
// file is its folder. Symlinked directories are not followed.
func walkClientSessions(dir, ext string, c *clientCollector, visit func(file, group, name string, data []byte)) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("sshconfig: %s is not a directory", dir)
	}
	files := 0
	found := false
	err = filepath.WalkDir(dir, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if file == dir {
				return walkErr
			}
			c.diagnose("file-unreadable", file, "无法读取 %s，已跳过", file)
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ext) {
			return nil
		}
		if files >= maxClientFiles {
			if !c.result.Truncated {
				c.result.Truncated = true
				c.diagnose("limit-truncated", file, "会话文件数量超过上限 %d", maxClientFiles)
			}
			return filepath.SkipAll
		}
		files++
		found = true
		data, err := readClientFile(file)
		if err != nil {
			c.diagnose("file-unreadable", file, "无法读取 %s，已跳过", file)
			return nil
		}
		rel, err := filepath.Rel(dir, filepath.Dir(file))
		if err != nil || rel == "." {
			rel = ""
		}
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		visit(file, path.Clean("/" + filepath.ToSlash(rel))[1:], name, data)
		return nil
	})
	if err == nil && !found {
		err = ErrNoClientSessions
	}
	return err
}

// previewClientResult plans hosts parsed from another client's sessions.
func previewClientResult(source string, result *Result, existing []ExistingAsset, existingKeys []ExistingKey, limits Limits) *ImportPreview {
	preview := previewHosts(source, result, existing, existingKeys, limits)
	if result.Truncated {
		preview.Truncated = true
	}
	return preview
}
