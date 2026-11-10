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
	// RootDir confines the root config file and every file reached through
	// Include directives (after glob expansion and symlink resolution) to its
	// directory tree. Empty disables the confinement (desktop behavior).
	RootDir string
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
	// Group, AuthMethod and Warnings are only set by the parsers of other clients' session files: the session's
	// folder path, "password" when the session authenticates with a password that is never imported, and notes
	// about settings the import drops.
	Group      string
	AuthMethod string
	Warnings   []string
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
	blocks   []*block
	boundary string
}

type directive struct {
	key  string
	val  string
	line int
}

// block is one Host section, or a global section (directives before the first Host of a file), which applies to
// every host. Blocks keep file order with includes expanded in place: the order OpenSSH applies them in.
type block struct {
	global     bool
	patterns   []string
	directives []directive
	source     string
	line       int
}

func Parse(path string, limits Limits) (*Result, error) {
	limits = withDefaultLimits(limits)
	p := &fileParser{
		limits:  limits,
		visited: map[string]bool{},
		active:  map[string]bool{},
	}
	abs, err := filepath.Abs(expandPath(path))
	if err != nil {
		return nil, err
	}
	if limits.RootDir != "" {
		boundary, err := filepath.Abs(limits.RootDir)
		if err != nil {
			return nil, err
		}
		p.boundary = canonicalHomePath(boundary)
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, err
		}
		if !pathWithinDir(p.boundary, resolved) {
			return nil, fmt.Errorf("sshconfig: %s escapes root boundary %s", abs, p.boundary)
		}
		abs = resolved
	}
	if err := p.parseFile(abs, 0); err != nil {
		return nil, err
	}
	p.resolveHosts()
	return &p.result, nil
}

func pathWithinDir(boundary, path string) bool {
	rel, err := filepath.Rel(boundary, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
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
		p.diagnose("include-cycle", path, 0, "Include 形成循环，已跳过 %s", path)
		return nil
	}
	if p.visited[path] {
		return nil
	}
	if depth > p.limits.MaxIncludeDepth {
		p.diagnose("include-depth-exceeded", path, 0, "Include 嵌套层数超过上限 %d", p.limits.MaxIncludeDepth)
		return nil
	}
	if p.files >= p.limits.MaxFiles {
		if !p.result.Truncated {
			p.result.Truncated = true
			p.diagnose("limit-truncated", path, 0, "配置文件数量超过上限 %d", p.limits.MaxFiles)
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
		p.diagnose("file-too-large", path, 0, "被包含的文件超过大小上限 %d 字节", p.limits.MaxFileBytes)
		return nil
	}

	p.visited[path] = true
	p.active[path] = true
	p.files++
	defer delete(p.active, path)

	f, err := openSharedDelete(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var cur *block
	var global *block
	inMatch := false

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
			inMatch = false
			cur = &block{patterns: strings.Fields(val), source: path, line: lineNo}
			p.blocks = append(p.blocks, cur)
			for _, pattern := range cur.patterns {
				if isWildcardPattern(pattern) {
					p.diagnose("host-pattern-skipped", path, lineNo, "通配 Host 模式 %q 不作为主机导入，其设置会应用到匹配的主机", pattern)
				}
			}
			continue
		case "match":
			cur = nil
			inMatch = true
			p.diagnose("match-skipped", path, lineNo, "Match 块不受支持，已跳过其中的设置")
			continue
		case "include":
			if cur != nil || inMatch {
				p.diagnose("include-ignored", path, lineNo, "Host 或 Match 块内的 Include 已忽略")
				continue
			}
			// Directives after the included files belong to a new global section, so file order is kept.
			global = nil
			for _, match := range resolveIncludes(filepath.Dir(path), val) {
				if p.boundary != "" {
					resolved, err := filepath.EvalSymlinks(match)
					if err != nil || !pathWithinDir(p.boundary, resolved) {
						p.diagnose("include-out-of-bounds", match, 0, "被包含的文件超出允许的目录 %s", p.boundary)
						continue
					}
					match = resolved
				}
				if err := p.parseFile(match, depth+1); err != nil {
					return err
				}
			}
			continue
		}
		if inMatch {
			continue
		}
		lower := strings.ToLower(key)
		switch lower {
		case "hostname", "user", "identityfile", "proxyjump":
		case "port":
			if port, err := strconv.Atoi(val); err != nil || port < 1 || port > 65535 {
				p.diagnose("invalid-port", path, lineNo, "端口 %q 无效，已忽略", val)
				continue
			}
		default:
			continue
		}
		target := cur
		if target == nil {
			if global == nil {
				global = &block{global: true, source: path, line: lineNo}
				p.blocks = append(p.blocks, global)
			}
			target = global
		}
		target.directives = append(target.directives, directive{key: lower, val: val, line: lineNo})
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("sshconfig: %s: %w", path, err)
	}
	return nil
}

