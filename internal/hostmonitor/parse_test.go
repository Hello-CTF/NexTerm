package hostmonitor

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readSample(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestParseOutputLinux(t *testing.T) {
	tests := []struct {
		file      string
		cpuTotal  uint64
		cpuIdle   uint64
		cpuCount  int
		memory    Memory
		load      Load
		uptime    float64
		ifaces    []string
		firstRx   uint64
		disks     []string
		rootTotal uint64
	}{
		{
			file:     "linux_ubuntu_1.txt",
			cpuTotal: 144959 + 36930 + 60958182 + 3370 + 9088, cpuIdle: 60958182 + 3370, cpuCount: 15,
			memory: Memory{
				TotalBytes: 12205776 * 1024, AvailableBytes: 9919620 * 1024, UsedBytes: (12205776 - 9919620) * 1024,
				SwapTotalBytes: 16777212 * 1024, SwapUsedBytes: (16777212 - 16777152) * 1024,
			},
			load:   Load{One: 5.87, Five: 2.59, Fifteen: 1.22},
			uptime: 40795.22,
			ifaces: []string{"enp2s0", "docker0", "vethc18f795"}, firstRx: 43966983,
			// tmpfs, efivarfs, the /run credentials and the /dev/shm mounts hold no disk.
			disks:     []string{"/", "/share", "/boot", "/boot/efi"},
			rootTotal: 202051056 * 1024,
		},
		{
			// A kernel before 3.14 has no MemAvailable, no steal columns and no swap; busybox df lacks -l.
			file:     "linux_busybox_old_kernel.txt",
			cpuTotal: 2255 + 34 + 2290 + 22625563 + 6290 + 127 + 456, cpuIdle: 22625563 + 6290, cpuCount: 2,
			memory: Memory{
				TotalBytes: 1017984 * 1024, AvailableBytes: (201324 + 61216 + 400040) * 1024,
				UsedBytes: (1017984 - 201324 - 61216 - 400040) * 1024,
			},
			load:   Load{One: 0, Five: 0.01, Fifteen: 0.05},
			uptime: 350735.47,
			ifaces: []string{"eth0"}, firstRx: 4294967000,
			disks:     []string{"/"},
			rootTotal: 20509308 * 1024,
		},
	}
	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			raw, err := ParseOutput(readSample(t, test.file))
			if err != nil {
				t.Fatal(err)
			}
			if raw.OS != "Linux" {
				t.Fatalf("OS = %q", raw.OS)
			}
			if raw.CPU == nil || raw.CPU.total != test.cpuTotal || raw.CPU.idle != test.cpuIdle || raw.CPUCount != test.cpuCount {
				t.Fatalf("cpu = %+v count %d", raw.CPU, raw.CPUCount)
			}
			if raw.Memory == nil || *raw.Memory != test.memory {
				t.Fatalf("memory = %+v, want %+v", raw.Memory, test.memory)
			}
			if raw.Load == nil || !near(raw.Load.One, test.load.One) || !near(raw.Load.Five, test.load.Five) || !near(raw.Load.Fifteen, test.load.Fifteen) {
				t.Fatalf("load = %+v", raw.Load)
			}
			if raw.Uptime == nil || !near(*raw.Uptime, test.uptime) {
				t.Fatalf("uptime = %v", raw.Uptime)
			}
			var names []string
			for _, iface := range raw.Net {
				names = append(names, iface.name)
			}
			if strings.Join(names, ",") != strings.Join(test.ifaces, ",") || raw.Net[0].rx != test.firstRx {
				t.Fatalf("interfaces = %+v", raw.Net)
			}
			var mounts []string
			for _, disk := range raw.Disks {
				mounts = append(mounts, disk.Mount)
			}
			if strings.Join(mounts, ",") != strings.Join(test.disks, ",") || raw.Disks[0].TotalBytes != test.rootTotal {
				t.Fatalf("disks = %+v", raw.Disks)
			}
		})
	}
}

func TestParseOutputUnsupportedHosts(t *testing.T) {
	raw, err := ParseOutput(readSample(t, "darwin.txt"))
	if err != nil || raw.OS != "Darwin" || raw.CPU != nil || raw.Memory != nil {
		t.Fatalf("darwin = %+v, %v", raw, err)
	}
	if _, err := ParseOutput(readSample(t, "windows_cmd.txt")); !errors.Is(err, ErrNoSentinel) {
		t.Fatalf("windows cmd error = %v", err)
	}
	if _, err := ParseOutput(""); !errors.Is(err, ErrNoSentinel) {
		t.Fatalf("empty output error = %v", err)
	}
}

func TestParseOutputLinuxWithoutProcIsAnError(t *testing.T) {
	_, err := ParseOutput("@@nexterm-monitor os Linux\n@@nexterm-monitor stat\n@@nexterm-monitor meminfo\n@@nexterm-monitor end\n")
	if !errors.Is(err, ErrNoMetrics) {
		t.Fatalf("error = %v", err)
	}
}

