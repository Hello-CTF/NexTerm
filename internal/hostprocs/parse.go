package hostprocs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// ErrNoSentinel reports output that did not come from the POSIX script, which is how a Windows host reveals itself.
var ErrNoSentinel = errors.New("process listing sentinel missing")

// ParseOutput parses the output of either listing command.
func ParseOutput(stdout string) (ProcessListResult, error) {
	text := strings.ReplaceAll(stdout, "\r", "")
	index := strings.Index(text, sentinelPrefix)
	if index < 0 {
		return ProcessListResult{}, ErrNoSentinel
	}
	name, rest, _ := strings.Cut(text[index+len(sentinelPrefix):], "\n")
	format := strings.TrimSpace(name)
	var (
		res ProcessListResult
		err error
	)
	switch format {
	case "none":
		return ProcessListResult{}, errors.New("主机上的 ps 不可用")
	case psFormatFull:
		table, comm, _ := strings.Cut(rest, "\n"+sentinelComm+"\n")
		res, err = parsePS(table, psFullSpec)
		if err == nil {
			applyComm(res.Processes, comm)
		}
	case psFormatBusybox:
		res, err = parsePS(rest, psBusyboxSpec)
	case psFormatBusyboxMin:
		res, err = parsePS(rest, psBusyboxMinSpec)
	case "windows-cim":
		res, err = parseWindows(rest)
	default:
		return ProcessListResult{}, fmt.Errorf("未知的输出格式 %q", format)
	}
	if err != nil {
		return ProcessListResult{}, fmt.Errorf("解析 %s 输出失败: %w", format, err)
	}
	res.Format = format
	sortProcesses(res.Processes)
	return res, nil
}

func sortProcesses(p []HostProcess) {
	sort.SliceStable(p, func(i, j int) bool {
		if p[i].CPU != p[j].CPU {
			return p[i].CPU > p[j].CPU
		}
		return p[i].PID < p[j].PID
	})
}

// psSpec describes the columns of one ps flavour. lstart is one header cell but five cells per row.
type psSpec struct {
	columns []string
	partial []string
}

var (
	psFullSpec       = psSpec{columns: []string{"PID", "PPID", "USER", "%CPU", "%MEM", "RSS", "ELAPSED", "STAT", "STARTED", "COMMAND"}}
	psBusyboxSpec    = psSpec{columns: []string{"PID", "PPID", "USER", "RSS", "ELAPSED", "STAT", "COMMAND"}, partial: []string{"cpu", "mem"}}
	psBusyboxMinSpec = psSpec{columns: []string{"PID", "PPID", "USER", "RSS", "STAT", "COMMAND"}, partial: []string{"cpu", "mem", "elapsed"}}
)

func columnName(s string) string {
	s = strings.ToUpper(s)
	switch s {
	case "ETIME", "ELAPSED", "TIME":
		return "ELAPSED"
	case "ARGS", "COMMAND", "CMD":
		return "COMMAND"
	case "S", "STATE":
		return "STAT"
	case "LSTART", "START", "STARTED":
		return "STARTED"
	}
	return s
}

func parsePS(text string, spec psSpec) (ProcessListResult, error) {
	columns := spec.columns
	lines := strings.Split(text, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	if len(lines) == 0 {
		return ProcessListResult{}, errors.New("缺少表头")
	}
	header := strings.Fields(lines[0])
	if len(header) < len(columns) {
		return ProcessListResult{}, fmt.Errorf("表头列数不足: %q", lines[0])
	}
	for i, want := range columns {
		if columnName(header[i]) != want {
			return ProcessListResult{}, fmt.Errorf("表头列顺序不符: %q", lines[0])
		}
	}
	res := ProcessListResult{Platform: platformPOSIX, Processes: []HostProcess{}, Partial: append([]string(nil), spec.partial...)}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if p, ok := parsePSRow(line, columns); ok {
			res.Processes = append(res.Processes, p)
		} else {
			res.SkippedRows++
		}
	}
	return res, nil
}

// startedTokens is how many cells lstart takes in C locale: weekday month day time year.
const startedTokens = 5

// splitFields cuts the first n whitespace-separated tokens and returns the trimmed remainder.
func splitFields(line string, n int) ([]string, string, bool) {
	fields := make([]string, 0, n)
	rest := line
	for len(fields) < n {
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			return nil, "", false
		}
		end := strings.IndexAny(rest, " \t")
		if end < 0 {
			end = len(rest)
		}
		fields = append(fields, rest[:end])
		rest = rest[end:]
	}
	return fields, strings.TrimSpace(rest), true
}

