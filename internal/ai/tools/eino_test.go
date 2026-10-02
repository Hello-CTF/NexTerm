package tools

import (
	"context"
	"testing"
)

func TestExecutionToolsFollowScopeCapabilities(t *testing.T) {
	execution := &Execution{JobID: "j", Registry: NewRegistry(Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}), Scope: Scope{SessionID: "s"}}
	einoTools, err := execution.Tools()
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(einoTools))
	for _, current := range einoTools {
		info, err := current.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		names[info.Name] = true
	}
	if len(names) != 5 || !names["docker_control"] || !names["list_assets"] || !names["ask_user"] || !names["todo_write"] || !names["exit_plan_mode"] {
		t.Fatalf("names=%v", names)
	}
	for _, unavailable := range []string{"exec_commands", "read_file", "read_screen", "docker_ps", "docker_logs", "docker_exec", "db_query"} {
		if names[unavailable] {
			t.Errorf("%s exposed without capability: %v", unavailable, names)
		}
	}
}
