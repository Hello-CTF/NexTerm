package production

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ai/skills"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// These tests exercise the production skills wiring recipe end to end: the
// real settings store, the real embedded index, the real dispatcher, and the
// same construction the production module registration performs
// (skills.NewManager over the store with the shared flag, then
// skills.RegisterIPC). They pin the contract the runtime registration must
// preserve — per-owner isolation, digest pinning, and disabled/override
// semantics — before the module entry itself lands.

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

func TestSkillsIPCProductionEmbeddedRoundTrip(t *testing.T) {
	database := openSkillsTestStore(t)
	dispatcher := registerProductionSkillsIPC(t, database, true)
	ctx := t.Context()

	response := dispatchSkillsIPC(ctx, dispatcher, skills.CommandList, `{}`)
	var entries []skills.Entry
	requireSkillsIPCResponse(t, response, &entries)
	var embedded *skills.Entry
	for index := range entries {
		if entries[index].Name == "skill-format" {
			embedded = &entries[index]
		}
	}
	if embedded == nil {
		t.Fatalf("embedded skill-format missing from list: %+v", entries)
	}
	if embedded.Source != skills.SourceEmbedded || embedded.Disabled || embedded.Overridden || embedded.Digest == "" {
		t.Fatalf("embedded entry = %+v", embedded)
	}

	response = dispatchSkillsIPC(ctx, dispatcher, skills.CommandDispatch, `{"name":"skill-format"}`)
	var baseline skills.Dispatch
	requireSkillsIPCResponse(t, response, &baseline)
	if baseline.Source != skills.SourceEmbedded || baseline.Body == "" || baseline.Digest != embedded.Digest {
		t.Fatalf("baseline dispatch = %+v", baseline)
	}
	// The pinned digest is exactly the digest of the dispatched material —
	// no more, no less.
	if want := skills.DigestOf(skills.Skill{Name: baseline.Name, Description: baseline.Description, Body: baseline.Body}); baseline.Digest != want {
		t.Fatalf("dispatch digest = %q, want digest of dispatched material %q", baseline.Digest, want)
	}

	response = dispatchSkillsIPC(ctx, dispatcher, skills.CommandSaveOverride, `{"name":"skill-format","description":"格式说明（实例定制）"}`)
	var saved skills.Entry
	requireSkillsIPCResponse(t, response, &saved)
	if !saved.Overridden || saved.Override == nil || saved.Override.Description == nil || saved.Override.Body != nil {
		t.Fatalf("saved entry = %+v", saved)
	}

	response = dispatchSkillsIPC(ctx, dispatcher, skills.CommandDispatch, `{"name":"skill-format"}`)
	var overridden skills.Dispatch
	requireSkillsIPCResponse(t, response, &overridden)
	if overridden.Description != "格式说明（实例定制）" || overridden.Body != baseline.Body {
		t.Fatalf("overridden dispatch = %+v", overridden)
	}
	if overridden.Digest == baseline.Digest {
		t.Fatal("override did not change the pinned digest")
	}
	if want := skills.DigestOf(skills.Skill{Name: overridden.Name, Description: overridden.Description, Body: overridden.Body}); overridden.Digest != want {
		t.Fatalf("override digest = %q, want %q", overridden.Digest, want)
	}

	response = dispatchSkillsIPC(ctx, dispatcher, skills.CommandDeleteOverride, `{"name":"skill-format"}`)
	if !response.OK {
		t.Fatalf("delete response = %+v", response)
	}
	response = dispatchSkillsIPC(ctx, dispatcher, skills.CommandDispatch, `{"name":"skill-format"}`)
	var reverted skills.Dispatch
	requireSkillsIPCResponse(t, response, &reverted)
	if reverted.Digest != baseline.Digest || reverted.Description != baseline.Description {
		t.Fatalf("delete did not revert the embedded material: %+v", reverted)
	}
}

func TestSkillsIPCProductionOwnerIsolationSurvivesRestart(t *testing.T) {
	database := openSkillsTestStore(t)
	dispatcher := registerProductionSkillsIPC(t, database, false)
	ctxA := ipc.WithUserID(t.Context(), "owner-a")
	ctxB := ipc.WithUserID(t.Context(), "owner-b")

	response := dispatchSkillsIPC(ctxA, dispatcher, skills.CommandSaveOverride, `{"name":"skill-format","body":"owner A body\n"}`)
	if !response.OK {
		t.Fatalf("owner A save response = %+v", response)
	}
	response = dispatchSkillsIPC(ctxA, dispatcher, skills.CommandSaveOverride, `{"name":"owner-a-custom","description":"A custom","body":"custom body\n"}`)
	if !response.OK {
		t.Fatalf("owner A custom save response = %+v", response)
	}

	response = dispatchSkillsIPC(ctxB, dispatcher, skills.CommandDispatch, `{"name":"skill-format"}`)
	var dispatchB skills.Dispatch
	requireSkillsIPCResponse(t, response, &dispatchB)
	if strings.Contains(dispatchB.Body, "owner A") {
		t.Fatalf("owner A's override leaked to owner B: %+v", dispatchB)
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
	response = dispatchSkillsIPC(ctxA, restarted, skills.CommandDispatch, `{"name":"skill-format"}`)
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

	response := dispatchSkillsIPC(ctx, dispatcher, skills.CommandSaveOverride, `{"name":"skill-format","disabled":true}`)
	if !response.OK {
		t.Fatalf("disable response = %+v", response)
	}
	requireSkillsIPCCode(t, dispatchSkillsIPC(ctx, dispatcher, skills.CommandDispatch, `{"name":"skill-format"}`), ipc.CodeBadParam)
	var material skills.Dispatch
	requireSkillsIPCResponse(t, dispatchSkillsIPC(ctx, dispatcher, skills.CommandMaterial, `{"name":"skill-format"}`), &material)
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
