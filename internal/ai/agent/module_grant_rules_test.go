package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
)

func TestGrantRuleCommandsLifecycle(t *testing.T) {
	grants, err := guard.NewGrants(context.Background(), &grantSettings{values: make(map[string]string)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := newGrantDispatcher(t, Config{Grants: grants})

	set := dispatchGrant(t, dispatcher, "ai_grant_rule_set", `{"deviceId":"asset-a","action":"write_file","path":"/var/log/**"}`)
	if !set.OK {
		t.Fatalf("rule set failed: %+v", set.Error)
	}
	var rule guard.GrantRule
	if err := json.Unmarshal(set.Data, &rule); err != nil {
		t.Fatal(err)
	}
	if rule.ID == "" || rule.DeviceID != "asset-a" || rule.Action != "write_file" || rule.Path != "/var/log/**" || rule.ExpiresAt != 0 {
		t.Fatalf("rule = %+v", rule)
	}

	expiring := dispatchGrant(t, dispatcher, "ai_grant_rule_set", `{"deviceId":"asset-a","action":"exec_commands","expiresAt":4102444800000}`)
	if !expiring.OK {
		t.Fatalf("expiring rule set failed: %+v", expiring.Error)
	}
	var expiringRule guard.GrantRule
	if err := json.Unmarshal(expiring.Data, &expiringRule); err != nil {
		t.Fatal(err)
	}
	if expiringRule.ExpiresAt != 4102444800000 {
		t.Fatalf("expiring rule = %+v", expiringRule)
	}

	list := dispatchGrant(t, dispatcher, "ai_grant_rule_list", `{}`)
	if !list.OK {
		t.Fatalf("rule list failed: %+v", list.Error)
	}
	var listed []guard.GrantRule
	if err := json.Unmarshal(list.Data, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed = %+v", listed)
	}

	revoke := dispatchGrant(t, dispatcher, "ai_grant_rule_revoke", `{"id":"`+rule.ID+`"}`)
	if !revoke.OK {
		t.Fatalf("rule revoke failed: %+v", revoke.Error)
	}
	again := dispatchGrant(t, dispatcher, "ai_grant_rule_revoke", `{"id":"`+rule.ID+`"}`)
	if !again.OK {
		t.Fatalf("rule revoke must be idempotent: %+v", again.Error)
	}
	list = dispatchGrant(t, dispatcher, "ai_grant_rule_list", `{}`)
	listed = nil
	if err := json.Unmarshal(list.Data, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != expiringRule.ID {
		t.Fatalf("listed after revoke = %+v", listed)
	}
}

func TestGrantRuleSetValidatesInput(t *testing.T) {
	grants, err := guard.NewGrants(context.Background(), &grantSettings{values: make(map[string]string)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := newGrantDispatcher(t, Config{Grants: grants})

	cases := map[string]string{
		"empty device": `{"deviceId":" ","action":"write_file","path":"/var/log/**"}`,
		"bad action":   `{"deviceId":"asset-a","action":"docker_exec"}`,
		"missing path": `{"deviceId":"asset-a","action":"write_file"}`,
		"stray path":   `{"deviceId":"asset-a","action":"send_keys","path":"/var/log/**"}`,
		"past expiry":  `{"deviceId":"asset-a","action":"send_keys","expiresAt":1}`,
	}
	for name, args := range cases {
		response := dispatchGrant(t, dispatcher, "ai_grant_rule_set", args)
		if response.OK || response.Error == nil {
			t.Fatalf("%s accepted: %+v", name, response)
		}
	}
	if rules := grants.Rules(); len(rules) != 0 {
		t.Fatalf("rejected rules persisted: %+v", rules)
	}
}

func TestGrantRuleCommandsUnavailableWithoutGrants(t *testing.T) {
	dispatcher := newGrantDispatcher(t, Config{})
	for command, args := range map[string]string{
		"ai_grant_rule_list":   `{}`,
		"ai_grant_rule_set":    `{"deviceId":"asset-a","action":"send_keys"}`,
		"ai_grant_rule_revoke": `{"id":"rule-a"}`,
	} {
		response := dispatchGrant(t, dispatcher, command, args)
		if response.OK || response.Error == nil || !strings.Contains(response.Error.Message, "设备授权未配置") {
			t.Fatalf("%s without grants = %+v", command, response)
		}
	}
}

func TestGrantRuleSetRejectsZeroExpiryTime(t *testing.T) {
	grants, err := guard.NewGrants(context.Background(), &grantSettings{values: make(map[string]string)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := newGrantDispatcher(t, Config{Grants: grants})
	response := dispatchGrant(t, dispatcher, "ai_grant_rule_set", `{"deviceId":"asset-a","action":"send_keys","expiresAt":`+jsonNumber(time.Now().Add(-time.Hour).UnixMilli())+`}`)
	if response.OK {
		t.Fatalf("past expiry accepted: %+v", response)
	}
}

func jsonNumber(value int64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
