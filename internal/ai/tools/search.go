package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/cloudwego/eino/components/tool/utils"
)

// find_files and grep_files search the session's host read-only. They run ripgrep, grep/find or PowerShell
// through the session's exec channel, and walk the file API only when the host has no usable exec channel.
const (
	FindFilesTool = "find_files"
	GrepFilesTool = "grep_files"
	// SearchFilesAlias is the deprecated single search tool. It stays callable for one release and maps to
	// find_files or grep_files; it is not offered to the model.
	SearchFilesAlias = "search_files"
)

const (
	searchDefaultHeadLimit        = 100
	searchDefaultContentHeadLimit = 200
	searchMaxHeadLimit            = 2000
	searchMaxContext              = 10
	// searchMaxLineRunes clips one shown line, so a minified file cannot fill the result.
	searchMaxLineRunes  = 1000
	searchExecTimeout   = 60 * time.Second
	searchProbeTimeout  = 15 * time.Second
	searchStdoutLimit   = 4 << 20
	searchStderrLimit   = 64 << 10
	searchMaxBraceAlts  = 64
	searchSentinel      = "@@nexterm-search"
	searchOutputContent = "content"
	searchOutputFiles   = "files_with_matches"
	searchOutputCount   = "count"
)

// searchSkipDirs are the VCS and dependency directories skipped unless include_ignored is set.
var searchSkipDirs = []string{".git", ".hg", ".svn", ".bzr", "CVS", "_darcs", "node_modules", "__pycache__", ".venv", ".tox"}

// searchFileTypes maps grep_files type names to base-name globs. The same table drives every backend, so a type
// means the same files whichever program runs the search.
var searchFileTypes = map[string][]string{
	"go":    {"*.go"},
	"js":    {"*.js", "*.jsx", "*.mjs", "*.cjs"},
	"ts":    {"*.ts", "*.tsx", "*.mts", "*.cts"},
	"py":    {"*.py", "*.pyi"},
	"java":  {"*.java"},
	"rs":    {"*.rs"},
	"c":     {"*.c", "*.h"},
	"cpp":   {"*.cpp", "*.cc", "*.cxx", "*.hpp", "*.hh", "*.hxx", "*.h"},
	"cs":    {"*.cs"},
	"rb":    {"*.rb"},
	"php":   {"*.php"},
	"kt":    {"*.kt", "*.kts"},
	"swift": {"*.swift"},
	"lua":   {"*.lua"},
	"md":    {"*.md", "*.markdown"},
	"json":  {"*.json"},
	"yaml":  {"*.yaml", "*.yml"},
	"toml":  {"*.toml"},
	"xml":   {"*.xml"},
	"html":  {"*.html", "*.htm"},
	"css":   {"*.css", "*.scss", "*.less"},
	"sql":   {"*.sql"},
	"sh":    {"*.sh", "*.bash", "*.zsh"},
	"conf":  {"*.conf", "*.cfg", "*.ini", "*.cnf"},
	"log":   {"*.log"},
}

type FindFilesArgs struct {
	Pattern        string `json:"pattern" jsonschema:"required" jsonschema_description:"File glob. Without / it matches file names at any depth (*.go, *.{ts,tsx}, nginx*.conf); with / it matches the path relative to path, where ** spans any number of directories (src/**/*.ts, conf.d/*.conf)"`
	Path           string `json:"path,omitempty" jsonschema_description:"Directory to search (default the current terminal's working directory when known, else the home directory); a relative path is resolved against that default"`
	IncludeIgnored bool   `json:"include_ignored,omitempty" jsonschema_description:"Also search VCS and dependency directories (.git, node_modules and similar) and, with ripgrep, files excluded by .gitignore (default false)"`
	HeadLimit      int    `json:"head_limit,omitempty" jsonschema_description:"Maximum number of paths to return (1-2000; default 100)"`
	Offset         int    `json:"offset,omitempty" jsonschema_description:"Number of results to skip before returning head_limit of them, to page through a long result (default 0)"`
}

