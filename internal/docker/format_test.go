package docker

import (
	"encoding/json"
	"net/netip"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
)

func TestContainerAndImageDTOs(t *testing.T) {
	now := time.Unix(10_000, 0)
	containers := containerDTOs([]container.Summary{{
		ID:     "0123456789abcdef",
		Names:  []string{"/web-1", "/alias"},
		Image:  "nginx:latest",
		State:  container.StateRunning,
		Status: "Up 2 hours",
		Labels: map[string]string{"com.docker.compose.project": "shop"},
		Ports: []container.PortSummary{
			{PrivatePort: 443, PublicPort: 8443, Type: "tcp", IP: netip.MustParseAddr("::")},
			{PrivatePort: 80, PublicPort: 8080, Type: "tcp", IP: netip.MustParseAddr("127.0.0.1")},
			{PrivatePort: 53, Type: "udp"},
		},
	}})
	if len(containers) != 1 {
		t.Fatalf("got %d containers", len(containers))
	}
	got := containers[0]
	if got.ID != "0123456789ab" || got.Name != "web-1,alias" || got.ComposeProject == nil || *got.ComposeProject != "shop" {
		t.Fatalf("unexpected container DTO: %+v", got)
	}
	wantPorts := "53/udp, 127.0.0.1:8080->80/tcp, [::]:8443->443/tcp"
	if got.Ports != wantPorts {
		t.Fatalf("ports = %q, want %q", got.Ports, wantPorts)
	}
	images := imageDTOs([]image.Summary{{
		ID:       "sha256:abcdef0123456789",
		RepoTags: []string{"registry.local:5000/team/app:v1", "app:latest", "<none>:<none>"},
		Size:     15_500_000,
		Created:  now.Add(-2 * time.Hour).Unix(),
	}}, now)
	if len(images) != 3 {
		t.Fatalf("got %d image rows", len(images))
	}
	if images[0].Repository != "registry.local:5000/team/app" || images[0].Tag != "v1" {
		t.Fatalf("unexpected tagged image: %+v", images[0])
	}
	if images[2].Repository != noneLabel || images[2].Tag != noneLabel {
		t.Fatalf("unexpected dangling image: %+v", images[2])
	}
	for _, image := range images {
		if image.ID != "abcdef012345" || image.Size != "15.5MB" || image.CreatedSince != "2 hours ago" {
			t.Fatalf("unexpected image formatting: %+v", image)
		}
	}
}

func TestStatsFormattingLinuxCgroups(t *testing.T) {
	for _, test := range []struct {
		name  string
		stats map[string]uint64
	}{
		{name: "cgroup-v1", stats: map[string]uint64{"total_inactive_file": 1_000}},
		{name: "cgroup-v2", stats: map[string]uint64{"inactive_file": 1_000}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stats := container.StatsResponse{
				ID:   "0123456789abcdef",
				Name: "/worker",
				CPUStats: container.CPUStats{
					CPUUsage:    container.CPUUsage{TotalUsage: 200},
					SystemUsage: 2_000,
					OnlineCPUs:  2,
				},
				PreCPUStats: container.CPUStats{
					CPUUsage:    container.CPUUsage{TotalUsage: 100},
					SystemUsage: 1_000,
				},
				MemoryStats: container.MemoryStats{Usage: 8_000, Limit: 10_000, Stats: test.stats},
				Networks: map[string]container.NetworkStats{
					"eth0": {RxBytes: 100, TxBytes: 200},
				},
				BlkioStats: container.BlkioStats{IoServiceBytesRecursive: []container.BlkioStatEntry{
					{Op: "read", Value: 300}, {Op: "write", Value: 400},
				}},
				PidsStats: container.PidsStats{Current: 7},
			}
			line, err := formatStats(stats, container.Summary{})
			if err != nil {
				t.Fatal(err)
			}
			var got statsLine
			if err := json.Unmarshal(line, &got); err != nil {
				t.Fatal(err)
			}
			if got.CPUPerc != "20.00%" || got.MemPerc != "70.00%" || got.PIDs != "7" || got.Name != "worker" {
				t.Fatalf("unexpected stats: %+v", got)
			}
		})
	}
}

func TestStatsFormattingWindows(t *testing.T) {
	read := time.Unix(10_000, 0)
	stats := container.StatsResponse{
		ID:           "abcdef0123456789",
		OSType:       "windows",
		Read:         read,
		PreRead:      read.Add(-time.Second),
		NumProcs:     2,
		CPUStats:     container.CPUStats{CPUUsage: container.CPUUsage{TotalUsage: 1_000_000}},
		PreCPUStats:  container.CPUStats{CPUUsage: container.CPUUsage{TotalUsage: 0}},
		MemoryStats:  container.MemoryStats{PrivateWorkingSet: 4_096},
		StorageStats: container.StorageStats{ReadSizeBytes: 20, WriteSizeBytes: 30},
	}
	line, err := formatStats(stats, container.Summary{})
	if err != nil {
		t.Fatal(err)
	}
	var got statsLine
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatal(err)
	}
	if got.CPUPerc != "5.00%" || got.PIDs != "" || got.MemPerc != "0.00%" {
		t.Fatalf("unexpected Windows stats: %+v", got)
	}
}
