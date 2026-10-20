package skills_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/skills"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func dispatchSkillCommand(t *testing.T, dispatcher *ipc.Dispatcher, ctx context.Context, command, args string) ipc.Response {
	t.Helper()
	return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
}

func requireResponseCode(t *testing.T, response ipc.Response, code ipc.Code) {
	t.Helper()
	if response.OK || response.Error == nil || response.Error.Code != code {
		t.Fatalf("response = %+v, want ipc code %s", response, code)
	}
}

func TestRegisterIPCRoundTrip(t *testing.T) {
	dispatcher := ipc.NewDispatcher()
	manager, err := skills.NewManager(t.Context(), newMemorySettings(), skills.WithIndex(testIndex(t)), skills.WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := skills.RegisterIPC(dispatcher, manager); err != nil {
		t.Fatal(err)
	}
	registered := dispatcher.Commands()
	if len(registered) != 5 {
		t.Fatalf("registered commands = %v", registered)
	}
	ctx := ipc.WithUserID(t.Context(), "owner-a")

	response := dispatchSkillCommand(t, dispatcher, ctx, skills.CommandList, `{}`)
	if !response.OK {
		t.Fatalf("list response = %+v", response)
	}
	var entries []skills.Entry
	if err := json.Unmarshal(response.Data, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "alpha-skill" || entries[0].Digest == "" {
		t.Fatalf("entries = %+v", entries)
	}

	response = dispatchSkillCommand(t, dispatcher, ctx, skills.CommandSaveOverride, `{"name":"alpha-skill","description":"Alpha overridden"}`)
	if !response.OK {
		t.Fatalf("save response = %+v", response)
	}
	var saved skills.Entry
	if err := json.Unmarshal(response.Data, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Overridden || saved.Override == nil || saved.Override.Description == nil || *saved.Override.Description != "Alpha overridden" || saved.Override.Body != nil {
		t.Fatalf("saved entry = %+v", saved)
	}

	response = dispatchSkillCommand(t, dispatcher, ctx, skills.CommandDispatch, `{"name":"alpha-skill"}`)
	if !response.OK {
		t.Fatalf("dispatch response = %+v", response)
	}
	var dispatch skills.Dispatch
	if err := json.Unmarshal(response.Data, &dispatch); err != nil {
		t.Fatal(err)
	}
	if dispatch.Description != "Alpha overridden" || dispatch.Body != "alpha body\n" || dispatch.Digest != saved.Digest {
		t.Fatalf("dispatch = %+v", dispatch)
	}

	response = dispatchSkillCommand(t, dispatcher, ctx, skills.CommandDeleteOverride, `{"name":"alpha-skill"}`)
	if !response.OK {
		t.Fatalf("delete response = %+v", response)
	}
	response = dispatchSkillCommand(t, dispatcher, ctx, skills.CommandDispatch, `{"name":"alpha-skill"}`)
	if !response.OK {
		t.Fatalf("dispatch after delete = %+v", response)
	}
	if err := json.Unmarshal(response.Data, &dispatch); err != nil {
		t.Fatal(err)
	}
	if dispatch.Description != "Alpha synthetic skill" {
		t.Fatalf("delete did not revert: %+v", dispatch)
	}
}

func TestRegisterIPCDispatchRejectsDisabledWhileMaterialReads(t *testing.T) {
	dispatcher := ipc.NewDispatcher()
	manager, err := skills.NewManager(t.Context(), newMemorySettings(), skills.WithIndex(testIndex(t)))
	if err != nil {
		t.Fatal(err)
	}
	if err := skills.RegisterIPC(dispatcher, manager); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	response := dispatchSkillCommand(t, dispatcher, ctx, skills.CommandSaveOverride, `{"name":"alpha-skill","disabled":true}`)
	if !response.OK {
		t.Fatalf("disable response = %+v", response)
	}

	response = dispatchSkillCommand(t, dispatcher, ctx, skills.CommandDispatch, `{"name":"alpha-skill"}`)
	requireResponseCode(t, response, ipc.CodeBadParam)

	response = dispatchSkillCommand(t, dispatcher, ctx, skills.CommandMaterial, `{"name":"alpha-skill"}`)
	if !response.OK {
		t.Fatalf("material response = %+v", response)
	}
	var material skills.Dispatch
	if err := json.Unmarshal(response.Data, &material); err != nil {
		t.Fatal(err)
	}
	if material.Body != "alpha body\n" || material.Digest == "" {
		t.Fatalf("material = %+v", material)
	}
}

func TestRegisterIPCErrorCodes(t *testing.T) {
	dispatcher := ipc.NewDispatcher()
	manager, err := skills.NewManager(t.Context(), newMemorySettings(), skills.WithIndex(testIndex(t)), skills.WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := skills.RegisterIPC(dispatcher, manager); err != nil {
		t.Fatal(err)
	}
	ctx := ipc.WithUserID(t.Context(), "owner-a")

	requireResponseCode(t, dispatchSkillCommand(t, dispatcher, ctx, skills.CommandDeleteOverride, `{"name":"alpha-skill"}`), ipc.CodeNotFound)
	requireResponseCode(t, dispatchSkillCommand(t, dispatcher, ctx, skills.CommandDispatch, `{"name":"missing-skill"}`), ipc.CodeNotFound)
	requireResponseCode(t, dispatchSkillCommand(t, dispatcher, ctx, skills.CommandSaveOverride, `{"name":"Bad_Name","disabled":true}`), ipc.CodeBadParam)
	requireResponseCode(t, dispatchSkillCommand(t, dispatcher, ctx, skills.CommandSaveOverride, `{"name":"custom-skill","description":"desc"}`), ipc.CodeBadParam)
	requireResponseCode(t, dispatchSkillCommand(t, dispatcher, ctx, skills.CommandSaveOverride, `{"name":"alpha-skill"}`), ipc.CodeBadParam)

	// Ownerless server requests never touch shared state.
	requireResponseCode(t, dispatchSkillCommand(t, dispatcher, t.Context(), skills.CommandList, `{}`), ipc.CodeForbidden)
}

func TestRegisterIPCRejectsDuplicateRegistration(t *testing.T) {
	dispatcher := ipc.NewDispatcher()
	manager, err := skills.NewManager(t.Context(), newMemorySettings(), skills.WithIndex(testIndex(t)))
	if err != nil {
		t.Fatal(err)
	}
	if err := skills.RegisterIPC(dispatcher, manager); err != nil {
		t.Fatal(err)
	}
	if err := skills.RegisterIPC(dispatcher, manager); err == nil {
		t.Fatal("duplicate registration succeeded")
	}
}

func TestRegisterIPCPreservesOwnerIsolation(t *testing.T) {
	dispatcher := ipc.NewDispatcher()
	manager, err := skills.NewManager(t.Context(), newMemorySettings(), skills.WithIndex(testIndex(t)), skills.WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := skills.RegisterIPC(dispatcher, manager); err != nil {
		t.Fatal(err)
	}
	ctxA := ipc.WithUserID(t.Context(), "owner-a")
	ctxB := ipc.WithUserID(t.Context(), "owner-b")

	response := dispatchSkillCommand(t, dispatcher, ctxA, skills.CommandSaveOverride, `{"name":"alpha-skill","body":"body from A\n"}`)
	if !response.OK {
		t.Fatalf("save response = %+v", response)
	}
	response = dispatchSkillCommand(t, dispatcher, ctxB, skills.CommandDispatch, `{"name":"alpha-skill"}`)
	if !response.OK {
		t.Fatalf("dispatch response = %+v", response)
	}
	var dispatch skills.Dispatch
	if err := json.Unmarshal(response.Data, &dispatch); err != nil {
		t.Fatal(err)
	}
	if dispatch.Body != "alpha body\n" {
		t.Fatalf("owner A's override leaked to owner B: %+v", dispatch)
	}
}
