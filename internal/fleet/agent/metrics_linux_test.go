//go:build linux

package agent

import (
	"testing"
	"time"
)

func TestProcCollectorSamplesHostMetrics(t *testing.T) {
	collector := NewCollector(t.TempDir(), time.Now)
	first, err := collector.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if first.TS <= 0 || first.UptimeS <= 0 {
		t.Fatalf("sample timing = %+v", first)
	}
	if first.MemTotal <= 0 || first.MemUsed < 0 || first.MemUsed > first.MemTotal {
		t.Fatalf("memory sample = %+v", first)
	}
	if first.DiskTotal <= 0 || first.DiskUsed < 0 || first.DiskUsed > first.DiskTotal {
		t.Fatalf("disk sample = %+v", first)
	}
	time.Sleep(50 * time.Millisecond)
	second, err := collector.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if second.CPUPct < 0 || second.CPUPct > 100 {
		t.Fatalf("cpu percent = %v, want within [0,100]", second.CPUPct)
	}
}
