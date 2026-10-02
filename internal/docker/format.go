package docker

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docker/go-units"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
)

const noneLabel = "<none>"

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func containerDTOs(items []container.Summary) []ContainerSummary {
	out := make([]ContainerSummary, 0, len(items))
	for _, item := range items {
		names := make([]string, 0, len(item.Names))
		for _, name := range item.Names {
			names = append(names, strings.TrimPrefix(name, "/"))
		}
		var project *string
		if value, ok := item.Labels["com.docker.compose.project"]; ok {
			valueCopy := value
			project = &valueCopy
		}
		out = append(out, ContainerSummary{
			ID:             shortID(item.ID),
			Name:           strings.Join(names, ","),
			Image:          item.Image,
			State:          string(item.State),
			Status:         item.Status,
			Ports:          formatPorts(item.Ports),
			ComposeProject: project,
		})
	}
	return out
}

func formatPorts(ports []container.PortSummary) string {
	if len(ports) == 0 {
		return ""
	}
	ordered := append([]container.PortSummary(nil), ports...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].PrivatePort != ordered[j].PrivatePort {
			return ordered[i].PrivatePort < ordered[j].PrivatePort
		}
		if ordered[i].Type != ordered[j].Type {
			return ordered[i].Type < ordered[j].Type
		}
		if !ordered[i].IP.IsValid() && ordered[j].IP.IsValid() {
			return true
		}
		if ordered[i].IP.IsValid() != ordered[j].IP.IsValid() {
			return false
		}
		if ordered[i].IP.IsValid() && ordered[i].IP.Compare(ordered[j].IP) != 0 {
			return ordered[i].IP.Compare(ordered[j].IP) < 0
		}
		return ordered[i].PublicPort < ordered[j].PublicPort
	})
	parts := make([]string, 0, len(ordered))
	for _, port := range ordered {
		private := strconv.FormatUint(uint64(port.PrivatePort), 10) + "/" + port.Type
		if port.PublicPort == 0 {
			parts = append(parts, private)
			continue
		}
		host := strconv.FormatUint(uint64(port.PublicPort), 10)
		if port.IP.IsValid() {
			host = formatHostIP(port.IP) + ":" + host
		}
		parts = append(parts, host+"->"+private)
	}
	return strings.Join(parts, ", ")
}

func formatHostIP(ip netip.Addr) string {
	if ip.Is6() && !ip.Is4In6() {
		return "[" + ip.String() + "]"
	}
	return ip.String()
}

func imageDTOs(items []image.Summary, now time.Time) []ImageSummary {
	var out []ImageSummary
	for _, item := range items {
		tags := item.RepoTags
		if len(tags) == 0 {
			tags = []string{noneLabel + ":" + noneLabel}
		}
		for _, ref := range tags {
			repository, tag := splitImageRef(ref)
			out = append(out, ImageSummary{
				ID:           shortID(item.ID),
				Repository:   repository,
				Tag:          tag,
				Size:         units.HumanSizeWithPrecision(float64(item.Size), 3),
				CreatedSince: createdSince(item.Created, now),
			})
		}
	}
	if out == nil {
		return []ImageSummary{}
	}
	return out
}

func splitImageRef(ref string) (string, string) {
	if ref == "" || ref == noneLabel {
		return noneLabel, noneLabel
	}
	lastSlash := strings.LastIndex(ref, "/")
	lastColon := strings.LastIndex(ref, ":")
	if lastColon > lastSlash {
		return ref[:lastColon], ref[lastColon+1:]
	}
	return ref, "latest"
}

func createdSince(created int64, now time.Time) string {
	if created <= 0 {
		return ""
	}
	d := now.Sub(time.Unix(created, 0))
	if d < 0 {
		d = 0
	}
	return units.HumanDuration(d) + " ago"
}

type statsLine struct {
	BlockIO   string `json:"BlockIO"`
	CPUPerc   string `json:"CPUPerc"`
	Container string `json:"Container"`
	ID        string `json:"ID"`
	MemPerc   string `json:"MemPerc"`
	MemUsage  string `json:"MemUsage"`
	Name      string `json:"Name"`
	NetIO     string `json:"NetIO"`
	PIDs      string `json:"PIDs"`
}