type GrepFilesArgs struct {
	Pattern         string `json:"pattern" jsonschema:"required" jsonschema_description:"Regular expression to look for in file contents (ripgrep/PCRE-style syntax, e.g. func\\s+main or (?:error|fatal)); escape regex metacharacters to match them literally"`
	Path            string `json:"path,omitempty" jsonschema_description:"File or directory to search (default the current terminal's working directory when known, else the home directory); a relative path is resolved against that default"`
	Glob            string `json:"glob,omitempty" jsonschema_description:"Only search files matching this glob, same syntax as find_files pattern (*.go, *.{ts,tsx}, src/**/*.py)"`
	Type            string `json:"type,omitempty" jsonschema:"enum=go,enum=js,enum=ts,enum=py,enum=java,enum=rs,enum=c,enum=cpp,enum=cs,enum=rb,enum=php,enum=kt,enum=swift,enum=lua,enum=md,enum=json,enum=yaml,enum=toml,enum=xml,enum=html,enum=css,enum=sql,enum=sh,enum=conf,enum=log" jsonschema_description:"Only search files of this type (by file extension); combines with glob"`
	CaseInsensitive bool   `json:"case_insensitive,omitempty" jsonschema_description:"Match letters regardless of case (default false)"`
	OutputMode      string `json:"output_mode,omitempty" jsonschema:"enum=files_with_matches,enum=content,enum=count" jsonschema_description:"files_with_matches (default) lists the matching files; content shows the matching lines as path:line:text; count shows path:number of matching lines"`
	Context         int    `json:"context,omitempty" jsonschema_description:"With output_mode=content, also show this many lines before and after each match (0-10; default 0)"`
	LineNumbers     *bool  `json:"line_numbers,omitempty" jsonschema_description:"With output_mode=content, prefix each line with its line number (default true)"`
	Multiline       bool   `json:"multiline,omitempty" jsonschema_description:"Let the pattern match across lines, with . matching newlines; needs ripgrep (rg) on the host and fails otherwise (default false)"`
	IncludeIgnored  bool   `json:"include_ignored,omitempty" jsonschema_description:"Also search VCS and dependency directories (.git, node_modules and similar) and, with ripgrep, files excluded by .gitignore (default false)"`
	HeadLimit       int    `json:"head_limit,omitempty" jsonschema_description:"Maximum number of entries to return: files or count rows (default 100), or output lines with output_mode=content (default 200); 1-2000"`
	Offset          int    `json:"offset,omitempty" jsonschema_description:"Number of entries to skip before returning head_limit of them, to page through a long result (default 0)"`
}

// SearchFilesArgs is the deprecated search_files payload. by is optional now: without it a glob-looking pattern
// searches names and anything else searches contents.
type SearchFilesArgs struct {
	Path    string `json:"path,omitempty" jsonschema_description:"Directory to search under"`
	Pattern string `json:"pattern" jsonschema:"required" jsonschema_description:"File name glob when by=name; regular expression when by=content"`
	By      string `json:"by,omitempty" jsonschema_description:"Match file names (name) or file contents (content)"`
}

// searchFilesAliasSchema validates search_files calls; it is never offered to the model.
var searchFilesAliasSchema = sync.OnceValue(func() Schema {
	info, err := utils.GoStruct2ToolInfo[SearchFilesArgs](SearchFilesAlias, "Deprecated: use find_files or grep_files.")
	if err != nil {
		panic(err)
	}
	converted, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		panic(err)
	}
	var parameters map[string]any
	encoded, _ := json.Marshal(converted)
	_ = json.Unmarshal(encoded, &parameters)
	return Schema{Name: SearchFilesAlias, Description: info.Desc, Parameters: parameters}
})

// searchRequest is a validated find_files or grep_files call.
type searchRequest struct {
	find           bool
	pattern        string
	path           string
	glob           string
	fileType       string
	ignoreCase     bool
	mode           string
	context        int
	lineNumbers    bool
	multiline      bool
	includeIgnored bool
	headLimit      int
	offset         int
	// names filters by the find pattern or the grep glob; types by the grep type. Both may be empty.
	names globMatcher
	types globMatcher
	// alias marks a call that came in as search_files, so the result can point the model at the new tools.
	alias string
}

func (q searchRequest) tool() string {
	if q.find {
		return FindFilesTool
	}
	return GrepFilesTool
}

// searchInvalid is an argument error, with the model wording next to the display wording.
func searchInvalid(display, model string) Output {
	return Output{Text: "工具参数无效: " + display, ModelText: "invalid tool arguments: " + model, ExitCode: 1}
}

func searchFailure(display, model string) Output {
	return Output{Text: display, ModelText: model, ExitCode: 1}
}

func normalizePaging(headLimit, offset, defaultLimit int) (int, int, *Output) {
	if headLimit == 0 {
		headLimit = defaultLimit
	}
	if headLimit < 1 || headLimit > searchMaxHeadLimit || offset < 0 {
		failure := searchInvalid(fmt.Sprintf("head_limit 必须在 1-%d 之间，offset 不能为负数", searchMaxHeadLimit),
			fmt.Sprintf("head_limit must be between 1 and %d, and offset cannot be negative", searchMaxHeadLimit))
		return 0, 0, &failure
	}
	return headLimit, offset, nil
}

func normalizeFindArgs(args FindFilesArgs) (searchRequest, *Output) {
	request := searchRequest{find: true, path: strings.TrimSpace(args.Path), includeIgnored: args.IncludeIgnored}
	pattern := strings.TrimSpace(args.Pattern)
	if pattern == "" {
		failure := searchInvalid("pattern 不能为空：请传文件名 glob，例如 *.go 或 src/**/*.ts", "pattern cannot be empty: pass a file glob such as *.go or src/**/*.ts")
		return request, &failure
	}
	var failure *Output
	request.path, pattern, failure = splitAbsoluteGlob(request.path, pattern)
	if failure != nil {
		return request, failure
	}
	request.pattern = pattern
	matcher, err := compileGlob(pattern)
	if err != nil {
		failure := searchInvalid(fmt.Sprintf("文件名 glob 无效: %v", err), fmt.Sprintf("invalid file glob: %v", err))
		return request, &failure
	}
	request.names = matcher
	request.headLimit, request.offset, failure = normalizePaging(args.HeadLimit, args.Offset, searchDefaultHeadLimit)
	return request, failure
}

