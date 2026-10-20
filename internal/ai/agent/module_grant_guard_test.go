package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestGrantMutationsAllowRegularUsersWithinServerOwnerScope(t *testing.T) {
	grants, err := guard.NewGrants(context.Background(), &grantSettings{values: make(map[string]string)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := newGrantDispatcher(t, Config{Grants: grants, ServerMode: true})

	plain := ipc.WithRole(ipc.WithUserID(context.Background(), "u-1"), "user")
	admin := ipc.WithRole(ipc.WithUserID(context.Background(), "u-admin"), "superadmin")
	mutations := map[string]string{
		"ai_grant_set":         `{"deviceId":"asset-g1","kinds":["terminal_write"]}`,
		"ai_grant_revoke":      `{"deviceId":"asset-g2"}`,
		"ai_grant_rule_set":    `{"deviceId":"asset-r1","action":"send_keys"}`,
		"ai_grant_rule_revoke": `{"id":"rule-a"}`,
	}
	dispatch := func(ctx context.Context, command, args string) ipc.Response {
		return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
	}
	for command, args := range mutations {
		if response := dispatch(plain, command, args); !response.OK {
			t.Fatalf("%s by regular user = %+v", command, response)
		}
		if response := dispatch(admin, command, args); !response.OK {
			t.Fatalf("%s by superadmin = %+v", command, response)
		}
		response := dispatch(context.Background(), command, args)
		if response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
			t.Fatalf("%s without identity = %+v, want forbidden", command, response)
		}
	}
	if listed := mustAgentGrantList(t, grants, plain); len(listed) != 1 {
		t.Fatalf("grants after mutations = %+v", listed)
	}
	if rules := mustAgentRules(t, grants, plain); len(rules) != 1 {
		t.Fatalf("rules after mutations = %+v", rules)
	}

	desktop := newGrantDispatcher(t, Config{Grants: grants})
	response := desktop.Dispatch(plain, ipc.Request{Command: "ai_grant_rule_set", Args: json.RawMessage(`{"deviceId":"asset-b","action":"send_keys"}`)}, ipc.Environment{})
	if !response.OK {
		t.Fatalf("desktop assembly rule set by regular user = %+v", response)
	}
}
