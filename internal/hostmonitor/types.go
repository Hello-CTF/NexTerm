package hostmonitor

// Status says what a Snapshot carries.
type Status string

const (
	// StatusOK carries metrics. Rates and CPU usage need two samples, so the first one leaves them unset.
	StatusOK Status = "ok"
	// StatusUnsupported means the host cannot be sampled (not Linux, or no POSIX sh); it is not retried.
	StatusUnsupported Status = "unsupported"
	// StatusError means the last sample failed; RetryAfterMs says when the next one may run.
	StatusError Status = "error"
)

// Memory is physical memory and swap in bytes. Used memory is total minus what the kernel reports available.
type Memory struct {
	TotalBytes     uint64 `json:"totalBytes"`
	UsedBytes      uint64 `json:"usedBytes"`
	AvailableBytes uint64 `json:"availableBytes"`
	SwapTotalBytes uint64 `json:"swapTotalBytes"`
	SwapUsedBytes  uint64 `json:"swapUsedBytes"`
}

// Load is the 1, 5 and 15 minute load average.
type Load struct {
	One     float64 `json:"one"`
	Five    float64 `json:"five"`
	Fifteen float64 `json:"fifteen"`
}

// Disk is one mounted file system.
type Disk struct {
	Mount          string  `json:"mount"`
	Filesystem     string  `json:"filesystem"`
	TotalBytes     uint64  `json:"totalBytes"`
	UsedBytes      uint64  `json:"usedBytes"`
	AvailableBytes uint64  `json:"availableBytes"`
	UsedPercent    float64 `json:"usedPercent"`
}

// NetInterface is one network interface with its cumulative counters and, from the second sample on, its rates.
type NetInterface struct {
	Name    string   `json:"name"`
	RxBytes uint64   `json:"rxBytes"`
	TxBytes uint64   `json:"txBytes"`
	RxRate  *float64 `json:"rxRate,omitempty"`
	TxRate  *float64 `json:"txRate,omitempty"`
}

// Snapshot is the answer to host_monitor_sample.
type Snapshot struct {
	Status Status `json:"status"`
	// Reason explains an unsupported host or a failed sample.
	Reason string `json:"reason,omitempty"`
	// Platform is the host's uname -s when it was learned.
	Platform string `json:"platform,omitempty"`
	// SampledAt is the Unix time in milliseconds the metrics were read; zero without metrics.
	SampledAt int64 `json:"sampledAt"`
	// IntervalMs is how often the caller should ask for a new sample.
	IntervalMs int64 `json:"intervalMs"`
	// RetryAfterMs is how long the service waits before it runs the command again after a failure.
	RetryAfterMs int64 `json:"retryAfterMs,omitempty"`

	CPUPercent *float64       `json:"cpuPercent,omitempty"`
	CPUCount   int            `json:"cpuCount,omitempty"`
	Memory     *Memory        `json:"memory,omitempty"`
	Load       *Load          `json:"load,omitempty"`
	Network    []NetInterface `json:"network"`
	Disks      []Disk         `json:"disks"`
	// DisksUnavailable is set once df ran too long on the host; disks are then no longer read for the session.
	DisksUnavailable bool     `json:"disksUnavailable,omitempty"`
	UptimeSeconds    *float64 `json:"uptimeSeconds,omitempty"`
}
