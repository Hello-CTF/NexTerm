package tools_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/skills"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// These tests drive the real model-facing surface: tools built by
// Execution.Tools() execute through a compose tool node exactly as the chat
// and unattended agents run them — e.run, Registry validation, plan-mode
// filtering, guard classification, and the shared skills Manager dispatch.

func newSkillExecution(t *testing.T, shared bool, mode guard.Mode, planMode bool) (*tools.Execution, *skills.Manager) {
	t.Helper()
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	manager, err := skills.NewManager(t.Context(), database, skills.WithShared(shared))
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(tools.Dependencies{Skills: manager})
	return &tools.Execution{JobID: "job-skills", Registry: registry, Permission: guard.Config{Mode: mode}, PlanMode: planMode}, manager
}

func einoToolNames(t *testing.T, execution *tools.Execution) map[string]bool {
	t.Helper()
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
	return names
}

func invokeLoadSkill(t *testing.T, execution *tools.Execution, ctx context.Context, arguments string) string {
	t.Helper()
	einoTools, err := execution.Tools()
	if err != nil {
		t.Fatal(err)
	}
	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: einoTools, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	message := schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Function: schema.FunctionCall{Name: "load_skill", Arguments: arguments}}})
	messages, err := node.Invoke(ctx, message)
	if err != nil {
		t.Fatalf("load_skill invoke: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %+v", messages)
	}
	return messages[0].Content
}

func TestExecutionToolsExposeLoadSkillOnlyWhenConfigured(t *testing.T) {
	configured, _ := newSkillExecution(t, false, guard.ReadWrite, false)
	if names := einoToolNames(t, configured); !names["load_skill"] {
		t.Fatalf("load_skill is missing from the model-facing tool list: %v", names)
	}

	without := &tools.Execution{JobID: "job", Registry: tools.NewRegistry(tools.Dependencies{})}
	if names := einoToolNames(t, without); names["load_skill"] {
		t.Fatal("load_skill is exposed without a configured skills backend")
	}

	// A typed-nil backend is "not configured" as well: the tool stays out of
	// the list and the registry rejects the call instead of panicking.
	var nilManager *skills.Manager
	typedNil := &tools.Execution{JobID: "job", Registry: tools.NewRegistry(tools.Dependencies{Skills: nilManager})}
	if names := einoToolNames(t, typedNil); names["load_skill"] {
		t.Fatal("load_skill is exposed with a typed-nil skills backend")
	}
	if output := typedNil.Registry.Execute(t.Context(), "job", tools.Scope{}, tools.Call{ID: "c", Name: "load_skill", Args: []byte(`{"name":"x"}`)}, nil); output.OK || output.Panic {
		t.Fatalf("typed-nil load_skill execution = %+v", output)
	}
}

