package services

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
)

const (
	QueryPort    = "port"
	QueryProcess = "process"
	QueryRegex   = "regex"
)

type finderMatcher struct {
	kind    string
	value   string
	port    int
	pattern *regexp.Regexp
}

func newFinderMatcher(query FinderQuery) (*finderMatcher, error) {
	value := strings.TrimSpace(query.Value)
	if value == "" || len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
		return nil, fmt.Errorf("%w: finder query must be one non-empty line of at most 512 bytes", ErrInvalid)
	}
	matcher := &finderMatcher{kind: query.Kind, value: value}
	switch query.Kind {
	case QueryPort:
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("%w: port must be an integer between 1 and 65535", ErrInvalid)
		}
		matcher.port = port
	case QueryProcess:
		matcher.value = strings.ToLower(value)
	case QueryRegex:
		pattern, err := regexp.Compile(value)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid process regex: %v", ErrInvalid, err)
		}
		matcher.pattern = pattern
	default:
		return nil, fmt.Errorf("%w: finder query kind must be port, process, or regex", ErrInvalid)
	}
	return matcher, nil
}

func (m *finderMatcher) matchesText(values ...string) bool {
	for _, value := range values {
		if m.kind == QueryProcess && strings.Contains(strings.ToLower(value), m.value) {
			return true
		}
		if m.kind == QueryRegex && m.pattern.MatchString(value) {
			return true
		}
	}
	return false
}

func ParseSS(output string) []Listener {
	listeners := make([]Listener, 0)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.EqualFold(fields[0], "LISTEN") {
			continue
		}
		address, port, ok := splitAddressPort(fields[3])
		if !ok {
			continue
		}
		processText := strings.Join(fields[5:], " ")
		matches := ssProcessPattern.FindAllStringSubmatch(processText, -1)
		if len(matches) == 0 {
			listeners = append(listeners, Listener{Address: address, Port: port})
			continue
		}
		for _, match := range matches {
			pid, _ := strconv.Atoi(match[2])
			listeners = append(listeners, Listener{Address: address, Port: port, PID: pid, Program: match[1]})
		}
	}
	return listeners
}

var ssProcessPattern = regexp.MustCompile(`"((?:\\.|[^"\\])*)",pid=([0-9]+)`)

func ParseNetstat(output string) []Listener {
	listeners := make([]Listener, 0)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || !strings.HasPrefix(strings.ToLower(fields[0]), "tcp") || !strings.EqualFold(fields[5], "LISTEN") {
			continue
		}
		address, port, ok := splitAddressPort(fields[3])
		if !ok {
			continue
		}
		listener := Listener{Address: address, Port: port}
		if len(fields) > 6 && fields[6] != "-" {
			pidText, program, found := strings.Cut(fields[6], "/")
			if found {
				listener.PID, _ = strconv.Atoi(pidText)
				listener.Program = program
			}
		}
		listeners = append(listeners, listener)
	}
	return listeners
}

func splitAddressPort(value string) (string, int, bool) {
	index := strings.LastIndex(value, ":")
	if index < 0 {
		return "", 0, false
	}
	port, err := strconv.Atoi(value[index+1:])
	if err != nil || port < 1 || port > 65535 {
		return "", 0, false
	}
	address := strings.Trim(value[:index], "[]")
	if address == "" {
		return "", 0, false
	}
	return address, port, true
}

func ParsePS(output string) []Process {
	processes := make([]Process, 0)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[0] == "PID" {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		args := strings.Join(fields[5:], " ")
		name := args
		if first := strings.Fields(args); len(first) > 0 {
			name = path.Base(first[0])
		}
		processes = append(processes, Process{
			PID: pid, User: fields[1], CPU: fields[2], Memory: fields[3], Elapsed: fields[4], Name: name, Args: args,
		})
	}
	return processes
}

type probeSections struct {
	tool           string
	listenerOutput string
	listenerError  string
	listenerCode   int
	listenerFound  bool
	processOutput  string
	processError   string
	processCode    int
	processFound   bool
}

