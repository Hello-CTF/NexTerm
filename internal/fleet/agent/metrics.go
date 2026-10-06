package agent

import "time"

type Sample struct {
	TS        int64   `json:"ts"`
	CPUPct    float64 `json:"cpu_pct"`
	MemUsed   int64   `json:"mem_used"`
	MemTotal  int64   `json:"mem_total"`
	DiskUsed  int64   `json:"disk_used"`
	DiskTotal int64   `json:"disk_total"`
	UptimeS   int64   `json:"uptime_s"`
}

type ServiceState struct {
	Installed       bool   `json:"installed"`
	Enabled         bool   `json:"enabled"`
	Active          bool   `json:"active"`
	LastReconcileAt int64  `json:"last_reconcile_at,omitempty"`
	LastError       string `json:"last_error,omitempty"`
}

// Collector samples host metrics without elevated privileges.
type Collector interface {
	Collect() (Sample, error)
}

func NewCollector(dataDir string, now func() time.Time) Collector {
	return newPlatformCollector(dataDir, now)
}
