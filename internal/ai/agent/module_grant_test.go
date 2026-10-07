package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

type grantSettings struct {
	values map[string]string
}

func (s *grantSettings) SettingGet(_ context.Context, key string) (string, bool, error) {
	value, ok := s.values[key]
	return value, ok, nil
}

func (s *grantSettings) SettingSet(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

type grantAuditRecorder struct {
	events []guard.GrantAuditEvent
}

func (r *grantAuditRecorder) record(_ context.Context, event guard.GrantAuditEvent) error {
	r.events = append(r.events, event)
	return nil
}

func newGrantDispatcher(t *testing.T, config Config) *ipc.Dispatcher {
	t.Helper()
	runner := NewRunner(config)
	t.Cleanup(func() { _ = runner.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := runner.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func dispatchGrant(t *testing.T, dispatcher *ipc.Dispatcher, command, args string) ipc.Response {
	t.Helper()
	return dispatcher.Dispatch(context.Background(), ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
}

func TestGrantCommandsListGrantRevoke(t *testing.T) {
	recorder := &grantAuditRecorder{}
	grants, err := guard.NewGrants(context.Background(), &grantSettings{values: make(map[string]string)}, recorder.record)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := newGrantDispatcher(t, Config{Grants: grants})

	set := dispatchGrant(t, dispatcher, "ai_grant_set", `{"deviceId":"asset-a","kinds":["terminal_write"]}`)
	if !set.OK {
		t.Fatalf("grant set failed: %+v", set.Error)
	}
	var granted guard.DeviceGrant
	if err := json.Unmarshal(set.Data, &granted); err != nil {
		t.Fatal(err)
	}
	if granted.DeviceID != "asset-a" || !granted.Covers(guard.GrantKindTerminalWrite) || granted.UpdatedAt == 0 {
		t.Fatalf("granted = %+v", granted)
	}

	both := dispatchGrant(t, dispatcher, "ai_grant_set", `{"deviceId":"asset-b","kinds":["session_exec","terminal_write","session_exec"]}`)
	if !both.OK {
		t.Fatalf("grant set both failed: %+v", both.Error)
	}
	var doubled guard.DeviceGrant
	if err := json.Unmarshal(both.Data, &doubled); err != nil {
		t.Fatal(err)
	}
	if len(doubled.Kinds) != 2 {
		t.Fatalf("kinds must dedupe, got %+v", doubled.Kinds)
	}

	list := dispatchGrant(t, dispatcher, "ai_grant_list", `{}`)
	if !list.OK {
		t.Fatalf("grant list failed: %+v", list.Error)
	}
	var listed []guard.DeviceGrant
	if err := json.Unmarshal(list.Data, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed = %+v", listed)
	}

	revoke := dispatchGrant(t, dispatcher, "ai_grant_revoke", `{"deviceId":"asset-a"}`)
	if !revoke.OK {
		t.Fatalf("grant revoke failed: %+v", revoke.Error)
	}
	again := dispatchGrant(t, dispatcher, "ai_grant_revoke", `{"deviceId":"asset-a"}`)
	if !again.OK {
		t.Fatalf("grant revoke must be idempotent: %+v", again.Error)
	}
	list = dispatchGrant(t, dispatcher, "ai_grant_list", `{}`)
	if !list.OK {
		t.Fatalf("grant list after revoke failed: %+v", list.Error)
	}
	listed = nil
	if err := json.Unmarshal(list.Data, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].DeviceID != "asset-b" {
		t.Fatalf("listed after revoke = %+v", listed)
	}

	actions := make([]string, 0, len(recorder.events))
	for _, event := range recorder.events {
		actions = append(actions, event.Action+":"+event.DeviceID)
	}
	want := []string{"grant:asset-a", "grant:asset-b", "revoke:asset-a"}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("audit actions = %v, want %v", actions, want)
	}
}

func TestGrantSetValidatesKindsAndDevice(t *testing.T) {
	grants, err := guard.NewGrants(context.Background(), &grantSettings{values: make(map[string]string)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := newGrantDispatcher(t, Config{Grants: grants})

	invalid := dispatchGrant(t, dispatcher, "ai_grant_set", `{"deviceId":"asset-a","kinds":["docker_admin"]}`)
	if invalid.OK || invalid.Error == nil || !strings.Contains(invalid.Error.Message, "仅支持 terminal_write/session_exec") {
		t.Fatalf("invalid kind dispatch = %+v", invalid)
	}
	empty := dispatchGrant(t, dispatcher, "ai_grant_set", `{"deviceId":"asset-a","kinds":[]}`)
	if empty.OK || empty.Error == nil || !strings.Contains(empty.Error.Message, "至少需要一个种类") {
		t.Fatalf("empty kinds dispatch = %+v", empty)
	}
	noDevice := dispatchGrant(t, dispatcher, "ai_grant_set", `{"deviceId":"  ","kinds":["terminal_write"]}`)
	if noDevice.OK || noDevice.Error == nil || !strings.Contains(noDevice.Error.Message, "缺少设备 ID") {
		t.Fatalf("empty device dispatch = %+v", noDevice)
	}
	if listed := grants.List(); len(listed) != 0 {
		t.Fatalf("rejected grants must not persist, got %+v", listed)
	}
}

func TestGrantCommandsUnavailableWithoutGrants(t *testing.T) {
	dispatcher := newGrantDispatcher(t, Config{})
	for command, args := range map[string]string{
		"ai_grant_list":   `{}`,
		"ai_grant_set":    `{"deviceId":"asset-a","kinds":["terminal_write"]}`,
		"ai_grant_revoke": `{"deviceId":"asset-a"}`,
	} {
		response := dispatchGrant(t, dispatcher, command, args)
		if response.OK || response.Error == nil || !strings.Contains(response.Error.Message, "设备授权未配置") {
			t.Fatalf("%s without grants = %+v", command, response)
		}
	}
}
