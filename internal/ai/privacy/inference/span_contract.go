package inference

import (
	_ "embed"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// Opt-in existing-ip-and-dynamic-url-v1 span contract for AstrLink Guard
// models (AstrLink Guard tools/span-worker/refine.rs, sha256
// fd7f5cfb3f5198e7a08d82828294fa8c25001fae5091a1dce71283be34c9cd64). It only
// narrows existing ip_address spans to the parsed IP literal and splits
// existing dynamic-host URL spans (scheme://user:password@%s[:port][/]) into
// account and common_secret parts. Offsets are UTF-8 bytes; scores are
// inherited from the model span.

const (
	spanContractField = "astrlink_guard_span_contract"
	spanContract      = "existing-ip-and-dynamic-url-v1"
)

// span_patterns.txt is the generated Rust pattern file, byte-identical to its
// upstream. The patterns are written for Rust's regex crate and translated
// to RE2 syntax at load.
//
//go:embed span_patterns.txt
var spanPatternSource string

type spanPatterns struct {
	atom, identifier, scope, port, url, version, word, left, right *regexp.Regexp
	atomChar, scheme, slot, urlPort, placeholder                   *regexp.Regexp
	literalForbidden, urlForbidden, urlLeft                        *regexp.Regexp
}

var loadSpanPatterns = sync.OnceValues(func() (*spanPatterns, error) {
	sources := map[string]string{}
	declaration := regexp.MustCompile(`(?m)^pub const ([A-Z_]+): &str = r###"(.*)"###;$`)
	for _, m := range declaration.FindAllStringSubmatch(spanPatternSource, -1) {
		sources[m[1]] = m[2]
	}
	var failure error
	compile := func(name string) *regexp.Regexp {
		source, ok := sources[name]
		if !ok {
			source = name
		}
		translated, err := translateRustRegex(source)
		if err == nil {
			var compiled *regexp.Regexp
			if compiled, err = regexp.Compile(translated); err == nil {
				return compiled
			}
		}
		if failure == nil {
			failure = fmt.Errorf("span pattern %.40q: %w", name, err)
		}
		return nil
	}
	p := &spanPatterns{
		atom: compile("ATOM"), identifier: compile("IDENTIFIER"), scope: compile("SCOPE"), port: compile("PORT"),
		url: compile("URL"), version: compile("VERSION"), word: compile("WORD"), left: compile("LEFT"),
		right: compile("RIGHT"), atomChar: compile(`[A-Za-z0-9_.:%~+\-]`), scheme: compile("SCHEME"),
		slot: compile("SLOT"), urlPort: compile("URL_PORT"), placeholder: compile("PLACEHOLDER"),
		literalForbidden: compile("LITERAL_FORBIDDEN"), urlForbidden: compile("URL_FORBIDDEN"),
		urlLeft: compile(`[A-Za-z0-9_+./:@%\-]`),
	}
	if len(sources) != 15 && failure == nil {
		failure = fmt.Errorf("span patterns: found %d declarations", len(sources))
	}
	return p, failure
})

// rustWhiteSpace is the Rust regex crate's Unicode \s (White_Space). RE2's \s
// is ASCII-only.
const rustWhiteSpace = `\x{9}-\x{d}\x{20}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

// translateRustRegex rewrites the Rust regex constructs used by the span
// contract into equivalent RE2 syntax: nested (union) character classes are
// flattened, \s becomes Unicode White_Space and \uXXXX becomes \x{XXXX}.
// Anything else that differs between the engines is rejected.
func translateRustRegex(source string) (string, error) {
	var out strings.Builder
	depth := 0
	for i := 0; i < len(source); {
		c := source[i]
		switch {
		case c == '\\':
			if i+1 >= len(source) {
				return "", fmt.Errorf("trailing escape")
			}
			e := source[i+1]
			switch {
			case e == 's':
				if depth == 0 {
					out.WriteString("[" + rustWhiteSpace + "]")
				} else {
					out.WriteString(rustWhiteSpace)
				}
				i += 2
			case e == 'u':
				if i+6 > len(source) {
					return "", fmt.Errorf("short \\u escape")
				}
				hex := source[i+2 : i+6]
				for _, h := range hex {
					if !strings.ContainsRune("0123456789abcdefABCDEF", h) {
						return "", fmt.Errorf("invalid \\u escape")
					}
				}
				out.WriteString(`\x{` + hex + `}`)
				i += 6
			case e == 'A' || e == 'z' || e == 'x':
				out.WriteString(source[i : i+2])
				i += 2
			case e >= 0x80 || (e >= '0' && e <= '9') || (e >= 'a' && e <= 'z') || (e >= 'A' && e <= 'Z'):
				return "", fmt.Errorf("unsupported escape \\%c", e)
			default:
				out.WriteString(source[i : i+2])
				i += 2
			}
		case depth > 0 && c == '[':
			if i+1 < len(source) && (source[i+1] == '^' || source[i+1] == ':') {
				return "", fmt.Errorf("unsupported nested class")
			}
			depth++
			i++
		case depth > 0 && c == ']':
			depth--
			if depth == 0 {
				out.WriteByte(']')
			} else if i+1 < len(source) && source[i+1] == '-' {
				return "", fmt.Errorf("range after nested class")
			}
			i++
		case depth > 0 && (strings.HasPrefix(source[i:], "&&") || strings.HasPrefix(source[i:], "--") || strings.HasPrefix(source[i:], "~~")):
			return "", fmt.Errorf("unsupported class set operation")
		case c == '[':
			depth = 1
			out.WriteByte('[')
			i++
			if i < len(source) && source[i] == '^' {
				out.WriteByte('^')
				i++
			}
			if i < len(source) && source[i] == ']' {
				out.WriteString(`\]`)
				i++
			}
		default:
			out.WriteByte(c)
			i++
		}
	}
	if depth != 0 {
		return "", fmt.Errorf("unterminated class")
	}
	return out.String(), nil
}

func spanContractRefine(text string, spans []Span) ([]Span, error) {
	for _, span := range spans {
		if span.Start >= span.End || span.End > len(text) || !charBoundary(text, span.Start) || !charBoundary(text, span.End) {
			return nil, errCode("invalid_span_offsets")
		}
	}
	p, err := loadSpanPatterns()
	if err != nil {
		return nil, err
	}
	return refineURLs(p, text, refineIPs(p, text, spans)), nil
}

func matchesRune(pattern *regexp.Regexp, r rune) bool { return pattern.MatchString(string(r)) }

func overlap(start, end, otherStart, otherEnd int) bool {
	return max(start, otherStart) < min(end, otherEnd)
}

// interval is a sorted (start, end, id) index with running maximum ends.
type interval struct {
	rows    [][3]int
	maxEnds []int
}

func newInterval(rows [][3]int) *interval {
	slices.SortFunc(rows, func(a, b [3]int) int {
		for i := range 3 {
			if a[i] != b[i] {
				return a[i] - b[i]
			}
		}
		return 0
	})
	maxEnds := make([]int, len(rows))
	maximum := 0
	for i, row := range rows {
		maximum = max(maximum, row[1])
		maxEnds[i] = maximum
	}
	return &interval{rows, maxEnds}
}

func spanInterval(spans []Span) *interval {
	rows := make([][3]int, len(spans))
	for i, span := range spans {
		rows[i] = [3]int{span.Start, span.End, i}
	}
	return newInterval(rows)
}

func (x *interval) overlapping(start, end int) []int {
	first := sort.Search(len(x.maxEnds), func(i int) bool { return x.maxEnds[i] > start })
	stop := sort.Search(len(x.rows), func(i int) bool { return x.rows[i][0] >= end })
	var ids []int
	for _, row := range x.rows[min(first, stop):stop] {
		if overlap(start, end, row[0], row[1]) {
			ids = append(ids, row[2])
		}
	}
	return ids
}

func containsOther(ids []int, self int) bool {
	for _, id := range ids {
		if id != self {
			return true
		}
	}
	return false
}

// parseIP accepts exactly what Rust's IpAddr::from_str accepts: dotted-quad
// IPv4 without leading zeros and IPv6 without a zone.
func parseIP(value string) (netip.Addr, bool) {
	if strings.ContainsRune(value, '%') {
		return netip.Addr{}, false
	}
	address, err := netip.ParseAddr(value)
	return address, err == nil
}

func validIP(p *spanPatterns, value string) bool {
	address := value
	if before, scope, found := strings.Cut(value, "%"); found {
		if !strings.Contains(before, ":") || !p.scope.MatchString(scope) {
			return false
		}
		address = before
	}
	_, ok := parseIP(address)
	return ok
}

func parseIPv4(value string) bool {
	address, ok := parseIP(value)
	return ok && address.Is4()
}

func isASCIIHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// parseAtom returns the byte range of the IP literal inside a candidate atom.
func parseAtom(p *spanPatterns, atom string) (int, int, bool) {
	if utf8.RuneCountInString(atom) > 256 {
		return 0, 0, false
	}
	start := 0
	if field, _, found := strings.Cut(atom, ":"); found && p.identifier.MatchString(field) && strings.ContainsFunc(field, func(r rune) bool { return !isASCIIHex(r) }) {
		start = len(field) + 1
	}
	value := atom[start:]
	if value == "" {
		return 0, 0, false
	}
	if validIP(p, value) {
		return start, len(atom), true
	}
	end := len(atom)
	if strings.HasSuffix(value, ".") && !strings.HasSuffix(value, "..") {
		value = value[:len(value)-1]
		end--
		if validIP(p, value) {
			return start, end, true
		}
	}
	if host, port, found := strings.Cut(value, ":"); found && (port == "" || p.port.MatchString(port)) && parseIPv4(host) {
		return start, start + len(host), true
	}
	if strings.HasSuffix(value, ":") && !strings.HasSuffix(value, "::") && validIP(p, value[:len(value)-1]) {
		return start, end - 1, true
	}
	return 0, 0, false
}

// versionBefore reports a version field within the 80 characters before end.
func versionBefore(p *spanPatterns, text string, end int) bool {
	start := end
	for count := 0; count < 80 && start > 0; count++ {
		_, size := utf8.DecodeLastRuneInString(text[:start])
		start -= size
	}
	prefix := text[start:end]
	for _, found := range p.version.FindAllStringIndex(prefix, -1) {
		if strings.HasPrefix(prefix[found[0]:], "版") || found[0] == 0 {
			return true
		}
		previous, _ := utf8.DecodeLastRuneInString(prefix[:found[0]])
		if !matchesRune(p.word, previous) {
			return true
		}
	}
	return false
}

func refineIPs(p *spanPatterns, text string, original []Span) []Span {
	hasIP := false
	for _, span := range original {
		hasIP = hasIP || span.Label == "ip_address"
	}
	if !hasIP {
		return slices.Clone(original)
	}
	inputIndex := spanInterval(original)
	var ipRows [][3]int
	for i, span := range original {
		if span.Label == "ip_address" {
			ipRows = append(ipRows, [3]int{span.Start, span.End, i})
		}
	}
	ipIndex := newInterval(ipRows)
	var urlRows [][3]int
	for _, found := range p.url.FindAllStringIndex(text, -1) {
		if !strings.HasPrefix(text[found[0]:found[1]], "//") || found[0] == 0 || text[found[0]-1] != ':' {
			urlRows = append(urlRows, [3]int{found[0], found[1], len(urlRows)})
		}
	}
	urlIndex := newInterval(urlRows)
	var candidates [][2]int
	for _, found := range p.atom.FindAllStringIndex(text, -1) {
		if len(ipIndex.overlapping(found[0], found[1])) == 0 {
			continue
		}
		relativeStart, relativeEnd, ok := parseAtom(p, text[found[0]:found[1]])
		if !ok {
			continue
		}
		start, end := found[0]+relativeStart, found[0]+relativeEnd
		following, size := utf8.DecodeRuneInString(text[found[1]:])
		hasFollowing := size > 0
		if strings.HasSuffix(text[found[0]:found[1]], "}") && hasFollowing && matchesRune(p.atomChar, following) {
			continue
		}
		if strings.Contains(text[start:end], "%") && hasFollowing && strings.ContainsRune(`/@\$%+`, following) {
			continue
		}
		if len(urlIndex.overlapping(start, end)) > 0 || versionBefore(p, text, start) {
			continue
		}
		candidates = append(candidates, [2]int{start, end})
	}
	candidateRows := make([][3]int, len(candidates))
	for i, c := range candidates {
		candidateRows[i] = [3]int{c[0], c[1], i}
	}
	candidateIndex := newInterval(candidateRows)
	proposed := slices.Clone(original)
	for index, span := range original {
		if span.Label != "ip_address" || utf8.RuneCountInString(text[span.Start:span.End]) > 256 {
			continue
		}
		hits := candidateIndex.overlapping(span.Start, span.End)
		if len(hits) != 1 {
			continue
		}
		start, end := candidates[hits[0]][0], candidates[hits[0]][1]
		left, right := "", ""
		if span.Start < start {
			left = text[span.Start:start]
		}
		if span.End > end {
			right = text[end:span.End]
		}
		if !p.left.MatchString(left) || !p.right.MatchString(right) {
			continue
		}
		if containsOther(inputIndex.overlapping(start, end), index) {
			continue
		}
		proposed[index].Start, proposed[index].End = start, end
	}
	proposedIndex := spanInterval(proposed)
	var conflicts []int
	for index, span := range proposed {
		if span != original[index] && containsOther(proposedIndex.overlapping(span.Start, span.End), index) {
			conflicts = append(conflicts, index)
		}
	}
	for _, index := range conflicts {
		proposed[index] = original[index]
	}
	return proposed
}

func categoryCRune(r rune) bool {
	i := sort.Search(len(categoryC), func(i int) bool { return categoryC[i][0] > r })
	return i > 0 && r <= categoryC[i-1][1]
}

func literal(p *spanPatterns, value string) bool {
	return value != "" && !p.slot.MatchString(value) && !p.placeholder.MatchString(value) &&
		!p.literalForbidden.MatchString(value) && !strings.ContainsFunc(value, categoryCRune)
}

type urlPart struct {
	start, end int
	label      string
}

// urlParts splits scheme://user:password@%s[:port][/] into user and password.
func urlParts(p *spanPatterns, value string) ([2]urlPart, bool) {
	scheme := p.scheme.FindStringIndex(value)
	if scheme == nil || scheme[0] != 0 || p.urlForbidden.MatchString(value) {
		return [2]urlPart{}, false
	}
	end := len(value)
	if i := strings.IndexAny(value[scheme[1]:], "/?#"); i >= 0 {
		end = i + scheme[1]
	}
	authority := value[scheme[1]:end]
	if strings.Count(authority, "@") != 1 {
		return [2]urlPart{}, false
	}
	userinfo, hostPort, _ := strings.Cut(authority, "@")
	if strings.Count(userinfo, ":") != 1 {
		return [2]urlPart{}, false
	}
	host, port, hasPort := strings.Cut(hostPort, ":")
	if !p.slot.MatchString(host) || (hasPort && !p.urlPort.MatchString(port)) {
		return [2]urlPart{}, false
	}
	user, password, _ := strings.Cut(userinfo, ":")
	if !literal(p, user) || !literal(p, password) || (value[end:] != "" && value[end:] != "/") {
		return [2]urlPart{}, false
	}
	start := scheme[1]
	return [2]urlPart{
		{start, start + len(user), "account"},
		{start + len(user) + 1, start + len(userinfo), "common_secret"},
	}, true
}

func refineURLs(p *spanPatterns, text string, original []Span) []Span {
	index := spanInterval(original)
	result := make([]Span, 0, len(original))
	for position, span := range original {
		var pieces [2]urlPart
		ok := false
		if span.Label == "url" {
			pieces, ok = urlParts(p, text[span.Start:span.End])
		}
		before, beforeSize := utf8.DecodeLastRuneInString(text[:span.Start])
		safeLeft := beforeSize == 0 || !matchesRune(p.urlLeft, before)
		after, afterSize := utf8.DecodeRuneInString(text[span.End:])
		safeRight := afterSize == 0 || strings.ContainsRune("\n\r\t \"'`),;]}", after)
		isolated := !containsOther(index.overlapping(span.Start, span.End), position)
		if ok && safeLeft && safeRight && isolated {
			for _, piece := range pieces {
				result = append(result, Span{Start: span.Start + piece.start, End: span.Start + piece.end, Label: piece.label, Score: span.Score})
			}
		} else {
			result = append(result, span)
		}
	}
	return result
}
