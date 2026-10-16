package guard

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestNormalizePath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input  string
		want   string
		wantOK bool
	}{
		{"", "", true},
		{"/var/log/app.log", "/var/log/app.log", true},
		{"  /var/log/app.log  ", "/var/log/app.log", true},
		{"/var//log/./app.log", "/var/log/app.log", true},
		{"/var/log/../log/app.log", "/var/log/app.log", true},
		{"/var/log/../etc/cron.d/x", "/var/etc/cron.d/x", true},
		{"/var/log/../../etc/cron.d/x", "/etc/cron.d/x", true},
		{"/ok/../..", "/", true},
		{"~", "~", true},
		{"~/a/../b", "~/b", true},
		{"../escape", "", false},
		{"a/../../escape", "", false},
		{"..", "", false},
	}
	for _, tc := range cases {
		got, ok := NormalizePath(tc.input)
		want := tc.want
		if tc.wantOK && want != "" {
			want = filepath.Clean(want)
		}
		if got != want || ok != tc.wantOK {
			t.Fatalf("NormalizePath(%q) = (%q, %v), want (%q, %v)", tc.input, got, ok, want, tc.wantOK)
		}
	}
}

func TestUnixBackslashPathSemantics(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Unix path semantics")
	}
	const name = `/tmp/a\b`
	if got, ok := NormalizePath(name); !ok || got != name {
		t.Fatalf("NormalizePath(%q) = (%q, %v), want unchanged path", name, got, ok)
	}
	if got := DirPattern(name); got != "/tmp/**" {
		t.Fatalf("DirPattern(%q) = %q, want /tmp/**", name, got)
	}
	if MatchPath("/tmp/a/**", name) {
		t.Fatal("Unix backslash was treated as a path separator")
	}
	if !MatchPath("/tmp/**", name) {
		t.Fatal("Unix file with backslash is outside its parent rule")
	}
}

func TestWindowsDriveRelativeParentRejected(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("Windows path semantics")
	}
	for _, name := range []string{`C:..\escape`, `..\..\etc\x`} {
		if got, ok := NormalizePath(name); ok {
			t.Fatalf("NormalizePath(%q) = (%q, true), want rejection", name, got)
		}
		if MatchPath("**", name) {
			t.Fatalf("MatchPath matched out-of-bounds path %q", name)
		}
	}
}

func TestWindowsUNCPathSemantics(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("Windows path semantics")
	}
	const name = `\\server\share\dir\file.txt`
	if got, ok := NormalizePath(name); !ok || got != name {
		t.Fatalf("NormalizePath(%q) = (%q, %v), want unchanged UNC path", name, got, ok)
	}
	if got := DirPattern(name); got != `//server/share/dir/**` {
		t.Fatalf("DirPattern(%q) = %q, want //server/share/dir/**", name, got)
	}
	if !MatchPath(`//server/share/dir/**`, name) {
		t.Fatal("UNC directory rule does not cover its file")
	}
	if MatchPath(`/server/share/**`, name) {
		t.Fatal("UNC path was collapsed to a drive-rooted path")
	}
	const rootFile = `\\server\share\file.txt`
	if got := DirPattern(rootFile); got != `//server/share/file.txt` {
		t.Fatalf("DirPattern(%q) = %q, want exact share-root file", rootFile, got)
	}
}

