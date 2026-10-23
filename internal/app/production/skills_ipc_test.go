package production

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/skills"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// These tests exercise the production skills wiring end to end: the real
// settings store, the real dispatcher, and the
// construction the production module registration performs
// (skills.NewManager over the store with the shared flag, then
// skills.RegisterIPC). They pin the contract the runtime registration must
// preserve — per-owner isolation, digest pinning, and disabled/override
// semantics. The composed-runtime tests at the bottom prove the landed
// production wiring: the ai-skills IPC surface and the load_skill tool
// share one services-level manager instance.

func registerProductionSkillsIPC(t *testing.T, database *store.Store, shared bool) *ipc.Dispatcher {
	t.Helper()
	dispatcher := ipc.NewDispatcher()
	manager, err := skills.NewManager(context.Background(), database, skills.WithShared(shared))
	if err != nil {
		t.Fatal(err)
	}
	if err := skills.RegisterIPC(dispatcher, manager); err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func dispatchSkillsIPC(ctx context.Context, dispatcher *ipc.Dispatcher, command, args string) ipc.Response {
	return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
}

func requireSkillsIPCResponse(t *testing.T, response ipc.Response, target any) {
	t.Helper()
	if !response.OK {
		t.Fatalf("dispatch failed: %+v", response)
	}
	if err := json.Unmarshal(response.Data, target); err != nil {
		t.Fatalf("decode %s: %v", response.Data, err)
	}
}

func requireSkillsIPCCode(t *testing.T, response ipc.Response, code ipc.Code) {
	t.Helper()
	if response.OK || response.Error == nil || response.Error.Code != code {
		t.Fatalf("response = %+v, want ipc code %s", response, code)
	}
}

func openSkillsTestStore(t *testing.T) *store.Store {
	t.Helper()
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestSkillsIPCProductionCustomRoundTrip(t *testing.T) {
	database := openSkillsTestStore(t)
	dispatcher := registerProductionSkillsIPC(t, database, true)
	ctx := t.Context()

	var entries []skills.Entry
	requireSkillsIPCResponse(t, dispatchSkillsIPC(ctx, dispatcher, skills.CommandList, `{}`), &entries)
	if len(entries) != 0 {
		t.Fatalf("NexTerm ships no built-in skills, got %+v", entries)
	}

	var created skills.Entry
	requireSkillsIPCResponse(t, dispatchSkillsIPC(ctx, dispatcher, skills.CommandSaveOverride, `{"name":"nginx-check","description":"检查 nginx 配置并重载","body":"先运行 nginx -t\n"}`), &created)
	if created.Source != skills.SourceCustom || created.Disabled || created.Digest == "" {
		t.Fatalf("created entry = %+v", created)
	}
	var baseline skills.Dispatch
	requireSkillsIPCResponse(t, dispatchSkillsIPC(ctx, dispatcher, skills.CommandDispatch, `{"name":"nginx-check"}`), &baseline)
	if baseline.Body != "先运行 nginx -t\n" || baseline.Digest != created.Digest {
		t.Fatalf("dispatch = %+v", baseline)
	}
	// The pinned digest is exactly the digest of the dispatched material.
	if want := skills.DigestOf(skills.Skill{Name: baseline.Name, Description: baseline.Description, Body: baseline.Body}); baseline.Digest != want {
		t.Fatalf("dispatch digest = %q, want digest of dispatched material %q", baseline.Digest, want)
	}

	requireSkillsIPCResponse(t, dispatchSkillsIPC(ctx, dispatcher, skills.CommandSaveOverride, `{"name":"nginx-check","description":"检查并重载 nginx","body":"先运行 nginx -t，再 reload\n"}`), &created)
	var updated skills.Dispatch
	requireSkillsIPCResponse(t, dispatchSkillsIPC(ctx, dispatcher, skills.CommandDispatch, `{"name":"nginx-check"}`), &updated)
	if updated.Description != "检查并重载 nginx" || updated.Digest == baseline.Digest {
		t.Fatalf("update did not change the material: %+v", updated)
	}

	if response := dispatchSkillsIPC(ctx, dispatcher, skills.CommandDeleteOverride, `{"name":"nginx-check"}`); !response.OK {
		t.Fatalf("delete response = %+v", response)
	}
	requireSkillsIPCCode(t, dispatchSkillsIPC(ctx, dispatcher, skills.CommandDispatch, `{"name":"nginx-check"}`), ipc.CodeNotFound)
}

func TestSkillsIPCProductionOwnerIsolationSurvivesRestart(t *testing.T) {
	database := openSkillsTestStore(t)
	dispatcher := registerProductionSkillsIPC(t, database, false)
	ctxA := ipc.WithUserID(t.Context(), "owner-a")
	ctxB := ipc.WithUserID(t.Context(), "owner-b")

	response := dispatchSkillsIPC(ctxA, dispatcher, skills.CommandSaveOverride, `{"name":"owner-a-custom","description":"A custom","body":"owner A body\n"}`)
	if !response.OK {
		t.Fatalf("owner A custom save response = %+v", response)
	}

	requireSkillsIPCCode(t, dispatchSkillsIPC(ctxB, dispatcher, skills.CommandDispatch, `{"name":"owner-a-custom"}`), ipc.CodeNotFound)

	// The real store persists each owner under its own settings key.
	if _, found, err := database.SettingGet(t.Context(), skills.SettingKeyForUser("owner-a")); err != nil || !found {
		t.Fatalf("owner A persisted state: found=%t err=%v", found, err)
	}
	if _, found, err := database.SettingGet(t.Context(), skills.SettingKeyForUser("owner-b")); err != nil || found {
		t.Fatalf("owner B must not have persisted state: found=%t err=%v", found, err)
	}

	// A fresh manager over the same store — the process-restart path — keeps
	// owner A's overrides and still isolates owner B.
	restarted := registerProductionSkillsIPC(t, database, false)
	response = dispatchSkillsIPC(ctxA, restarted, skills.CommandDispatch, `{"name":"owner-a-custom"}`)
	var dispatchA skills.Dispatch
	requireSkillsIPCResponse(t, response, &dispatchA)
	if dispatchA.Body != "owner A body\n" {
		t.Fatalf("owner A override lost across restart: %+v", dispatchA)
	}
	var entriesB []skills.Entry
	requireSkillsIPCResponse(t, dispatchSkillsIPC(ctxB, restarted, skills.CommandList, `{}`), &entriesB)
	for _, entry := range entriesB {
		if entry.Name == "owner-a-custom" || entry.Overridden {
			t.Fatalf("owner A state visible to owner B after restart: %+v", entry)
		}
	}
}

func TestSkillsIPCProductionAccessMatrix(t *testing.T) {
	database := openSkillsTestStore(t)
	server := registerProductionSkillsIPC(t, database, false)

	// A server gateway request without an account never reaches shared state.
	requireSkillsIPCCode(t, dispatchSkillsIPC(t.Context(), server, skills.CommandList, `{}`), ipc.CodeForbidden)

	// An explicit open deployment resolves to the shared state.
	open := owner.WithOpenAccess(t.Context())
	var sharedEntries []skills.Entry
	requireSkillsIPCResponse(t, dispatchSkillsIPC(open, server, skills.CommandList, `{}`), &sharedEntries)

	// An account owner resolves to its own state.
	ownerCtx := ipc.WithUserID(t.Context(), "owner-a")
	var ownerEntries []skills.Entry
	requireSkillsIPCResponse(t, dispatchSkillsIPC(ownerCtx, server, skills.CommandList, `{}`), &ownerEntries)

	// Desktop wiring (shared) serves ownerless requests.
	desktop := registerProductionSkillsIPC(t, openSkillsTestStore(t), true)
	var desktopEntries []skills.Entry
	requireSkillsIPCResponse(t, dispatchSkillsIPC(t.Context(), desktop, skills.CommandList, `{}`), &desktopEntries)
}

func TestSkillsIPCProductionDisabledAndCustomLifecycle(t *testing.T) {
	database := openSkillsTestStore(t)
	dispatcher := registerProductionSkillsIPC(t, database, false)
	ctx := ipc.WithUserID(t.Context(), "owner-a")

	response := dispatchSkillsIPC(ctx, dispatcher, skills.CommandSaveOverride, `{"name":"disk-report","description":"整理磁盘占用","body":"用 df -h\n","disabled":true}`)
	if !response.OK {
		t.Fatalf("disable response = %+v", response)
	}
	requireSkillsIPCCode(t, dispatchSkillsIPC(ctx, dispatcher, skills.CommandDispatch, `{"name":"disk-report"}`), ipc.CodeBadParam)
	var material skills.Dispatch
	requireSkillsIPCResponse(t, dispatchSkillsIPC(ctx, dispatcher, skills.CommandMaterial, `{"name":"disk-report"}`), &material)
	if material.Body == "" || material.Digest == "" {
		t.Fatalf("disabled material must stay readable for Settings: %+v", material)
	}
	var entries []skills.Entry
	requireSkillsIPCResponse(t, dispatchSkillsIPC(ctx, dispatcher, skills.CommandList, `{}`), &entries)
	if len(entries) != 1 || !entries[0].Disabled {
		t.Fatalf("disabled skill must stay listed: %+v", entries)
	}

	response = dispatchSkillsIPC(ctx, dispatcher, skills.CommandSaveOverride, `{"name":"ops-runbook","description":"Ops runbook","body":"runbook body\n"}`)
	var custom skills.Entry
	requireSkillsIPCResponse(t, response, &custom)
	if custom.Source != skills.SourceCustom || custom.Overridden || custom.Digest == "" {
		t.Fatalf("custom entry = %+v", custom)
	}
	var customDispatch skills.Dispatch
	requireSkillsIPCResponse(t, dispatchSkillsIPC(ctx, dispatcher, skills.CommandDispatch, `{"name":"ops-runbook"}`), &customDispatch)
	if customDispatch.Source != skills.SourceCustom || customDispatch.Digest != custom.Digest {
		t.Fatalf("custom dispatch = %+v", customDispatch)
	}

	if response := dispatchSkillsIPC(ctx, dispatcher, skills.CommandDeleteOverride, `{"name":"ops-runbook"}`); !response.OK {
		t.Fatalf("custom delete response = %+v", response)
	}
	requireSkillsIPCCode(t, dispatchSkillsIPC(ctx, dispatcher, skills.CommandDispatch, `{"name":"ops-runbook"}`), ipc.CodeNotFound)
	requireSkillsIPCCode(t, dispatchSkillsIPC(ctx, dispatcher, skills.CommandSaveOverride, `{"name":"Bad_Name","disabled":true}`), ipc.CodeBadParam)
	requireSkillsIPCCode(t, dispatchSkillsIPC(ctx, dispatcher, skills.CommandSaveOverride, `{"name":"another-custom","description":"missing body"}`), ipc.CodeBadParam)
}

// composeProductionSkillsRuntime builds the real production AI runtime
// composition over a real store and returns the services-level skills
// manager together with the composed runner's tools registry. The manager is
// the same instance the production builder hands to the ai-skills IPC
// module, so these tests observe exactly what production wires.
func composeProductionSkillsRuntime(t *testing.T, shared bool) (*skills.Manager, *tools.Registry) {
	t.Helper()
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	profileManager, err := profiles.NewManager(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager(session.Config{IdleTimeout: -1})
	skillsManager, err := skills.NewManager(t.Context(), database, skills.WithShared(shared))
	if err != nil {
		t.Fatal(err)
	}
	services := &ProductionServices{Store: database, Profiles: profileManager, Sessions: sessions, Skills: skillsManager}
	if err := composeAIRuntime(t.Context(), services); err != nil {
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
	return skillsManager, registry
}

func loadSkillThroughRegistry(registry *tools.Registry, ctx context.Context, name string) tools.Output {
	return registry.Execute(ctx, "job-skills", tools.Scope{}, tools.Call{
		ID: "call-load", Name: "load_skill", Args: json.RawMessage(`{"name":"` + name + `"}`),
	}, nil)
}

// The production proof: the IPC surface and load_skill consume one shared
// manager. An override saved over IPC is exactly what load_skill returns,
// with the same pinned digest as the IPC dispatch surface; a disable over
// IPC closes load_skill while the Settings read path stays open; and another
// owner's load_skill keeps seeing the embedded material.
func TestSkillsIPCProductionSurfacesShareOverridesAndDigests(t *testing.T) {
	manager, registry := composeProductionSkillsRuntime(t, false)
	dispatcher := ipc.NewDispatcher()
	if err := skills.RegisterIPC(dispatcher, manager); err != nil {
		t.Fatal(err)
	}
	ctxA := ipc.WithUserID(t.Context(), "owner-a")
	ctxB := ipc.WithUserID(t.Context(), "owner-b")

	response := dispatchSkillsIPC(ctxA, dispatcher, skills.CommandSaveOverride, `{"name":"ops-check","description":"实例格式说明","body":"custom body\n"}`)
	var saved skills.Entry
	requireSkillsIPCResponse(t, response, &saved)
	if saved.Source != skills.SourceCustom || saved.Digest == "" {
		t.Fatalf("saved entry = %+v", saved)
	}

	var dispatched skills.Dispatch
	requireSkillsIPCResponse(t, dispatchSkillsIPC(ctxA, dispatcher, skills.CommandDispatch, `{"name":"ops-check"}`), &dispatched)
	output := loadSkillThroughRegistry(registry, ctxA, "ops-check")
	if !output.OK {
		t.Fatalf("load_skill output = %+v", output)
	}
	for _, fragment := range []string{"skill: ops-check", "digest: " + dispatched.Digest, "实例格式说明", "custom body\n"} {
		if !strings.Contains(output.Text, fragment) {
			t.Fatalf("load_skill text missing %q:\n%s", fragment, output.Text)
		}
	}

	// Disabling preserves the overridden fields, the same payload the
	// Settings UI sends when toggling an overridden skill off.
	if response := dispatchSkillsIPC(ctxA, dispatcher, skills.CommandSaveOverride, `{"name":"ops-check","description":"实例格式说明","body":"custom body\n","disabled":true}`); !response.OK {
		t.Fatalf("disable response = %+v", response)
	}
	output = loadSkillThroughRegistry(registry, ctxA, "ops-check")
	if output.OK || !strings.Contains(output.Text, "禁用") {
		t.Fatalf("disabled load_skill output = %+v", output)
	}
	var material skills.Dispatch
	requireSkillsIPCResponse(t, dispatchSkillsIPC(ctxA, dispatcher, skills.CommandMaterial, `{"name":"ops-check"}`), &material)
	if material.Body != "custom body\n" {
		t.Fatalf("disabled material must stay readable for Settings: %+v", material)
	}

	outputB := loadSkillThroughRegistry(registry, ctxB, "ops-check")
	if outputB.OK || strings.Contains(outputB.Text, "custom body\n") || strings.Contains(outputB.Text, "实例格式说明") {
		t.Fatalf("owner A's skill leaked to owner B's load_skill: %+v", outputB)
	}
	requireSkillsIPCCode(t, dispatchSkillsIPC(ctxB, dispatcher, skills.CommandDispatch, `{"name":"ops-check"}`), ipc.CodeNotFound)
}

// Legacy shared behavior: with the desktop/open shared flag, ownerless
// requests resolve the shared override state on both surfaces.
func TestSkillsIPCProductionLegacySharedLoadSkill(t *testing.T) {
	manager, registry := composeProductionSkillsRuntime(t, true)
	dispatcher := ipc.NewDispatcher()
	if err := skills.RegisterIPC(dispatcher, manager); err != nil {
		t.Fatal(err)
	}

	response := dispatchSkillsIPC(t.Context(), dispatcher, skills.CommandSaveOverride, `{"name":"ops-check","description":"共享检查","body":"shared body\n"}`)
	if !response.OK {
		t.Fatalf("shared save response = %+v", response)
	}
	output := loadSkillThroughRegistry(registry, t.Context(), "ops-check")
	if !output.OK || !strings.Contains(output.Text, "shared body\n") {
		t.Fatalf("legacy shared load_skill output = %+v", output)
	}
	var dispatched skills.Dispatch
	requireSkillsIPCResponse(t, dispatchSkillsIPC(t.Context(), dispatcher, skills.CommandDispatch, `{"name":"ops-check"}`), &dispatched)
	if !strings.Contains(output.Text, dispatched.Digest) {
		t.Fatalf("load_skill digest %q does not pin the IPC dispatch digest %q", output.Text, dispatched.Digest)
	}
}
