package guard

import (
	"context"
	"testing"
	"time"
)

func TestMatchPathSegments(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"", "/any/thing", true},
		{"/var/log/**", "/var/log/app.log", true},
		{"/var/log/**", "/var/log/nested/app.log", true},
		{"/var/log/**", "/var/log", true},
		{"/var/log/**", "/var/logarchive/app.log", false},
		{"/var/log/**", "/etc/app.log", false},
		{"/var/log/*", "/var/log/app.log", true},
		{"/var/log/*", "/var/log/nested/app.log", false},
		{"/var/*/app.log", "/var/log/app.log", true},
		{"/var/*/app.log", "/var/log/x/app.log", false},
		{"/exact/path", "/exact/path", true},
		{"/exact/path", "/exact/path/child", false},
		{"/var/log/**", "", false},
		{"/tmp/**", "/tmp", true},
	}
	for _, tc := range cases {
		if got := MatchPath(tc.pattern, tc.value); got != tc.want {
			t.Fatalf("MatchPath(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
		}
	}
}

func TestDirPattern(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"/var/log/app.log": "/var/log/**",
		"/app.log":         "/app.log",
		"app.log":          "app.log",
		"/var/log/":        "/var/log/**",
		"":                 "",
	}
	for input, want := range cases {
		if got := DirPattern(input); got != want {
			t.Fatalf("DirPattern(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAddRuleValidation(t *testing.T) {
	t.Parallel()
	grants, _ := newTestGrants(t, nil)
	ctx := context.Background()
	if _, err := grants.AddRule(ctx, " ", "write_file", "/var/log/**", time.Time{}); err == nil {
		t.Fatal("empty device accepted")
	}
	if _, err := grants.AddRule(ctx, "device-a", "docker_exec", "", time.Time{}); err == nil {
		t.Fatal("unsupported action accepted")
	}
	if _, err := grants.AddRule(ctx, "device-a", "write_file", "", time.Time{}); err == nil {
		t.Fatal("write rule without path accepted")
	}
	if _, err := grants.AddRule(ctx, "device-a", "exec_commands", "/var/log/**", time.Time{}); err == nil {
		t.Fatal("exec rule with path accepted")
	}
	if _, err := grants.AddRule(ctx, "device-a", "write_file", "/var/log/**", time.Now().Add(-time.Minute)); err == nil {
		t.Fatal("past expiry accepted")
	}
	if rules := grants.Rules(); len(rules) != 0 {
		t.Fatalf("rejected rules persisted: %+v", rules)
	}
}

func TestGrantRuleCoversOnlyScopedActionAndPath(t *testing.T) {
	grants, _ := newTestGrants(t, nil)
	ctx := context.Background()
	rule, err := grants.AddRule(ctx, "device-a", "write_file", "/var/log/**", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if rule.ID == "" || rule.CreatedAt == 0 || rule.ExpiresAt != 0 {
		t.Fatalf("rule = %+v", rule)
	}
	confirm := Confirm(KindWriteFS, "写入或编辑文件")
	allow := Config{Mode: ReadWrite}
	if decision := grants.EvaluateResource(ctx, allow, confirm, nil, "device-a", "write_file", "run-1", Resource{Action: "write_file", Path: "/var/log/app.log"}); decision.Action != ActionAllow {
		t.Fatalf("scoped write decision = %+v", decision)
	}
	if decision := grants.EvaluateResource(ctx, allow, confirm, nil, "device-a", "write_file", "run-1", Resource{Action: "write_file", Path: "/etc/app.log"}); decision.Action != ActionAsk {
		t.Fatalf("out-of-scope write decision = %+v", decision)
	}
	if decision := grants.EvaluateResource(ctx, allow, confirm, nil, "device-b", "write_file", "run-1", Resource{Action: "write_file", Path: "/var/log/app.log"}); decision.Action != ActionAsk {
		t.Fatalf("cross-device write decision = %+v", decision)
	}
	edit := Confirm(KindWriteFS, "写入或编辑文件")
	if decision := grants.EvaluateResource(ctx, allow, edit, nil, "device-a", "edit_file", "run-1", Resource{Action: "edit_file", Path: "/var/log/app.log"}); decision.Action != ActionAsk {
		t.Fatalf("cross-action write decision = %+v", decision)
	}
	danger := Dangerous("删除容器需要始终确认")
	if decision := grants.EvaluateResource(ctx, allow, danger, nil, "device-a", "write_file", "run-1", Resource{Action: "write_file", Path: "/var/log/app.log"}); decision.Action != ActionAsk {
		t.Fatalf("danger covered by rule = %+v", decision)
	}
	forbidden := Deny("禁止")
	if decision := grants.EvaluateResource(ctx, allow, forbidden, nil, "device-a", "write_file", "run-1", Resource{Action: "write_file", Path: "/var/log/app.log"}); decision.Action != ActionDeny {
		t.Fatalf("forbidden covered by rule = %+v", decision)
	}
}

func TestGrantRuleActionWideCoversOldEvaluate(t *testing.T) {
	grants, _ := newTestGrants(t, nil)
	ctx := context.Background()
	if _, err := grants.AddRule(ctx, "device-a", "send_keys", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	ruling := Confirm(KindProcess, "终端控制键可能中断或改变程序")
	if decision := grants.Evaluate(ctx, Config{Mode: ReadWrite}, ruling, nil, "device-a", "send_keys", "run-1"); decision.Action != ActionAllow {
		t.Fatalf("action-wide rule via Evaluate = %+v", decision)
	}
	if decision := grants.Evaluate(ctx, Config{Mode: ReadWrite}, ruling, nil, "device-a", "exec_commands", "run-1"); decision.Action != ActionAsk {
		t.Fatalf("cross-action Evaluate = %+v", decision)
	}
}

func TestGrantRuleExpiry(t *testing.T) {
	grants, _ := newTestGrants(t, nil)
	ctx := context.Background()
	if _, err := grants.AddRule(ctx, "device-a", "exec_commands", "", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	ruling := Confirm(KindWriteFS, "写入或编辑文件")
	resource := Resource{Action: "exec_commands"}
	if decision := grants.EvaluateResource(ctx, Config{Mode: ReadWrite}, ruling, nil, "device-a", "exec_commands", "run-1", resource); decision.Action != ActionAllow {
		t.Fatalf("unexpired rule decision = %+v", decision)
	}
	grants.mu.Lock()
	grants.rules[0].ExpiresAt = grants.now().Add(-time.Millisecond).UnixMilli()
	grants.mu.Unlock()
	if decision := grants.EvaluateResource(ctx, Config{Mode: ReadWrite}, ruling, nil, "device-a", "exec_commands", "run-1", resource); decision.Action != ActionAsk {
		t.Fatalf("expired rule decision = %+v", decision)
	}
}

func TestGrantRuleAuditAndRevoke(t *testing.T) {
	recorder := &grantAuditRecorder{}
	grants, settings := newTestGrants(t, recorder.record)
	ctx := context.Background()
	rule, err := grants.AddRule(ctx, "device-a", "write_file", "/var/log/**", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	ruling := Confirm(KindWriteFS, "写入或编辑文件")
	grants.EvaluateResource(ctx, Config{Mode: ReadWrite}, ruling, nil, "device-a", "write_file", "run-1", Resource{Action: "write_file", Path: "/var/log/app.log"})
	if err := grants.RevokeRule(ctx, rule.ID); err != nil {
		t.Fatal(err)
	}
	if err := grants.RevokeRule(ctx, rule.ID); err != nil {
		t.Fatalf("repeated rule revoke: %v", err)
	}
	if decision := grants.EvaluateResource(ctx, Config{Mode: ReadWrite}, ruling, nil, "device-a", "write_file", "run-1", Resource{Action: "write_file", Path: "/var/log/app.log"}); decision.Action != ActionAsk {
		t.Fatalf("post-revoke decision = %+v", decision)
	}
	events := recorder.snapshot()
	if len(events) != 3 {
		t.Fatalf("audit events = %+v", events)
	}
	if events[0].Action != "rule_grant" || events[0].RuleID != rule.ID || events[0].RulePath != "/var/log/**" {
		t.Fatalf("rule_grant event = %+v", events[0])
	}
	if events[1].Action != "use_rule" || events[1].RuleID != rule.ID || events[1].Operation != "write_file" {
		t.Fatalf("use_rule event = %+v", events[1])
	}
	if events[2].Action != "rule_revoke" || events[2].RuleID != rule.ID {
		t.Fatalf("rule_revoke event = %+v", events[2])
	}
	reloaded, err := NewGrants(context.Background(), settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rules := reloaded.Rules(); len(rules) != 0 {
		t.Fatalf("rule revoke not persisted: %+v", rules)
	}
}

func TestGrantRuleSameScopeUpdatesInsteadOfDuplicating(t *testing.T) {
	grants, _ := newTestGrants(t, nil)
	ctx := context.Background()
	first, err := grants.AddRule(ctx, "device-a", "write_file", "/var/log/**", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	second, err := grants.AddRule(ctx, "device-a", "write_file", "/var/log/**", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.ExpiresAt != 0 {
		t.Fatalf("same-scope rule must be updated in place: first=%+v second=%+v", first, second)
	}
	grants.mu.Lock()
	grants.rules[0].ExpiresAt = grants.now().Add(-time.Millisecond).UnixMilli()
	grants.mu.Unlock()
	revived, err := grants.AddRule(ctx, "device-a", "write_file", "/var/log/**", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if revived.ID != first.ID {
		t.Fatalf("expired same-scope rule must be revived, got %+v", revived)
	}
	if rules := grants.Rules(); len(rules) != 1 {
		t.Fatalf("rules duplicated: %+v", rules)
	}
	ruling := Confirm(KindWriteFS, "写入或编辑文件")
	if decision := grants.EvaluateResource(ctx, Config{Mode: ReadWrite}, ruling, nil, "device-a", "write_file", "run-1", Resource{Action: "write_file", Path: "/var/log/app.log"}); decision.Action != ActionAllow {
		t.Fatalf("revived rule decision = %+v", decision)
	}
}

func TestGrantRulePersistenceRoundTrip(t *testing.T) {
	grants, settings := newTestGrants(t, nil)
	ctx := context.Background()
	created, err := grants.AddRule(ctx, "device-a", "write_file", "/var/log/**", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewGrants(context.Background(), settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	rules := reloaded.Rules()
	if len(rules) != 1 || rules[0].ID != created.ID || rules[0].Path != "/var/log/**" || rules[0].Action != "write_file" {
		t.Fatalf("reloaded rules = %+v", rules)
	}
}

func TestGrantRuleCorruptSettingsRejected(t *testing.T) {
	settings := &memorySettings{values: map[string]string{GrantRulesSettingKey: "{"}}
	if _, err := NewGrants(context.Background(), settings, nil); err == nil {
		t.Fatal("corrupt rules settings accepted")
	}
}

func TestMemoryPathRules(t *testing.T) {
	t.Parallel()
	memory := NewMemory()
	memory.AddPathRule("write_file", "/var/log/**")
	if !memory.CoversPathRule("write_file", "/var/log/app.log") {
		t.Fatal("path rule does not cover nested path")
	}
	if memory.CoversPathRule("write_file", "/etc/app.log") {
		t.Fatal("path rule covers out-of-scope path")
	}
	if memory.CoversPathRule("edit_file", "/var/log/app.log") {
		t.Fatal("path rule covers other action")
	}
	if memory.CoversPathRule("write_file", "") {
		t.Fatal("path rule covers empty path")
	}
	memory.AddPathRule("write_file", "/var/log/**")
	ruling := Confirm(KindWriteFS, "写入或编辑文件")
	if decision := DecideResource(Config{Mode: ReadWrite}, ruling, memory, Resource{Action: "write_file", Path: "/var/log/app.log"}); decision.Action != ActionAllow {
		t.Fatalf("session path rule decision = %+v", decision)
	}
	if decision := DecideResource(Config{Mode: ReadWrite}, ruling, memory, Resource{Action: "write_file", Path: "/etc/app.log"}); decision.Action != ActionAsk {
		t.Fatalf("out-of-scope session decision = %+v", decision)
	}
	danger := Dangerous("删除容器需要始终确认")
	if decision := DecideResource(Config{Mode: ReadWrite}, danger, memory, Resource{Action: "write_file", Path: "/var/log/app.log"}); decision.Action != ActionAsk {
		t.Fatalf("danger covered by session path rule = %+v", decision)
	}
}

func TestRuleActionSets(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"write_file", "edit_file", "exec_commands", "send_keys"} {
		if !RuleActionSupported(action) {
			t.Fatalf("action %s must be supported", action)
		}
	}
	for _, action := range []string{"docker_exec", "db_query", "shell_history", "read_file", ""} {
		if RuleActionSupported(action) {
			t.Fatalf("action %s must not be supported", action)
		}
	}
	if !RuleActionNeedsPath("write_file") || !RuleActionNeedsPath("edit_file") {
		t.Fatal("file actions must require path scope")
	}
	if RuleActionNeedsPath("exec_commands") || RuleActionNeedsPath("send_keys") {
		t.Fatal("command actions must not take path scope")
	}
}
