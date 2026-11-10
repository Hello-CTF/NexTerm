package hostmonitor

import (
	"errors"
	"slices"
	"strconv"
	"strings"
)

// ErrNoSentinel reports output that did not come from the sample script: the host has no POSIX sh, as on
// Windows OpenSSH.
var ErrNoSentinel = errors.New("host monitor sentinel missing")

// ErrNoMetrics reports a Linux host whose output carried none of the expected metrics.
var ErrNoMetrics = errors.New("host monitor output carried no metrics")

// cpuTimes are the cumulative jiffies of the aggregate cpu line of /proc/stat.
type cpuTimes struct {
	total uint64
	idle  uint64
}

type netCounters struct {
	name string
	rx   uint64
	tx   uint64
}

// Raw is one parsed sample before deltas against the previous one are applied.
type Raw struct {
	OS       string
	CPU      *cpuTimes
	CPUCount int
	Memory   *Memory
	Load     *Load
	Net      []netCounters
	Uptime   *float64
	Disks    []Disk
	// DisksTimedOut is set when the host stopped df for running too long.
	DisksTimedOut bool
}

// ParseOutput splits the output of SampleCommand into its sections and parses each one. A section that fails
// to parse leaves its metric unset instead of failing the whole sample.
func ParseOutput(stdout string) (Raw, error) {
	text := strings.ReplaceAll(stdout, "\r", "")
	index := strings.Index(text, sentinelPrefix)
	if index < 0 {
		return Raw{}, ErrNoSentinel
	}
	sections := map[string][]string{}
	var raw Raw
	current := ""
	for line := range strings.SplitSeq(text[index:], "\n") {
		if rest, ok := strings.CutPrefix(line, sentinelPrefix); ok {
			name, value, _ := strings.Cut(rest, " ")
			current = name
			switch name {
			case sectionOS:
				raw.OS = strings.TrimSpace(value)
			case sectionDFTimeout:
				raw.DisksTimedOut = true
			}
			continue
		}
		if current != "" && strings.TrimSpace(line) != "" {
			sections[current] = append(sections[current], line)
		}
	}
	if raw.OS != "Linux" {
		return raw, nil
	}
	raw.CPU, raw.CPUCount = parseStat(sections[sectionStat])
	raw.Memory = parseMeminfo(sections[sectionMeminfo])
	raw.Load = parseLoadavg(sections[sectionLoadavg])
	raw.Net = parseNetDev(sections[sectionNetDev])
	raw.Uptime = parseUptime(sections[sectionUptime])
	raw.Disks = parseDF(sections[sectionDF])
	if raw.CPU == nil && raw.Memory == nil && raw.Load == nil && raw.Net == nil && raw.Uptime == nil && raw.Disks == nil {
		return raw, ErrNoMetrics
	}
	return raw, nil
}

// parseStat reads the aggregate "cpu" line and counts the per-core "cpuN" lines. Idle time includes iowait,
// which is time the core had nothing to run; guest time is already part of user and is not added twice.
func parseStat(lines []string) (*cpuTimes, int) {
	var times *cpuTimes
	count := 0
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if fields[0] != "cpu" {
			if strings.HasPrefix(fields[0], "cpu") {
				count++
			}
			continue
		}
		values := make([]uint64, 0, 8)
		for _, field := range fields[1:min(len(fields), 9)] {
			value, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				values = nil
				break
			}
			values = append(values, value)
		}
		if len(values) < 4 {
			continue
		}
		var total uint64
		for _, value := range values {
			total += value
		}
		idle := values[3]
		if len(values) > 4 {
			idle += values[4]
		}
		times = &cpuTimes{total: total, idle: idle}
	}
	return times, count
}

