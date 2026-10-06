//go:build linux

package agent

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type procCollector struct {
	dataDir string
	now     func() time.Time

	mu        sync.Mutex
	prevBusy  uint64
	prevTotal uint64
	havePrev  bool
}

func newPlatformCollector(dataDir string, now func() time.Time) Collector {
	if now == nil {
		now = time.Now
	}
	return &procCollector{dataDir: dataDir, now: now}
}

func (c *procCollector) Collect() (Sample, error) {
	sample := Sample{TS: c.now().UnixMilli()}
	busy, total, err := c.readCPUTimes()
	if err != nil {
		return Sample{}, err
	}
	c.mu.Lock()
	if c.havePrev && total > c.prevTotal {
		sample.CPUPct = float64(busy-c.prevBusy) * 100 / float64(total-c.prevTotal)
	}
	c.prevBusy, c.prevTotal, c.havePrev = busy, total, true
	c.mu.Unlock()

	memTotal, memAvailable, err := readMemInfo()
	if err != nil {
		return Sample{}, err
	}
	sample.MemTotal = memTotal
	sample.MemUsed = memTotal - memAvailable

	uptime, err := readUptime()
	if err != nil {
		return Sample{}, err
	}
	sample.UptimeS = uptime

	var stat syscall.Statfs_t
	if err := syscall.Statfs(c.dataDir, &stat); err != nil {
		return Sample{}, fmt.Errorf("statfs %s: %w", c.dataDir, err)
	}
	blockSize := uint64(stat.Bsize)
	sample.DiskTotal = int64(stat.Blocks * blockSize)
	sample.DiskUsed = int64((stat.Blocks - stat.Bfree) * blockSize)
	return sample, nil
}

func (c *procCollector) readCPUTimes() (busy, total uint64, err error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, fmt.Errorf("read /proc/stat: %w", err)
	}
	line, _, _ := strings.Cut(string(data), "\n")
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, fmt.Errorf("unexpected /proc/stat cpu line %q", line)
	}
	values := make([]uint64, 0, len(fields)-1)
	for _, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("parse /proc/stat cpu field %q: %w", field, err)
		}
		values = append(values, value)
	}
	idle := values[3]
	if len(values) > 4 {
		idle += values[4]
	}
	for _, value := range values {
		total += value
	}
	return total - idle, total, nil
}

func readMemInfo() (total, available int64, err error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, fmt.Errorf("read /proc/meminfo: %w", err)
	}
	fields := map[string]int64{}
	for _, line := range strings.Split(string(data), "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value, _, _ := strings.Cut(strings.TrimSpace(rest), " ")
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			continue
		}
		fields[strings.TrimSpace(name)] = parsed
	}
	total, totalOK := fields["MemTotal"]
	available, availableOK := fields["MemAvailable"]
	if !totalOK || !availableOK || total <= 0 || available < 0 || available > total {
		return 0, 0, fmt.Errorf("unexpected /proc/meminfo contents")
	}
	return total * 1024, available * 1024, nil
}

func readUptime() (int64, error) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, fmt.Errorf("read /proc/uptime: %w", err)
	}
	field, _, _ := strings.Cut(strings.TrimSpace(string(data)), " ")
	seconds, err := strconv.ParseFloat(field, 64)
	if err != nil {
		return 0, fmt.Errorf("parse /proc/uptime: %w", err)
	}
	return int64(seconds), nil
}
