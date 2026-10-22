package production

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type cronConversationLookupFunc func(context.Context, string) (store.ConversationRow, error)

func (f cronConversationLookupFunc) ConvGet(ctx context.Context, id string) (store.ConversationRow, error) {
	return f(ctx, id)
}

func TestCronScopeForConversationChecksOwnerAndContext(t *testing.T) {
	trigger := cron.Trigger{SessionID: "conversation", OwnerID: "user-a", OwnerKind: cron.OwnerKindAccount}
	lookup := cronConversationLookupFunc(func(ctx context.Context, id string) (store.ConversationRow, error) {
		if ipc.OwnerID(ctx) != "user-a" || id != trigger.SessionID {
			t.Errorf("lookup identity = %q, conversation = %q", ipc.OwnerID(ctx), id)
		}
		return store.ConversationRow{ID: id, OwnerID: "user-a", ScopeJSON: `{"scope":{"sessionId":"terminal-a"}}`}, nil
	})
	ownerContext := ipc.WithUserID(context.Background(), "user-a")
	scope, err := scopeForConversation(lookup)(ownerContext, trigger)
	if err != nil || scope.SessionID != "terminal-a" {
		t.Fatalf("scope = %+v, %v", scope, err)
	}

	foreign := cronConversationLookupFunc(func(context.Context, string) (store.ConversationRow, error) {
		return store.ConversationRow{ID: trigger.SessionID, OwnerID: "user-b", ScopeJSON: `{}`}, nil
	})
	if _, err := scopeForConversation(foreign)(ownerContext, trigger); !errors.Is(err, cron.ErrForeignScope) {
		t.Fatalf("foreign conversation error = %v", err)
	}
	openTrigger := cron.Trigger{SessionID: "conversation", OwnerKind: cron.OwnerKindOpen}
	if _, err := scopeForConversation(foreign)(context.Background(), openTrigger); !errors.Is(err, cron.ErrForeignScope) {
		t.Fatalf("open trigger borrowed account conversation: %v", err)
	}

	called := false
	canceledLookup := cronConversationLookupFunc(func(context.Context, string) (store.ConversationRow, error) {
		called = true
		return store.ConversationRow{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scopeForConversation(canceledLookup)(ctx, trigger); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scope error = %v", err)
	}
	if called {
		t.Fatal("canceled scope invoked conversation lookup")
	}
}
