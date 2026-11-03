package sync

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

func TestAIGrantRuleScopeSyncRoundTrip(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	for _, device := range []*testDevice{deviceA, deviceB} {
		enableDeviceAIGrantOptIn(t, device, userID)
	}
	ctx := WithUserID(context.Background(), userID)

	grantsA, err := guard.NewGrants(ctx, deviceA.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := grantsA.AddRule(ctx, "dev-1", "exec_commands", "", guard.RuleScope{Kinds: []guard.Kind{guard.KindProgram, guard.KindService}, Programs: []string{"git"}, Unattended: true}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	syncDeviceWithContext(t, ctx, deviceA, server, "alice", "alice-pw-123")
	syncDeviceWithContext(t, ctx, deviceB, server, "alice", "alice-pw-123")

	grantsB, err := guard.NewGrants(ctx, deviceB.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := grantsB.Rules(ctx)
	if err != nil || len(rules) != 1 {
		t.Fatalf("rules on B = %+v, %v", rules, err)
	}
	got := rules[0]
	if got.ID != rule.ID || got.Legacy() || !got.Unattended || len(got.Kinds) != 2 || len(got.Programs) != 1 || got.Programs[0] != "git" {
		t.Fatalf("scope lost in sync: %+v", got)
	}
	if !got.Covers("exec_commands", "", guard.ConfirmProgram("git", "x")) || got.Covers("exec_commands", "", guard.ConfirmProgram("curl", "x")) {
		t.Fatal("synced rule coverage is wrong")
	}

	collected, err := (&Service{engine: deviceB.engine}).CollectAIGrants(ctx)
	if err != nil || len(collected.Rules) != 1 {
		t.Fatalf("collect = %+v, %v", collected, err)
	}
	c := collected.Rules[0]
	if len(c.Kinds) != 2 || len(c.Programs) != 1 || !c.Unattended {
		t.Fatalf("collect lost scope: %+v", c)
	}
	encoded, _ := json.Marshal(aiGrantRuleObjectFor("rule/x", got))
	text := string(encoded)
	last := -1
	for _, key := range []string{`"id"`, `"deviceId"`, `"action"`, `"kinds"`, `"programs"`, `"unattended"`, `"createdAt"`, `"updatedAt"`} {
		index := strings.Index(text, key)
		if index <= last {
			t.Fatalf("payload field order broken at %s: %s", key, text)
		}
		last = index
	}
}

func TestApplyAIGrantRuleAcceptsLegacyAndRejectsBadScope(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	device := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	enableDeviceAIGrantOptIn(t, device, userID)
	ctx := WithUserID(context.Background(), userID)

	apply := func(object aiGrantRuleObject) {
		t.Helper()
		plaintext, err := marshalObject(object)
		if err != nil {
			t.Fatal(err)
		}
		var report SyncReport
		device.engine.applyAIGrantRule(ctx, plaintext, &report)
	}
	apply(aiGrantRuleObject{ID: "rule/legacy", DeviceID: "dev-1", Action: "exec_commands", CreatedAt: 1, UpdatedAt: 1})
	apply(aiGrantRuleObject{ID: "rule/bad", DeviceID: "dev-1", Action: "exec_commands", Kinds: []string{"danger"}, CreatedAt: 1, UpdatedAt: 1})
	apply(aiGrantRuleObject{ID: "rule/pathkinds", DeviceID: "dev-1", Action: "write_file", Path: "/a/**", Kinds: []string{"service"}, CreatedAt: 1, UpdatedAt: 1})

	grants, err := guard.NewGrants(ctx, device.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := grants.Rules(ctx)
	if err != nil || len(rules) != 1 || rules[0].ID != "legacy" || !rules[0].Legacy() {
		t.Fatalf("rules = %+v, %v", rules, err)
	}
	decision := grants.EvaluateResource(ctx, guard.Config{Mode: guard.ReadWrite}, guard.Confirm(guard.KindService, "x"), nil, "dev-1", "exec_commands", "r", guard.Resource{Action: "exec_commands"})
	if decision.Action != guard.ActionAsk {
		t.Fatalf("legacy rule authorized: %+v", decision)
	}
}
