// Package sshconfig parses OpenSSH client config files into bounded import previews.
package sshconfig

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

type Limits struct {
	MaxFileBytes    int64
	MaxLineBytes    int
	MaxIncludeDepth int
	MaxFiles        int
	MaxHosts        int
	MaxKeys         int
}

var DefaultLimits = Limits{
	MaxFileBytes:    1 << 20,
	MaxLineBytes:    64 << 10,
	MaxIncludeDepth: 8,
	MaxFiles:        128,
	MaxHosts:        10000,
	MaxKeys:         10000,
}

type Host struct {
	Alias         string
	Hostname      string
	Port          int
	Username      string
	IdentityFiles []string
	ProxyJump     string
	Source        string
}

type Diagnostic struct {
	Code    string
	Source  string
	Message string
}

type Result struct {
	Hosts       []Host
	Diagnostics []Diagnostic
	Truncated   bool
}

type fileParser struct {
	limits   Limits
	result   Result
	visited  map[string]bool
	active   map[string]bool
	files    int
	aliasIdx map[string]int
}

type block struct {
	patterns  []string
	hostname  string
	port      int
	username  string
	idents    []string
	proxyJump string
	source    string
	line      int
}

func Parse(path string, limits Limits) (*Result, error) {
	limits = withDefaultLimits(limits)
	p := &fileParser{
		limits:   limits,
		visited:  map[string]bool{},
		active:   map[string]bool{},
		aliasIdx: map[string]int{},
	}
	abs, err := filepath.Abs(expandPath(path))
	if err != nil {
		return nil, err
	}
	if err := p.parseFile(abs, 0); err != nil {
		return nil, err
	}
	return &p.result, nil
}

func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "config")
}

func withDefaultLimits(l Limits) Limits {
	d := DefaultLimits
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = d.MaxFileBytes
	}
	if l.MaxLineBytes <= 0 {
		l.MaxLineBytes = d.MaxLineBytes
	}
	if l.MaxIncludeDepth <= 0 {
		l.MaxIncludeDepth = d.MaxIncludeDepth
	}
	if l.MaxFiles <= 0 {
		l.MaxFiles = d.MaxFiles
	}
	if l.MaxHosts <= 0 {
		l.MaxHosts = d.MaxHosts
	}
	if l.MaxKeys <= 0 {
		l.MaxKeys = d.MaxKeys
	}
	return l
}

