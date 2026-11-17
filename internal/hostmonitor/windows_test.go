package hostmonitor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/platform/psquote"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func windowsOutput(body string) string {
	return windowsSentinel + "\r\n" + body + "\r\n"
}

func diskMounts(disks []Disk) string {
	mounts := make([]string, 0, len(disks))
	for _, disk := range disks {
		mounts = append(mounts, disk.Mount)
	}
	return strings.Join(mounts, ",")
}

func netNames(net []netCounters) string {
	names := make([]string, 0, len(net))
	for _, iface := range net {
		names = append(names, iface.name)
	}
	return strings.Join(names, ",")
}

func TestWindowsSampleCommandCarriesTheScript(t *testing.T) {
	for _, withDisks := range []bool{true, false} {
		command := WindowsSampleCommand(withDisks)
		// cmd.exe, the default shell of Windows OpenSSH, refuses command lines longer than 8191 characters.
		if len(command) > 8191 {
			t.Fatalf("command is %d characters long", len(command))
		}
		script, err := psquote.DecodeCommand(command)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(script, windowsSentinel) || !strings.Contains(script, "ConvertTo-Json -Compress") {
			t.Fatalf("script = %q", script)
		}
		if strings.Contains(script, "Win32_LogicalDisk") != withDisks {
			t.Fatalf("withDisks %v: script = %q", withDisks, script)
		}
	}
}