func parsePSRow(line string, columns []string) (HostProcess, bool) {
	cells := len(columns) - 1
	for _, c := range columns {
		if c == "STARTED" {
			cells += startedTokens - 1
		}
	}
	f, args, ok := splitFields(line, cells)
	if !ok || args == "" {
		return HostProcess{}, false
	}
	var p HostProcess
	var err error
	i := 0
	for _, column := range columns[:len(columns)-1] {
		cell := f[i]
		i++
		switch column {
		case "PID":
			p.PID, err = strconv.Atoi(cell)
		case "PPID":
			p.PPID, err = strconv.Atoi(cell)
		case "USER":
			p.User = cell
		case "%CPU":
			p.CPU, err = strconv.ParseFloat(cell, 64)
		case "%MEM":
			p.Mem, err = strconv.ParseFloat(cell, 64)
		case "RSS":
			var ok bool
			if p.RSSKiB, ok = parseRSS(cell); !ok {
				err = errors.New("rss")
			}
		case "ELAPSED":
			p.Elapsed = cell
			p.ElapsedSec = parseElapsed(cell)
		case "STAT":
			p.Stat = cell
		case "STARTED":
			p.Started = strings.Join(f[i-1:i-1+startedTokens], " ")
			i += startedTokens - 1
		}
		if err != nil {
			return HostProcess{}, false
		}
	}
	if !slices.Contains(columns, "ELAPSED") {
		p.ElapsedSec = -1
	}
	p.Command = args
	p.ArgName = posixName(args)
	p.Name = p.ArgName
	p.Zombie = strings.HasPrefix(p.Stat, "Z")
	return p, true
}

