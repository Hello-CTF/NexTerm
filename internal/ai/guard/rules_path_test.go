package guard

import (
	"context"
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
		if got != tc.want || ok != tc.wantOK {
			t.Fatalf("NormalizePath(%q) = (%q, %v), want (%q, %v)", tc.input, got, ok, tc.want, tc.wantOK)
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
