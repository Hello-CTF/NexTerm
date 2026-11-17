package tools

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// The file-API walk is the fallback for hosts without a usable exec channel. It lists and reads every file over the
// file transport, so it keeps tight bounds.
var searchWalkTimeout = 30 * time.Second

const (
	searchWalkMaxDepth        = 6
	searchWalkMaxEntries      = 10000
	searchWalkMaxContentBytes = 1 << 20
)

type fileSearch struct {
	files   base.FileSystem
	request searchRequest
	regex   *regexp.Regexp
	records []searchRecord
	visited int
	// limited is set when the depth or entry bound stopped the walk.
	limited bool
	// Files and directories the search could not look into, by reason; reported instead of silently dropped.
	skippedLarge, skippedBinary, skippedUnreadable, skippedDirs int
}

func (r *Registry) walkSearch(ctx context.Context, scope Scope, request searchRequest) Output {
	if request.multiline {
		return searchFailure("multiline=true 需要主机上安装 ripgrep (rg)；该会话没有可用的命令通道，只能逐个读取文件搜索，请去掉 multiline 改用单行正则",
			"multiline=true needs ripgrep (rg); this session has no usable command channel and is searched file by file, so drop multiline and use a single-line regex")
	}
	files, err := r.fileSystem(ctx, scope)
	if err != nil {
		return Fail(err)
	}
	root := request.path
	if root == "~" || strings.HasPrefix(root, "~/") {
		provider, ok := files.(base.RootsProvider)
		if !ok {
			return searchFailure("无法确定主目录，请传绝对路径 path", "cannot resolve the home directory; pass an absolute path")
		}
		roots, err := provider.Roots(ctx)
		if err != nil || roots.HomePath == "" {
			return searchFailure("无法确定主目录，请传绝对路径 path", "cannot resolve the home directory; pass an absolute path")
		}
		root = strings.TrimRight(roots.HomePath, "/") + strings.TrimPrefix(root, "~")
	}
	search := fileSearch{files: files, request: request}
	if !request.find {
		expression := request.pattern
		if request.ignoreCase {
			expression = "(?i)" + expression
		}
		search.regex, err = regexp.Compile(expression)
		if err != nil {
			return searchFailure(fmt.Sprintf("内容正则无效: %v", err), fmt.Sprintf("invalid content regex: %v", err))
		}
	}
	searchContext, cancel := context.WithTimeout(ctx, searchWalkTimeout)
	defer cancel()
	single := false
	walkErr := search.walk(searchContext, root, "", 0)
	if walkErr != nil && ctx.Err() == nil && searchContext.Err() == nil && !request.find {
		// The root may be one file rather than a directory.
		if content, readErr := files.ReadFile(searchContext, root, searchWalkMaxContentBytes); readErr == nil {
			single = true
			walkErr = nil
			search.searchContent(path.Base(root), content)
		}
	}
	timedOut := false
	if walkErr != nil {
		if ctx.Err() != nil {
			return Fail(ctx.Err())
		}
		if !errors.Is(searchContext.Err(), context.DeadlineExceeded) {
			return Fail(walkErr)
		}
		// The search's own deadline: what was found so far is still an answer, flagged as partial.
		timedOut = true
	}
	found := searchResult{backend: "file API walk", root: root, file: single, records: search.records}
	found.incomplete, found.incompleteModel = search.limitNotes(timedOut)
	found.skipped, found.skippedModel = search.skippedNotes()
	return request.finish(found, false)
}

// walk searches directory, whose path relative to the search root is relative ("" for the root). Only a failure to
// list the root, or the context ending, stops the search; a subdirectory that cannot be listed is counted and skipped.
func (s *fileSearch) walk(ctx context.Context, directory, relative string, depth int) error {
	if depth > searchWalkMaxDepth || s.visited > searchWalkMaxEntries {
		s.limited = true
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := s.files.List(ctx, directory)
	if err != nil {
		if depth == 0 || ctx.Err() != nil {
			return err
		}
		s.skippedDirs++
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.visited++
		if s.visited > searchWalkMaxEntries {
			s.limited = true
			return nil
		}
		entryPath := entry.Path
		if entryPath == "" {
			entryPath = strings.TrimRight(directory, "/") + "/" + entry.Name
		}
		entryRelative := entry.Name
		if relative != "" {
			entryRelative = relative + "/" + entry.Name
		}
		if entry.Kind == base.FileDirectory {
			if !s.request.includeIgnored && searchSkipped(entry.Name, false) {
				continue
			}
			if err := s.walk(ctx, entryPath, entryRelative, depth+1); err != nil {
				return err
			}
			continue
		}
		if entry.Kind != base.FileFile || !s.request.names.match(entryRelative) || !s.request.types.match(entryRelative) {
			continue
		}
		if s.request.find {
			record := searchRecord{path: entryRelative}
			if !entry.ModTime.IsZero() {
				record.mtime, record.hasTime = float64(entry.ModTime.UnixNano())/1e9, true
			}
			s.records = append(s.records, record)
			continue
		}
		if entry.Size > searchWalkMaxContentBytes {
			s.skippedLarge++
			continue
		}
		content, err := s.files.ReadFile(ctx, entryPath, searchWalkMaxContentBytes)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			s.skippedUnreadable++
			continue
		}
		s.searchContent(entryRelative, content)
	}
	return nil
}

