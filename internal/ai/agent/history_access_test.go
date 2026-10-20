package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestAuthorizeJobWithAccountHistoryAccess(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	database.SetAIHistoryShared(false)
	ownerA := ipc.WithUserID(ctx, "owner-a")
	conversation, err := database.ConvCreate(ownerA, "owned", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RunInsert(ownerA, store.RunRow{ID: "run-a", ConversationID: conversation.ID, Status: store.RunStatusInterrupted}); err != nil {
		t.Fatal(err)
	}
	if err := database.GORM().Create(&store.RunRow{ID: "run-orphan", ConversationID: conversation.ID, Status: store.RunStatusInterrupted, Source: "chat", CreatedAt: 1, UpdatedAt: 1}).Error; err != nil {
		t.Fatal(err)
	}
	runner := &Runner{runs: database}
	runner.config.ServerMode = true
	if err := runner.authorizeJob(ownerA, "run-a", nil); err != nil {
		t.Fatalf("owner could not authorize restored run: %v", err)
	}
	if err := runner.authorizeJob(ipc.WithUserID(ctx, "owner-b"), "run-a", nil); !errors.Is(err, ErrJobForbidden) {
		t.Fatalf("foreign authorization error = %v, want ErrJobForbidden", err)
	}
	if err := runner.authorizeJob(ownerA, "run-orphan", nil); err == nil {
		t.Fatal("ownerless quarantine run was authorized")
	}
}
