package agent

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

func agentOwnerContext(contexts []context.Context) context.Context {
	if len(contexts) != 0 {
		return contexts[0]
	}
	return context.Background()
}

func mustAgentGrantList(t *testing.T, grants *guard.Grants, contexts ...context.Context) []guard.DeviceGrant {
	t.Helper()
	listed, err := grants.List(agentOwnerContext(contexts))
	if err != nil {
		t.Fatal(err)
	}
	return listed
}

func mustAgentRules(t *testing.T, grants *guard.Grants, contexts ...context.Context) []guard.GrantRule {
	t.Helper()
	rules, err := grants.Rules(agentOwnerContext(contexts))
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

func mustAgentPermission(t *testing.T, manager *guard.Manager, contexts ...context.Context) guard.Config {
	t.Helper()
	config, err := manager.Get(agentOwnerContext(contexts))
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func mustAgentDeviceMode(t *testing.T, manager *guard.Manager, deviceID string, contexts ...context.Context) (guard.Mode, bool) {
	t.Helper()
	mode, ok, err := manager.DeviceMode(agentOwnerContext(contexts), deviceID)
	if err != nil {
		t.Fatal(err)
	}
	return mode, ok
}