func (s *fileSearch) searchContent(name string, content []byte) {
	if !utf8.Valid(content) {
		s.skippedBinary++
		return
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	matched := make([]bool, len(lines))
	count := 0
	for index, line := range lines {
		if s.regex.MatchString(strings.TrimSuffix(line, "\r")) {
			matched[index] = true
			count++
		}
	}
	if count == 0 {
		return
	}
	switch s.request.mode {
	case searchOutputFiles:
		s.records = append(s.records, searchRecord{path: name})
		return
	case searchOutputCount:
		s.records = append(s.records, searchRecord{path: name, count: count})
		return
	}
	window := s.request.context
	shown := make([]bool, len(lines))
	for index, isMatch := range matched {
		if !isMatch {
			continue
		}
		for line := max(0, index-window); line <= min(len(lines)-1, index+window); line++ {
			shown[line] = true
		}
	}
	for index, line := range lines {
		if shown[index] {
			s.records = append(s.records, searchRecord{path: name, line: index + 1, match: matched[index], text: line})
		}
	}
}

// limitNotes describes, in display and model wording, why the results may be incomplete.
func (s *fileSearch) limitNotes(timedOut bool) ([]string, []string) {
	switch {
	case timedOut:
		return []string{fmt.Sprintf("[搜索超时（%d 秒），以上为部分结果]", int(searchWalkTimeout/time.Second))},
			[]string{fmt.Sprintf("[search timed out after %ds; the results above are partial]", int(searchWalkTimeout/time.Second))}
	case s.limited:
		return []string{fmt.Sprintf("[结果不完整：逐个读取文件的搜索最多进入 %d 层目录、检查 %d 个条目]", searchWalkMaxDepth, searchWalkMaxEntries)},
			[]string{fmt.Sprintf("[results incomplete: the file-by-file search stops at %d directory levels and %d entries]", searchWalkMaxDepth, searchWalkMaxEntries)}
	}
	return nil, nil
}

func (s *fileSearch) skippedNotes() ([]string, []string) {
	var skippedText, skippedModel []string
	add := func(count int, display, english string) {
		if count > 0 {
			skippedText = append(skippedText, fmt.Sprintf(display, count))
			skippedModel = append(skippedModel, fmt.Sprintf(english, count))
		}
	}
	add(s.skippedLarge, "%d 个超过 1 MiB 的文件", "%d files over 1 MiB")
	add(s.skippedBinary, "%d 个非 UTF-8 文件", "%d non-UTF-8 files")
	add(s.skippedUnreadable, "%d 个无法读取的文件", "%d unreadable files")
	add(s.skippedDirs, "%d 个无法列出的目录", "%d directories that could not be listed")
	if len(skippedText) == 0 {
		return nil, nil
	}
	return []string{"[已跳过：" + strings.Join(skippedText, "，") + "]"}, []string{"[skipped: " + strings.Join(skippedModel, ", ") + "]"}
}

// nameGlob is one glob alternative. A pattern without "/" matches the file's base name, as path.Match does; a
// pattern with "/" matches the path relative to the search root, where a "**" segment matches any number of
// directories, including none.
type nameGlob struct {
	pattern  string
	segments []string
}

func compileNameGlob(pattern string) (nameGlob, error) {
	// find, ripgrep and PowerShell negate a class with [!...]; path.Match spells it [^...].
	pattern = strings.ReplaceAll(pattern, "[!", "[^")
	if !strings.Contains(pattern, "/") {
		if _, err := path.Match(pattern, "probe"); err != nil {
			return nameGlob{}, err
		}
		return nameGlob{pattern: pattern}, nil
	}
	segments := strings.Split(path.Clean(pattern), "/")
	for _, segment := range segments {
		if segment == "**" {
			continue
		}
		if _, err := path.Match(segment, "probe"); err != nil {
			return nameGlob{}, err
		}
	}
	return nameGlob{pattern: pattern, segments: segments}, nil
}

func (g nameGlob) match(name, relative string) bool {
	if g.segments == nil {
		matched, _ := path.Match(g.pattern, name)
		return matched
	}
	return matchGlobSegments(g.segments, strings.Split(relative, "/"))
}

func matchGlobSegments(pattern, name []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			for len(pattern) > 0 && pattern[0] == "**" {
				pattern = pattern[1:]
			}
			if len(pattern) == 0 {
				return true
			}
			for skip := 0; skip <= len(name); skip++ {
				if matchGlobSegments(pattern, name[skip:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if matched, _ := path.Match(pattern[0], name[0]); !matched {
			return false
		}
		pattern, name = pattern[1:], name[1:]
	}
	return len(name) == 0
}