// resolveHosts turns every concrete Host alias into a host, applying the directives of each block that matches the
// alias in file order with the first value winning, as OpenSSH does. Wildcard and negated patterns only contribute
// settings; repeated blocks for one alias merge.
func (p *fileParser) resolveHosts() {
	type aliasEntry struct {
		alias  string
		source string
	}
	var aliases []aliasEntry
	firstSource := map[string]string{}
	literal := map[string][]int{}
	var general []int
	for idx, b := range p.blocks {
		if b.global {
			general = append(general, idx)
			continue
		}
		wildcard := false
		for _, pattern := range b.patterns {
			if isWildcardPattern(pattern) {
				wildcard = true
			}
		}
		if wildcard {
			general = append(general, idx)
		}
		for _, pattern := range b.patterns {
			if isWildcardPattern(pattern) {
				continue
			}
			alias := sanitizeValue(pattern)
			if alias == "" {
				continue
			}
			if !wildcard {
				if list := literal[alias]; len(list) == 0 || list[len(list)-1] != idx {
					literal[alias] = append(list, idx)
				}
			}
			if source, dup := firstSource[alias]; dup {
				p.diagnose("duplicate-alias", b.source, b.line, "主机 %q 已在 %s 中定义，两处设置已合并，先出现的值优先", alias, source)
				continue
			}
			firstSource[alias] = b.source
			aliases = append(aliases, aliasEntry{alias: alias, source: b.source})
		}
	}

	reportedTokens := map[string]bool{}
	for _, entry := range aliases {
		if len(p.result.Hosts) >= p.limits.MaxHosts {
			if !p.result.Truncated {
				p.result.Truncated = true
				p.diagnose("limit-truncated", entry.source, 0, "主机数量超过上限 %d", p.limits.MaxHosts)
			}
			return
		}
		host := Host{Alias: entry.alias, Source: entry.source}
		var hostname, username, proxyJump string
		var hostnameDirective directive
		var hostnameSource string
		apply := func(b *block) {
			for _, d := range b.directives {
				switch d.key {
				case "hostname":
					if hostname == "" {
						hostname = d.val
						hostnameDirective = d
						hostnameSource = b.source
					}
				case "port":
					if host.Port == 0 {
						host.Port, _ = strconv.Atoi(d.val)
					}
				case "user":
					if username == "" {
						username = d.val
					}
				case "identityfile":
					if ident := expandPath(d.val); !containsString(host.IdentityFiles, ident) {
						host.IdentityFiles = append(host.IdentityFiles, ident)
					}
				case "proxyjump":
					if proxyJump == "" {
						proxyJump = d.val
					}
				}
			}
		}
		// literal[entry.alias] (blocks naming the alias) and general (global and wildcard blocks) are both ascending
		// block indexes; merge them to keep file order.
		lits := literal[entry.alias]
		i, j := 0, 0
		for i < len(lits) || j < len(general) {
			if j >= len(general) || i < len(lits) && lits[i] < general[j] {
				apply(p.blocks[lits[i]])
				i++
				continue
			}
			if b := p.blocks[general[j]]; b.global || hostPatternsMatch(b.patterns, entry.alias) {
				apply(b)
			}
			j++
		}
		if hostname != "" {
			expanded, unsupported := expandHostnameTokens(hostname, entry.alias)
			hostname = expanded
			for _, token := range unsupported {
				where := fmt.Sprintf("%s:%d", hostnameSource, hostnameDirective.line)
				if reportedTokens[where+token] {
					continue
				}
				reportedTokens[where+token] = true
				p.diagnose("hostname-token-unsupported", hostnameSource, hostnameDirective.line, "HostName 中的 %s 暂不支持展开，已按原样保留", token)
			}
		}
		host.Hostname = sanitizeValue(hostname)
		host.Username = sanitizeValue(username)
		host.ProxyJump = sanitizeValue(proxyJump)
		if host.Hostname == "" {
			host.Hostname = entry.alias
		}
		if host.Port == 0 {
			host.Port = 22
		}
		p.result.Hosts = append(p.result.Hosts, host)
	}
}

// hostPatternsMatch reports whether a Host line matches alias: some pattern matches and no negated pattern does.
func hostPatternsMatch(patterns []string, alias string) bool {
	matched := false
	for _, pattern := range patterns {
		if negated, ok := strings.CutPrefix(pattern, "!"); ok {
			if matchHostPattern(negated, alias) {
				return false
			}
			continue
		}
		if matchHostPattern(pattern, alias) {
			matched = true
		}
	}
	return matched
}

// matchHostPattern matches s against an OpenSSH pattern, where * matches any run of characters and ? one character.
func matchHostPattern(pattern, s string) bool {
	p, str := []rune(pattern), []rune(s)
	pi, si := 0, 0
	star, mark := -1, 0
	for si < len(str) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == str[si]):
			pi++
			si++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, si
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			si = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// expandHostnameTokens expands the HostName tokens OpenSSH supports there: %h (the host alias) and %%. Any other
// token stays as written and is returned so it can be reported.
func expandHostnameTokens(value, alias string) (string, []string) {
	if !strings.Contains(value, "%") {
		return value, nil
	}
	var out strings.Builder
	var unsupported []string
	for i := 0; i < len(value); i++ {
		if value[i] != '%' || i+1 >= len(value) {
			out.WriteByte(value[i])
			continue
		}
		switch value[i+1] {
		case 'h':
			out.WriteString(alias)
		case '%':
			out.WriteByte('%')
		default:
			token := value[i : i+2]
			if !containsString(unsupported, token) {
				unsupported = append(unsupported, token)
			}
			out.WriteString(token)
		}
		i++
	}
	return out.String(), unsupported
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
		expanded := strings.Contains(p, "%d")
		if expanded {
			p = strings.ReplaceAll(p, "%d", home)
		}
		if p == "~" {
			return home
		}
		if strings.HasPrefix(p, "~/") || filepath.Separator == '\\' && strings.HasPrefix(p, `~\`) {
			return filepath.Join(home, p[2:])
		}
		if expanded && filepath.Separator == '\\' {
			return filepath.FromSlash(p)
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
