package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

// 服务端装配(ServerMode)下, 设备授权与授权规则的变更仅超管可用; 无身份的
// 开放部署(auth=off / loopback 免登录)与桌面端(ServerMode=false)保持原行为。
func TestGrantMutationsRequireSuperadminInServerMode(t *testing.T) {
	grants, err := guard.NewGrants(context.Background(), &grantSettings{values: make(map[string]string)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := newGrantDispatcher(t, Config{Grants: grants, ServerMode: true})

	plain := ipc.WithUserID(context.Background(), "u-1")
	plain = ipc.WithRole(plain, "user")
	admin := ipc.WithUserID(context.Background(), "u-admin")
	admin = ipc.WithRole(admin, "superadmin")

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
		if response := dispatch(plain, command, args); response.OK || response.Error == nil || !strings.Contains(response.Error.Message, "超管") {
			t.Fatalf("%s by plain user = %+v, want superadmin required", command, response)
		}
		if response := dispatch(admin, command, args); !response.OK {
			t.Fatalf("%s by superadmin = %+v", command, response)
		}
		if response := dispatch(context.Background(), command, args); !response.OK {
			t.Fatalf("%s without identity (open deployment) = %+v", command, response)
		}
	}
	if listed := grants.List(); len(listed) != 1 {
		t.Fatalf("grants after mutations = %+v", listed)
	}
	if rules := grants.Rules(); len(rules) != 1 {
		t.Fatalf("rules after mutations = %+v", rules)
	}

	// 桌面端(ServerMode=false)带身份也不受限。
	desktop := newGrantDispatcher(t, Config{Grants: grants})
	response := desktop.Dispatch(plain, ipc.Request{Command: "ai_grant_rule_set", Args: json.RawMessage(`{"deviceId":"asset-b","action":"send_keys"}`)}, ipc.Environment{})
	if !response.OK {
		t.Fatalf("desktop assembly rule set by plain user = %+v", response)
	}
}
