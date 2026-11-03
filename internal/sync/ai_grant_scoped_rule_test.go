package sync

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

type scopedRuleEnv struct {
	ctx    context.Context
	device *testDevice
	grants *guard.Grants
}

func newScopedRuleEnv(t *testing.T) scopedRuleEnv {
	t.Helper()
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	device := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	enableDeviceAIGrantOptIn(t, device, userID)
	ctx := WithUserID(context.Background(), userID)
	grants, err := guard.NewGrants(ctx, device.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	return scopedRuleEnv{ctx: ctx, device: device, grants: grants}
}

func (e scopedRuleEnv) collectRules(t *testing.T) map[string]aiGrantRuleObject {
	t.Helper()
	objects := map[string]localObject{}
	if err := e.device.engine.collectAIGrantObjects(e.ctx, objects); err != nil {
		t.Fatal(err)
	}
	out := map[string]aiGrantRuleObject{}
	for id, object := range objects {
		if !strings.HasPrefix(id, "rule/") && !strings.HasPrefix(id, "scoped-rule/") {
			continue
		}
		var payload aiGrantRuleObject
		if err := json.Unmarshal(object.plaintext, &payload); err != nil {
			t.Fatal(err)
		}
		out[id] = payload
	}
	return out
}

func (e scopedRuleEnv) apply(t *testing.T, object aiGrantRuleObject) {
	t.Helper()
	plaintext, err := marshalObject(object)
	if err != nil {
		t.Fatal(err)
	}
	var report SyncReport
	e.device.engine.applyAIGrantRule(e.ctx, plaintext, &report)
}

// fresh reloads the guard state; applied sync objects land in the settings row, not the live cache.
func (e scopedRuleEnv) fresh(t *testing.T) *guard.Grants {
	t.Helper()
	grants, err := guard.NewGrants(e.ctx, e.device.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	return grants
}

func (e scopedRuleEnv) authorizes(t *testing.T) bool {
	t.Helper()
	d := e.fresh(t).EvaluateResource(e.ctx, guard.Config{Mode: guard.ReadWrite}, guard.Confirm(guard.KindService, "x"), nil, "dev-1", "exec_commands", "r", guard.Resource{Action: "exec_commands"})
	return d.Action == guard.ActionAllow
}

func TestCollectUsesScopedPrefixOnlyForScopedRules(t *testing.T) {
	env := newScopedRuleEnv(t)
	scoped, err := env.grants.AddRule(env.ctx, "dev-1", "exec_commands", "", guard.RuleScope{Kinds: []guard.Kind{guard.KindService}}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	path, err := env.grants.AddRule(env.ctx, "dev-1", "write_file", "/var/log/**", guard.RuleScope{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	rules := env.collectRules(t)
	if _, ok := rules["scoped-rule/"+scoped.ID]; !ok {
		t.Fatalf("scoped rule id missing: %v", rules)
	}
	if _, ok := rules["rule/"+path.ID]; !ok || len(rules) != 2 {
		t.Fatalf("path rule id missing: %v", rules)
	}
	collected, err := (&Service{engine: env.device.engine}).CollectAIGrants(env.ctx)
	if err != nil || len(collected.Rules) != 2 {
		t.Fatalf("collect = %+v, %v", collected, err)
	}
	seen := map[string]bool{}
	for _, r := range collected.Rules {
		seen[r.ID] = true
	}
	if !seen["scoped-rule/"+scoped.ID] || !seen["rule/"+path.ID] {
		t.Fatalf("browser collect ids = %v", seen)
	}
}

func TestOldFormatPushBackDoesNotOverwriteOrDeleteScopedRule(t *testing.T) {
	env := newScopedRuleEnv(t)
	scoped, err := env.grants.AddRule(env.ctx, "dev-1", "exec_commands", "", guard.RuleScope{Kinds: []guard.Kind{guard.KindService}}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !env.authorizes(t) {
		t.Fatal("scoped rule does not authorize")
	}
	// An old client decodes the object leniently, drops the new fields and pushes it back as rule/<id>.
	env.apply(t, aiGrantRuleObject{ID: "rule/" + scoped.ID, DeviceID: "dev-1", Action: "exec_commands", CreatedAt: scoped.CreatedAt, UpdatedAt: scoped.UpdatedAt + 1000})
	rules, err := env.fresh(t).Rules(env.ctx)
	if err != nil || len(rules) != 1 || len(rules[0].Kinds) != 1 || rules[0].Legacy() || rules[0].UpdatedAt != scoped.UpdatedAt {
		t.Fatalf("rules after push-back = %+v, %v", rules, err)
	}
	if !env.authorizes(t) {
		t.Fatal("scoped rule stopped authorizing after old-format push-back")
	}
	var report SyncReport
	env.device.engine.applyAIGrantTombstone(env.ctx, "rule/"+scoped.ID, time.Now().UnixMilli()+1000, &report)
	rules, _ = env.fresh(t).Rules(env.ctx)
	if len(rules) != 1 || !env.authorizes(t) {
		t.Fatalf("old-format tombstone deleted scoped rule: %+v", rules)
	}
}

func TestScopedPrefixRequiresMatchingScopeFields(t *testing.T) {
	env := newScopedRuleEnv(t)
	env.apply(t, aiGrantRuleObject{ID: "scoped-rule/plain", DeviceID: "dev-1", Action: "write_file", Path: "/a/**", CreatedAt: 1, UpdatedAt: 1})
	env.apply(t, aiGrantRuleObject{ID: "rule/scopedbody", DeviceID: "dev-1", Action: "exec_commands", Kinds: []string{"service"}, CreatedAt: 1, UpdatedAt: 1})
	env.apply(t, aiGrantRuleObject{ID: "scoped-rule/good", DeviceID: "dev-1", Action: "exec_commands", Kinds: []string{"service"}, CreatedAt: 1, UpdatedAt: 1})
	rules, err := env.fresh(t).Rules(env.ctx)
	if err != nil || len(rules) != 1 || rules[0].ID != "good" {
		t.Fatalf("rules = %+v, %v", rules, err)
	}
}

func TestRevokedScopedRuleTombstoneConvergesOnOtherClient(t *testing.T) {
	a := newScopedRuleEnv(t)
	b := newScopedRuleEnv(t)
	scoped, err := a.grants.AddRule(a.ctx, "dev-1", "exec_commands", "", guard.RuleScope{Kinds: []guard.Kind{guard.KindService}}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	id := "scoped-rule/" + scoped.ID
	b.apply(t, a.collectRules(t)[id])
	if rules, _ := b.fresh(t).Rules(b.ctx); len(rules) != 1 {
		t.Fatalf("B did not receive scoped rule: %+v", rules)
	}
	if err := a.grants.RevokeRule(a.ctx, scoped.ID); err != nil {
		t.Fatal(err)
	}
	tombstone, found, err := a.device.engine.syncTombstoneGet(a.ctx, id)
	if err != nil || !found {
		t.Fatalf("scoped tombstone missing: found=%v err=%v", found, err)
	}
	if _, found, _ := a.device.engine.syncTombstoneGet(a.ctx, "rule/"+scoped.ID); found {
		t.Fatal("tombstone recorded under the legacy prefix")
	}
	var report SyncReport
	b.device.engine.applyAIGrantTombstone(b.ctx, id, tombstone.DeletedAt, &report)
	if rules, _ := b.fresh(t).Rules(b.ctx); len(rules) != 0 {
		t.Fatalf("B still holds revoked rule: %+v", rules)
	}
}

// oldClientRoundTrip models a v1.3.x client: it only accepts rule/ objects, drops
// the new fields, stores a whole-device rule and pushes every rule it holds back.
func oldClientRoundTrip(t *testing.T, objects map[string]aiGrantRuleObject, store map[string]aiGrantRuleObject) map[string]aiGrantRuleObject {
	t.Helper()
	for id, object := range objects {
		if !strings.HasPrefix(id, "rule/") {
			continue
		}
		object.Kinds, object.Programs, object.Unattended = nil, nil, false
		store[id] = object
	}
	pushed := map[string]aiGrantRuleObject{}
	for id, object := range store {
		object.UpdatedAt += 1000
		pushed[id] = object
	}
	return pushed
}

func TestMixedVersionClientsConvergeWithoutOverwrite(t *testing.T) {
	a := newScopedRuleEnv(t)
	scoped, err := a.grants.AddRule(a.ctx, "dev-1", "exec_commands", "", guard.RuleScope{Kinds: []guard.Kind{guard.KindService}}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	path, err := a.grants.AddRule(a.ctx, "dev-1", "write_file", "/var/log/**", guard.RuleScope{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	oldStore := map[string]aiGrantRuleObject{}
	for round := 0; round < 3; round++ {
		pushed := oldClientRoundTrip(t, a.collectRules(t), oldStore)
		for _, object := range pushed {
			a.apply(t, object)
		}
	}
	rules, err := a.fresh(t).Rules(a.ctx)
	if err != nil || len(rules) != 2 {
		t.Fatalf("rules = %+v, %v", rules, err)
	}
	for _, rule := range rules {
		if rule.ID == scoped.ID && (len(rule.Kinds) != 1 || rule.Legacy() || rule.UpdatedAt != scoped.UpdatedAt) {
			t.Fatalf("scoped rule was rewritten: %+v", rule)
		}
	}
	if _, ok := oldStore["rule/"+path.ID]; !ok {
		t.Fatal("old client never received the path rule")
	}
	if _, ok := oldStore["rule/"+scoped.ID]; ok {
		t.Fatal("old client received the scoped rule under the legacy prefix")
	}
	if !a.authorizes(t) {
		t.Fatal("scoped rule stopped authorizing")
	}
}