// seedSkill creates one enabled custom skill for the caller on top of the built-in skills.
func seedSkill(t *testing.T, manager *skills.Manager, ctx context.Context, name string) {
	t.Helper()
	description := "检查服务状态"
	body := "Markdown steps: run systemctl status first.\n"
	if _, err := manager.SaveOverride(ctx, skills.OverrideInput{Name: name, Description: &description, Body: &body}); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSkillEinoToolExecutesThroughTheRealGraph(t *testing.T) {
	// Unattended mode is the strictest path: the read-only classification is
	// what keeps the tool usable there.
	execution, manager := newSkillExecution(t, false, guard.Unattended, false)
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	seedSkill(t, manager, ctx, "service-check")

	content := invokeLoadSkill(t, execution, ctx, `{"name":"service-check"}`)
	dispatch, err := manager.Dispatch(ctx, "service-check")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"ok":true`, dispatch.Digest, "Markdown"} {
		if !strings.Contains(content, fragment) {
			t.Fatalf("tool result missing %q: %s", fragment, content)
		}
	}

	// The eino surface decodes through the typed args struct (unknown fields
	// are dropped by the framework's decode, as for every tool); a missing
	// name still fails closed in the handler.
	if content := invokeLoadSkill(t, execution, ctx, `{}`); !strings.Contains(content, "skill name is required") || !strings.Contains(content, `"ok":false`) {
		t.Fatalf("missing name was not rejected: %s", content)
	}
	// An unknown skill fails visibly with the not-found guidance.
	if content := invokeLoadSkill(t, execution, ctx, `{"name":"missing-skill"}`); !strings.Contains(content, "不存在") || !strings.Contains(content, `"ok":false`) {
		t.Fatalf("unknown skill result = %s", content)
	}
}

func TestLoadSkillEinoToolHonorsOverrideDisabledAndOwners(t *testing.T) {
	execution, manager := newSkillExecution(t, false, guard.ReadWrite, false)
	ctxA := ipc.WithUserID(t.Context(), "owner-a")
	ctxB := ipc.WithUserID(t.Context(), "owner-b")

	description := "实例格式说明"
	body := "custom body\n"
	if _, err := manager.SaveOverride(ctxA, skills.OverrideInput{Name: "ops-check", Description: &description, Body: &body}); err != nil {
		t.Fatal(err)
	}
	contentA := invokeLoadSkill(t, execution, ctxA, `{"name":"ops-check"}`)
	dispatchA, err := manager.Dispatch(ctxA, "ops-check")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"ok":true`, dispatchA.Digest, description, "custom body"} {
		if !strings.Contains(contentA, fragment) {
			t.Fatalf("owner A result missing %q: %s", fragment, contentA)
		}
	}

	contentB := invokeLoadSkill(t, execution, ctxB, `{"name":"ops-check"}`)
	if !strings.Contains(contentB, `"ok":false`) || strings.Contains(contentB, "custom body") || strings.Contains(contentB, description) {
		t.Fatalf("owner A's skill leaked to owner B: %s", contentB)
	}
	seedSkill(t, manager, ctxB, "ops-check")

	if _, err := manager.SaveOverride(ctxA, skills.OverrideInput{Name: "ops-check", Description: &description, Body: &body, Disabled: true}); err != nil {
		t.Fatal(err)
	}
	contentA = invokeLoadSkill(t, execution, ctxA, `{"name":"ops-check"}`)
	if !strings.Contains(contentA, `"ok":false`) || !strings.Contains(contentA, "禁用") {
		t.Fatalf("disabled load_skill result = %s", contentA)
	}
	if contentB = invokeLoadSkill(t, execution, ctxB, `{"name":"ops-check"}`); !strings.Contains(contentB, `"ok":true`) {
		t.Fatalf("owner B must stay enabled: %s", contentB)
	}
}

func TestLoadSkillEinoToolStaysAvailableInPlanMode(t *testing.T) {
	plain, _ := newSkillExecution(t, false, guard.ReadWrite, false)
	if names := einoToolNames(t, plain); names["exit_plan_mode"] {
		t.Fatal("exit_plan_mode must stay hidden outside plan mode")
	}
	planned, manager := newSkillExecution(t, false, guard.ReadWrite, true)
	names := einoToolNames(t, planned)
	if !names["load_skill"] || !names["exit_plan_mode"] {
		t.Fatalf("plan-mode tool list = %v", names)
	}
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	seedSkill(t, manager, ctx, "ops-check")
	content := invokeLoadSkill(t, planned, ctx, `{"name":"ops-check"}`)
	dispatch, err := manager.Dispatch(ctx, "ops-check")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, `"ok":true`) || !strings.Contains(content, dispatch.Digest) {
		t.Fatalf("plan-mode load_skill result = %s", content)
	}
}

func TestLoadSkillEinoToolServesLegacySharedStateInReadOnlyMode(t *testing.T) {
	execution, manager := newSkillExecution(t, true, guard.ReadOnly, false)
	body := "shared body\n"
	description := "共享检查"
	if _, err := manager.SaveOverride(t.Context(), skills.OverrideInput{Name: "ops-check", Description: &description, Body: &body}); err != nil {
		t.Fatal(err)
	}
	content := invokeLoadSkill(t, execution, t.Context(), `{"name":"ops-check"}`)
	dispatch, err := manager.Dispatch(t.Context(), "ops-check")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"ok":true`, dispatch.Digest, "shared body"} {
		if !strings.Contains(content, fragment) {
			t.Fatalf("legacy shared result missing %q: %s", fragment, content)
		}
	}
}