func parseProbeSections(output string) probeSections {
	sections := probeSections{listenerCode: -1, processCode: -1}
	lines := strings.Split(output, "\n")
	current := ""
	var content []string
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		switch line {
		case "__NEXTERM_LISTENER_BEGIN__":
			current = "listener"
			content = nil
			sections.listenerFound = true
			continue
		case "__NEXTERM_PS_BEGIN__":
			current = "ps"
			content = nil
			sections.processFound = true
			continue
		}
		if strings.HasPrefix(line, "__NEXTERM_LISTENER_END__:") {
			sections.listenerCode, _ = strconv.Atoi(strings.TrimPrefix(line, "__NEXTERM_LISTENER_END__:"))
			sections.listenerOutput = strings.Join(content, "\n")
			current = ""
			content = nil
			continue
		}
		if strings.HasPrefix(line, "__NEXTERM_PS_END__:") {
			sections.processCode, _ = strconv.Atoi(strings.TrimPrefix(line, "__NEXTERM_PS_END__:"))
			sections.processOutput = strings.Join(content, "\n")
			current = ""
			content = nil
			continue
		}
		if current == "listener" {
			switch {
			case strings.HasPrefix(line, "__NEXTERM_TOOL__"):
				sections.tool = strings.TrimPrefix(line, "__NEXTERM_TOOL__")
			case strings.HasPrefix(line, "__NEXTERM_LISTENER_ERROR__"):
				sections.listenerError = strings.TrimPrefix(line, "__NEXTERM_LISTENER_ERROR__")
			default:
				content = append(content, line)
			}
		} else if current == "ps" {
			if strings.HasPrefix(line, "__NEXTERM_PS_ERROR__") {
				sections.processError = strings.TrimPrefix(line, "__NEXTERM_PS_ERROR__")
			} else {
				content = append(content, line)
			}
		}
	}
	return sections
}

func finderHostResult(result execbatch.Result, matcher *finderMatcher) FinderHostResult {
	host := FinderHostResult{Result: result, Matches: []FinderMatch{}}
	sections := parseProbeSections(result.Stdout)
	host.ListenerTool = sections.tool
	if !sections.listenerFound || sections.listenerCode < 0 {
		host.ListenerError = resultFailure(result, "监听端口探测输出不完整")
	} else if sections.listenerCode != 0 {
		host.ListenerError = probeSectionError(sections.listenerError, sections.listenerOutput, sections.listenerCode, "ss/netstat")
	}
	if !sections.processFound || sections.processCode < 0 {
		host.ProcessError = resultFailure(result, "进程探测输出不完整")
	} else if sections.processCode != 0 {
		host.ProcessError = probeSectionError(sections.processError, sections.processOutput, sections.processCode, "ps")
	}

	var listeners []Listener
	if host.ListenerError == "" {
		switch sections.tool {
		case "ss":
			listeners = ParseSS(sections.listenerOutput)
		case "netstat":
			listeners = ParseNetstat(sections.listenerOutput)
		default:
			host.ListenerError = "无法识别监听端口探测工具"
		}
	}
	var processes []Process
	if host.ProcessError == "" {
		processes = ParsePS(sections.processOutput)
	}
	processByPID := make(map[int]Process, len(processes))
	for _, process := range processes {
		processByPID[process.PID] = process
	}
	if matcher.kind == QueryPort {
		for _, listener := range listeners {
			if listener.Port != matcher.port {
				continue
			}
			host.Matches = append(host.Matches, listenerMatch(listener, processByPID))
		}
		return host
	}

	matchedPIDs := make(map[int]bool)
	for _, process := range processes {
		if !matcher.matchesText(process.Name, process.Args) {
			continue
		}
		copy := process
		host.Matches = append(host.Matches, FinderMatch{Kind: "process", Process: &copy})
		matchedPIDs[process.PID] = true
	}
	for _, listener := range listeners {
		if matchedPIDs[listener.PID] || !matcher.matchesText(listener.Program) {
			continue
		}
		host.Matches = append(host.Matches, listenerMatch(listener, processByPID))
		if listener.PID > 0 {
			matchedPIDs[listener.PID] = true
		}
	}
	return host
}

func listenerMatch(listener Listener, processes map[int]Process) FinderMatch {
	copy := listener
	match := FinderMatch{Kind: "listener", Listener: &copy}
	if process, ok := processes[listener.PID]; ok {
		processCopy := process
		match.Process = &processCopy
	}
	return match
}

func probeSectionError(marker, output string, code int, tool string) string {
	if marker == "missing ss and netstat" {
		return "缺少 ss 和 netstat，无法查询监听端口"
	}
	if marker == "missing ps" {
		return "缺少 ps，无法查询进程"
	}
	if text := strings.TrimSpace(output); text != "" {
		return text
	}
	if marker != "" {
		return marker
	}
	return fmt.Sprintf("%s 执行失败（退出码 %d）", tool, code)
}

func resultFailure(result execbatch.Result, fallback string) string {
	for _, value := range []string{result.Error, result.Stderr, result.Stdout} {
		if text := strings.TrimSpace(value); text != "" {
			return text
		}
	}
	if result.Status != execbatch.StatusSuccess {
		return string(result.Status)
	}
	return fallback
}