func normalizeGrepArgs(args GrepFilesArgs) (searchRequest, *Output) {
	request := searchRequest{
		pattern: args.Pattern, path: strings.TrimSpace(args.Path), ignoreCase: args.CaseInsensitive,
		multiline: args.Multiline, includeIgnored: args.IncludeIgnored, context: args.Context,
		mode: strings.TrimSpace(args.OutputMode), fileType: strings.ToLower(strings.TrimSpace(args.Type)),
	}
	if strings.TrimSpace(args.Pattern) == "" {
		failure := searchInvalid("pattern 不能为空：请传要在文件内容中查找的正则表达式", "pattern cannot be empty: pass the regular expression to look for in file contents")
		return request, &failure
	}
	if strings.ContainsAny(args.Pattern, "\x00\n\r") {
		failure := searchInvalid("pattern 不能包含换行或 NUL 字符；跨行匹配请设置 multiline=true 并在正则中写 \\n", "pattern cannot contain a newline or NUL character; to match across lines set multiline=true and write \\n in the regex")
		return request, &failure
	}
	switch request.mode {
	case "":
		request.mode = searchOutputFiles
	case searchOutputFiles, searchOutputContent, searchOutputCount:
	default:
		failure := searchInvalid("output_mode 必须为 files_with_matches、content 或 count", "output_mode must be files_with_matches, content or count")
		return request, &failure
	}
	if request.context < 0 || request.context > searchMaxContext {
		failure := searchInvalid(fmt.Sprintf("context 必须在 0-%d 行之间", searchMaxContext), fmt.Sprintf("context must be between 0 and %d lines", searchMaxContext))
		return request, &failure
	}
	if request.mode != searchOutputContent {
		request.context = 0
	}
	request.lineNumbers = args.LineNumbers == nil || *args.LineNumbers
	if glob := strings.TrimSpace(args.Glob); glob != "" {
		var failure *Output
		request.path, glob, failure = splitAbsoluteGlob(request.path, glob)
		if failure != nil {
			return request, failure
		}
		matcher, err := compileGlob(glob)
		if err != nil {
			failure := searchInvalid(fmt.Sprintf("glob 无效: %v", err), fmt.Sprintf("invalid glob: %v", err))
			return request, &failure
		}
		request.glob, request.names = glob, matcher
	}
	if request.fileType != "" {
		globs, ok := searchFileTypes[request.fileType]
		if !ok {
			names := searchTypeNames()
			failure := searchInvalid(fmt.Sprintf("不支持的文件类型 %s；可用类型：%s", args.Type, strings.Join(names, ", ")),
				fmt.Sprintf("unsupported file type %s; available types: %s", args.Type, strings.Join(names, ", ")))
			return request, &failure
		}
		matcher, _ := compileGlob("{" + strings.Join(globs, ",") + "}")
		request.types = matcher
	}
	defaultLimit := searchDefaultHeadLimit
	if request.mode == searchOutputContent {
		defaultLimit = searchDefaultContentHeadLimit
	}
	var failure *Output
	request.headLimit, request.offset, failure = normalizePaging(args.HeadLimit, args.Offset, defaultLimit)
	return request, failure
}