// parseMeminfo derives used memory from MemAvailable, the kernel's own estimate. Kernels before 3.14 lack it;
// free + buffers + cached stands in for them.
func parseMeminfo(lines []string) *Memory {
	values := map[string]uint64{}
	for _, line := range lines {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		value, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		if len(fields) > 1 && strings.EqualFold(fields[1], "kB") {
			value *= 1024
		}
		values[strings.TrimSpace(name)] = value
	}
	total, ok := values["MemTotal"]
	if !ok || total == 0 {
		return nil
	}
	available, ok := values["MemAvailable"]
	if !ok {
		available = values["MemFree"] + values["Buffers"] + values["Cached"]
	}
	available = min(available, total)
	memory := &Memory{TotalBytes: total, AvailableBytes: available, UsedBytes: total - available}
	swapTotal := values["SwapTotal"]
	memory.SwapTotalBytes = swapTotal
	memory.SwapUsedBytes = swapTotal - min(values["SwapFree"], swapTotal)
	return memory
}

func parseLoadavg(lines []string) *Load {
	if len(lines) == 0 {
		return nil
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 3 {
		return nil
	}
	var load [3]float64
	for i := range load {
		value, err := strconv.ParseFloat(fields[i], 64)
		if err != nil || value < 0 {
			return nil
		}
		load[i] = value
	}
	return &Load{One: load[0], Five: load[1], Fifteen: load[2]}
}

// parseNetDev reads /proc/net/dev. The interface name and the first counter can touch ("eth0:4294967000")
// once the counter is wide, so the line is split at the colon. Loopback carries no host traffic and is left out.
func parseNetDev(lines []string) []netCounters {
	var out []netCounters
	for _, line := range lines {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" || name == "lo" || strings.ContainsAny(name, "|") {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		rx, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		tx, err := strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			continue
		}
		out = append(out, netCounters{name: name, rx: rx, tx: tx})
	}
	return out
}

func parseUptime(lines []string) *float64 {
	if len(lines) == 0 {
		return nil
	}
	fields := strings.Fields(lines[0])
	if len(fields) == 0 {
		return nil
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || value < 0 {
		return nil
	}
	return &value
}

// pseudoFilesystems are df sources that hold no disk.
var pseudoFilesystems = map[string]bool{
	"tmpfs": true, "devtmpfs": true, "none": true, "udev": true, "overlay": true, "shm": true, "efivarfs": true,
	"run": true, "dev": true, "cgroup": true, "cgroup2": true, "proc": true, "sysfs": true,
}

// maxDisks bounds how many file systems one sample reports: the root one plus the fullest others.
const maxDisks = 5

// parseDF reads POSIX df output in 1024-byte blocks. The root file system comes first, then the other real
// file systems from the fullest down. Mount points may contain spaces, so everything after the capacity column
// is the mount point.
func parseDF(lines []string) []Disk {
	var root *Disk
	var others []Disk
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[0] == "Filesystem" {
			continue
		}
		total, err1 := strconv.ParseUint(fields[1], 10, 64)
		used, err2 := strconv.ParseUint(fields[2], 10, 64)
		available, err3 := strconv.ParseUint(fields[3], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || !strings.HasSuffix(fields[4], "%") {
			continue
		}
		mount := strings.Join(fields[5:], " ")
		disk := Disk{
			Mount: mount, Filesystem: fields[0],
			TotalBytes: total * 1024, UsedBytes: used * 1024, AvailableBytes: available * 1024,
		}
		// df rounds capacity up from used / (used + available), which reserved blocks make differ from used / total.
		if used+available > 0 {
			disk.UsedPercent = float64(used) / float64(used+available) * 100
		}
		if mount == "/" {
			root = &disk
			continue
		}
		if total == 0 || pseudoFilesystems[disk.Filesystem] || pseudoMount(mount) {
			continue
		}
		others = append(others, disk)
	}
	slices.SortStableFunc(others, func(a, b Disk) int {
		switch {
		case a.UsedPercent > b.UsedPercent:
			return -1
		case a.UsedPercent < b.UsedPercent:
			return 1
		}
		return strings.Compare(a.Mount, b.Mount)
	})
	var out []Disk
	if root != nil {
		out = append(out, *root)
	}
	out = append(out, others...)
	if len(out) > maxDisks {
		out = out[:maxDisks]
	}
	return out
}

func pseudoMount(mount string) bool {
	for _, prefix := range []string{"/run", "/sys", "/dev", "/proc", "/snap"} {
		if mount == prefix || strings.HasPrefix(mount, prefix+"/") {
			return true
		}
	}
	return false
}
