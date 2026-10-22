package production

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/memory"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func composeAccountMemoryRuntime(t *testing.T, providerURL string) (*ProductionServices, *store.Store) {
	t.Helper()
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	initTestVault(t, ctx, database)
	profileManager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager(session.Config{})
	services := &ProductionServices{Store: database, Profiles: profileManager, Sessions: sessions, dataDir: t.TempDir(), desktop: false}
	if err := composeAIRuntime(ctx, services); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		services.closeAIRuntime()
		_ = sessions.Close()
		_ = database.Close()
	})
	if providerURL != "" {
		for _, ownerID := range []string{"owner-a", "owner-b"} {
			if _, err := profileManager.Save(ipc.WithUserID(ctx, ownerID), profiles.Profile{
				BaseURL: providerURL, APIKey: "test-key", Model: "test-model", ContextWindow: 32768, Stream: true,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return services, database
}

func memoryAccountDispatch(t *testing.T, dispatcher *ipc.Dispatcher, ctx context.Context, command, args string) ipc.Response {
	t.Helper()
	return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
}

func TestProductionMemoryScopeForAccountDesktopAndOpen(t *testing.T) {
	account, err := productionMemoryScopeFor(false)(ipc.WithUserID(context.Background(), "owner-a"))
	if err != nil || account != (memory.Scope{Tenant: "user", Subject: "owner-a"}) {
		t.Fatalf("account scope = %+v, err = %v", account, err)
	}
	for _, test := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "desktop", ctx: context.Background()},
		{name: "open", ctx: owner.WithOpenAccess(context.Background())},
	} {
		var scope memory.Scope
		if test.name == "desktop" {
			scope, err = productionMemoryScopeFor(true)(test.ctx)
		} else {
			scope, err = productionMemoryScopeFor(false)(test.ctx)
		}
		if err != nil || scope != productionMemoryScope {
			t.Fatalf("%s scope = %+v, err = %v", test.name, scope, err)
		}
	}
	if _, err := productionMemoryScopeFor(false)(context.Background()); err == nil || ipc.NormalizeError(err).Code != ipc.CodeForbidden {
		t.Fatalf("ownerless account scope error = %v", err)
	}
}

func TestAccountMemoryIPCCRUDAndSettingsIgnoreSubmittedScope(t *testing.T) {
	services, database := composeAccountMemoryRuntime(t, "")
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	scopeA := memory.Scope{Tenant: "user", Subject: "owner-a"}
	scopeB := memory.Scope{Tenant: "user", Subject: "owner-b"}
	memoryStore, err := memory.NewStore(database.GORM())
	if err != nil {
		t.Fatal(err)
	}
	other, err := memoryStore.Create(context.Background(), scopeB, memory.CreateInput{Topic: "private", Content: "owner-b-existing"})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := ipc.NewDispatcher()
	if err := agent.Module(services.Agent).RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	forgedB := `{"tenant":"user","subject":"owner-b"}`
	forgedA := `{"tenant":"user","subject":"owner-a"}`
	created := memoryAccountDispatch(t, dispatcher, ctxA, "memory_create", `{"scope":`+forgedB+`,"topic":"ops","content":"owner-a-created"}`)
	if !created.OK {
		t.Fatalf("create = %+v", created.Error)
	}
	var entry struct {
		ID      string
		Version uint64
	}
	if err := json.Unmarshal(created.Data, &entry); err != nil || entry.ID == "" || entry.Version != 1 {
		t.Fatalf("created entry = %+v, err = %v", entry, err)
	}
	if _, err := memoryStore.Get(context.Background(), scopeA, entry.ID); err != nil {
		t.Fatalf("entry was not written to request owner: %v", err)
	}
	if _, err := memoryStore.Get(context.Background(), scopeB, entry.ID); err == nil {
		t.Fatal("submitted scope redirected the create to another owner")
	}
	if got := memoryAccountDispatch(t, dispatcher, ctxA, "memory_get", `{"scope":`+forgedB+`,"id":"`+entry.ID+`"}`); !got.OK {
		t.Fatalf("own get with ignored scope = %+v", got.Error)
	}
	if got := memoryAccountDispatch(t, dispatcher, ctxA, "memory_get", `{"scope":`+forgedB+`,"id":"`+other.ID+`"}`); got.OK || got.Error == nil || got.Error.Code != ipc.CodeForbidden {
		t.Fatalf("foreign get = %+v", got)
	}
	if got := memoryAccountDispatch(t, dispatcher, ctxB, "memory_get", `{"scope":`+forgedA+`,"id":"`+entry.ID+`"}`); got.OK || got.Error == nil || got.Error.Code != ipc.CodeForbidden {
		t.Fatalf("reverse foreign get = %+v", got)
	}
	edited := memoryAccountDispatch(t, dispatcher, ctxA, "memory_edit", `{"scope":`+forgedB+`,"id":"`+entry.ID+`","expectedVersion":1,"content":"owner-a-updated"}`)
	if !edited.OK {
		t.Fatalf("edit = %+v", edited.Error)
	}
	for _, test := range []struct {
		ctx       context.Context
		forged    string
		present   string
		forbidden string
	}{
		{ctx: ctxA, forged: forgedB, present: entry.ID, forbidden: other.ID},
		{ctx: ctxB, forged: forgedA, present: other.ID, forbidden: entry.ID},
	} {
		index := memoryAccountDispatch(t, dispatcher, test.ctx, "memory_index", `{"scope":`+test.forged+`}`)
		if !index.OK || !strings.Contains(string(index.Data), test.present) || strings.Contains(string(index.Data), test.forbidden) {
			t.Fatalf("index = %s, err = %+v", index.Data, index.Error)
		}
	}
	updatedSettings := memoryAccountDispatch(t, dispatcher, ctxA, "memory_settings_set", `{"scope":`+forgedB+`,"expectedVersion":0,"injectionEnabled":false}`)
	if !updatedSettings.OK || !strings.Contains(string(updatedSettings.Data), `"version":1`) {
		t.Fatalf("owner A settings update = %s, err = %+v", updatedSettings.Data, updatedSettings.Error)
	}
	settingsB := memoryAccountDispatch(t, dispatcher, ctxB, "memory_settings_get", `{"scope":`+forgedA+`}`)
	if !settingsB.OK || !strings.Contains(string(settingsB.Data), `"injectionEnabled":true`) || !strings.Contains(string(settingsB.Data), `"version":0`) {
		t.Fatalf("owner A changed owner B settings: %s, err = %+v", settingsB.Data, settingsB.Error)
	}
	updatedB := memoryAccountDispatch(t, dispatcher, ctxB, "memory_settings_set", `{"scope":`+forgedA+`,"expectedVersion":0,"toolsEnabled":false}`)
	if !updatedB.OK {
		t.Fatalf("owner B settings update = %+v", updatedB.Error)
	}
	settingsA, err := memoryStore.Settings(context.Background(), scopeA)
	if err != nil || settingsA.InjectionEnabled || !settingsA.ToolsEnabled || settingsA.Version != 1 {
		t.Fatalf("owner A settings = %+v, err = %v", settingsA, err)
	}
	deleted := memoryAccountDispatch(t, dispatcher, ctxA, "memory_delete", `{"scope":`+forgedB+`,"id":"`+entry.ID+`","expectedVersion":2}`)
	if !deleted.OK {
		t.Fatalf("delete = %+v", deleted.Error)
	}
	for _, command := range []struct{ name, args string }{
		{"memory_create", `{"topic":"ops","content":"no-owner"}`},
		{"memory_get", `{"id":"missing"}`},
		{"memory_edit", `{"id":"missing","expectedVersion":1,"content":"x"}`},
		{"memory_delete", `{"id":"missing","expectedVersion":1}`},
		{"memory_index", `{}`},
		{"memory_settings_get", `{}`},
		{"memory_settings_set", `{"expectedVersion":0,"toolsEnabled":false}`},
	} {
		response := memoryAccountDispatch(t, dispatcher, context.Background(), command.name, command.args)
		if response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
			t.Errorf("%s without owner = %+v", command.name, response)
		}
	}
}

