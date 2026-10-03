package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/memory"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

// memoryIPCHarness registers the runner's commands — including the memory
// surface — on a real dispatcher backed by a real memory store.
func memoryIPCHarness(t *testing.T) (*ipc.Dispatcher, *memory.Store) {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	memoryStore := openRunnerMemory(t)
	runner := NewRunner(Config{Store: storage, Memory: memoryStore, MemoryScope: runnerMemoryScope})
	t.Cleanup(func() { _ = runner.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := runner.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	return dispatcher, memoryStore
}

func memoryDispatch(t *testing.T, dispatcher *ipc.Dispatcher, command, args string) ipc.Response {
	t.Helper()
	return dispatcher.Dispatch(context.Background(), ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
}

func memoryScopeJSON() string {
	return `{"tenant":"tenant-a","subject":"subject-a"}`
}

func TestMemoryIPCCommandsRegisterOnlyWithStore(t *testing.T) {
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	without := NewRunner(Config{Store: storage})
	defer without.Close()
	dispatcher := ipc.NewDispatcher()
	if err := without.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{memoryCommandCreate, memoryCommandGet, memoryCommandEdit, memoryCommandDelete, memoryCommandIndex, memoryCommandSettingsGet, memoryCommandSettingsSet} {
		for _, name := range dispatcher.Commands() {
			if name == command {
				t.Fatalf("memory command %s registered without a store", command)
			}
		}
	}
	with := NewRunner(Config{Store: storage, Memory: openRunnerMemory(t), MemoryScope: runnerMemoryScope})
	defer with.Close()
	dispatcher = ipc.NewDispatcher()
	if err := with.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	registered := dispatcher.Commands()
	for _, command := range []string{memoryCommandCreate, memoryCommandGet, memoryCommandEdit, memoryCommandDelete, memoryCommandIndex, memoryCommandSettingsGet, memoryCommandSettingsSet} {
		found := false
		for _, name := range registered {
			found = found || name == command
		}
		if !found {
			t.Fatalf("memory command %s missing from %v", command, registered)
		}
	}
}

func TestMemoryIPCScopeAuthorizedCRUD(t *testing.T) {
	dispatcher, _ := memoryIPCHarness(t)

	created := memoryDispatch(t, dispatcher, memoryCommandCreate, `{"scope":`+memoryScopeJSON()+`,"topic":"operations","content":"restart at 02:00"}`)
	if !created.OK {
		t.Fatalf("create = %+v", created.Error)
	}
	var entry memoryEntryDTO
	if err := json.Unmarshal(created.Data, &entry); err != nil {
		t.Fatal(err)
	}
	if entry.ID == "" || entry.Version != 1 || entry.Topic != "operations" || entry.Content != "restart at 02:00" {
		t.Fatalf("created entry = %+v", entry)
	}

	got := memoryDispatch(t, dispatcher, memoryCommandGet, `{"scope":`+memoryScopeJSON()+`,"id":"`+entry.ID+`"}`)
	if !got.OK {
		t.Fatalf("get = %+v", got.Error)
	}
	// The same entry is invisible to another scope: the store authorizes the
	// owner on every row, and the IPC surfaces that as forbidden.
	foreign := memoryDispatch(t, dispatcher, memoryCommandGet, `{"scope":{"tenant":"tenant-a","subject":"subject-b"},"id":"`+entry.ID+`"}`)
	if foreign.OK || foreign.Error == nil || foreign.Error.Code != ipc.CodeForbidden {
		t.Fatalf("cross-scope get = %+v", foreign)
	}

	edited := memoryDispatch(t, dispatcher, memoryCommandEdit, `{"scope":`+memoryScopeJSON()+`,"id":"`+entry.ID+`","expectedVersion":1,"content":"restart at 03:00"}`)
	if !edited.OK {
		t.Fatalf("edit = %+v", edited.Error)
	}
	var updated memoryEntryDTO
	if err := json.Unmarshal(edited.Data, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Content != "restart at 03:00" {
		t.Fatalf("edited entry = %+v", updated)
	}
	stale := memoryDispatch(t, dispatcher, memoryCommandEdit, `{"scope":`+memoryScopeJSON()+`,"id":"`+entry.ID+`","expectedVersion":1,"content":"stale"}`)
	if stale.OK || stale.Error == nil || stale.Error.Code != ipc.CodeBadParam {
		t.Fatalf("stale edit = %+v", stale)
	}
	detail, _ := stale.Error.Detail.(map[string]any)
	if detail == nil || detail["expected"] != uint64(1) || detail["actual"] != uint64(2) {
		t.Fatalf("stale edit detail = %+v", stale.Error.Detail)
	}

	index := memoryDispatch(t, dispatcher, memoryCommandIndex, `{"scope":`+memoryScopeJSON()+`}`)
	if !index.OK || !strings.Contains(string(index.Data), "operations") {
		t.Fatalf("index = %s err=%+v", index.Data, index.Error)
	}

	deleted := memoryDispatch(t, dispatcher, memoryCommandDelete, `{"scope":`+memoryScopeJSON()+`,"id":"`+entry.ID+`","expectedVersion":2}`)
	if !deleted.OK {
		t.Fatalf("delete = %+v", deleted.Error)
	}
	missing := memoryDispatch(t, dispatcher, memoryCommandGet, `{"scope":`+memoryScopeJSON()+`,"id":"`+entry.ID+`"}`)
	if missing.OK || missing.Error == nil || missing.Error.Code != ipc.CodeNotFound {
		t.Fatalf("get after delete = %+v", missing)
	}
}

func TestMemoryIPCSecretPoliciesAndSettingsCAS(t *testing.T) {
	dispatcher, _ := memoryIPCHarness(t)

	rejected := memoryDispatch(t, dispatcher, memoryCommandCreate, `{"scope":`+memoryScopeJSON()+`,"topic":"ops","content":"api_key=sk-live-123"}`)
	if rejected.OK || rejected.Error == nil || rejected.Error.Code != ipc.CodeBadParam || !strings.Contains(rejected.Error.Message, "secret") {
		t.Fatalf("secret reject = %+v", rejected)
	}
	redacted := memoryDispatch(t, dispatcher, memoryCommandCreate, `{"scope":`+memoryScopeJSON()+`,"topic":"ops","content":"api_key=sk-live-123","secrets":"redact"}`)
	if !redacted.OK {
		t.Fatalf("secret redact = %+v", redacted.Error)
	}
	var entry memoryEntryDTO
	if err := json.Unmarshal(redacted.Data, &entry); err != nil {
		t.Fatal(err)
	}
	if !entry.Redacted || strings.Contains(entry.Content, "sk-live-123") {
		t.Fatalf("redacted entry = %+v", entry)
	}
	badPolicy := memoryDispatch(t, dispatcher, memoryCommandCreate, `{"scope":`+memoryScopeJSON()+`,"topic":"ops","content":"x","secrets":"drop"}`)
	if badPolicy.OK || badPolicy.Error == nil || badPolicy.Error.Code != ipc.CodeBadParam {
		t.Fatalf("bad policy = %+v", badPolicy)
	}

	settings := memoryDispatch(t, dispatcher, memoryCommandSettingsGet, `{"scope":`+memoryScopeJSON()+`}`)
	if !settings.OK {
		t.Fatalf("settings get = %+v", settings.Error)
	}
	var current memorySettingsDTO
	if err := json.Unmarshal(settings.Data, &current); err != nil {
		t.Fatal(err)
	}
	if current.InjectionEnabled || current.ToolsEnabled || current.Version != 0 {
		t.Fatalf("default settings = %+v", current)
	}
	updated := memoryDispatch(t, dispatcher, memoryCommandSettingsSet, `{"scope":`+memoryScopeJSON()+`,"expectedVersion":0,"injectionEnabled":true}`)
	if !updated.OK {
		t.Fatalf("settings set = %+v", updated.Error)
	}
	var next memorySettingsDTO
	if err := json.Unmarshal(updated.Data, &next); err != nil {
		t.Fatal(err)
	}
	if !next.InjectionEnabled || next.ToolsEnabled || next.Version != 1 {
		t.Fatalf("updated settings = %+v", next)
	}
	conflict := memoryDispatch(t, dispatcher, memoryCommandSettingsSet, `{"scope":`+memoryScopeJSON()+`,"expectedVersion":0,"toolsEnabled":true}`)
	if conflict.OK || conflict.Error == nil || conflict.Error.Code != ipc.CodeBadParam {
		t.Fatalf("stale settings set = %+v", conflict)
	}
	empty := memoryDispatch(t, dispatcher, memoryCommandSettingsSet, `{"scope":`+memoryScopeJSON()+`,"expectedVersion":1}`)
	if empty.OK || empty.Error == nil || empty.Error.Code != ipc.CodeBadParam {
		t.Fatalf("empty settings set = %+v", empty)
	}
}
