package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func injectedIDs(injection Injection) []string {
	ids := make([]string, 0, len(injection.Selected))
	for _, ref := range injection.Selected {
		ids = append(ids, ref.ID)
	}
	return ids
}

func TestInjectionRanksRelevantEntryWithinBudget(t *testing.T) {
	ctx := context.Background()
	store, _ := newMemoryStore(t)
	sequenceIDs(store)
	mustCreate(t, store, testScope, "a-editor", "prefers vim keybindings")
	mustCreate(t, store, testScope, "b-theme", "likes dark theme")
	relevant := mustCreate(t, store, testScope, "z-deploy", "production deploy uses kubectl rollout in cluster east")
	mustEnableInjection(t, store, testScope)

	injection, err := store.Inject(ctx, testScope, nil, Selection{Query: "how do I deploy with kubectl?"}, Budget{MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := injectedIDs(injection); len(got) != 1 || got[0] != relevant.ID || injection.Omitted != 2 {
		t.Fatalf("ranked injection ids=%v omitted=%d", got, injection.Omitted)
	}
	if strings.Contains(injection.Memory.Content, "vim") {
		t.Fatalf("irrelevant entry injected: %q", injection.Memory.Content)
	}

	// The selection depends only on the query, not on the history passed in,
	// so repeating it within a run cannot change the injected block.
	again, err := store.Inject(ctx, testScope, []*schema.Message{schema.UserMessage("something about vim")}, Selection{Query: "how do I deploy with kubectl?"}, Budget{MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := injectedIDs(again); len(got) != 1 || got[0] != relevant.ID || again.Memory.Content != injection.Memory.Content {
		t.Fatalf("selection changed: %v", got)
	}
}

func TestInjectionRankingByteBudgetAndOmitted(t *testing.T) {
	ctx := context.Background()
	store, _ := newMemoryStore(t)
	sequenceIDs(store)
	mustCreate(t, store, testScope, "a-filler", "filler"+strings.Repeat("x", 500))
	relevant := mustCreate(t, store, testScope, "b-ssh", "ssh bastion host is jump.example")
	mustEnableInjection(t, store, testScope)
	only, err := store.Inject(ctx, testScope, nil, Selection{IDs: []string{relevant.ID}}, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	injection, err := store.Inject(ctx, testScope, nil, Selection{Query: "ssh bastion"}, Budget{MaxBytes: only.Bytes})
	if err != nil {
		t.Fatal(err)
	}
	if got := injectedIDs(injection); len(got) != 1 || got[0] != relevant.ID || injection.Omitted != 1 || injection.Bytes > only.Bytes {
		t.Fatalf("injection ids=%v omitted=%d bytes=%d", got, injection.Omitted, injection.Bytes)
	}
}

func TestInjectionRankingMatchesCJK(t *testing.T) {
	ctx := context.Background()
	store, _ := newMemoryStore(t)
	sequenceIDs(store)
	mustCreate(t, store, testScope, "a-misc", "常用编辑器是 vim")
	relevant := mustCreate(t, store, testScope, "z-db", "生产数据库每天凌晨备份")
	mustEnableInjection(t, store, testScope)
	injection, err := store.Inject(ctx, testScope, nil, Selection{Query: "帮我检查数据库备份"}, Budget{MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := injectedIDs(injection); len(got) != 1 || got[0] != relevant.ID {
		t.Fatalf("CJK ranking ids=%v", got)
	}
}

func TestInjectionRankingTiesAndFallbackKeepTopicIDOrder(t *testing.T) {
	ctx := context.Background()
	store, _ := newMemoryStore(t)
	sequenceIDs(store)
	store.now = func() int64 { return 1000 }
	first := mustCreate(t, store, testScope, "a", "alpha")
	second := mustCreate(t, store, testScope, "b", "beta")
	third := mustCreate(t, store, testScope, "c", "gamma")
	mustEnableInjection(t, store, testScope)
	want := strings.Join([]string{first.ID, second.ID, third.ID}, ",")
	for _, query := range []string{"", "   ", "unrelated words"} {
		injection, err := store.Inject(ctx, testScope, nil, Selection{Query: query}, Budget{})
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(injectedIDs(injection), ","); got != want {
			t.Fatalf("query %q order = %v, want %v", query, got, want)
		}
	}
}
