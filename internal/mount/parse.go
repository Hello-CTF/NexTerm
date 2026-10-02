package mount

import (
	"regexp"
	"sort"
	"strings"
)

var windowsDrivePattern = regexp.MustCompile(`(?i)(?:^|\s)([A-Z]:)\s+`)
var windowsColumnGapPattern = regexp.MustCompile(`\s{2,}`)

func ParseTable(platform, output string) []Entry {
	var entries []Entry
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		var entry Entry
		var ok bool
		if platform == "windows" {
			entry, ok = parseWindowsLine(line)
		} else {
			entry, ok = parseUnixLine(platform, line)
		}
		if ok {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].LocalPoint == entries[j].LocalPoint {
			return entries[i].Remote < entries[j].Remote
		}
		return entries[i].LocalPoint < entries[j].LocalPoint
	})
	return entries
}

func parseWindowsLine(line string) (Entry, bool) {
	if strings.Contains(line, "命令") || strings.Contains(line, "---") || strings.HasPrefix(strings.ToLower(line), "new connections") {
		return Entry{}, false
	}
	match := windowsDrivePattern.FindStringSubmatchIndex(line)
	if match == nil {
		return Entry{}, false
	}
	local := line[match[2]:match[3]]
	remote := strings.TrimSpace(line[match[1]:])
	if gaps := windowsColumnGapPattern.FindAllStringIndex(remote, -1); len(gaps) > 0 {
		last := gaps[len(gaps)-1]
		remote = strings.TrimSpace(remote[:last[0]])
	}
	remote = strings.Trim(remote, "\"")
	if !strings.HasPrefix(remote, `\\`) {
		return Entry{}, false
	}
	return Entry{LocalPoint: local, Remote: remote}, true
}

func parseUnixLine(platform, line string) (Entry, bool) {
	on := strings.Index(line, " on ")
	if on <= 0 {
		return Entry{}, false
	}
	remote := line[:on]
	rest := line[on+4:]
	var local string
	if platform == "darwin" {
		open := strings.LastIndex(rest, " (")
		if open <= 0 {
			return Entry{}, false
		}
		local = rest[:open]
		options := strings.Split(rest[open+2:], ",")
		filesystem := strings.Trim(strings.ToLower(options[0]), "() ")
		if filesystem != "osxfuse" && filesystem != "macfuse" {
			return Entry{}, false
		}
	} else {
		marker := strings.LastIndex(rest, " type ")
		if marker <= 0 {
			return Entry{}, false
		}
		local = rest[:marker]
		fields := strings.Fields(rest[marker+6:])
		if len(fields) == 0 || !strings.EqualFold(fields[0], "fuse.sshfs") {
			return Entry{}, false
		}
	}
	if local == "" || remote == "" {
		return Entry{}, false
	}
	return Entry{LocalPoint: unescapeMount(local), Remote: unescapeMount(remote)}, true
}

func unescapeMount(value string) string {
	var result strings.Builder
	for i := 0; i < len(value); {
		if i+3 < len(value) && value[i] == '\\' && isOctal(value[i+1]) && isOctal(value[i+2]) && isOctal(value[i+3]) {
			decoded := (value[i+1]-'0')*64 + (value[i+2]-'0')*8 + value[i+3] - '0'
			result.WriteByte(decoded)
			i += 4
			continue
		}
		result.WriteByte(value[i])
		i++
	}
	return result.String()
}

func isOctal(value byte) bool {
	return value >= '0' && value <= '7'
}

func isASCIILetter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}
