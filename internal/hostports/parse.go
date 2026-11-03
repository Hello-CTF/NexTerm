package hostports

import (
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
)

// Format names the tool whose output is being parsed.
type Format string

const (
	FormatSS             Format = "ss"
	FormatNetstatLinux   Format = "netstat-linux"
	FormatNetstatBSD     Format = "netstat-bsd"
	FormatLsof           Format = "lsof"
	FormatNetstatWindows Format = "netstat-windows"
)

// Parse extracts the listening TCP sockets from the output of the tool named by format.
// Unrecognized lines are skipped. A non-blank output in which no header or row is recognized is an error.
func Parse(format Format, output string) ([]Listener, error) {
	var parse func(string) ([]Listener, bool)
	switch format {
	case FormatSS:
		parse = parseSS
	case FormatNetstatLinux:
		parse = parseNetstatLinux
	case FormatNetstatBSD:
		parse = parseNetstatBSD
	case FormatLsof:
		parse = parseLsof
	case FormatNetstatWindows:
		parse = parseNetstatWindows
	default:
		return nil, fmt.Errorf("未知的端口列表格式 %q", format)
	}
	listeners, recognized := parse(output)
	if !recognized && strings.TrimSpace(output) != "" {
		return nil, fmt.Errorf("无法识别 %s 输出", format)
	}
	return Normalize(listeners), nil
}

func lines(output string) []string {
	return strings.Split(strings.ReplaceAll(output, "\r", ""), "\n")
}

func parsePort(text string) (uint16, bool) {
	n, err := strconv.ParseUint(text, 10, 16)
	return uint16(n), err == nil
}

// splitAddr splits "host<sep>port" at the last separator and strips IPv6 brackets.
func splitAddr(text string, sep byte) (host string, port uint16, ok bool) {
	i := strings.LastIndexByte(text, sep)
	if i < 0 {
		return "", 0, false
	}
	port, ok = parsePort(text[i+1:])
	if !ok {
		return "", 0, false
	}
	host = text[:i]
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	return host, port, true
}

// classify maps a bound host to the normalized address and protocol. wildcardV6 selects "::" for "*".
func classify(host string, wildcardV6 bool) (address, proto string) {
	switch {
	case host == "*" || host == "":
		if wildcardV6 {
			return "::", "tcp6"
		}
		return "0.0.0.0", "tcp"
	case strings.Contains(host, ":"):
		return host, "tcp6"
	}
	// An IPv4 zone suffix such as "127.0.0.53%lo" names the interface, not a different address.
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	return host, "tcp"
}

func hostLooksValid(host string) bool {
	if host == "*" {
		return true
	}
	for _, r := range host {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F', r == '.', r == ':', r == '%', r == '_', r == '-',
			r >= 'g' && r <= 'z', r >= 'G' && r <= 'Z':
		default:
			return false
		}
	}
	return host != ""
}

// parseSS handles `ss -ltn[p][H]`. Rows start with the socket state; a header row starts with "State".
func parseSS(output string) ([]Listener, bool) {
	var out []Listener
	recognized := false
	for _, line := range lines(output) {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if fields[0] == "State" && fields[1] == "Recv-Q" {
			recognized = true
			continue
		}
		if fields[0] != "LISTEN" {
			continue
		}
		host, port, ok := splitAddr(fields[3], ':')
		if !ok || !hostLooksValid(host) {
			continue
		}
		recognized = true
		address, proto := classify(host, false)
		l := Listener{Proto: proto, Address: address, Port: port}
		if rest := strings.Join(fields[5:], " "); strings.Contains(rest, "users:((") {
			users := parseSSUsers(rest)
			if len(users) > 0 {
				l.Process, l.PID = users[0].name, users[0].pid
			}
		}
		out = append(out, l)
	}
	return out, recognized
}

type ssUser struct {
	name string
	pid  int
}

// parseSSUsers reads `users:(("node",pid=123,fd=20),("node",pid=124,fd=20))`.
func parseSSUsers(text string) []ssUser {
	var users []ssUser
	for {
		start := strings.Index(text, `(("`)
		if start < 0 {
			start = strings.Index(text, `,("`)
			if start < 0 {
				return users
			}
			start++
		} else {
			start++
		}
		text = text[start+2:]
		end := strings.IndexByte(text, '"')
		if end < 0 {
			return users
		}
		user := ssUser{name: text[:end]}
		text = text[end+1:]
		if i := strings.Index(text, "pid="); i >= 0 && i < strings.IndexByte(text+")", ')') {
			digits := text[i+4:]
			j := 0
			for j < len(digits) && digits[j] >= '0' && digits[j] <= '9' {
				j++
			}
			user.pid, _ = strconv.Atoi(digits[:j])
		}
		users = append(users, user)
	}
}