func (p *fileParser) parseFile(path string, depth int) error {
	if p.active[path] {
		p.diagnose("include-cycle", path, 0, "include cycle back to %s", path)
		return nil
	}
	if p.visited[path] {
		return nil
	}
	if depth > p.limits.MaxIncludeDepth {
		p.diagnose("include-depth-exceeded", path, 0, "include depth exceeds %d", p.limits.MaxIncludeDepth)
		return nil
	}
	if p.files >= p.limits.MaxFiles {
		if !p.result.Truncated {
			p.result.Truncated = true
			p.diagnose("limit-truncated", path, 0, "file count exceeds %d", p.limits.MaxFiles)
		}
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > p.limits.MaxFileBytes {
		if depth == 0 {
			return fmt.Errorf("sshconfig: %s exceeds size limit %d", path, p.limits.MaxFileBytes)
		}
		p.diagnose("file-too-large", path, 0, "included file exceeds size limit %d", p.limits.MaxFileBytes)
		return nil
	}

	p.visited[path] = true
	p.active[path] = true
	p.files++
	defer delete(p.active, path)

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var cur *block
	inMatch := false
	flush := func() {
		if cur != nil {
			p.emit(*cur)
			cur = nil
		}
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), p.limits.MaxLineBytes)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := splitDirective(line)
		if !ok {
			continue
		}
		switch strings.ToLower(key) {
		case "host":
			flush()
			inMatch = false
			cur = &block{patterns: strings.Fields(val), source: path, line: lineNo}
			continue
		case "match":
			flush()
			inMatch = true
			p.diagnose("match-skipped", path, lineNo, "match block skipped")
			continue
		case "include":
			if cur != nil || inMatch {
				p.diagnose("include-ignored", path, lineNo, "include inside host or match block ignored")
				continue
			}
			for _, match := range resolveIncludes(filepath.Dir(path), val) {
				if err := p.parseFile(match, depth+1); err != nil {
					return err
				}
			}
			continue
		}
		if cur == nil || inMatch {
			continue
		}
		switch strings.ToLower(key) {
		case "hostname":
			if cur.hostname == "" {
				cur.hostname = val
			}
		case "port":
			if cur.port == 0 {
				port, err := strconv.Atoi(val)
				if err != nil || port < 1 || port > 65535 {
					p.diagnose("invalid-port", path, lineNo, "invalid port %q", val)
					continue
				}
				cur.port = port
			}
		case "user":
			if cur.username == "" {
				cur.username = val
			}
		case "identityfile":
			ident := expandPath(val)
			if !containsString(cur.idents, ident) {
				cur.idents = append(cur.idents, ident)
			}
		case "proxyjump":
			if cur.proxyJump == "" {
				cur.proxyJump = val
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("sshconfig: %s: %w", path, err)
	}
	flush()
	return nil
}

func (p *fileParser) emit(b block) {
	hostname := sanitizeValue(b.hostname)
	username := sanitizeValue(b.username)
	proxyJump := sanitizeValue(b.proxyJump)
	for _, pattern := range b.patterns {
		if isWildcardPattern(pattern) {
			p.diagnose("host-pattern-skipped", b.source, b.line, "host pattern %q skipped", pattern)
			continue
		}
		alias := sanitizeValue(pattern)
		if alias == "" {
			continue
		}
		if idx, dup := p.aliasIdx[alias]; dup {
			p.diagnose("duplicate-alias", b.source, b.line, "host %q already defined in %s", alias, p.result.Hosts[idx].Source)
			continue
		}
		if len(p.result.Hosts) >= p.limits.MaxHosts {
			if !p.result.Truncated {
				p.result.Truncated = true
				p.diagnose("limit-truncated", b.source, b.line, "host count exceeds %d", p.limits.MaxHosts)
			}
			return
		}
		host := Host{
			Alias:         alias,
			Hostname:      hostname,
			Port:          b.port,
			Username:      username,
			IdentityFiles: append([]string(nil), b.idents...),
			ProxyJump:     proxyJump,
			Source:        b.source,
		}
		if host.Hostname == "" {
			host.Hostname = alias
		}
		if host.Port == 0 {
			host.Port = 22
		}
		p.aliasIdx[alias] = len(p.result.Hosts)
		p.result.Hosts = append(p.result.Hosts, host)
	}
}

func (p *fileParser) diagnose(code, source string, line int, format string, args ...interface{}) {
	where := source
	if line > 0 {
		where = fmt.Sprintf("%s:%d", source, line)
	}
	p.result.Diagnostics = append(p.result.Diagnostics, Diagnostic{
		Code:    code,
		Source:  where,
		Message: fmt.Sprintf(format, args...),
	})
}

func splitDirective(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", false
	}
	ws := strings.IndexAny(line, " \t")
	if eq := strings.Index(line, "="); eq >= 0 && (ws < 0 || eq < ws) {
		if key := strings.TrimSpace(line[:eq]); key != "" {
			return key, strings.TrimSpace(line[eq+1:]), true
		}
	}
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return "", "", false
	}
	key := parts[0]
	val := strings.TrimSpace(line[len(key):])
	val = strings.TrimSpace(strings.TrimPrefix(val, "="))
	val = trimQuotes(val)
	if val == "" {
		return "", "", false
	}
	return key, val, true
}

func trimQuotes(s string) string {
	if len(s) >= 2 {
		if s[0] == '"' && s[len(s)-1] == '"' {
			return s[1 : len(s)-1]
		}
		if s[0] == '\'' && s[len(s)-1] == '\'' {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func resolveIncludes(baseDir, raw string) []string {
	var out []string
	for _, field := range strings.Fields(raw) {
		field = expandPath(strings.Trim(field, `"'`))
		if !filepath.IsAbs(field) {
			field = filepath.Join(baseDir, field)
		}
		matches, err := filepath.Glob(field)
		if err != nil {
			continue
		}
		out = append(out, matches...)
	}
	return out
}

func expandPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, `"'`)
	home, _ := os.UserHomeDir()
	if home != "" {
		p = strings.ReplaceAll(p, "%d", home)
		if p == "~" {
			return home
		}
		if strings.HasPrefix(p, "~/") {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func isWildcardPattern(pattern string) bool {
	return strings.ContainsAny(pattern, "*?!")
}

func sanitizeValue(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
}

func containsString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