func TestWindowsLocalPathSemantics(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("Windows path semantics")
	}
	cases := []struct {
		input  string
		want   string
		wantOK bool
	}{
		{`C:\work\logs\app.log`, `C:\work\logs\app.log`, true},
		{` C:\work\\logs\.\app.log `, `C:\work\logs\app.log`, true},
		{`C:\work\logs\..\other\app.log`, `C:\work\other\app.log`, true},
		{`C:\ok\..\..`, `C:\`, true},
		{`..\escape`, "", false},
		{`logs\..\..\escape`, "", false},
	}
	for _, tc := range cases {
		got, ok := NormalizePath(tc.input)
		if got != tc.want || ok != tc.wantOK {
			t.Fatalf("NormalizePath(%q) = (%q, %v), want (%q, %v)", tc.input, got, ok, tc.want, tc.wantOK)
		}
	}
	patterns := map[string]string{
		`C:\work\logs\app.log`: "C:/work/logs/**",
		`C:\app.log`:           "C:/app.log",
		`logs\app.log`:         "logs/**",
	}
	for input, want := range patterns {
		if got := DirPattern(input); got != want {
			t.Fatalf("DirPattern(%q) = %q, want %q", input, got, want)
		}
	}
	matches := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"C:/work/logs/**", `C:\work\logs\second.log`, true},
		{"C:/work/logs/**", `C:\work\logs\nested\b.log`, true},
		{"C:/work/logs/**", `C:\work\other\b.log`, false},
		{"C:/work/logs/**", `C:\work\logs\..\other\b.log`, false},
		{"C:/work/logs/**", `C:\work\logs\..\logs\b.log`, true},
	}
	for _, tc := range matches {
		if got := MatchPath(tc.pattern, tc.value); got != tc.want {
			t.Fatalf("MatchPath(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
		}
	}
}

func TestMatchPathParentTraversal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"/var/log/**", "/var/log/../etc/cron.d/x", false},
		{"/var/log/**", "/var/log/..", false},
		{"/var/log/**", "/var/log/../log/app.log", true},
		{"/var/log/**", "../../etc/x", false},
		{"/var/log/**", "/../../etc/x", false},
		{"/var/log/*", "/var/log/..", false},
		{"**", "/var/log/../etc/cron.d/x", true},
		{"**", "../../etc/x", false},
	}
	for _, tc := range cases {
		if got := MatchPath(tc.pattern, tc.value); got != tc.want {
			t.Fatalf("MatchPath(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
		}
	}
}

func TestMatchPathSymbolicPaths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"~/logs/**", "~/logs/a.log", true},
		{"~/logs/**", "~/other/a.log", false},
		{"~/logs/**", "/home/u/logs/a.log", false},
		{"~", "~", true},
		{"logs/**", "logs/a.log", true},
		{"logs/**", "./logs/a.log", true},
		{"logs/**", "logs/../other/a.log", false},
		{"./logs/**", "logs/a.log", true},
	}
	for _, tc := range cases {
		if got := MatchPath(tc.pattern, tc.value); got != tc.want {
			t.Fatalf("MatchPath(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
		}
	}
}

func TestMatchPathDirAndFileRules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"/var/log/**", "/var/log", true},
		{"/var/log/**", "/var/log/nested/app.log", true},
		{"/var/log/**", "/var/log/./app.log", true},
		{"/var/log/**", "/var/logarchive/app.log", false},
		{"/etc/app.conf", "/etc/app.conf", true},
		{"/etc/app.conf", "/etc/./app.conf", true},
		{"/etc/app.conf", "/etc/other.conf", false},
		{"/etc/app.conf", "/etc/app.conf/child", false},
		{"/etc/app.conf", "/etc/subdir/../app.conf", true},
	}
	for _, tc := range cases {
		if got := MatchPath(tc.pattern, tc.value); got != tc.want {
			t.Fatalf("MatchPath(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
		}
	}
}

func TestGrantRuleDoesNotCoverParentTraversal(t *testing.T) {
	grants, _ := newTestGrants(t, nil)
	ctx := context.Background()
	if _, err := grants.AddRule(ctx, "device-a", "write_file", "/var/log/**", time.Time{}); err != nil {
		t.Fatal(err)
	}
	ruling := Confirm(KindWriteFS, "写入或编辑文件")
	config := Config{Mode: ReadWrite}
	if decision := grants.EvaluateResource(ctx, config, ruling, nil, "device-a", "write_file", "run-1", Resource{Action: "write_file", Path: "/var/log/../etc/cron.d/x"}); decision.Action != ActionAsk {
		t.Fatalf("traversal covered by rule: %+v", decision)
	}
	if decision := grants.EvaluateResource(ctx, config, ruling, nil, "device-a", "write_file", "run-1", Resource{Action: "write_file", Path: "../../etc/x"}); decision.Action != ActionAsk {
		t.Fatalf("escape covered by rule: %+v", decision)
	}
	if decision := grants.EvaluateResource(ctx, config, ruling, nil, "device-a", "write_file", "run-1", Resource{Action: "write_file", Path: "/var/log/../log/app.log"}); decision.Action != ActionAllow {
		t.Fatalf("normalized in-tree path not covered: %+v", decision)
	}
}

func TestMemoryPathRuleDoesNotCoverParentTraversal(t *testing.T) {
	t.Parallel()
	memory := NewMemory()
	memory.AddPathRule("write_file", "/var/log/**")
	if memory.CoversPathRule("write_file", "/var/log/../etc/cron.d/x") {
		t.Fatal("session path rule covered traversal")
	}
	if memory.CoversPathRule("write_file", "../../etc/x") {
		t.Fatal("session path rule covered escape")
	}
	if !memory.CoversPathRule("write_file", "/var/log/../log/app.log") {
		t.Fatal("session path rule does not cover normalized in-tree path")
	}
	ruling := Confirm(KindWriteFS, "写入或编辑文件")
	if decision := DecideResource(Config{Mode: ReadWrite}, ruling, memory, Resource{Action: "write_file", Path: "/var/log/../etc/cron.d/x"}); decision.Action != ActionAsk {
		t.Fatalf("traversal covered by session rule: %+v", decision)
	}
}