// parseRSS reads a KiB count with the optional k/m/g/t suffix busybox ps appends to big values.
func parseRSS(s string) (int64, bool) {
	mult := int64(1)
	if n := len(s); n > 1 {
		switch s[n-1] {
		case 'k', 'K':
			s = s[:n-1]
		case 'm', 'M':
			mult, s = 1<<10, s[:n-1]
		case 'g', 'G':
			mult, s = 1<<20, s[:n-1]
		case 't', 'T':
			mult, s = 1<<30, s[:n-1]
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v * mult, true
}

// applyComm sets Name from the pid/comm table. comm may contain spaces, so everything after the pid is the
// name; macOS prints a full path, of which the basename is kept. Missing entries keep the argv-derived name.
func applyComm(procs []HostProcess, text string) {
	names := map[int]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		end := strings.IndexAny(line, " \t")
		if end < 0 {
			continue
		}
		pidText, name := line[:end], line[end:]
		pid, err := strconv.Atoi(pidText)
		name = strings.TrimSpace(name)
		if err != nil || name == "" {
			continue
		}
		if strings.HasPrefix(name, "/") {
			name = path.Base(name)
		}
		names[pid] = name
	}
	for i := range procs {
		if name, ok := names[procs[i].PID]; ok {
			procs[i].Name = name
		}
	}
}

// posixName is the basename of the first token of args; bracketed kernel thread names are kept whole.
func posixName(args string) string {
	token, _, _ := strings.Cut(strings.TrimSpace(args), " ")
	if strings.HasPrefix(token, "[") {
		return token
	}
	if strings.Contains(token, "/") {
		return path.Base(token)
	}
	return token
}

// parseElapsed reads [[dd-]hh:]mm:ss and returns -1 for anything else.
func parseElapsed(s string) int64 {
	s = strings.TrimSpace(s)
	var days int64
	if d, rest, found := strings.Cut(s, "-"); found {
		n, err := strconv.ParseInt(d, 10, 64)
		if err != nil || n < 0 {
			return -1
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 || (days > 0 && len(parts) != 3) {
		return -1
	}
	var total int64
	for _, part := range parts {
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil || n < 0 {
			return -1
		}
		total = total*60 + n
	}
	return total + days*86400
}

type windowsItem struct {
	PID        int      `json:"pid"`
	PPID       int      `json:"ppid"`
	Name       string   `json:"name"`
	Cmd        *string  `json:"cmd"`
	RSSKiB     int64    `json:"rssKiB"`
	CPU        float64  `json:"cpu"`
	User       *string  `json:"user"`
	ElapsedSec *float64 `json:"elapsedSec"`
	Started    *string  `json:"started"`
}

// decodeObjects reads what ConvertTo-Json made of a PowerShell array: a JSON array, a bare object for one
// element, or the {"value":[...],"Count":n} wrapper some PowerShell versions emit.
func decodeObjects[T any](raw json.RawMessage) ([]T, error) {
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0 || bytes.Equal(raw, []byte("null")):
		return nil, nil
	case raw[0] == '[':
		var items []T
		err := json.Unmarshal(raw, &items)
		return items, err
	case raw[0] == '{':
		var wrapper map[string]json.RawMessage
		if err := json.Unmarshal(raw, &wrapper); err != nil {
			return nil, err
		}
		if inner, ok := wrapper["value"]; ok {
			return decodeObjects[T](inner)
		}
		var one T
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, err
		}
		return []T{one}, nil
	}
	return nil, errors.New("意外的 JSON 数据")
}

func parseWindows(text string) (ProcessListResult, error) {
	start := strings.Index(text, "{")
	if start < 0 {
		return ProcessListResult{}, errors.New("缺少 JSON 数据")
	}
	var doc struct {
		CPUOK       bool            `json:"cpuOk"`
		UserOK      bool            `json:"userOk"`
		MemTotalKiB float64         `json:"memTotalKiB"`
		Items       json.RawMessage `json:"items"`
	}
	dec := json.NewDecoder(strings.NewReader(text[start:]))
	if err := dec.Decode(&doc); err != nil {
		return ProcessListResult{}, err
	}
	items, err := decodeObjects[windowsItem](doc.Items)
	if err != nil {
		return ProcessListResult{}, err
	}
	res := ProcessListResult{Platform: platformWindows, Processes: make([]HostProcess, 0, len(items))}
	for _, it := range items {
		p := HostProcess{PID: it.PID, PPID: it.PPID, Name: it.Name, RSSKiB: it.RSSKiB, CPU: it.CPU, ElapsedSec: -1}
		if it.Cmd != nil && strings.TrimSpace(*it.Cmd) != "" {
			p.Command = *it.Cmd
		} else {
			p.Command = it.Name
		}
		if it.User != nil {
			p.User = *it.User
		}
		if it.Started != nil {
			p.Started = *it.Started
		}
		if it.ElapsedSec != nil && *it.ElapsedSec >= 0 {
			p.ElapsedSec = int64(*it.ElapsedSec)
		}
		if doc.MemTotalKiB > 0 {
			p.Mem = math.Round(float64(it.RSSKiB)/doc.MemTotalKiB*1000) / 10
		}
		res.Processes = append(res.Processes, p)
	}
	if !doc.CPUOK {
		res.Partial = append(res.Partial, "cpu")
	}
	if doc.MemTotalKiB <= 0 {
		res.Partial = append(res.Partial, "mem")
	}
	if !doc.UserOK {
		res.Partial = append(res.Partial, "user")
	}
	return res, nil
}

// identity is what the recheck found running under a PID. Name is the argv-derived name on POSIX and the
// image name on Windows; Started is empty when the host cannot tell.
type identity struct {
	Name    string
	PPID    int
	Started string
}

// parseIdentity reads the output of identityCommand; ok is false when the PID is gone or the output is not
// understood.
func parseIdentity(stdout string) (identity, bool) {
	text := strings.ReplaceAll(stdout, "\r", "")
	index := strings.Index(text, sentinelPrefix)
	if index < 0 {
		return identity{}, false
	}
	format, rest, _ := strings.Cut(text[index:], "\n")
	switch strings.TrimSpace(format) {
	case sentinelIDPS:
		first, rest, _ := strings.Cut(rest, "\n")
		_, argsText, _ := strings.Cut(rest, sentinelIDArgs+"\n")
		args, _, _ := strings.Cut(argsText, "\n")
		fields := strings.Fields(first)
		if len(fields) == 0 || strings.TrimSpace(args) == "" {
			return identity{}, false
		}
		ppid, err := strconv.Atoi(fields[0])
		if err != nil {
			return identity{}, false
		}
		id := identity{Name: posixName(args), PPID: ppid}
		if len(fields) == 1+startedTokens {
			id.Started = strings.Join(fields[1:], " ")
		}
		return id, true
	case sentinelIDBusybox:
		ppidText, args, _ := strings.Cut(rest, "\n")
		args, _, _ = strings.Cut(args, "\n")
		ppid, err := strconv.Atoi(strings.TrimSpace(ppidText))
		if err != nil || strings.TrimSpace(args) == "" {
			return identity{}, false
		}
		return identity{Name: posixName(args), PPID: ppid}, true
	case sentinelIDWindows:
		start := strings.IndexAny(rest, "{[")
		if start < 0 {
			return identity{}, false
		}
		var raw json.RawMessage
		if err := json.NewDecoder(strings.NewReader(rest[start:])).Decode(&raw); err != nil {
			return identity{}, false
		}
		items, err := decodeObjects[struct {
			Name    string  `json:"name"`
			PPID    int     `json:"ppid"`
			Started *string `json:"started"`
		}](raw)
		if err != nil || len(items) == 0 || items[0].Name == "" {
			return identity{}, false
		}
		id := identity{Name: items[0].Name, PPID: items[0].PPID}
		if items[0].Started != nil {
			id.Started = *items[0].Started
		}
		return id, true
	}
	return identity{}, false
}

// windowsCriticalNames are processes whose end takes the system down or locks the session; the kill is refused
// for them whatever the caller confirmed. Names are compared without ".exe" and case-insensitively.
var windowsCriticalNames = []string{"csrss", "wininit", "smss", "services", "lsass", "winlogon"}

// WindowsCriticalProcesses lists the Windows processes that cannot be ended; the UI hides the action for them.
func WindowsCriticalProcesses() []string { return slices.Clone(windowsCriticalNames) }

func isWindowsCritical(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimSuffix(name, ".exe")
	return slices.Contains(windowsCriticalNames, name)
}
