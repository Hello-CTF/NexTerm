package production

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/skills"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// TestComposeAIRuntimeSharesSingleSkillsManager proves the production AI
// runtime wires the services-level skills manager into the tools registry:
// load_skill is exposed, and executing it through the runner's registry
// returns exactly what the same services-level instance dispatches — the
// instance the ai-skills IPC module registers — so the tool and the IPC
// surface share one per-owner state instead of splitting two managers.
func TestComposeAIRuntimeSharesSingleSkillsManager(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	profileManager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager(session.Config{IdleTimeout: -1})
	skillsManager, err := skills.NewManager(ctx, database, skills.WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	services := &ProductionServices{Store: database, Profiles: profileManager, Sessions: sessions, Skills: skillsManager}
	if err := composeAIRuntime(ctx, services); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		services.closeAIRuntime()
		_ = sessions.Close()
		_ = database.Close()
	})

	body := "watch the deploy window and report failures"
	description := "deploy watch notes"
	if _, err := services.Skills.SaveOverride(ctx, skills.OverrideInput{Name: "ops-note", Description: &description, Body: &body}); err != nil {
		t.Fatal(err)
	}

	registry := services.Agent.ToolsRegistry()
	if registry == nil {
		t.Fatal("the composed runner has no tools registry")
	}
	call := tools.Call{ID: "call-1", Name: "load_skill", Args: json.RawMessage(`{"name":"ops-note"}`)}
	if err := registry.Validate(call); err != nil {
		t.Fatalf("load_skill is not exposed by the composed registry: %v", err)
	}
	viaRegistry := registry.Execute(ctx, "job-1", tools.Scope{}, call, nil)
	viaManager := services.Skills.LoadSkill(ctx, call.Args)
	if !viaRegistry.OK || !viaManager.OK {
		t.Fatalf("load_skill failed: registry=%+v manager=%+v", viaRegistry, viaManager)
	}
	if viaRegistry.Text != viaManager.Text {
		t.Fatalf("load_skill outputs diverge:\nregistry: %q\nmanager:  %q", viaRegistry.Text, viaManager.Text)
	}

	// The IPC surface registered with the same services-level instance lists
	// the override the tool just dispatched.
	dispatcher := ipc.NewDispatcher()
	if err := skills.RegisterIPC(dispatcher, services.Skills); err != nil {
		t.Fatal(err)
	}
	response := dispatcher.Dispatch(ctx, ipc.Request{Command: skills.CommandList, Args: json.RawMessage(`{}`)}, ipc.Environment{})
	if !response.OK {
		t.Fatalf("%s = %+v", skills.CommandList, response.Error)
	}
	var entries []skills.Entry
	if err := json.Unmarshal(response.Data, &entries); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Name == "ops-note" && entry.Override != nil && entry.Override.Body != nil && *entry.Override.Body == body {
			found = true
		}
	}
	if !found {
		t.Fatalf("ops-note override is not visible over the skills IPC: %+v", entries)
	}
}

// TestComposeAIRuntimeWithoutSkillsKeepsLoadSkillUnavailable proves the nil
// path stays honest: without a services-level manager the registry does not
// invent a skills backend.
func TestComposeAIRuntimeWithoutSkillsKeepsLoadSkillUnavailable(t *testing.T) {
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	profileManager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager(session.Config{IdleTimeout: -1})
	services := &ProductionServices{Store: database, Profiles: profileManager, Sessions: sessions}
	if err := composeAIRuntime(ctx, services); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		services.closeAIRuntime()
		_ = sessions.Close()
		_ = database.Close()
	})
	registry := services.Agent.ToolsRegistry()
	if registry == nil {
		t.Fatal("the composed runner has no tools registry")
	}
	call := tools.Call{ID: "call-1", Name: "load_skill", Args: json.RawMessage(`{"name":"ops-note"}`)}
	if err := registry.Validate(call); err == nil {
		t.Fatal("load_skill validated without a configured skills manager")
	}
	if output := registry.Execute(ctx, "job-1", tools.Scope{}, call, nil); output.OK || output.Panic {
		t.Fatalf("load_skill without a manager must fail closed without panicking: %+v", output)
	}
}
