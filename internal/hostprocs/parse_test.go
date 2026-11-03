package hostprocs

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

// Fixtures in testdata are constructed (no real hosts): procps-ng 4.x, macOS 14 ps and busybox 1.36 layouts.
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func byPID(procs []Process) map[int]Process {
	m := map[int]Process{}
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
	if srv.User != "abcdefg+" || srv.Name != "server" || srv.CPU != 12.5 || srv.Mem != 3.2 || srv.RSSKiB != 262144 || srv.ElapsedSec != 7 {
		t.Errorf("server: %+v", srv)
	}
	if srv.Command != `/usr/lib/srv/server --name "a b" --port 80` {
		t.Errorf("command: %q", srv.Command)
	}
	if k := m[55]; k.Name != "[kworker/0:1-events]" || k.ElapsedSec != 3541 {
		t.Errorf("kworker: %+v", k)
	}
	if z := m[900]; !z.Zombie || z.Name != "[worker]" {
		t.Errorf("zombie: %+v", z)
	}
	if w := m[1000]; w.ElapsedSec != -1 || w.Elapsed != "xx:yy" {
		t.Errorf("weird etime: %+v", w)
	}
	if m[1100].Name != "sshd:" {
		t.Errorf("trailing colon is kept: %+v", m[1100])
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
	if z := m[777]; !z.Zombie {
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
	m := byPID(res.Processes)
	if m[120].Name != "node" || m[120].RSSKiB != 4096 || m[120].User != "app" || m[120].ElapsedSec != 30 || !m[40].Zombie {
		t.Errorf("rows: %+v", res.Processes)
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

func TestRecheckMatching(t *testing.T) {
	if !posixNameMatches("/usr/bin/node /srv/app.js\n", "node") || posixNameMatches("", "node") || posixNameMatches("bash", "node") {
		t.Error("posix name match")
	}
	if !windowsImageMatches("\"Chrome.exe\",\"4120\",\"Console\",\"1\",\"100 K\"\r\n", "chrome.exe") {
		t.Error("case-insensitive image match")
	}
	if windowsImageMatches("INFO: No tasks are running which match the specified criteria.\r\n", "chrome.exe") {
		t.Error("no task must not match")
	}
	long := "a_very_long_process_name_x.exe"
	if !windowsImageMatches("\""+long[:25]+"\",\"7\",\"Services\",\"0\",\"1 K\"", long) {
		t.Error("tasklist truncates image names to 25 chars")
	}
}
