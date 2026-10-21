package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type failingRosterStore struct{ err error }

func (f failingRosterStore) AgentRosterProvision(context.Context, store.AgentRosterRow) (store.AgentRosterRow, error) {
	return store.AgentRosterRow{}, f.err
}
func (f failingRosterStore) AgentRosterGet(context.Context, string) (store.AgentRosterRow, error) {
	return store.AgentRosterRow{}, f.err
}
func (f failingRosterStore) AgentRosterGetByHandle(context.Context, string) (store.AgentRosterRow, error) {
	return store.AgentRosterRow{}, f.err
}
func (f failingRosterStore) AgentRosterList(context.Context, string) ([]store.AgentRosterRow, error) {
	return nil, f.err
}
func (f failingRosterStore) AgentRosterUpdate(context.Context, string, store.AgentRosterIdentity, map[string]any) (store.AgentRosterRow, error) {
	return store.AgentRosterRow{}, f.err
}
func (f failingRosterStore) AgentRosterCompareAndSwapState(context.Context, string, store.AgentRosterIdentity, string, map[string]any) (store.AgentRosterRow, error) {
	return store.AgentRosterRow{}, f.err
}

func rosterInjectionRunner(t *testing.T, chat model.BaseChatModel, roster *Roster) (*Runner, *store.Store) {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{
		Model:  func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Tools:  tools.NewRegistry(tools.Dependencies{}),
		Store:  storage,
		Roster: roster,
	})
	t.Cleanup(func() {
		_ = runner.Close()
		_ = storage.Close()
	})
	return runner, storage
}

func TestRunInjectsRosterContextWithRealEntries(t *testing.T) {
	database, manager := newRosterProbeFixture(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	tab, err := manager.RegisterRemoteSession(ctx, session.RemoteSessionOptions{OwnerID: "owner-a", AssetID: "asset-1", Title: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	roster := NewRoster(database, NewSessionRosterProbe(database, manager))

	var recorded [][]*schema.Message
	chat := sequenceModel(schema.AssistantMessage("done", nil))
	chat.stream = func(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		recorded = append(recorded, messages)
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
	}
	runner, storage := rosterInjectionRunner(t, chat, roster)

	// The conversation lives in the runner's store; the roster row references
	// it by ID under the same owner, so the in-job roster load sees the
	// provisioned handle.
	conversation, err := storage.ConvCreate(ctx, "roster", map[string]any{"scope": tools.Scope{SessionID: "session"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := roster.Provision(ctx, conversation.ID, "handle-a", "agent-a", tab.ID, nil); err != nil {
		t.Fatal(err)
	}

	startCtx := ipc.WithUserID(context.Background(), "owner-a")
	response, err := runner.Start(startCtx, ChatArgs{Message: "check the agent", ConversationID: conversation.ID}, StaticStream(&SliceStream{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Wait(context.Background(), response.JobID); err != nil {
		t.Fatal(err)
	}

	if len(recorded) == 0 {
		t.Fatal("model never received messages")
	}
	joined := flattenRecordings(recorded)
	if !strings.Contains(joined, "handle-a") || !strings.Contains(joined, rosterPromptHeader) {
		t.Fatalf("roster context missing from model input:\n%s", joined)
	}
}

func TestRunFailsClosedWhenRosterLoadFails(t *testing.T) {
	loadErr := errors.New("roster store unavailable")
	roster := NewRoster(failingRosterStore{err: loadErr}, nil)

	streamed := false
	chat := sequenceModel(schema.AssistantMessage("done", nil))
	chat.stream = func(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		streamed = true
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
	}
	runner, _ := rosterInjectionRunner(t, chat, roster)

	stream := &SliceStream{}
	if _, err := runner.Start(context.Background(), ChatArgs{Message: "go", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream)); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if streamed {
		t.Fatal("model was invoked although the roster load failed")
	}
	found := false
	for _, event := range events {
		if event.Type == "error" && strings.Contains(event.Message, loadErr.Error()) {
			found = true
		}
	}
	if !found {
		t.Fatalf("roster load failure not surfaced: %+v", events)
	}
}

func flattenRecordings(recorded [][]*schema.Message) string {
	var builder strings.Builder
	for _, messages := range recorded {
		for _, message := range messages {
			builder.WriteString(message.Content)
			builder.WriteString("\n")
		}
	}
	return builder.String()
}