func searchTypeNames() []string {
	names := make([]string, 0, len(searchFileTypes))
	for name := range searchFileTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// splitAbsoluteGlob lets a glob carry its directory: with no path, "/etc/nginx/**/*.conf" searches /etc/nginx for
// "**/*.conf"; with a path, an absolute glob must lie under it and is made relative to it.
func splitAbsoluteGlob(root, pattern string) (string, string, *Output) {
	pattern = strings.TrimPrefix(pattern, "./")
	if !strings.HasPrefix(pattern, "/") && !strings.HasPrefix(pattern, "~/") {
		return root, pattern, nil
	}
	if root == "" {
		segments := strings.Split(pattern, "/")
		literal := 0
		for literal < len(segments)-1 && !strings.ContainsAny(segments[literal], "*?[{\\") {
			literal++
		}
		directory := strings.Join(segments[:literal], "/")
		if directory == "" {
			directory = "/"
		}
		return directory, strings.Join(segments[literal:], "/"), nil
	}
	base := strings.TrimRight(root, "/")
	if relative, ok := strings.CutPrefix(pattern, base+"/"); ok && relative != "" {
		return root, relative, nil
	}
	failure := searchInvalid(fmt.Sprintf("glob %s 不在 path %s 下；glob 相对于 path，请把目录写进 path", pattern, root),
		fmt.Sprintf("glob %s is not under path %s; globs are relative to path, so put the directory in path", pattern, root))
	return root, pattern, &failure
}

// globMatcher is one glob after brace expansion: a path matches when any alternative matches.
type globMatcher struct {
	alternatives []nameGlob
	// expanded keeps the alternatives as text for backends that take globs.
	expanded []string
}

func (m globMatcher) empty() bool { return len(m.alternatives) == 0 }

func (m globMatcher) match(relative string) bool {
	if m.empty() {
		return true
	}
	name := path.Base(relative)
	for _, alternative := range m.alternatives {
		if alternative.match(name, relative) {
			return true
		}
	}
	return false
}

// baseNameOnly reports whether every alternative matches base names only, as find -name and grep --include do.
func (m globMatcher) baseNameOnly() bool {
	for _, alternative := range m.alternatives {
		if alternative.segments != nil {
			return false
		}
	}
	return true
}

func compileGlob(pattern string) (globMatcher, error) {
	expanded, err := expandBraces(pattern)
	if err != nil {
		return globMatcher{}, err
	}
	matcher := globMatcher{}
	for _, alternative := range expanded {
		alternative = strings.TrimPrefix(alternative, "./")
		if alternative == "" {
			return globMatcher{}, errors.New("empty glob")
		}
		if strings.HasPrefix(alternative, "/") {
			return globMatcher{}, errors.New("glob must be relative to path")
		}
		compiled, err := compileNameGlob(alternative)
		if err != nil {
			return globMatcher{}, err
		}
		matcher.alternatives = append(matcher.alternatives, compiled)
		matcher.expanded = append(matcher.expanded, alternative)
	}
	return matcher, nil
}

// expandBraces expands {a,b} groups, which ripgrep and most glob tools accept but path.Match does not.
func expandBraces(pattern string) ([]string, error) {
	open := -1
	depth := 0
	for index := 0; index < len(pattern); index++ {
		switch pattern[index] {
		case '\\':
			index++
		case '{':
			if depth == 0 {
				open = index
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			depth--
			if depth != 0 {
				continue
			}
			var options []string
			start, inner := open+1, 0
			for cursor := open + 1; cursor < index; cursor++ {
				switch pattern[cursor] {
				case '\\':
					cursor++
				case '{':
					inner++
				case '}':
					inner--
				case ',':
					if inner == 0 {
						options = append(options, pattern[start:cursor])
						start = cursor + 1
					}
				}
			}
			options = append(options, pattern[start:index])
			var result []string
			for _, option := range options {
				rest, err := expandBraces(pattern[:open] + option + pattern[index+1:])
				if err != nil {
					return nil, err
				}
				result = append(result, rest...)
				if len(result) > searchMaxBraceAlts {
					return nil, fmt.Errorf("brace expansion yields more than %d patterns", searchMaxBraceAlts)
				}
			}
			return result, nil
		}
	}
	if depth != 0 {
		return nil, errors.New("unclosed {")
	}
	return []string{pattern}, nil
}

// globRegexp renders the matcher as a regular expression over a "/"-separated relative path, in the syntax both
// Go and .NET accept, for backends that match with a regex (PowerShell).
func (m globMatcher) globRegexp() string {
	parts := make([]string, 0, len(m.expanded))
	for _, alternative := range m.expanded {
		if !strings.Contains(alternative, "/") {
			parts = append(parts, "(?:^|/)"+globSegmentRegexp(alternative)+"$")
			continue
		}
		segments := strings.Split(path.Clean(alternative), "/")
		var expression strings.Builder
		expression.WriteString("^")
		for index, segment := range segments {
			last := index == len(segments)-1
			if segment == "**" {
				if last {
					expression.WriteString(".*")
				} else {
					expression.WriteString("(?:[^/]+/)*")
				}
				continue
			}
			expression.WriteString(globSegmentRegexp(segment))
			if !last {
				expression.WriteString("/")
			}
		}
		expression.WriteString("$")
		parts = append(parts, expression.String())
	}
	return "(?:" + strings.Join(parts, "|") + ")"
}

func globSegmentRegexp(segment string) string {
	var out strings.Builder
	for index := 0; index < len(segment); index++ {
		character := segment[index]
		switch character {
		case '*':
			out.WriteString("[^/]*")
		case '?':
			out.WriteString("[^/]")
		case '\\':
			if index+1 < len(segment) {
				index++
				out.WriteString(regexp.QuoteMeta(segment[index : index+1]))
			}
		case '[':
			end := strings.IndexByte(segment[index+1:], ']')
			if end < 0 {
				out.WriteString(`\[`)
				continue
			}
			class := segment[index+1 : index+1+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			out.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			index += end + 1
		default:
			out.WriteString(regexp.QuoteMeta(string(character)))
		}
	}
	return out.String()
}

// searchCWD is the shell-reported working directory of the scope's terminal, when it belongs to the scope's session.
func (r *Registry) searchCWD(ctx context.Context, scope Scope) string {
	if scope.TabID == "" {
		return ""
	}
	detail := r.tabDetail(ctx, scope.TabID)
	if detail.SessionID != "" && detail.SessionID != scope.SessionID {
		return ""
	}
	return strings.TrimSpace(detail.CWD)
}

func searchPathAbsolute(value string) bool {
	if value == "~" || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) || strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 && value[1] == ':' && (value[2] == '\\' || value[2] == '/') &&
		(value[0] >= 'A' && value[0] <= 'Z' || value[0] >= 'a' && value[0] <= 'z')
}

// resolveSearchPath applies the default directory: an empty path is the terminal's directory, else home ("~"), and a
// relative path is taken relative to the terminal's directory when it is known.
func resolveSearchPath(requested, cwd string) string {
	if requested == "" {
		if cwd != "" {
			return cwd
		}
		return "~"
	}
	if searchPathAbsolute(requested) || cwd == "" {
		return requested
	}
	separator := "/"
	if strings.Contains(cwd, `\`) && !strings.Contains(cwd, "/") {
		separator = `\`
	}
	return strings.TrimRight(cwd, `/\`) + separator + strings.TrimPrefix(strings.TrimPrefix(requested, "./"), `.\`)
}

func (r *Registry) findFiles(ctx context.Context, scope Scope, raw []byte) Output {
	var args FindFilesArgs
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	request, failure := normalizeFindArgs(args)
	if failure != nil {
		return *failure
	}
	return r.runSearch(ctx, scope, request)
}

func (r *Registry) grepFiles(ctx context.Context, scope Scope, raw []byte) Output {
	var args GrepFilesArgs
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	request, failure := normalizeGrepArgs(args)
	if failure != nil {
		return *failure
	}
	return r.runSearch(ctx, scope, request)
}

// searchFilesAlias maps a search_files call: by=name to find_files, by=content to grep_files in content mode (the
// old tool returned matching lines). Without by, a pattern that looks like a glob searches names.
func (r *Registry) searchFilesAlias(ctx context.Context, scope Scope, raw []byte) Output {
	var args SearchFilesArgs
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	by := strings.TrimSpace(args.By)
	if by == "" {
		by = "content"
		if looksLikeGlob(args.Pattern) {
			by = "name"
		}
	}
	var request searchRequest
	var failure *Output
	switch by {
	case "name":
		request, failure = normalizeFindArgs(FindFilesArgs{Pattern: args.Pattern, Path: args.Path})
	case "content":
		request, failure = normalizeGrepArgs(GrepFilesArgs{Pattern: args.Pattern, Path: args.Path, OutputMode: searchOutputContent})
	default:
		output := searchInvalid("by 必须为 name 或 content（可以省略）；search_files 已弃用，请改用 find_files 或 grep_files",
			"by must be name or content (or omitted); search_files is deprecated, use find_files or grep_files instead")
		return output
	}
	if failure != nil {
		return *failure
	}
	request.alias = by
	return r.runSearch(ctx, scope, request)
}

// looksLikeGlob tells a file glob from a regular expression for a search_files call without by. Regex-only syntax
// (escapes, groups, alternation, anchors, ".*") means content; glob wildcards or a path means name; plain text is a
// content search.
func looksLikeGlob(pattern string) bool {
	if strings.ContainsAny(pattern, `\()|+^$`) || strings.Contains(pattern, ".*") || strings.Contains(pattern, ".+") {
		return false
	}
	return strings.ContainsAny(pattern, "*?[/{")
}

func (r *Registry) runSearch(ctx context.Context, scope Scope, request searchRequest) Output {
	transport, err := r.transport(ctx, scope)
	if err != nil {
		return Fail(err)
	}
	request.path = resolveSearchPath(request.path, r.searchCWD(ctx, scope))
	host, err := r.searchHost(ctx, scope.SessionID, transport)
	if err != nil {
		return Fail(err)
	}
	var output Output
	switch host.platform {
	case searchPlatformPOSIX:
		output = runPOSIXSearch(ctx, transport, host, request)
	case searchPlatformWindows:
		output = runPowerShellSearch(ctx, transport, request)
	default:
		output = r.walkSearch(ctx, scope, request)
	}
	if request.alias != "" && output.ModelText != "" {
		output.ModelText += fmt.Sprintf("\n[search_files is deprecated and was run as %s; call %s directly next time]", request.tool(), request.tool())
	}
	return output
}

// searchRecord is one result entry: a file (find_files, files_with_matches), a count row, or a content line.
type searchRecord struct {
	path    string
	line    int
	match   bool
	text    string
	count   int
	mtime   float64
	hasTime bool
}

// searchResult is what a backend found, before filtering, ordering and paging.
type searchResult struct {
	backend string
	root    string
	// file is set when path named one file rather than a directory.
	file    bool
	records []searchRecord
	// newestFirst means the backend already ordered the records by modification time.
	newestFirst bool
	// incomplete names why records may be missing (output limit, timeout, walk limits), in both wordings.
	incomplete, incompleteModel []string
	// skipped describes paths that could not be searched, in both wordings.
	skipped, skippedModel []string
}

func searchSkipped(name string, caseInsensitive bool) bool {
	for _, skip := range searchSkipDirs {
		if name == skip || caseInsensitive && strings.EqualFold(name, skip) {
			return true
		}
	}
	return false
}

// finish filters, orders, pages and renders a result. Filtering happens here for every backend, so a glob, a type
// and the skipped directories mean the same thing whichever program produced the records.
func (q searchRequest) finish(result searchResult, caseInsensitivePaths bool) Output {
	names, types := q.names, q.types
	if caseInsensitivePaths {
		names, types = names.lowered(), types.lowered()
	}
	kept := result.records[:0]
	for _, record := range result.records {
		record.path = strings.TrimPrefix(record.path, "./")
		if record.path == "" {
			continue
		}
		matchPath := record.path
		if result.file {
			// One file was searched; the result note names its full path.
			record.path = path.Base(strings.ReplaceAll(record.path, `\`, "/"))
			matchPath = record.path
		} else if !q.includeIgnored && hasSkippedSegment(record.path, caseInsensitivePaths) {
			continue
		}
		if caseInsensitivePaths {
			matchPath = strings.ToLower(matchPath)
		}
		if !names.match(matchPath) || !types.match(matchPath) {
			continue
		}
		if q.mode == searchOutputCount && record.count == 0 {
			continue
		}
		kept = append(kept, record)
	}
	records := kept
	switch {
	case q.find && result.newestFirst:
	case q.find:
		sort.SliceStable(records, func(i, j int) bool {
			if records[i].hasTime && records[j].hasTime && records[i].mtime != records[j].mtime {
				return records[i].mtime > records[j].mtime
			}
			return records[i].path < records[j].path
		})
		result.newestFirst = len(records) > 0 && records[0].hasTime
	default:
		sort.SliceStable(records, func(i, j int) bool { return records[i].path < records[j].path })
	}
	if q.mode == searchOutputContent {
		records = dedupeContent(records)
	}
	total := len(records)
	start := min(q.offset, total)
	end := min(start+q.headLimit, total)
	page := records[start:end]

	var body strings.Builder
	for index, record := range page {
		if q.context > 0 && index > 0 {
			previous := page[index-1]
			if previous.path != record.path || record.line > previous.line+1 {
				body.WriteString("--\n")
			}
		}
		body.WriteString(q.renderRecord(record))
		body.WriteByte('\n')
	}
	text := strings.TrimSuffix(body.String(), "\n")
	model := text
	if total == 0 {
		text, model = "无匹配", "no matches"
	} else if start >= total {
		text = fmt.Sprintf("offset=%d 超出结果数 %d", q.offset, total)
		model = fmt.Sprintf("offset=%d is past the %d results", q.offset, total)
	}
	unit, unitModel := "条", "results"
	if q.mode == searchOutputContent {
		unit, unitModel = "行", "lines"
	}
	var notes, modelNotes []string
	incomplete := len(result.incomplete) > 0
	if end < total {
		notes = append(notes, fmt.Sprintf("[已显示第 %d-%d %s，共 %d %s；继续请传 offset=%d]", start+1, end, unit, total, unit, end))
		modelNotes = append(modelNotes, fmt.Sprintf("[showing %s %d-%d of %d; to see more, call %s again with offset=%d]", unitModel, start+1, end, total, q.tool(), end))
	} else if start > 0 && start < total {
		notes = append(notes, fmt.Sprintf("[已显示第 %d-%d %s，共 %d %s]", start+1, end, unit, total, unit))
		modelNotes = append(modelNotes, fmt.Sprintf("[showing %s %d-%d of %d]", unitModel, start+1, end, total))
	}
	notes = append(notes, result.incomplete...)
	modelNotes = append(modelNotes, result.incompleteModel...)
	notes = append(notes, result.skipped...)
	modelNotes = append(modelNotes, result.skippedModel...)
	where, whereModel := result.root, result.root
	if where == "" {
		where, whereModel = q.path, q.path
	}
	order, orderModel := "", ""
	if q.find && result.newestFirst {
		order, orderModel = "；按修改时间从新到旧", "; newest first"
	}
	if result.file {
		notes = append(notes, fmt.Sprintf("[文件：%s；后端：%s]", where, result.backend))
		modelNotes = append(modelNotes, fmt.Sprintf("[searched file %s with %s]", whereModel, result.backend))
	} else {
		notes = append(notes, fmt.Sprintf("[目录：%s；路径相对于该目录；后端：%s%s]", where, result.backend, order))
		modelNotes = append(modelNotes, fmt.Sprintf("[searched %s with %s; paths are relative to it%s]", whereModel, result.backend, orderModel))
	}
	text += "\n" + strings.Join(notes, "\n")
	model += "\n" + strings.Join(modelNotes, "\n")
	text, cappedModel, cut := capHeadTail(text)
	if cut {
		model = cappedModel
	}
	return Output{OK: true, Text: text, ModelText: model, Truncated: end < total || incomplete || cut}
}

// lowered is the matcher for case-insensitive file systems, applied to a lowercased path.
func (m globMatcher) lowered() globMatcher {
	if m.empty() {
		return m
	}
	lowered, err := compileGlob(strings.ToLower("{" + strings.Join(m.expanded, ",") + "}"))
	if err != nil {
		return m
	}
	return lowered
}

func hasSkippedSegment(relative string, caseInsensitive bool) bool {
	segments := strings.Split(strings.ReplaceAll(relative, `\`, "/"), "/")
	for _, segment := range segments[:len(segments)-1] {
		if searchSkipped(segment, caseInsensitive) {
			return true
		}
	}
	return false
}

// dedupeContent drops repeated lines, which overlapping context windows of some backends print twice.
func dedupeContent(records []searchRecord) []searchRecord {
	kept := records[:0]
	for index, record := range records {
		if index > 0 {
			previous := kept[len(kept)-1]
			if previous.path == record.path && previous.line == record.line && record.line != 0 {
				if record.match && !previous.match {
					kept[len(kept)-1] = record
				}
				continue
			}
		}
		kept = append(kept, record)
	}
	return kept
}

func (q searchRequest) renderRecord(record searchRecord) string {
	switch {
	case q.find || q.mode == searchOutputFiles:
		return record.path
	case q.mode == searchOutputCount:
		return record.path + ":" + strconv.Itoa(record.count)
	}
	separator := "-"
	if record.match {
		separator = ":"
	}
	text := clipSearchLine(record.text)
	if q.lineNumbers && record.line > 0 {
		return record.path + separator + strconv.Itoa(record.line) + separator + text
	}
	return record.path + separator + text
}

func clipSearchLine(text string) string {
	text = strings.TrimSuffix(text, "\r")
	runes := []rune(text)
	if len(runes) <= searchMaxLineRunes {
		return text
	}
	return string(runes[:searchMaxLineRunes]) + fmt.Sprintf("…[+%d]", len(runes)-searchMaxLineRunes)
}

// searchErrorLines are the backend's complaints about paths it could not read; the first one is quoted.
func searchErrorNotes(stderr string) ([]string, []string) {
	var lines []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "No files were searched") {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return nil, nil
	}
	first := clipSearchLine(lines[0])
	if len([]rune(first)) > 200 {
		first = string([]rune(first)[:200]) + "…"
	}
	return []string{fmt.Sprintf("[已跳过：%d 条路径无法搜索（如权限不足），首条：%s]", len(lines), first)},
		[]string{fmt.Sprintf("[skipped: %d paths could not be searched (for example permission denied); first: %s]", len(lines), first)}
}

// pathErrorsOnly reports whether every stderr line is a per-path error rather than a failed search (bad regex,
// unknown option).
func pathErrorsOnly(stderr string) bool {
	found := false
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "No files were searched") {
			continue
		}
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "permission denied") && !strings.Contains(lower, "no such file") &&
			!strings.Contains(lower, "operation not permitted") && !strings.Contains(lower, "input/output error") &&
			!strings.Contains(lower, "access is denied") && !strings.Contains(lower, "is a directory") {
			return false
		}
		found = true
	}
	return found
}

func searchLimitNotes(truncated, timedOut bool) ([]string, []string) {
	switch {
	case timedOut:
		return []string{fmt.Sprintf("[搜索超时（%d 秒），以上为部分结果；请缩小 path 或加 glob/type]", int(searchExecTimeout/time.Second))},
			[]string{fmt.Sprintf("[search timed out after %ds; the results above are partial; narrow path or add glob/type]", int(searchExecTimeout/time.Second))}
	case truncated:
		return []string{"[输出达到上限，结果不完整；请缩小 path 或加 glob/type]"},
			[]string{"[the search output hit its size limit, so results are incomplete; narrow path or add glob/type]"}
	}
	return nil, nil
}

// execSearch runs one generated search command and reports whether its deadline cut it short.
func execSearch(ctx context.Context, transport base.Transport, command string) (base.ExecResult, bool, error) {
	searchContext, cancel := context.WithTimeout(ctx, searchExecTimeout)
	defer cancel()
	result, err := transport.Exec(searchContext, command, base.ExecOptions{
		Timeout:            searchExecTimeout,
		Limits:             base.OutputLimits{Stdout: searchStdoutLimit, Stderr: searchStderrLimit},
		ExpectedGeneration: transport.Generation(),
	})
	if err != nil {
		if ctx.Err() != nil {
			return result, false, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(searchContext.Err(), context.DeadlineExceeded) {
			return result, true, nil
		}
		return result, false, err
	}
	return result, false, nil
}

// searchHeader is what the generated scripts print before the backend's own output.
type searchHeader struct {
	shell   bool
	root    string
	file    bool
	notDir  bool
	missing bool
	backend string
	failure string
}

// splitSearchOutput separates the sentinel lines a generated script prints first from the backend output. Anything
// a host's startup files print before the script's first line is dropped.
func splitSearchOutput(stdout, shellSentinel string) (searchHeader, string) {
	var header searchHeader
	rest := stdout
	if start := strings.Index(stdout, searchSentinel+" "+shellSentinel); start > 0 {
		rest = stdout[start:]
	}
	for rest != "" {
		line, remainder, _ := strings.Cut(rest, "\n")
		line = strings.TrimSuffix(line, "\r")
		value, ok := strings.CutPrefix(line, searchSentinel+" ")
		if !ok {
			break
		}
		rest = remainder
		key, argument, _ := strings.Cut(value, " ")
		switch key {
		case shellSentinel:
			header.shell = true
		case "root":
			header.root = argument
		case "file":
			header.file = true
		case "notdir":
			header.notDir = true
		case "missing":
			header.missing = true
		case "failure":
			header.failure = argument
		case "backend":
			header.backend = argument
			return header, rest
		}
	}
	return header, rest
}

// parseSearchRecords reads backend output. NUL-separated output (rg --null, grep --null, the PowerShell script)
// is exact; without it a content line is split at the first ":N:" or "-N-".
// timed means find_files records carry "mtime<TAB>" before the path.
func parseSearchRecords(request searchRequest, data string, nul, timed, truncated bool) []searchRecord {
	if truncated {
		if cut := strings.LastIndexByte(data, '\n'); cut >= 0 {
			data = data[:cut+1]
		} else {
			data = ""
		}
	}
	var records []searchRecord
	if request.find || request.mode == searchOutputFiles {
		for _, field := range strings.FieldsFunc(data, func(r rune) bool { return r == 0 || r == '\n' }) {
			field = strings.TrimSuffix(field, "\r")
			if field == "" {
				continue
			}
			record := searchRecord{path: field}
			if request.find && timed {
				if stamp, name, ok := strings.Cut(field, "\t"); ok {
					if seconds, err := strconv.ParseFloat(stamp, 64); err == nil {
						record = searchRecord{path: name, mtime: seconds, hasTime: true}
					}
				}
			}
			records = append(records, record)
		}
		return records
	}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || line == "--" {
			continue
		}
		var name, rest string
		if nul {
			var ok bool
			name, rest, ok = strings.Cut(line, "\x00")
			if !ok {
				continue
			}
		}
		if request.mode == searchOutputCount {
			if !nul {
				cut := strings.LastIndexByte(line, ':')
				if cut < 0 {
					continue
				}
				name, rest = line[:cut], line[cut+1:]
			}
			count, err := strconv.Atoi(strings.TrimSpace(rest))
			if err != nil {
				continue
			}
			records = append(records, searchRecord{path: name, count: count})
			continue
		}
		var record searchRecord
		var ok bool
		if nul {
			record, ok = parseContentRest(rest)
			record.path = name
		} else {
			record, ok = parseContentLine(line)
		}
		if ok {
			records = append(records, record)
		}
	}
	return records
}

// parseContentRest reads "N:text" (a match) or "N-text" (context).
func parseContentRest(rest string) (searchRecord, bool) {
	digits := 0
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits >= len(rest) || rest[digits] != ':' && rest[digits] != '-' {
		return searchRecord{}, false
	}
	line, _ := strconv.Atoi(rest[:digits])
	return searchRecord{line: line, match: rest[digits] == ':', text: rest[digits+1:]}, true
}

var (
	contentMatchLine   = regexp.MustCompile(`^(.+?):([0-9]+):`)
	contentContextLine = regexp.MustCompile(`^(.+?)-([0-9]+)-`)
)

func parseContentLine(line string) (searchRecord, bool) {
	for _, expression := range []*regexp.Regexp{contentMatchLine, contentContextLine} {
		if found := expression.FindStringSubmatchIndex(line); found != nil {
			number, _ := strconv.Atoi(line[found[4]:found[5]])
			return searchRecord{path: line[found[2]:found[3]], line: number, match: expression == contentMatchLine, text: line[found[1]:]}, true
		}
	}
	return searchRecord{}, false
}

// DisplaySearchCall renders a search call for the tool bubble: the pattern, where it searches and the filters that
// narrow it, instead of raw JSON.
func DisplaySearchCall(call Call) string {
	var fields struct {
		Pattern         string `json:"pattern"`
		Path            string `json:"path"`
		By              string `json:"by"`
		Glob            string `json:"glob"`
		Type            string `json:"type"`
		CaseInsensitive bool   `json:"case_insensitive"`
		OutputMode      string `json:"output_mode"`
		Context         int    `json:"context"`
		Multiline       bool   `json:"multiline"`
		IncludeIgnored  bool   `json:"include_ignored"`
		Offset          int    `json:"offset"`
	}
	_ = json.Unmarshal(call.Args, &fields)
	display := call.Name
	if call.Name == SearchFilesAlias && fields.By != "" {
		display += " " + fields.By
	}
	display += " " + displayValue(fields.Pattern)
	if fields.Path != "" {
		display += " in " + fields.Path
	}
	var filters []string
	if fields.Glob != "" {
		filters = append(filters, "glob "+fields.Glob)
	}
	if fields.Type != "" {
		filters = append(filters, "type "+fields.Type)
	}
	if fields.CaseInsensitive {
		filters = append(filters, "-i")
	}
	if fields.OutputMode != "" && fields.OutputMode != searchOutputFiles {
		filters = append(filters, fields.OutputMode)
	}
	if fields.Context > 0 {
		filters = append(filters, fmt.Sprintf("-C %d", fields.Context))
	}
	if fields.Multiline {
		filters = append(filters, "multiline")
	}
	if fields.IncludeIgnored {
		filters = append(filters, "include_ignored")
	}
	if fields.Offset > 0 {
		filters = append(filters, fmt.Sprintf("offset %d", fields.Offset))
	}
	if len(filters) > 0 {
		display += " (" + strings.Join(filters, ", ") + ")"
	}
	return compactDisplay(display)
}
