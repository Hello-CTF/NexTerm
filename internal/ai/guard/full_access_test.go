package guard

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func fullAccessRulings() map[Risk]Ruling {
	return map[Risk]Ruling{
		Safe:         Allow(),
		NeedsConfirm: Confirm(KindService, "service change"),
		Unknowable:   Indeterminate("cannot tell"),
		Danger:       Dangerous("dangerous"),
		Forbidden:    Deny("forbidden"),
	}
}

func TestFullAccessDecideAllowsEverythingButForbidden(t *testing.T) {
	for risk, ruling := range fullAccessRulings() {
		want := ActionAllow
		if risk == Forbidden {
			want = ActionDeny
		}
		if got := Decide(Config{Mode: FullAccess}, ruling, nil); got.Action != want {
			t.Fatalf("%s: action = %q, want %q (%+v)", risk, got.Action, want, got)
		}
		resource := Resource{Action: "exec_commands", Environment: "staging"}
		got := DecideResource(Config{Mode: FullAccess}, ruling, nil, resource)
		if got.Action != want || got.Production {
			t.Fatalf("%s ordinary target: %+v, want %q", risk, got, want)
		}
	}
}

// On a production target full access is never more permissive than silent.
func TestFullAccessOnProductionDecidesAsSilent(t *testing.T) {
	productionTargets := []Resource{
		{Action: "exec_commands", Environment: "production"},
		{Action: "exec_commands", Environment: "prod"},
		{Action: "exec_commands", Environment: "live", Production: true},
	}
	memories := map[string]func() *Memory{
		"nil":  func() *Memory { return nil },
		"kind": func() *Memory { m := NewMemory(); m.Add(KindService); return m },
	}
	for risk, ruling := range fullAccessRulings() {
		for _, resource := range productionTargets {
			for name, build := range memories {
				silent := DecideResource(Config{Mode: Silent}, ruling, build(), resource)
				full := DecideResource(Config{Mode: FullAccess}, ruling, build(), resource)
				if full.Action != silent.Action || !full.Production {
					t.Fatalf("%s/%s/%s: full access = %+v, silent = %+v", risk, resource.Environment, name, full, silent)
				}
			}
		}
	}
	for _, risk := range []Risk{Unknowable, Danger} {
		got := DecideResource(Config{Mode: FullAccess}, fullAccessRulings()[risk], nil, productionTargets[0])
		if got.Action != ActionAsk {
			t.Fatalf("%s on production = %+v, want ask", risk, got)
		}
	}
	if got := DecideResource(Config{Mode: FullAccess}, Deny("forbidden"), nil, productionTargets[0]); got.Action != ActionDeny {
		t.Fatalf("forbidden on production = %+v", got)
	}
}

// The production classification resolved from the real asset caps full access too.
func TestFullAccessGrantsEvaluationResolvesProduction(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	grants := environmentGrants(t, func(_ context.Context, assetID string) (string, error) {
		if assetID == "asset-prod" {
			return "production", nil
		}
		return "staging", nil
	})
	for _, risk := range []Risk{NeedsConfirm, Unknowable, Danger} {
		ruling := fullAccessRulings()[risk]
		resource := Resource{Action: "exec_commands"}
		if got := grants.EvaluateResource(ctx, Config{Mode: FullAccess}, ruling, nil, "asset-stage", "exec_commands", "run-1", resource); got.Action != ActionAllow {
			t.Fatalf("%s on staging = %+v, want allow", risk, got)
		}
		silent := grants.EvaluateResource(ctx, Config{Mode: Silent}, ruling, nil, "asset-prod", "exec_commands", "run-1", resource)
		full := grants.EvaluateResource(ctx, Config{Mode: FullAccess}, ruling, nil, "asset-prod", "exec_commands", "run-1", resource)
		if full.Action != silent.Action || !full.Production {
			t.Fatalf("%s on production: full access = %+v, silent = %+v", risk, full, silent)
		}
	}
	if got := grants.EvaluateResource(ctx, Config{Mode: FullAccess}, Deny("x"), nil, "asset-stage", "exec_commands", "run-1", Resource{}); got.Action != ActionDeny {
		t.Fatalf("forbidden = %+v", got)
	}
}

// The user's own interception rules keep asking in full access mode, on every
// classification path that consults them.
func TestFullAccessStillAsksOnCustomInterceptionRules(t *testing.T) {
	config := Config{Mode: FullAccess, DangerRules: []string{"prod-secret"}}
	calls := map[string]string{
		"exec_commands": `{"commands":["cat /srv/prod-secret.txt"]}`,
		"nested shell":  `{"commands":["bash -c 'cat /srv/prod-secret.txt'"]}`,
		"interpreter":   `{"commands":["python3 -c 'open(\"prod-secret\")'"]}`,
		"risky + rule":  `{"commands":["reboot prod-secret"]}`,
		"write_file":    `{"path":"/srv/prod-secret.txt","content":"x"}`,
		"docker_logs":   `{"container_id":"prod-secret"}`,
		"docker_exec":   `{"container_id":"prod-secret","cmd":"ls"}`,
		"db_query":      `{"sql":"SELECT * FROM prod_secret WHERE note = 'prod-secret'"}`,
		"send_keys":     `{"keys":"cat prod-secret","enter":true}`,
	}
	tools := map[string]string{"nested shell": "exec_commands", "interpreter": "exec_commands", "risky + rule": "exec_commands"}
	for name, args := range calls {
		tool := name
		if mapped, ok := tools[name]; ok {
			tool = mapped
		}
		ruling := ClassifyTool(tool, []byte(args), config)
		if ruling.Risk == Forbidden {
			t.Fatalf("%s: unexpected forbidden ruling %+v", name, ruling)
		}
		if !ruling.CustomRule {
			t.Fatalf("%s: rule hit not marked: %+v", name, ruling)
		}
		if got := Decide(config, ruling, nil); got.Action != ActionAsk {
			t.Fatalf("%s: full access decision = %+v, want ask", name, got)
		}
		if got := Decide(Config{Mode: Silent}, ruling, nil); got.Action != ActionAsk {
			t.Fatalf("%s: silent decision = %+v, want ask", name, got)
		}
	}
	plain := ClassifyTool("exec_commands", []byte(`{"commands":["reboot"]}`), config)
	if plain.CustomRule || Decide(config, plain, nil).Action != ActionAllow {
		t.Fatalf("plain high-risk command without a rule hit = %+v", plain)
	}
}

func TestFullAccessIsAValidNonDefaultMode(t *testing.T) {
	if got := (Config{Mode: FullAccess}).Normalized().Mode; got != FullAccess {
		t.Fatalf("normalized full access = %s", got)
	}
	// A build that does not know a mode falls back to the read_write default.
	if got := (Config{Mode: "some_future_mode"}).Normalized().Mode; got != ReadWrite {
		t.Fatalf("unknown mode normalized to %s", got)
	}
	settings := &memorySettings{values: make(map[string]string)}
	manager, err := NewManager(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	config, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != ReadWrite {
		t.Fatalf("default mode = %s", config.Mode)
	}
	if err := manager.Set(context.Background(), Config{Mode: FullAccess}); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetDeviceMode(context.Background(), "dev", FullAccess); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewManager(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if config, err := reloaded.Snapshot(context.Background()); err != nil || config.Mode != FullAccess {
		t.Fatalf("reloaded global mode = %s, %v", config.Mode, err)
	}
	if mode, ok, err := reloaded.DeviceMode(context.Background(), "dev"); err != nil || !ok || mode != FullAccess {
		t.Fatalf("reloaded device mode = %s %v %v", mode, ok, err)
	}
}