func TestParseWindowsOutputServer(t *testing.T) {
	raw, err := ParseWindowsOutput(readSample(t, "windows_server_1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if raw.OS != windowsOS || raw.Load != nil {
		t.Fatalf("os %q, load %+v", raw.OS, raw.Load)
	}
	wantMemory := Memory{
		TotalBytes: 16658016 * 1024, AvailableBytes: 9123456 * 1024, UsedBytes: (16658016 - 9123456) * 1024,
		SwapTotalBytes: 2490368 * 1024, SwapUsedBytes: (2490368 - 2401024) * 1024,
	}
	if raw.Memory == nil || *raw.Memory != wantMemory {
		t.Fatalf("memory = %+v, want %+v", raw.Memory, wantMemory)
	}
	if raw.Uptime == nil || !near(*raw.Uptime, 86400) {
		t.Fatalf("uptime = %v", raw.Uptime)
	}
	if raw.CPU == nil || raw.CPU.total != 133758864000000000 || raw.CPU.idle != 124416000000000 || raw.CPUCount != 8 {
		t.Fatalf("cpu = %+v, count %d", raw.CPU, raw.CPUCount)
	}
	// The ISATAP tunnel is left out like loopback on Linux.
	if got := netNames(raw.Net); got != "Intel[R] Ethernet Connection [7] I219-V,Hyper-V Virtual Ethernet Adapter" || raw.Net[0].rx != 4294967000 {
		t.Fatalf("network = %+v", raw.Net)
	}
	// The system drive first, then the fullest.
	if got := diskMounts(raw.Disks); got != "C:,D:,E:" {
		t.Fatalf("disks = %+v", raw.Disks)
	}
	c := raw.Disks[0]
	if c.Filesystem != "NTFS" || c.TotalBytes != 510770802688 || c.AvailableBytes != 201234567168 || c.UsedBytes != 510770802688-201234567168 || !near(c.UsedPercent, float64(c.UsedBytes)/float64(c.TotalBytes)*100) {
		t.Fatalf("C: = %+v", c)
	}
}

func TestComputeWindowsAcrossSamples(t *testing.T) {
	first, err := ParseWindowsOutput(readSample(t, "windows_server_1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseWindowsOutput(readSample(t, "windows_server_2.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// The wall time is ignored in favour of the host's own uptime, which advanced by 3 seconds.
	snapshot := Compute(&first, second, 10*time.Second)
	if snapshot.Platform != windowsOS || snapshot.Load != nil || snapshot.CPUCount != 8 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.CPUPercent == nil || !near(*snapshot.CPUPercent, 25) {
		t.Fatalf("cpu = %v", snapshot.CPUPercent)
	}
	ethernet, hyperV := snapshot.Network[0], snapshot.Network[1]
	if ethernet.RxRate == nil || !near(*ethernet.RxRate, 1048576) || ethernet.TxRate == nil || !near(*ethernet.TxRate, 65536) {
		t.Fatalf("ethernet = %+v", ethernet)
	}
	if hyperV.RxRate == nil || *hyperV.RxRate != 0 {
		t.Fatalf("idle adapter = %+v", hyperV)
	}
}

func TestComputeWindowsCounterResetAndReboot(t *testing.T) {
	first, _ := ParseWindowsOutput(readSample(t, "windows_server_1.txt"))
	second, _ := ParseWindowsOutput(readSample(t, "windows_server_2.txt"))

	// An adapter whose counters went backwards (driver reset, 32-bit wrap) yields no rate; the others keep theirs.
	reset := second
	reset.Net = []netCounters{{name: first.Net[0].name, rx: 10, tx: 10}, second.Net[1]}
	snapshot := Compute(&first, reset, 3*time.Second)
	if snapshot.Network[0].RxRate != nil || snapshot.Network[0].TxRate != nil || snapshot.Network[1].RxRate == nil {
		t.Fatalf("after reset = %+v", snapshot.Network)
	}
	if snapshot.CPUPercent == nil {
		t.Fatal("cpu lost by an adapter reset")
	}

	// After a reboot the uptime went backwards and no counter is compared.
	rebooted := second
	uptime := 60.0
	rebooted.Uptime = &uptime
	rebooted.CPU = &cpuTimes{total: second.CPU.total, idle: 1}
	snapshot = Compute(&first, rebooted, 3*time.Second)
	if snapshot.CPUPercent != nil || snapshot.Network[0].RxRate != nil {
		t.Fatalf("after reboot = %+v", snapshot)
	}
}

func TestParseWindowsOutputEdgeCases(t *testing.T) {
	t.Run("missing sections", func(t *testing.T) {
		raw, err := ParseWindowsOutput(windowsOutput(`{"os":{"mem":1024,"free":256,"drive":"C:"},"errors":["cpu: Invalid class "]}`))
		if err != nil {
			t.Fatal(err)
		}
		if raw.Memory == nil || raw.Memory.UsedBytes != 768*1024 || raw.Memory.SwapTotalBytes != 0 || raw.CPU != nil || raw.Uptime != nil || len(raw.Net) != 0 || len(raw.Disks) != 0 {
			t.Fatalf("raw = %+v", raw)
		}
	})
	t.Run("single elements and nulls", func(t *testing.T) {
		// A one-element list may come back as a bare object; a null counter drops only its entry.
		raw, err := ParseWindowsOutput(windowsOutput(`{"net":{"name":"Ethernet","rx":5,"tx":6},"disks":[{"id":"C:","fs":"NTFS","size":100,"free":40},{"id":"F:","fs":null,"size":null,"free":null}],"cpu":null,"up":null}`))
		if err != nil {
			t.Fatal(err)
		}
		if netNames(raw.Net) != "Ethernet" || raw.Net[0].tx != 6 || diskMounts(raw.Disks) != "C:" || raw.Disks[0].UsedPercent != 60 {
			t.Fatalf("raw = %+v", raw)
		}
	})
	t.Run("wrapped arrays", func(t *testing.T) {
		raw, err := ParseWindowsOutput(windowsOutput(`{"net":{"value":[{"name":"a","rx":1,"tx":2},{"name":"b","rx":3,"tx":4}],"Count":2},"errors":{"value":[],"Count":0}}`))
		if err != nil || netNames(raw.Net) != "a,b" {
			t.Fatalf("raw = %+v, %v", raw, err)
		}
	})
	t.Run("malformed values", func(t *testing.T) {
		// Negative, fractional and string numbers invalidate their section or entry instead of being guessed at.
		raw, err := ParseWindowsOutput(windowsOutput(`{"os":{"mem":-1,"free":5},"cpu":{"idle":1.5,"ts":10,"n":2},"up":{"boot":"0","ts":10,"freq":1},"net":[{"name":"a","rx":-1,"tx":1},{"name":"b","rx":1,"tx":2}],"disks":[{"id":"C:","size":10,"free":20}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if raw.Memory != nil || raw.CPU != nil || raw.Uptime != nil || netNames(raw.Net) != "b" {
			t.Fatalf("raw = %+v", raw)
		}
		// More free space than size is clamped rather than underflowing.
		if len(raw.Disks) != 1 || raw.Disks[0].UsedBytes != 0 || raw.Disks[0].AvailableBytes != 10 {
			t.Fatalf("disks = %+v", raw.Disks)
		}
	})
	t.Run("many volumes", func(t *testing.T) {
		raw, err := ParseWindowsOutput(windowsOutput(`{"os":{"mem":1,"free":1,"drive":"D:"},"disks":[` +
			`{"id":"C:","size":100,"free":90},{"id":"D:","size":100,"free":95},{"id":"E:","size":100,"free":10},` +
			`{"id":"F:","size":100,"free":50},{"id":"G:","size":100,"free":0},{"id":"H:","size":100,"free":70},` +
			`{"id":"c:","size":100,"free":1},{"id":"Z:","size":0,"free":0}]}`))
		if err != nil {
			t.Fatal(err)
		}
		// The system drive leads even when it is the emptiest; duplicates and empty drives are dropped; at most
		// maxDisks are kept.
		if got := diskMounts(raw.Disks); got != "D:,G:,E:,F:,H:" {
			t.Fatalf("disks = %s", got)
		}
	})
	t.Run("many interfaces", func(t *testing.T) {
		var entries []string
		for i := range maxInterfaces + 5 {
			entries = append(entries, `{"name":"nic`+string(rune('a'+i%26))+strings.Repeat("x", i/26)+`","rx":1,"tx":1}`)
		}
		entries = append(entries, `{"name":"Teredo Tunneling Pseudo-Interface","rx":1,"tx":1}`, `{"name":"nica","rx":9,"tx":9}`)
		raw, err := ParseWindowsOutput(windowsOutput(`{"net":[` + strings.Join(entries, ",") + `]}`))
		if err != nil {
			t.Fatal(err)
		}
		if len(raw.Net) != maxInterfaces+5 || raw.Net[0].rx != 1 || strings.Contains(netNames(raw.Net), "Teredo") {
			t.Fatalf("net = %+v", raw.Net)
		}
		if snapshot := Compute(nil, raw, 0); len(snapshot.Network) != maxInterfaces {
			t.Fatalf("snapshot keeps %d interfaces", len(snapshot.Network))
		}
	})
}

func TestParseWindowsOutputFailures(t *testing.T) {
	if _, err := ParseWindowsOutput(readSample(t, "windows_cmd.txt")); !errors.Is(err, ErrNoSentinel) {
		t.Fatalf("cmd output: %v", err)
	}
	if _, err := ParseWindowsOutput(windowsOutput(`{"os":{"mem":1`)); !errors.Is(err, ErrNoMetrics) {
		t.Fatalf("cut JSON: %v", err)
	}
	if _, err := ParseWindowsOutput(windowsSentinel); !errors.Is(err, ErrNoMetrics) {
		t.Fatalf("no JSON: %v", err)
	}
	_, err := ParseWindowsOutput(readSample(t, "windows_cim_denied.txt"))
	if !errors.Is(err, ErrNoMetrics) {
		t.Fatalf("denied: %v", err)
	}
	if reason := windowsFailure(err); !strings.Contains(reason, "Windows") || !strings.Contains(reason, "os: Access denied") {
		t.Fatalf("reason = %q", reason)
	}
	if reason := windowsFailure(ErrNoMetrics); reason != "无法读取 Windows 主机指标" {
		t.Fatalf("bare reason = %q", reason)
	}
}

func TestWindowsOSName(t *testing.T) {
	for name, want := range map[string]bool{
		"MINGW64_NT-10.0-19045": true, "MSYS_NT-10.0": true, "CYGWIN_NT-10.0": true, "Windows_NT": true,
		"Linux": false, "Darwin": false, "": false,
	} {
		if got := windowsOSName(name); got != want {
			t.Errorf("%q: %v", name, got)
		}
	}
}

func newTargetService(t *testing.T, target Target) (*Service, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	service := NewService(func(context.Context, string) (Target, error) { return target, nil })
	service.now = clock.Now
	return service, clock
}

func TestSampleFallsBackToPowerShellAndRemembersIt(t *testing.T) {
	for _, file := range []string{"windows_cmd.txt", "mingw"} {
		t.Run(file, func(t *testing.T) {
			reply := func(context.Context, int32) (base.ExecResult, error) {
				// Git for Windows on PATH: sh runs, but uname names the POSIX layer.
				code := 0
				return base.ExecResult{Stdout: "@@nexterm-monitor os MINGW64_NT-10.0-19045\n@@nexterm-monitor end\n", ExitCode: &code}, nil
			}
			if file != "mingw" {
				reply = sampleReplies(t, file)
			}
			runner := &fakeRunner{reply: reply, windows: sampleReplies(t, "windows_server_1.txt", "windows_server_2.txt")}
			service, clock := newTestService(t, runner)
			first, err := service.Sample(t.Context(), "s1")
			if err != nil || first.Status != StatusOK || first.Platform != windowsOS || first.Memory == nil || first.CPUPercent != nil || first.Load != nil {
				t.Fatalf("first = %+v, %v", first, err)
			}
			clock.Advance(DefaultInterval)
			second, err := service.Sample(t.Context(), "s1")
			if err != nil || second.CPUPercent == nil || !near(*second.CPUPercent, 25) {
				t.Fatalf("second = %+v, %v", second, err)
			}
			if runner.calls.Load() != 1 || runner.windowsCalls.Load() != 2 {
				t.Fatalf("POSIX runs %d, PowerShell runs %d; the POSIX script must run only to find out", runner.calls.Load(), runner.windowsCalls.Load())
			}
		})
	}
}

func TestSampleKnownWindowsRunsOnlyPowerShell(t *testing.T) {
	runner := &fakeRunner{reply: sampleReplies(t, "linux_ubuntu_1.txt"), windows: sampleReplies(t, "windows_server_1.txt")}
	service, _ := newTargetService(t, Target{Runner: runner, Platform: PlatformWindows})
	snapshot, err := service.Sample(t.Context(), "s1")
	if err != nil || snapshot.Status != StatusOK || len(snapshot.Disks) != 3 || runner.calls.Load() != 0 || runner.windowsWithDisks.Load() != 1 {
		t.Fatalf("snapshot = %+v, %v, POSIX runs %d", snapshot, err, runner.calls.Load())
	}
}

func TestSampleKnownWindowsWithoutPowerShellIsUnsupported(t *testing.T) {
	runner := &fakeRunner{reply: sampleReplies(t, "linux_ubuntu_1.txt")}
	service, clock := newTargetService(t, Target{Runner: runner, Platform: PlatformWindows})
	for range 2 {
		snapshot, err := service.Sample(t.Context(), "s1")
		if err != nil || snapshot.Status != StatusUnsupported || snapshot.Reason != reasonNoPowerShell {
			t.Fatalf("snapshot = %+v, %v", snapshot, err)
		}
		clock.Advance(time.Minute)
	}
	if runner.windowsCalls.Load() != 1 || runner.calls.Load() != 0 {
		t.Fatalf("PowerShell runs %d, POSIX runs %d", runner.windowsCalls.Load(), runner.calls.Load())
	}
}

func TestSampleKnownPOSIXDoesNotTryPowerShell(t *testing.T) {
	runner := &fakeRunner{reply: sampleReplies(t, "windows_cmd.txt"), windows: sampleReplies(t, "windows_server_1.txt")}
	service, _ := newTargetService(t, Target{Runner: runner, Platform: PlatformPOSIX})
	snapshot, err := service.Sample(t.Context(), "s1")
	if err != nil || snapshot.Status != StatusUnsupported || snapshot.Reason != reasonNoPOSIXSh || runner.windowsCalls.Load() != 0 {
		t.Fatalf("snapshot = %+v, %v, PowerShell runs %d", snapshot, err, runner.windowsCalls.Load())
	}
}

func TestSampleWindowsCIMFailureIsRetriedWithItsReason(t *testing.T) {
	runner := &fakeRunner{windows: sampleReplies(t, "windows_cim_denied.txt", "windows_server_1.txt")}
	service, clock := newTargetService(t, Target{Runner: runner, Platform: PlatformWindows})
	failed, err := service.Sample(t.Context(), "s1")
	if err != nil || failed.Status != StatusError || !strings.Contains(failed.Reason, "Access denied") || failed.RetryAfterMs != backoff(1).Milliseconds() {
		t.Fatalf("failed = %+v, %v", failed, err)
	}
	clock.Advance(backoff(1))
	recovered, err := service.Sample(t.Context(), "s1")
	if err != nil || recovered.Status != StatusOK || recovered.Memory == nil {
		t.Fatalf("recovered = %+v, %v", recovered, err)
	}
}

func TestSampleFallbackRemembersWindowsAcrossAFailure(t *testing.T) {
	runner := &fakeRunner{reply: sampleReplies(t, "windows_cmd.txt"), windows: sampleReplies(t, "windows_cim_denied.txt", "windows_server_1.txt")}
	service, clock := newTestService(t, runner)
	if failed, _ := service.Sample(t.Context(), "s1"); failed.Status != StatusError {
		t.Fatalf("failed = %+v", failed)
	}
	clock.Advance(backoff(1))
	if recovered, err := service.Sample(t.Context(), "s1"); err != nil || recovered.Status != StatusOK {
		t.Fatalf("recovered = %+v, %v", recovered, err)
	}
	if runner.calls.Load() != 1 || runner.windowsCalls.Load() != 2 {
		t.Fatalf("POSIX runs %d, PowerShell runs %d", runner.calls.Load(), runner.windowsCalls.Load())
	}
}

func TestSampleWindowsTimeoutKeepsDisks(t *testing.T) {
	output := readSample(t, "windows_server_1.txt")
	runner := &fakeRunner{windows: func(_ context.Context, call int32) (base.ExecResult, error) {
		if call == 1 {
			return base.ExecResult{}, context.DeadlineExceeded
		}
		code := 0
		return base.ExecResult{Stdout: output, ExitCode: &code}, nil
	}}
	service, clock := newTargetService(t, Target{Runner: runner, Platform: PlatformWindows})
	if snapshot, _ := service.Sample(t.Context(), "s1"); snapshot.Status != StatusError {
		t.Fatalf("first = %+v", snapshot)
	}
	clock.Advance(backoff(1))
	snapshot, err := service.Sample(t.Context(), "s1")
	if err != nil || snapshot.Status != StatusOK || snapshot.DisksUnavailable || len(snapshot.Disks) == 0 || runner.windowsWithDisks.Load() != 2 {
		t.Fatalf("after timeout = %+v, %v", snapshot, err)
	}
}