func formatStats(stats container.StatsResponse, fallback container.Summary) ([]byte, error) {
	if stats.ID == "" {
		stats.ID = fallback.ID
	}
	if stats.Name == "" && len(fallback.Names) > 0 {
		stats.Name = fallback.Names[0]
	}
	name := strings.TrimPrefix(stats.Name, "/")
	rx, tx := networkTotals(stats.Networks)
	var read, write uint64
	var cpu, memory, memoryLimit, memoryPerc float64
	pids := "0"
	if stats.OSType == "windows" || stats.NumProcs > 0 {
		cpu = windowsCPU(stats)
		memory = float64(stats.MemoryStats.PrivateWorkingSet)
		read = stats.StorageStats.ReadSizeBytes
		write = stats.StorageStats.WriteSizeBytes
		memoryPerc = 0
		pids = ""
	} else {
		cpu = unixCPU(stats.PreCPUStats, stats.CPUStats)
		memory = unixMemory(stats.MemoryStats)
		memoryLimit = float64(stats.MemoryStats.Limit)
		if memoryLimit > 0 {
			memoryPerc = memory / memoryLimit * 100
		}
		read, write = blockTotals(stats.BlkioStats)
		pids = strconv.FormatUint(stats.PidsStats.Current, 10)
	}
	line := statsLine{
		BlockIO:   pairSize(float64(read), float64(write)),
		CPUPerc:   fmt.Sprintf("%.2f%%", cpu),
		Container: shortID(stats.ID),
		ID:        shortID(stats.ID),
		MemPerc:   fmt.Sprintf("%.2f%%", memoryPerc),
		MemUsage:  pairSize(memory, memoryLimit),
		Name:      name,
		NetIO:     pairSize(rx, tx),
		PIDs:      pids,
	}
	return json.Marshal(line)
}

func pairSize(in, out float64) string {
	return units.HumanSizeWithPrecision(in, 3) + " / " + units.HumanSizeWithPrecision(out, 3)
}

func unixCPU(previous, current container.CPUStats) float64 {
	cpuDelta := float64(current.CPUUsage.TotalUsage) - float64(previous.CPUUsage.TotalUsage)
	systemDelta := float64(current.SystemUsage) - float64(previous.SystemUsage)
	online := float64(current.OnlineCPUs)
	if online == 0 {
		online = float64(len(current.CPUUsage.PercpuUsage))
	}
	if systemDelta > 0 && cpuDelta > 0 {
		return cpuDelta / systemDelta * online * 100
	}
	return 0
}

func windowsCPU(stats container.StatsResponse) float64 {
	nanos := stats.Read.Sub(stats.PreRead).Nanoseconds()
	if nanos <= 0 || stats.NumProcs == 0 {
		return 0
	}
	possible := uint64(nanos/100) * uint64(stats.NumProcs)
	if possible == 0 || stats.CPUStats.CPUUsage.TotalUsage < stats.PreCPUStats.CPUUsage.TotalUsage {
		return 0
	}
	used := stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage
	return float64(used) / float64(possible) * 100
}

func unixMemory(stats container.MemoryStats) float64 {
	if value, ok := stats.Stats["total_inactive_file"]; ok && value < stats.Usage {
		return float64(stats.Usage - value)
	}
	if value := stats.Stats["inactive_file"]; value < stats.Usage {
		return float64(stats.Usage - value)
	}
	return float64(stats.Usage)
}

func networkTotals(networks map[string]container.NetworkStats) (float64, float64) {
	var rx, tx float64
	for _, network := range networks {
		rx += float64(network.RxBytes)
		tx += float64(network.TxBytes)
	}
	return rx, tx
}

func blockTotals(stats container.BlkioStats) (uint64, uint64) {
	var read, write uint64
	for _, entry := range stats.IoServiceBytesRecursive {
		if entry.Op == "" {
			continue
		}
		switch entry.Op[0] {
		case 'r', 'R':
			read += entry.Value
		case 'w', 'W':
			write += entry.Value
		}
	}
	return read, write
}
