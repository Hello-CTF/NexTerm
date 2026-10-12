//go:build darwin

package agent

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	vmStatTimeout = 5 * time.Second
	topTimeout    = 15 * time.Second
)

type darwinCollector struct {
	dataDir string
	now     func() time.Time
}

func newPlatformCollector(dataDir string, now func() time.Time) Collector {
	if now == nil {
		now = time.Now
	}
	return &darwinCollector{dataDir: dataDir, now: now}
}

func (c *darwinCollector) Collect() (Sample, error) {
	sample := Sample{TS: c.now().UnixMilli()}

	topOut, err := metricsCommandOutput(topTimeout, "top", "-l", "2", "-n", "0")
	if err != nil {
		return Sample{}, err
	}
	sample.CPUPct, err = parseTopCPUPercent(string(topOut))
	if err != nil {
		return Sample{}, err
	}

	memsize, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return Sample{}, fmt.Errorf("sysctl hw.memsize: %w", err)
	}
	sample.MemTotal = int64(memsize)

	vmOut, err := metricsCommandOutput(vmStatTimeout, "vm_stat")
	if err != nil {
		return Sample{}, err
	}
	usedPages, pageSize, err := parseVMStat(string(vmOut))
	if err != nil {
		return Sample{}, err
	}
	if pageSize == 0 {
		sysctlPageSize, err := unix.SysctlUint64("hw.pagesize")
		if err != nil {
			return Sample{}, fmt.Errorf("sysctl hw.pagesize: %w", err)
		}
		pageSize = int64(sysctlPageSize)
	}
	sample.MemUsed = usedPages * pageSize

	boottime, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return Sample{}, fmt.Errorf("sysctl kern.boottime: %w", err)
	}
	sample.UptimeS = c.now().Unix() - boottime.Sec

	var stat syscall.Statfs_t
	if err := syscall.Statfs(c.dataDir, &stat); err != nil {
		return Sample{}, fmt.Errorf("statfs %s: %w", c.dataDir, err)
	}
	blockSize := uint64(stat.Bsize)
	sample.DiskTotal = int64(stat.Blocks * blockSize)
	sample.DiskUsed = int64((stat.Blocks - stat.Bfree) * blockSize)
	return sample, nil
}

func metricsCommandOutput(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("run %s: %w", name, err)
	}
	return out, nil
}