// parseNetstatLinux handles net-tools `netstat -ltn[p]`.
func parseNetstatLinux(output string) ([]Listener, bool) {
	var out []Listener
	recognized := false
	for _, line := range lines(output) {
		fields := strings.Fields(line)
		if len(fields) >= 6 && fields[0] == "Proto" && fields[1] == "Recv-Q" {
			recognized = true
			continue
		}
		if len(fields) < 6 || (fields[0] != "tcp" && fields[0] != "tcp6") || fields[5] != "LISTEN" {
			continue
		}
		host, port, ok := splitAddr(fields[3], ':')
		if !ok || !hostLooksValid(host) {
			continue
		}
		recognized = true
		l := Listener{Proto: fields[0], Address: host, Port: port}
		if strings.Contains(host, ":") {
			l.Proto = "tcp6"
		}
		if len(fields) > 6 && fields[6] != "-" {
			if slash := strings.IndexByte(fields[6], '/'); slash > 0 {
				if pid, err := strconv.Atoi(fields[6][:slash]); err == nil {
					l.PID = pid
					l.Process = strings.Join(append([]string{fields[6][slash+1:]}, fields[7:]...), " ")
				}
			}
		}
		out = append(out, l)
	}
	return out, recognized
}

// parseNetstatBSD handles macOS `netstat -an[v] -p tcp`; addresses look like "*.3000" or "::1.8080".
func parseNetstatBSD(output string) ([]Listener, bool) {
	var out []Listener
	recognized := false
	for _, line := range lines(output) {
		fields := strings.Fields(line)
		if len(fields) >= 5 && fields[0] == "Proto" && fields[1] == "Recv-Q" {
			recognized = true
			continue
		}
		if len(fields) < 6 || !strings.HasPrefix(fields[0], "tcp") || fields[5] != "LISTEN" {
			continue
		}
		host, port, ok := splitAddr(fields[3], '.')
		if !ok || !hostLooksValid(host) {
			continue
		}
		recognized = true
		address, proto := classify(host, fields[0] != "tcp4")
		l := Listener{Proto: proto, Address: address, Port: port}
		if len(fields) >= 9 {
			if _, err := strconv.Atoi(fields[6]); err == nil {
				if _, err := strconv.Atoi(fields[7]); err == nil {
					l.PID, _ = strconv.Atoi(fields[8])
				}
			}
		}
		out = append(out, l)
	}
	return out, recognized
}

// parseLsof handles `lsof -nP -iTCP -sTCP:LISTEN`; lsof escapes spaces in command names as \x20.
func parseLsof(output string) ([]Listener, bool) {
	var out []Listener
	recognized := false
	for _, line := range lines(output) {
		fields := strings.Fields(line)
		if len(fields) >= 9 && fields[0] == "COMMAND" && fields[1] == "PID" {
			recognized = true
			continue
		}
		last := len(fields) - 1
		pidIndex := len(fields) - 9
		if pidIndex < 1 || fields[last] != "(LISTEN)" || fields[last-2] != "TCP" {
			continue
		}
		pid, err := strconv.Atoi(fields[pidIndex])
		if err != nil {
			continue
		}
		host, port, ok := splitAddr(fields[last-1], ':')
		if !ok || !hostLooksValid(host) {
			continue
		}
		recognized = true
		address, proto := classify(host, fields[pidIndex+3] == "IPv6")
		name := unescapeLsof(strings.Join(fields[:pidIndex], " "))
		out = append(out, Listener{Proto: proto, Address: address, Port: port, PID: pid, Process: name})
	}
	return out, recognized
}

func unescapeLsof(name string) string {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		if name[i] == '\\' && i+3 < len(name) && name[i+1] == 'x' {
			if v, err := strconv.ParseUint(name[i+2:i+4], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(name[i])
	}
	return b.String()
}

// parseNetstatWindows handles `netstat -ano -p tcp`. The state column is localized, so a listener is a row
// whose foreign address is the all-zero wildcard.
func parseNetstatWindows(output string) ([]Listener, bool) {
	var out []Listener
	recognized := false
	for _, line := range lines(output) {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.EqualFold(fields[0], "TCP") {
			continue
		}
		host, port, ok := splitAddr(fields[1], ':')
		if !ok || !hostLooksValid(host) {
			continue
		}
		pid, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil {
			continue
		}
		recognized = true
		if fields[2] != "0.0.0.0:0" && fields[2] != "[::]:0" {
			continue
		}
		address, proto := classify(host, false)
		out = append(out, Listener{Proto: proto, Address: address, Port: port, PID: pid})
	}
	return out, recognized
}

// ParseTasklistCSV maps PID to image name from `tasklist /FO CSV /NH` output.
func ParseTasklistCSV(output string) map[int]string {
	names := make(map[int]string)
	for _, line := range lines(output) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		record, err := csv.NewReader(strings.NewReader(line)).Read()
		if err != nil || len(record) < 2 {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(record[1])); err == nil && record[0] != "" {
			names[pid] = record[0]
		}
	}
	return names
}
