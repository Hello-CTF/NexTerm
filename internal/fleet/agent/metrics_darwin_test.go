//go:build darwin

package agent

import (
	"testing"
	"time"
)

func TestDarwinCollectorSamplesHostMetrics(t *testing.T) {
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
	if first.CPUPct < 0 || first.CPUPct > 100 {
		t.Fatalf("cpu percent = %v, want within [0,100]", first.CPUPct)
	}
	second, err := collector.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if second.TS < first.TS {
		t.Fatalf("sample timestamps = %d then %d", first.TS, second.TS)
	}
	if second.CPUPct < 0 || second.CPUPct > 100 {
		t.Fatalf("cpu percent = %v, want within [0,100]", second.CPUPct)
	}
}
