package guard

import (
	"context"
	"testing"
	"time"
)

// An event-triggered run is unattended, survives re-marking as unattended, and
// carries across InheritUnattended.
func TestEventTriggeredContextMarker(t *testing.T) {
	event := WithEventTriggered(context.Background())
	if !UnattendedFrom(event) || !EventTriggeredFrom(event) {
		t.Fatal("event marker must imply unattended")
	}
	if !EventTriggeredFrom(WithUnattended(event)) {
		t.Fatal("re-marking as unattended dropped the event marker")
	}
	if EventTriggeredFrom(WithUnattended(context.Background())) {
		t.Fatal("a scheduled run reported event-triggered")
	}
	inherited := InheritUnattended(context.Background(), event)
	if !EventTriggeredFrom(inherited) {
		t.Fatal("InheritUnattended dropped the event marker")
	}
	if UnattendedFrom(InheritUnattended(context.Background(), context.Background())) {
		t.Fatal("InheritUnattended invented a marker")
	}
}

// A rule that allows scheduled unattended runs authorizes a scheduled run but
// never an event-triggered one, in any mode, and a device grant does not
// either; safe rulings still pass.
func TestEventTriggeredRunIgnoresUnattendedRulesAndGrants(t *testing.T) {
	ctx := context.Background()
	grants, _ := newTestGrants(t, nil)
	if _, err := grants.AddRule(ctx, "dev-a", "write_file", "/srv/app/**", RuleScope{Unattended: true}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := grants.Grant(ctx, "dev-a", []GrantKind{GrantKindSessionExec, GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	write := Confirm(KindWriteFS, "write file")
	resource := Resource{Action: "write_file", Path: "/srv/app/run.sh"}

	scheduled := grants.EvaluateResource(WithUnattended(ctx), Config{Mode: Unattended}, write, nil, "dev-a", "write_file", "run-1", resource)
	if scheduled.Action != ActionAllow {
		t.Fatalf("control: scheduled run with an unattended rule = %+v, want allow", scheduled)
	}
	event := WithEventTriggered(ctx)
	for _, mode := range []Mode{Unattended, ReadWrite, Silent} {
		decision := grants.EvaluateResource(event, Config{Mode: mode}, write, nil, "dev-a", "write_file", "run-2", resource)
		if decision.Authorization != nil {
			t.Fatalf("mode %s: event run was authorized by %+v", mode, decision.Authorization)
		}
		if mode == Unattended && decision.Action != ActionDeny {
			t.Fatalf("event run write = %+v, want deny", decision)
		}
	}
	command := Confirm(KindService, "service change")
	if decision := grants.EvaluateResource(event, Config{Mode: Unattended}, command, nil, "dev-a", "exec_commands", "run-2", Resource{Action: "exec_commands"}); decision.Action != ActionDeny {
		t.Fatalf("event run command under a device grant = %+v, want deny", decision)
	}
	if decision := grants.EvaluateResource(event, Config{Mode: Unattended}, Ruling{Risk: Safe}, nil, "dev-a", "read_screen", "run-2", Resource{Action: "read_screen"}); decision.Action != ActionAllow {
		t.Fatalf("event run safe read = %+v, want allow", decision)
	}
}
