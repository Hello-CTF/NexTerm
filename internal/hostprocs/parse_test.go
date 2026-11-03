package hostprocs

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Fixtures in testdata are constructed (no real hosts): procps-ng 4.x and macOS 14 ps layouts, and busybox
// layouts built from the output format of busybox 1.36 procps/ps.c (RSS through format_vsz with k/m/g
// suffixes, etime as "mm:ss" with unbounded minutes).
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func byPID(procs []HostProcess) map[int]HostProcess {
	m := map[int]HostProcess{}
	for _, p := range procs {
		m[p.PID] = p
	}
	return m
}

func TestParseElapsed(t *testing.T) {
	cases := map[string]int64{
		"00:07": 7, "59:01": 3541, "12:34:56": 45296, "3-04:05:06": 3*86400 + 4*3600 + 5*60 + 6, " 1:02": 62, "12-01:02:03": 12*86400 + 3723,
		"xx:yy": -1, "": -1, "1-2": -1, "1:2:3:4": -1,
	}
	for in, want := range cases {
		if got := parseElapsed(in); got != want {
			t.Errorf("parseElapsed(%q)=%d want %d", in, got, want)
		}
	}
}

func TestParsePSProcps(t *testing.T) {
	res, err := ParseOutput(fixture(t, "ps_procps.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Platform != "posix" || res.Format != "ps" || len(res.Partial) != 0 {
		t.Fatalf("meta: %+v", res)
	}
	m := byPID(res.Processes)
	if _, ok := m[901]; ok {
		t.Error("row with a non-numeric column must be skipped")
	}
	srv := m[812]
	if srv.User != "abcdefg+" || srv.Name != "server" || srv.ArgName != "server" || srv.Started != "Tue Nov 3 08:10:00 2026" || srv.CPU != 12.5 || srv.Mem != 3.2 || srv.RSSKiB != 262144 || srv.ElapsedSec != 7 {
		t.Errorf("server: %+v", srv)
	}
	if srv.Command != `/usr/lib/srv/server --name "a b" --port 80` {
		t.Errorf("command: %q", srv.Command)
	}
	if k := m[55]; k.Name != "kworker/0:1-events" || k.ArgName != "[kworker/0:1-events]" || k.ElapsedSec != 3541 {
		t.Errorf("kworker: %+v", k)
	}
	if z := m[900]; !z.Zombie || z.Name != "worker" || z.ArgName != "[worker]" {
		t.Errorf("zombie: %+v", z)
	}
	if w := m[1000]; w.ElapsedSec != -1 || w.Elapsed != "xx:yy" {
		t.Errorf("weird etime: %+v", w)
	}
	if m[1100].Name != "sshd:" || m[1100].ArgName != "sshd:" {
		t.Errorf("no comm entry falls back to the argv name: %+v", m[1100])
	}
	if m[1].Name != "systemd" || m[1].ArgName != "init" {
		t.Errorf("comm wins for the display name: %+v", m[1])
	}
	if m[1000].Name != "weird name" || m[1000].ArgName != "weird" {
		t.Errorf("comm with a space is kept whole: %+v", m[1000])
	}
	if m[1200].Name != "sync-服务" || m[1200].ArgName != "sync-服务" || m[1200].Command != "/opt/数据同步/sync-服务 --x" {
		t.Errorf("multibyte: %+v", m[1200])
	}
	if res.SkippedRows != 1 {
		t.Errorf("skipped rows = %d", res.SkippedRows)
	}
	if m[2].ElapsedSec != 3*86400+4*3600+5*60+6 || m[1].ElapsedSec != 45296 {
		t.Errorf("etime forms: %+v %+v", m[2], m[1])
	}
	// sorted by CPU desc then PID asc, never nil
	if res.Processes[0].PID != 812 || res.Processes[1].PID != 1100 {
		t.Errorf("order: %+v", res.Processes[:2])
	}
	for i := 1; i < len(res.Processes); i++ {
		a, b := res.Processes[i-1], res.Processes[i]
		if a.CPU < b.CPU || (a.CPU == b.CPU && a.PID > b.PID) {
			t.Errorf("not sorted at %d", i)
		}
	}
}

func TestParsePSMacOS(t *testing.T) {
	res, err := ParseOutput(fixture(t, "ps_macos.txt"))
	if err != nil {
		t.Fatal(err)
	}
	m := byPID(res.Processes)
	if m[501].Command != "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome --type=gpu" || m[501].ElapsedSec != 303 {
		t.Errorf("chrome: %+v", m[501])
	}
	if m[501].Name != "Google Chrome" || m[501].ArgName != "Google" || m[501].Started != "Tue Nov 3 08:00:00 2026" {
		t.Errorf("comm path with spaces keeps its basename: %+v", m[501])
	}
	if z := m[777]; !z.Zombie || z.Name != "<defunct>" {
		t.Errorf("zombie: %+v", z)
	}
	if m[778].ElapsedSec != 62 || m[778].Name != "-zsh" || m[1].ElapsedSec != 12*86400+3723 {
		t.Errorf("etime: %+v %+v", m[778], m[1])
	}
}

func TestParsePSBusybox(t *testing.T) {
	res, err := ParseOutput(fixture(t, "ps_busybox.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != "ps-busybox" || !reflect.DeepEqual(res.Partial, []string{"cpu", "mem"}) {
		t.Fatalf("meta: %+v", res)
	}
	if res.SkippedRows != 1 {
		t.Errorf("skipped rows = %d, want the unparsable RSS row", res.SkippedRows)
	}
	m := byPID(res.Processes)
	if m[120].Name != "node" || m[120].RSSKiB != 12*1024 || m[120].User != "app" || m[120].ElapsedSec != 30 || !m[40].Zombie {
		t.Errorf("rows: %+v", res.Processes)
	}
	if m[1].ElapsedSec != 1234*60+56 || m[1].RSSKiB != 1234 {
		t.Errorf("minutes above 59: %+v", m[1])
	}
	if m[121].RSSKiB != 1500<<20 || m[121].ElapsedSec != 99999*60 {
		t.Errorf("g suffix: %+v", m[121])
	}
	if m[120].Started != "" {
		t.Errorf("busybox has no start time: %+v", m[120])
	}
}

func TestParsePSBusyboxWithoutElapsed(t *testing.T) {
	res, err := ParseOutput(fixture(t, "ps_busybox_min.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != "ps-busybox-min" || !reflect.DeepEqual(res.Partial, []string{"cpu", "mem", "elapsed"}) || res.SkippedRows != 0 {
		t.Fatalf("meta: %+v", res)
	}
	m := byPID(res.Processes)
	if m[120].ElapsedSec != -1 || m[120].Elapsed != "" || m[120].RSSKiB != 12*1024 || m[120].Name != "node" || !m[40].Zombie {
		t.Errorf("rows: %+v", res.Processes)
	}
}

func TestParseRSS(t *testing.T) {
	cases := map[string]int64{"100": 100, "12m": 12288, "12M": 12288, "3g": 3 << 20, "2T": 2 << 30, "512k": 512, "0": 0}
	for in, want := range cases {
		if got, ok := parseRSS(in); !ok || got != want {
			t.Errorf("parseRSS(%q)=%d,%v want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "m", "x1", "-5", "1.5m", "12q"} {
		if _, ok := parseRSS(in); ok {
			t.Errorf("parseRSS(%q) must fail", in)
		}
	}
}

func TestParseOutputErrors(t *testing.T) {
	if _, err := ParseOutput("no sentinel here\n"); !errors.Is(err, ErrNoSentinel) {
		t.Errorf("want ErrNoSentinel, got %v", err)
	}
	if _, err := ParseOutput(sentinelPrefix + "none\n"); err == nil || errors.Is(err, ErrNoSentinel) {
		t.Errorf("none must be a plain error, got %v", err)
	}
	if _, err := ParseOutput(sentinelPrefix + "ps\nUSER PID COMMAND\n1 2 3\n"); err == nil {
		t.Error("unexpected header must fail")
	}
}

func TestParseWindows(t *testing.T) {
	res, err := ParseOutput(fixture(t, "windows_multi.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Platform != "windows" || res.Format != "windows-cim" || len(res.Partial) != 0 || len(res.Processes) != 2 {
		t.Fatalf("meta: %+v", res)
	}
	m := byPID(res.Processes)
	if res.Processes[0].PID != 0 {
		t.Error("sorted by cpu desc")
	}
	if idle := m[0]; idle.Command != "System Idle Process" || idle.User != `NT AUTHORITY\SYSTEM` {
		t.Errorf("idle: %+v", idle)
	}
	c := m[4120]
	if c.Name != "chrome.exe" || c.RSSKiB != 838860 || c.Mem != 10 || c.ElapsedSec != -1 || c.CPU != 4.2 || c.Command[0] != '"' {
		t.Errorf("chrome: %+v", c)
	}
}

func TestParseWindowsSingleObjectAndPartial(t *testing.T) {
	res, err := ParseOutput(fixture(t, "windows_single_nouser.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Processes) != 1 || res.Processes[0].PID != 321 || res.Processes[0].User != "" {
		t.Fatalf("%+v", res.Processes)
	}
	if !reflect.DeepEqual(res.Partial, []string{"user"}) {
		t.Errorf("partial: %v", res.Partial)
	}
	empty, err := ParseOutput(fixture(t, "windows_empty.txt"))
	if err != nil || empty.Processes == nil || len(empty.Processes) != 0 {
		t.Fatalf("empty: %+v %v", empty, err)
	}
	if !reflect.DeepEqual(empty.Partial, []string{"cpu", "mem", "user"}) {
		t.Errorf("partial: %v", empty.Partial)
	}
}

func TestParseWindowsValueWrapper(t *testing.T) {
	obj := `{"pid":7,"ppid":4,"name":"a.exe","cmd":"a.exe","rssKiB":1,"cpu":0,"user":null,"elapsedSec":1,"started":"2026-11-03T05:00:00.0000000Z"}`
	for name, items := range map[string]string{
		"wrapper array":  `{"value":[` + obj + `,` + obj + `],"Count":2}`,
		"wrapper object": `{"value":` + obj + `,"Count":1}`,
		"array":          `[` + obj + `,` + obj + `]`,
		"object":         obj,
	} {
		res, err := ParseOutput(sentinelPrefix + `windows-cim` + "\n" + `{"cpuOk":true,"userOk":true,"memTotalKiB":10,"items":` + items + `}`)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := 2
		if strings.Contains(name, "object") {
			want = 1
		}
		if len(res.Processes) != want || res.Processes[0].PID != 7 || res.Processes[0].Started != "2026-11-03T05:00:00.0000000Z" {
			t.Errorf("%s: %+v", name, res.Processes)
		}
	}
}

func TestParseIdentity(t *testing.T) {
	obj := `{"name":"chrome.exe","ppid":600,"started":"2026-11-03T05:00:00.0000000Z"}`
	cases := []struct {
		name, in string
		want     identity
		ok       bool
	}{
		{"procps", sentinelIDPS + "\n  600 Tue Nov  3 07:44:03 2026\n" + sentinelIDArgs + "\n/usr/bin/node /srv/app.js\n", identity{"node", 600, "Tue Nov 3 07:44:03 2026"}, true},
		{"procps odd lstart", sentinelIDPS + "\n 600 Mar 3 Nov\n" + sentinelIDArgs + "\nnode\n", identity{"node", 600, ""}, true},
		{"busybox", sentinelIDBusybox + "\n12\n/bin/sh -c x\n", identity{"sh", 12, ""}, true},
		{"gone", "", identity{}, false},
		{"empty args", sentinelIDPS + "\n600 Tue Nov  3 07:44:03 2026\n" + sentinelIDArgs + "\n\n", identity{}, false},
		{"windows object", sentinelIDWindows + "\r\n" + obj, identity{"chrome.exe", 600, "2026-11-03T05:00:00.0000000Z"}, true},
		{"windows array", sentinelIDWindows + "\n[" + obj + "]", identity{"chrome.exe", 600, "2026-11-03T05:00:00.0000000Z"}, true},
		{"windows wrapper", sentinelIDWindows + "\n" + `{"value":[` + obj + `],"Count":1}`, identity{"chrome.exe", 600, "2026-11-03T05:00:00.0000000Z"}, true},
		{"windows empty", sentinelIDWindows + "\n[]", identity{}, false},
		{"windows nothing", sentinelIDWindows + "\n", identity{}, false},
	}
	for _, tc := range cases {
		got, ok := parseIdentity(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: got %+v,%v want %+v,%v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestWindowsCritical(t *testing.T) {
	for _, n := range []string{"csrss", "CSRSS.EXE", "Wininit.exe", "smss.exe", "services.exe", "lsass.exe", "winlogon"} {
		if !isWindowsCritical(n) {
			t.Errorf("%q must be critical", n)
		}
	}
	for _, n := range []string{"chrome.exe", "svchost.exe", "csrss2.exe", ""} {
		if isWindowsCritical(n) {
			t.Errorf("%q must not be critical", n)
		}
	}
	if len(WindowsCriticalProcesses()) != 6 {
		t.Error("exported list")
	}
}
