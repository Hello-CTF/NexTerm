package hostmonitor

import "time"

// maxInterfaces bounds the interfaces one snapshot reports; container hosts can have hundreds of veth pairs.
const maxInterfaces = 32

// Compute builds the snapshot of cur. CPU usage and network rates are deltas against prev, which is nil for the
// first sample. The elapsed time comes from the host's own uptime when both samples carry it, because it does
// not include the round trip; wall, measured by the caller, stands in otherwise. A host whose uptime went
// backwards rebooted in between, so its counters restarted and no delta is computed.
func Compute(prev *Raw, cur Raw, wall time.Duration) Snapshot {
	snapshot := Snapshot{
		Status:        StatusOK,
		Platform:      cur.OS,
		CPUCount:      cur.CPUCount,
		Memory:        cur.Memory,
		Load:          cur.Load,
		Disks:         cur.Disks,
		UptimeSeconds: cur.Uptime,
		Network:       make([]NetInterface, 0, min(len(cur.Net), maxInterfaces)),
	}
	if snapshot.Disks == nil {
		snapshot.Disks = []Disk{}
	}
	elapsed := wall.Seconds()
	comparable := prev != nil
	if comparable && prev.Uptime != nil && cur.Uptime != nil {
		if *cur.Uptime < *prev.Uptime {
			comparable = false
		} else if delta := *cur.Uptime - *prev.Uptime; delta > 0 {
			elapsed = delta
		}
	}
	if comparable && prev.CPU != nil && cur.CPU != nil {
		snapshot.CPUPercent = cpuPercent(*prev.CPU, *cur.CPU)
	}
	var previous map[string]netCounters
	if comparable && elapsed > 0 {
		previous = make(map[string]netCounters, len(prev.Net))
		for _, counters := range prev.Net {
			previous[counters.name] = counters
		}
	}
	for _, counters := range cur.Net {
		if len(snapshot.Network) == maxInterfaces {
			break
		}
		iface := NetInterface{Name: counters.name, RxBytes: counters.rx, TxBytes: counters.tx}
		if before, ok := previous[counters.name]; ok {
			iface.RxRate = rate(before.rx, counters.rx, elapsed)
			iface.TxRate = rate(before.tx, counters.tx, elapsed)
		}
		snapshot.Network = append(snapshot.Network, iface)
	}
	return snapshot
}

func cpuPercent(prev, cur cpuTimes) *float64 {
	if cur.total <= prev.total || cur.idle < prev.idle {
		return nil
	}
	total := cur.total - prev.total
	idle := min(cur.idle-prev.idle, total)
	value := float64(total-idle) / float64(total) * 100
	return &value
}

// rate is bytes per second between two counter readings; a counter that went backwards was reset or wrapped
// and yields no rate.
func rate(prev, cur uint64, seconds float64) *float64 {
	if cur < prev || seconds <= 0 {
		return nil
	}
	value := float64(cur-prev) / seconds
	return &value
}