func TestCronMemoryInjectionUsesPersistedConversationOwner(t *testing.T) {
	script := &memoryScriptServer{}
	provider := httptest.NewServer(script)
	t.Cleanup(provider.Close)
	services, database := composeAccountMemoryRuntime(t, provider.URL)
	memoryStore, err := memory.NewStore(database.GORM())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ ownerID, content string }{{"owner-a", "cron-alpha-memory"}, {"owner-b", "cron-bravo-memory"}} {
		if _, err := memoryStore.Create(context.Background(), memory.Scope{Tenant: "user", Subject: item.ownerID}, memory.CreateInput{Topic: "ops", Content: item.content}); err != nil {
			t.Fatal(err)
		}
	}
	executor := &cron.AgentExecutor{Runner: cronAgentRunner{Runner: services.Agent}}
	for _, item := range []struct{ ownerID, prompt, present, forbidden string }{
		{ownerID: "owner-a", prompt: "cron-owner-a", present: "cron-alpha-memory", forbidden: "cron-bravo-memory"},
		{ownerID: "owner-b", prompt: "cron-owner-b", present: "cron-bravo-memory", forbidden: "cron-alpha-memory"},
	} {
		ownerCtx := ipc.WithUserID(context.Background(), item.ownerID)
		conversation, err := database.ConvCreate(ownerCtx, "cron-"+item.ownerID, map[string]any{"scope": map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		if err := executor.Execute(context.Background(), cron.Trigger{
			SessionID: conversation.ID, Prompt: item.prompt, OwnerID: item.ownerID, OwnerKind: cron.OwnerKindAccount,
		}); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, request := range script.snapshot() {
			isPrompt := false
			for _, message := range request.Messages {
				if message.Role == "user" && spawnMessageText(message.Content) == item.prompt {
					isPrompt = true
					break
				}
			}
			if !isPrompt {
				continue
			}
			found = true
			payload := string(mustJSON(t, request))
			if !strings.Contains(payload, item.present) || strings.Contains(payload, item.forbidden) {
				t.Fatalf("cron request for %s contains incorrect memory: %s", item.ownerID, payload)
			}
		}
		if !found {
			t.Fatalf("no provider request for %s", item.ownerID)
		}
	}
}
