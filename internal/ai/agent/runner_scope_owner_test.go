package agent

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/cloudwego/eino/schema"
)

func TestRunnerDerivesPersistentConversationOwner(t *testing.T) {
	storage := restartStore(t)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	conversation, err := storage.ConvCreate(ctxA, "owned", map[string]any{"scope": tools.Scope{SessionID: "session-a"}})
	if err != nil {
		t.Fatal(err)
	}
	owners := make(chan string, 1)
	runner := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("done", nil)), tools.Dependencies{}, nil)
	runner.config.ServerMode = true
	runner.config.ScopeValidator = ScopeValidatorFunc(func(ctx context.Context, scope tools.Scope) error {
		if owner.OpenAccess(ctx) {
			t.Fatal("persistent account execution retained Open access")
		}
		if scope.SessionID != "session-a" {
			t.Fatalf("scope = %+v", scope)
		}
		owners <- ipc.OwnerID(ctx)
		return nil
	})
	stream := &SliceStream{}
	response, err := runner.Start(owner.WithOpenAccess(context.Background()), ChatArgs{ConversationID: conversation.ID, Message: "go", Scope: tools.Scope{SessionID: "replacement-session"}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	if got := <-owners; got != "owner-a" {
		t.Fatalf("scope validator owner = %q", got)
	}
	waitClosed(t, stream)
	row, err := storage.RunGet(ctxA, response.JobID)
	if err != nil || row.OwnerID != "owner-a" {
		t.Fatalf("run owner = %q, %v", row.OwnerID, err)
	}
	if _, err := runner.Start(context.Background(), ChatArgs{ConversationID: conversation.ID, Message: "missing identity"}, StaticStream(&SliceStream{})); err == nil {
		t.Fatal("ownerless request adopted the persistent conversation owner")
	}
}

func TestRestoredRunRevalidatesScopeWithPersistentOwner(t *testing.T) {
	storage := restartStore(t)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	conversation, err := storage.ConvCreate(ctxA, "owned", map[string]any{"scope": tools.Scope{SessionID: "session-a"}})
	if err != nil {
		t.Fatal(err)
	}
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	first := durableRunner(t, storage, dockerConfirmChat(), deps, nil)
	first.config.ServerMode = true
	stream := &SliceStream{}
	response, err := first.Start(ctxA, ChatArgs{ConversationID: conversation.ID, Message: "go", Scope: tools.Scope{SessionID: "session-a"}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitEvent(t, stream, "confirmRequired")

	owners := make(chan string, 1)
	restarted := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("done", nil)), deps, nil)
	restarted.config.ServerMode = true
	restarted.config.ScopeValidator = ScopeValidatorFunc(func(ctx context.Context, scope tools.Scope) error {
		if owner.OpenAccess(ctx) {
			t.Fatal("restored account execution retained Open access")
		}
		if scope.SessionID != "session-a" {
			t.Fatalf("restored scope = %+v", scope)
		}
		owners <- ipc.OwnerID(ctx)
		return nil
	})
	if err := restarted.RecoverRuns(owner.WithOpenAccess(context.Background())); err != nil {
		t.Fatal(err)
	}
	resumed := &SliceStream{}
	if err := restarted.ConfirmStream(owner.WithOpenAccess(context.Background()), Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}, StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	if got := <-owners; got != "owner-a" {
		t.Fatalf("restored scope validator owner = %q", got)
	}
	waitClosed(t, resumed)
}
