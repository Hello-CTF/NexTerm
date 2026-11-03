package hostprocs

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
)

// ErrNoSentinel reports output that did not come from the POSIX script, which is how a Windows host reveals itself.
var ErrNoSentinel = errors.New("process listing sentinel missing")

// ParseOutput parses the output of either listing command.
func ParseOutput(stdout string) (ListResult, error) {
	text := strings.ReplaceAll(stdout, "\r", "")
	index := strings.Index(text, sentinelPrefix)
	if index < 0 {
		return ListResult{}, ErrNoSentinel
	}
	name, rest, _ := strings.Cut(text[index+len(sentinelPrefix):], "\n")
	format := strings.TrimSpace(name)
	var (
		res ListResult
		err error
	)
	switch format {
	case "none":
		return ListResult{}, errors.New("主机上的 ps 不可用")
	case "ps":
		res, err = parsePS(rest, false)
	case "ps-busybox":
		res, err = parsePS(rest, true)
	case "windows-cim":
		res, err = parseWindows(rest)
	default:
		return ListResult{}, fmt.Errorf("未知的输出格式 %q", format)
	}
	if err != nil {
		return ListResult{}, fmt.Errorf("解析 %s 输出失败: %w", format, err)
	}
	res.Format = format
	sortProcesses(res.Processes)
	return res, nil
}

func sortProcesses(p []Process) {
	sort.SliceStable(p, func(i, j int) bool {
		if p[i].CPU != p[j].CPU {
			return p[i].CPU > p[j].CPU
		}
		return p[i].PID < p[j].PID
	})
}

var (
	psColumns        = []string{"PID", "PPID", "USER", "%CPU", "%MEM", "RSS", "ELAPSED", "STAT", "COMMAND"}
	psBusyboxColumns = []string{"PID", "PPID", "USER", "RSS", "ELAPSED", "STAT", "COMMAND"}
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
	}
	return s
}

func parsePS(text string, busybox bool) (ListResult, error) {
	columns := psColumns
	if busybox {
		columns = psBusyboxColumns
	}
	lines := strings.Split(text, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	if len(lines) == 0 {
		return ListResult{}, errors.New("缺少表头")
	}
	header := strings.Fields(lines[0])
	if len(header) < len(columns) {
		return ListResult{}, fmt.Errorf("表头列数不足: %q", lines[0])
	}
	for i, want := range columns {
		if columnName(header[i]) != want {
			return ListResult{}, fmt.Errorf("表头列顺序不符: %q", lines[0])
		}
	}
	res := ListResult{Platform: platformPOSIX, Processes: []Process{}}
	if busybox {
		res.Partial = []string{"cpu", "mem"}
	}
	for _, line := range lines[1:] {
		if p, ok := parsePSRow(line, busybox, len(columns)); ok {
			res.Processes = append(res.Processes, p)
		}
	}
	return res, nil
}

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

func parsePSRow(line string, busybox bool, columns int) (Process, bool) {
	f, args, ok := splitFields(line, columns-1)
	if !ok {
		return Process{}, false
	}
	var p Process
	var err error
	if p.PID, err = strconv.Atoi(f[0]); err != nil {
		return Process{}, false
	}
	if p.PPID, err = strconv.Atoi(f[1]); err != nil {
		return Process{}, false
	}
	p.User = f[2]
	i := 3
	if !busybox {
		if p.CPU, err = strconv.ParseFloat(f[3], 64); err != nil {
			return Process{}, false
		}
		if p.Mem, err = strconv.ParseFloat(f[4], 64); err != nil {
			return Process{}, false
		}
		i = 5
	}
	if p.RSSKiB, err = strconv.ParseInt(f[i], 10, 64); err != nil {
		return Process{}, false
	}
	p.Elapsed = f[i+1]
	p.ElapsedSec = parseElapsed(p.Elapsed)
	p.Stat = f[i+2]
	p.Command = args
	p.Name = posixName(args)
	p.Zombie = strings.HasPrefix(p.Stat, "Z")
	return p, true
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
}

func parseWindows(text string) (ListResult, error) {
	start := strings.Index(text, "{")
	if start < 0 {
		return ListResult{}, errors.New("缺少 JSON 数据")
	}
	var doc struct {
		CPUOK       bool            `json:"cpuOk"`
		UserOK      bool            `json:"userOk"`
		MemTotalKiB float64         `json:"memTotalKiB"`
		Items       json.RawMessage `json:"items"`
	}
	dec := json.NewDecoder(strings.NewReader(text[start:]))
	if err := dec.Decode(&doc); err != nil {
		return ListResult{}, err
	}
	var items []windowsItem
	raw := strings.TrimSpace(string(doc.Items))
	switch {
	case strings.HasPrefix(raw, "["):
		if err := json.Unmarshal(doc.Items, &items); err != nil {
			return ListResult{}, err
		}
	case strings.HasPrefix(raw, "{"):
		// PowerShell collapses a one-element array into an object.
		var one windowsItem
		if err := json.Unmarshal(doc.Items, &one); err != nil {
			return ListResult{}, err
		}
		items = []windowsItem{one}
	}
	res := ListResult{Platform: platformWindows, Processes: make([]Process, 0, len(items))}
	for _, it := range items {
		p := Process{PID: it.PID, PPID: it.PPID, Name: it.Name, RSSKiB: it.RSSKiB, CPU: it.CPU, ElapsedSec: -1}
		if it.Cmd != nil && strings.TrimSpace(*it.Cmd) != "" {
			p.Command = *it.Cmd
		} else {
			p.Command = it.Name
		}
		if it.User != nil {
			p.User = *it.User
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

// posixNameMatches compares the first token of `ps -p PID -o args=` output with the confirmed name.
func posixNameMatches(output, name string) bool {
	line, _, _ := strings.Cut(strings.TrimSpace(output), "\n")
	return line != "" && posixName(line) == name
}

// windowsImageMatches compares the image name of one `tasklist /FO CSV /NH` row with the confirmed name.
// tasklist cuts image names at 25 characters, so a name of that length matches by prefix.
func windowsImageMatches(output, name string) bool {
	r := csv.NewReader(strings.NewReader(strings.TrimSpace(output)))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	rec, err := r.Read()
	if err != nil || len(rec) < 2 {
		return false
	}
	image := strings.ToLower(strings.TrimSpace(rec[0]))
	want := strings.ToLower(name)
	return image != "" && (image == want || (len(image) >= 25 && strings.HasPrefix(want, image)))
}
