package memory

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestInjectionDefaultsOffAndNeverMutatesHistory(t *testing.T) {
	ctx := context.Background()
	store, _ := newMemoryStore(t)
	mustCreate(t, store, testScope, "operations", "restart at 02:00")
	leadingSystem := schema.SystemMessage("base instructions")
	user := schema.UserMessage("current question")
	unusedCapacity := schema.AssistantMessage("persisted sentinel", nil)
	backing := make([]*schema.Message, 1, 4)
	backing[0] = leadingSystem
	backing = backing[:3]
	backing[1] = user
	backing[2] = unusedCapacity
	history := backing[:2]
	checkpoint := append([]*schema.Message(nil), history...)

	injection, err := store.Inject(ctx, testScope, history, Selection{}, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if injection.Enabled || injection.Memory != nil || len(injection.Messages) != len(history) {
		t.Fatalf("disabled injection = %+v", injection)
	}
	if &injection.Messages[0] == &history[0] {
		t.Fatal("disabled injection reused the caller's slice")
	}
	mustEnableInjection(t, store, testScope)
	injection, err = store.Inject(ctx, testScope, history, Selection{}, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if !injection.Enabled || injection.Memory == nil || len(injection.Messages) != 3 {
		t.Fatalf("enabled injection = %+v", injection)
	}
	if injection.Messages[0] != leadingSystem || injection.Messages[1] != injection.Memory || injection.Messages[2] != user {
		t.Fatalf("message order = %+v", injection.Messages)
	}
	if !IsEphemeral(injection.Memory) || IsEphemeral(user) {
		t.Fatal("ephemeral marker missing or present on conversation message")
	}
	if len(history) != 2 || history[0] != leadingSystem || history[1] != user || backing[2] != unusedCapacity {
		t.Fatalf("caller history changed: %+v", backing)
	}
	if !reflect.DeepEqual(checkpoint, history) {
		t.Fatalf("checkpoint history changed: %+v", checkpoint)
	}
	if injection.Bytes != len(injection.Memory.Content) || injection.Bytes > DefaultPromptBytes {
		t.Fatalf("injection bytes = %d, content = %d", injection.Bytes, len(injection.Memory.Content))
	}
}

func TestInjectionSelectionAndOrderingAreDeterministic(t *testing.T) {
	ctx := context.Background()
	store, path := newMemoryStore(t)
	sequenceIDs(store)
	first := mustCreate(t, store, testScope, "z-topic", "z content")
	second := mustCreate(t, store, testScope, "a-topic", "a second")
	third := mustCreate(t, store, testScope, "a-topic", "a third")
	mustEnableInjection(t, store, testScope)

	assertOrder := func(injection Injection, want []Entry) {
		t.Helper()
		ids := make([]string, len(injection.Selected))
		for index, selected := range injection.Selected {
			ids[index] = selected.ID
		}
		wantIDs := []string{second.ID, third.ID, first.ID}
		if want != nil {
			wantIDs = wantIDs[:0]
			for _, entry := range want {
				wantIDs = append(wantIDs, entry.ID)
			}
		}
		if !reflect.DeepEqual(ids, wantIDs) {
			t.Fatalf("selected IDs = %v, want %v", ids, wantIDs)
		}
	}
	all, err := store.Inject(ctx, testScope, nil, Selection{}, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	assertOrder(all, nil)
	again, err := store.Inject(ctx, testScope, nil, Selection{IDs: []string{first.ID, second.ID, first.ID}}, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	assertOrder(again, []Entry{second, first})
	byTopic, err := store.Inject(ctx, testScope, nil, Selection{Topics: []string{" a-topic ", "a-topic"}}, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	assertOrder(byTopic, []Entry{second, third})
	if _, err := store.Inject(ctx, testScope, nil, Selection{Topics: []string{"a-topic"}, IDs: []string{first.ID}}, Budget{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("mixed selection err = %v", err)
	}
	if strings.Contains(all.Memory.Content, testScope.Tenant) || strings.Contains(all.Memory.Content, testScope.Subject) {
		t.Fatalf("prompt leaked owner scope: %q", all.Memory.Content)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openMemoryStore(t, path)
	afterRestart, err := reopened.Inject(ctx, testScope, nil, Selection{}, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if afterRestart.Memory.Content != all.Memory.Content {
		t.Fatalf("restart changed prompt:\nbefore %q\nafter %q", all.Memory.Content, afterRestart.Memory.Content)
	}
}

func TestPromptBudgetKeepsWholeEntries(t *testing.T) {
	ctx := context.Background()
	store, _ := newMemoryStore(t)
	sequenceIDs(store)
	large := mustCreate(t, store, testScope, "a-large", "large-marker"+strings.Repeat("x", 1000))
	small := mustCreate(t, store, testScope, "b-small", "small-marker")
	mustEnableInjection(t, store, testScope)
	smallOnly, err := store.Inject(ctx, testScope, nil, Selection{IDs: []string{small.ID}}, Budget{})
	if err != nil {
		t.Fatal(err)
	}

	injection, err := store.Inject(ctx, testScope, nil, Selection{}, Budget{MaxBytes: smallOnly.Bytes, MaxEntries: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(injection.Selected) != 1 || injection.Selected[0].ID != small.ID || injection.Omitted != 1 {
		t.Fatalf("bounded injection = %+v", injection)
	}
	if injection.Bytes > smallOnly.Bytes || strings.Contains(injection.Memory.Content, "large-marker") || !strings.Contains(injection.Memory.Content, "small-marker") {
		t.Fatalf("bounded prompt = %q", injection.Memory.Content)
	}
	limited, err := store.Inject(ctx, testScope, nil, Selection{}, Budget{MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(limited.Selected) != 1 || limited.Selected[0].ID != large.ID || limited.Omitted != 1 {
		t.Fatalf("entry-limited injection = %+v", limited)
	}
	tiny, err := store.Inject(ctx, testScope, nil, Selection{}, Budget{MaxBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	if tiny.Memory != nil || tiny.Bytes != 0 || tiny.Omitted != 2 || len(tiny.Messages) != 0 {
		t.Fatalf("tiny-budget injection = %+v", tiny)
	}
	if _, err := store.Inject(ctx, testScope, nil, Selection{}, Budget{MaxBytes: -1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("negative budget err = %v", err)
	}
}

func TestInjectionRedactsUnexpectedPersistedSecret(t *testing.T) {
	ctx := context.Background()
	store, _ := newMemoryStore(t)
	entry := mustCreate(t, store, testScope, "operations", "clean")
	mustEnableInjection(t, store, testScope)
	const secret = "unexpected-persisted-secret"
	if _, err := store.db.Exec("UPDATE memory_entry SET content = ? WHERE id = ?", "token="+secret, entry.ID); err != nil {
		t.Fatal(err)
	}
	injection, err := store.Inject(ctx, testScope, nil, Selection{}, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if injection.Memory == nil || strings.Contains(injection.Memory.Content, secret) || injection.Redactions != 1 {
		t.Fatalf("secret injection = %+v", injection)
	}
	if len(injection.Selected) != 1 || !injection.Selected[0].Redacted {
		t.Fatalf("redaction reference = %+v", injection.Selected)
	}
}
