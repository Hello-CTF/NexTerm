package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

func fullAccessCommands(t *testing.T) map[guard.Risk]string {
	t.Helper()
	commands := map[guard.Risk]string{
		guard.NeedsConfirm: "systemctl restart nginx",
		guard.Unknowable:   `eval "$X"`,
		guard.Danger:       "reboot",
	}
	for risk, command := range commands {
		if got := guard.ClassifyCommand(command, nil).Risk; got != risk {
			t.Fatalf("%q classified as %s, want %s", command, got, risk)
		}
	}
	return commands
}

func execArgs(t *testing.T, command string) string {
	t.Helper()
	encoded, err := json.Marshal(ExecCommandsArgs{Commands: []string{command}})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func fullAccessEnvironmentGrants(t *testing.T, environment string) *guard.Grants {
	t.Helper()
	grants, err := guard.NewGrants(context.Background(), &chatGrantSettings{values: make(map[string]string)}, (&chatGrantRecorder{}).record, guard.WithEnvironmentResolver(func(context.Context, string) (string, error) {
		return environment, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	return grants
}

func TestFullAccessRunsRiskyCommandsOnOrdinaryHostWithoutAsking(t *testing.T) {
	for risk, command := range fullAccessCommands(t) {
		t.Run(risk.String(), func(t *testing.T) {
			h := newChatGrantHarness(guard.Config{Mode: guard.FullAccess}, "asset-a", fullAccessEnvironmentGrants(t, "staging"), &chatGrantRecorder{})
			messages, err := h.invoke("call-1", "exec_commands", execArgs(t, command))
			if err != nil {
				t.Fatalf("full access asked for %q: %v", command, err)
			}
			if len(messages) != 1 || h.transport.calls() != 1 {
				t.Fatalf("exec runs = %d, messages = %+v", h.transport.calls(), messages)
			}
		})
	}
	h := newChatGrantHarness(guard.Config{Mode: guard.FullAccess}, "asset-a", nil, &chatGrantRecorder{})
	messages, err := h.invoke("call-1", "exec_commands", `{"commands":["rm -rf /"]}`)
	requireChatGrantResult(t, messages, err, "权限策略已拒绝")
	if h.transport.calls() != 0 {
		t.Fatalf("forbidden command executed: %v", h.transport.execCalls)
	}
}

// On a production host full access decides exactly like silent: high-risk and
// undeterminable commands still ask.
func TestFullAccessOnProductionHostAsksLikeSilent(t *testing.T) {
	for risk, command := range fullAccessCommands(t) {
		t.Run(risk.String(), func(t *testing.T) {
			for _, mode := range []guard.Mode{guard.Silent, guard.FullAccess} {
				h := newChatGrantHarness(guard.Config{Mode: mode}, "asset-a", fullAccessEnvironmentGrants(t, "production"), &chatGrantRecorder{})
				_, err := h.invoke("call-1", "exec_commands", execArgs(t, command))
				asked := err != nil
				if asked {
					requireChatGrantInterrupt(t, err)
				}
				if want := risk != guard.NeedsConfirm; asked != want {
					t.Fatalf("%s on production: asked = %v, want %v", mode, asked, want)
				}
				if asked && h.transport.calls() != 0 {
					t.Fatalf("%s executed before confirmation: %v", mode, h.transport.execCalls)
				}
			}
		})
	}
}