func TestParseOutputToleratesBrokenSections(t *testing.T) {
	out := strings.Join([]string{
		"motd banner printed by the login shell",
		"@@nexterm-monitor os Linux",
		"@@nexterm-monitor stat",
		"cpu  x y z",
		"@@nexterm-monitor loadavg",
		"0.50 0.25 0.10 1/100 42",
		"@@nexterm-monitor df",
		"Filesystem 1024-blocks Used Available Capacity Mounted on",
		"/dev/sdb1 1000 500 500 50% /mnt/my disk",
		"/dev/sda1 1000 100 900 10% /",
		"@@nexterm-monitor end",
	}, "\r\n")
	raw, err := ParseOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if raw.CPU != nil || raw.Memory != nil || raw.Load == nil || raw.Load.One != 0.5 {
		t.Fatalf("raw = %+v", raw)
	}
	if len(raw.Disks) != 2 || raw.Disks[0].Mount != "/" || raw.Disks[1].Mount != "/mnt/my disk" || raw.Disks[1].UsedPercent != 50 {
		t.Fatalf("disks = %+v", raw.Disks)
	}
}

func TestParseDFKeepsRootAndFullestOthers(t *testing.T) {
	lines := []string{
		"Filesystem 1024-blocks Used Available Capacity Mounted on",
		"/dev/sdb1 100 10 90 10% /data10",
		"/dev/sdc1 100 90 10 90% /data90",
		"/dev/sdd1 100 50 50 50% /data50",
		"/dev/sde1 100 70 30 70% /data70",
		"/dev/sdf1 100 30 70 30% /data30",
		"/dev/sdg1 100 80 20 80% /data80",
		"/dev/loop0 100 100 0 100% /snap/core/1",
		"/dev/sda1 100 1 99 1% /",
	}
	var mounts []string
	for _, disk := range parseDF(lines) {
		mounts = append(mounts, disk.Mount)
	}
	if got := strings.Join(mounts, ","); got != "/,/data90,/data80,/data70,/data50" {
		t.Fatalf("mounts = %s", got)
	}
}

func TestComputeDeltasFromCapturedSamples(t *testing.T) {
	first, err := ParseOutput(readSample(t, "linux_ubuntu_1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseOutput(readSample(t, "linux_ubuntu_2.txt"))
	if err != nil {
		t.Fatal(err)
	}

	initial := Compute(nil, first, 0)
	if initial.Status != StatusOK || initial.CPUPercent != nil || len(initial.Network) != 3 || initial.Network[0].RxRate != nil {
		t.Fatalf("first snapshot = %+v", initial)
	}

	// The wall clock disagrees with the host's uptime on purpose: the host's own clock wins.
	snapshot := Compute(&first, second, 10*time.Second)
	// busy = total delta 4515 - idle delta (3622 + 122).
	if snapshot.CPUPercent == nil || !near(*snapshot.CPUPercent, float64(4515-3744)/4515*100) {
		t.Fatalf("cpu = %v", snapshot.CPUPercent)
	}
	elapsed := 40798.25 - 40795.22
	enp := snapshot.Network[0]
	if enp.Name != "enp2s0" || enp.RxRate == nil || enp.TxRate == nil ||
		!near(*enp.RxRate, float64(44074921-43966983)/elapsed) || !near(*enp.TxRate, float64(78056460-77986511)/elapsed) {
		t.Fatalf("enp2s0 = %+v", enp)
	}
	if snapshot.Memory == nil || snapshot.Memory.AvailableBytes != 9661052*1024 || snapshot.Load == nil || snapshot.Disks[0].Mount != "/" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestComputeSkipsDeltasAcrossResetsAndReboots(t *testing.T) {
	uptime := func(v float64) *float64 { return &v }
	prev := Raw{OS: "Linux", CPU: &cpuTimes{total: 1000, idle: 500}, Uptime: uptime(100), Net: []netCounters{{name: "eth0", rx: 5000, tx: 5000}, {name: "gone", rx: 1, tx: 1}}}

	counterReset := Raw{OS: "Linux", CPU: &cpuTimes{total: 1100, idle: 550}, Uptime: uptime(102), Net: []netCounters{{name: "eth0", rx: 10, tx: 7000}, {name: "new0", rx: 1, tx: 1}}}
	snapshot := Compute(&prev, counterReset, time.Second)
	if snapshot.CPUPercent == nil || *snapshot.CPUPercent != 50 {
		t.Fatalf("cpu = %v", snapshot.CPUPercent)
	}
	if snapshot.Network[0].RxRate != nil || snapshot.Network[0].TxRate == nil || *snapshot.Network[0].TxRate != 1000 {
		t.Fatalf("eth0 = %+v", snapshot.Network[0])
	}
	if snapshot.Network[1].RxRate != nil || snapshot.Network[1].TxRate != nil {
		t.Fatalf("a new interface has no rate yet: %+v", snapshot.Network[1])
	}

	rebooted := Raw{OS: "Linux", CPU: &cpuTimes{total: 2000, idle: 1000}, Uptime: uptime(5), Net: []netCounters{{name: "eth0", rx: 6000, tx: 6000}}}
	snapshot = Compute(&prev, rebooted, time.Second)
	if snapshot.CPUPercent != nil || snapshot.Network[0].RxRate != nil {
		t.Fatalf("deltas across a reboot = %+v", snapshot)
	}
}
