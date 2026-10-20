package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/adk"
)

func TestHITLReadRPCOwnerIsolation(t *testing.T) {
	ctx := context.Background()
	storage, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	storage.SetAIHistoryShared(false)
	ownerA := ipc.WithUserID(ctx, "owner-a")
	ownerB := ipc.WithUserID(ctx, "owner-b")
	admin := ipc.WithRole(ipc.WithUserID(ctx, "admin"), "superadmin")
	for _, fixture := range []struct {
		ctx            context.Context
		conversationID string
		runID          string
		ownerID        string
	}{
		{ctx: ownerA, conversationID: "conv-a", runID: "run-a", ownerID: "owner-a"},
		{ctx: admin, conversationID: "conv-admin", runID: "run-admin", ownerID: "admin"},
	} {
		conversation, err := storage.ConvCreate(fixture.ctx, fixture.conversationID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.RunInsert(fixture.ctx, store.RunRow{ID: fixture.runID, ConversationID: conversation.ID, Status: store.RunStatusInterrupted}); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.GORM().Create(&store.ConversationRow{ID: "conv-orphan", ScopeJSON: "{}", CreatedAt: 1, UpdatedAt: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := storage.GORM().Create(&store.RunRow{ID: "run-orphan", ConversationID: "conv-orphan", Status: store.RunStatusInterrupted, Source: "chat", CreatedAt: 1, UpdatedAt: 1}).Error; err != nil {
		t.Fatal(err)
	}

	runner := NewRunner(Config{Store: storage, Runs: storage, ServerMode: true})
	defer runner.Close()
	dispatcher := ipc.NewDispatcher()
	if err := runner.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	nonces := make(map[string]string)
	for _, fixture := range []struct {
		ctx   context.Context
		runID string
	}{
		{ctx: ownerA, runID: "run-a"},
		{ctx: admin, runID: "run-admin"},
		{ctx: context.Background(), runID: "run-orphan"},
	} {
		checkpointID := "checkpoint-" + fixture.runID
		if err := runner.checkpoints.Set(fixture.ctx, checkpointID, []byte("{}")); err != nil {
			t.Fatal(err)
		}
		if _, err := runner.hitl.RegisterRun(fixture.ctx, fixture.runID, checkpointID); err != nil {
			t.Fatal(err)
		}
		interrupt, err := runner.hitl.Interrupt(fixture.ctx, &adk.InterruptCtx{ID: "target-" + fixture.runID}, hitl.InterruptInput{
			RunID: fixture.runID, CheckpointID: checkpointID, CallID: "call-" + fixture.runID,
			Tool: "exec_commands", Kind: hitl.KindConfirm, Parameters: json.RawMessage(fmt.Sprintf(`{"secret":"classified-%s"}`, fixture.runID)),
		})
		if err != nil {
			t.Fatal(err)
		}
		nonces[fixture.runID] = interrupt.Nonce
	}

	for _, command := range []string{"ai_hitl_snapshot", "ai_hitl_events"} {
		t.Run(command, func(t *testing.T) {
			for _, test := range []struct {
				name   string
				ctx    context.Context
				jobID  string
				wantOK bool
			}{
				{name: "regular owner", ctx: ownerA, jobID: "run-a", wantOK: true},
				{name: "other regular owner", ctx: ownerB, jobID: "run-a"},
				{name: "anonymous other owner", ctx: context.Background(), jobID: "run-a"},
				{name: "superadmin other owner", ctx: admin, jobID: "run-a"},
				{name: "regular ownerless", ctx: ownerA, jobID: "run-orphan"},
				{name: "anonymous ownerless", ctx: context.Background(), jobID: "run-orphan"},
				{name: "superadmin ownerless", ctx: admin, jobID: "run-orphan", wantOK: true},
				{name: "superadmin own", ctx: admin, jobID: "run-admin", wantOK: true},
			} {
				t.Run(test.name, func(t *testing.T) {
					args := fmt.Sprintf(`{"jobId":%q}`, test.jobID)
					if command == "ai_hitl_events" {
						args = fmt.Sprintf(`{"jobId":%q,"afterSeq":0}`, test.jobID)
					}
					response := dispatcher.Dispatch(test.ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
					if response.OK != test.wantOK {
						t.Fatalf("response OK=%v error=%+v, want %v", response.OK, response.Error, test.wantOK)
					}
					if !test.wantOK {
						if response.Error == nil {
							t.Fatal("denied read has no error")
						}
						if strings.Contains(string(response.Data), "classified-") || strings.Contains(string(response.Data), nonces[test.jobID]) ||
							strings.Contains(response.Error.Message, "classified-") || strings.Contains(response.Error.Message, nonces[test.jobID]) {
							t.Fatalf("denied response leaked HITL state: %+v", response)
						}
						return
					}
					if command == "ai_hitl_snapshot" {
						var snapshot hitl.Snapshot
						if err := json.Unmarshal(response.Data, &snapshot); err != nil {
							t.Fatal(err)
						}
						if len(snapshot.Pending) != 1 || snapshot.Pending[0].Nonce != nonces[test.jobID] ||
							!strings.Contains(string(snapshot.Pending[0].Parameters), "classified-"+test.jobID) {
							t.Fatalf("authorized snapshot = %+v", snapshot)
						}
						return
					}
					var events []hitl.Event
					if err := json.Unmarshal(response.Data, &events); err != nil {
						t.Fatal(err)
					}
					if len(events) != 1 || events[0].RunID != test.jobID || events[0].Kind != hitl.EventInterrupted {
						t.Fatalf("authorized events = %+v", events)
					}
				})
			}
		})
	}
}
