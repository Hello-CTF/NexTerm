package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// A resumed HITL run's toolResult carries the same verified roster handle
// the original toolCall emitted: the mapping is rebuilt from the run's
// persisted toolCall journal events, never re-guessed from the live target
// and never borrowed from another call. Non-roster targets stay unknown and
// no structured rows are duplicated.

func sendKeysConfirmChat() model.BaseChatModel {
	return sequenceModel(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"sudo reboot<enter>"}`)),
		schema.AssistantMessage("done", nil),
	)
}

func rosterRestoreFixture(t *testing.T, scopeTab string) (*store.Store, tools.Dependencies, string) {
	t.Helper()
	storage := restartStore(t)
	deps := tools.Dependencies{
		Terminal:   ruleTerminal{},
		TabSession: func(string) string { return "session" },
	}
	ctx := context.Background()
	conversation, err := storage.ConvCreate(ctx, "restore-handle", map[string]any{
		"scope": tools.Scope{SessionID: "session", TabID: scopeTab},
	})
	if err != nil {
		t.Fatal(err)
	}
	return storage, deps, conversation.ID
}

func startRestoreHandleJob(t *testing.T, runner *Runner, stream *SliceStream, conversationID string) StartResponse {
	t.Helper()
	response, err := runner.Start(context.Background(), ChatArgs{Message: "go", ConversationID: conversationID}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestRestoreHITLResumeKeepsRosterHandle(t *testing.T) {
	storage, deps, conversationID := rosterRestoreFixture(t, "tab-roster")
	runner := durableRunner(t, storage, sendKeysConfirmChat(), deps, nil)
	roster := NewRoster(storage, nil)
	runner.config.Roster = roster
	ctx := context.Background()
	if _, err := roster.Provision(ctx, conversationID, "handle-a", "agent-a", "tab-roster", nil); err != nil {
		t.Fatal(err)
	}

	stream := &SliceStream{}
	response := startRestoreHandleJob(t, runner, stream, conversationID)
	confirmation := waitEvent(t, stream, "confirmRequired")
	original, _ := stream.Snapshot()
	callHandle, found := toolEventHandles(original, "toolCall", "keys")
	if !found || callHandle != "handle-a" {
		t.Fatalf("original toolCall handle = %q found=%t", callHandle, found)
	}

	restarted := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("done", nil)), deps, nil)
	restarted.config.Roster = NewRoster(storage, nil)
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	resumed := &SliceStream{}
	if err := restarted.ConfirmStream(context.Background(), Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}, StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, resumed)
	resultHandle, found := toolEventHandles(events, "toolResult", "keys")
	if !found || resultHandle != "handle-a" {
		t.Fatalf("recovered toolResult handle = %q found=%t, want handle-a", resultHandle, found)
	}

	// The resume never duplicates the structured rows: one toolCall, one
	// toolResult, exactly like a single-run HITL flow.
	rows, err := storage.MsgList(context.Background(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	roles := make([]string, 0, len(rows))
	for _, row := range rows {
		roles = append(roles, row.Role)
	}
	want := []string{"user", historyTypeToolCall, historyTypeToolResult, "assistant"}
	if len(roles) != len(want) {
		t.Fatalf("post-resume row roles = %v", roles)
	}
	for index, role := range want {
		if roles[index] != role {
			t.Fatalf("post-resume row roles = %v, want %v", roles, want)
		}
	}
}

func TestRestoreHITLResumeKeepsUnknownForNonRosterTarget(t *testing.T) {
	storage, deps, conversationID := rosterRestoreFixture(t, "tab-plain")
	runner := durableRunner(t, storage, sendKeysConfirmChat(), deps, nil)
	runner.config.Roster = NewRoster(storage, nil)

	stream := &SliceStream{}
	response := startRestoreHandleJob(t, runner, stream, conversationID)
	confirmation := waitEvent(t, stream, "confirmRequired")
	original, _ := stream.Snapshot()
	if handle, found := toolEventHandles(original, "toolCall", "keys"); !found || handle != "" {
		t.Fatalf("non-roster toolCall emitted handle %q found=%t", handle, found)
	}

	restarted := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("done", nil)), deps, nil)
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	resumed := &SliceStream{}
	if err := restarted.ConfirmStream(context.Background(), Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}, StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, resumed)
	if handle, found := toolEventHandles(events, "toolResult", "keys"); !found || handle != "" {
		t.Fatalf("non-roster recovered toolResult emitted handle %q found=%t", handle, found)
	}
}

// A corrupt journal entry or another call's handle must never leak into a
// resumed result: the mapping is per call ID, and unparseable payloads leave
// the handle unknown instead of failing the restore.
func TestRestoreHITLResumeHandleNeverCrossesCallsOrBreaksOnCorruption(t *testing.T) {
	storage, deps, conversationID := rosterRestoreFixture(t, "tab-roster")
	runner := durableRunner(t, storage, sendKeysConfirmChat(), deps, nil)
	roster := NewRoster(storage, nil)
	runner.config.Roster = roster
	ctx := context.Background()
	if _, err := roster.Provision(ctx, conversationID, "handle-a", "agent-a", "tab-roster", nil); err != nil {
		t.Fatal(err)
	}

	stream := &SliceStream{}
	response := startRestoreHandleJob(t, runner, stream, conversationID)
	confirmation := waitEvent(t, stream, "confirmRequired")

	// Corrupt the run's journal with an unparseable toolCall payload and a
	// well-formed toolCall for a different call carrying its own handle.
	if _, err := storage.RunAppendEvent(ctx, response.JobID, "toolCall", func(seq uint64) ([]byte, error) {
		return []byte("{not-json"), nil
	}); err != nil {
		t.Fatal(err)
	}
	other := Event{Type: "toolCall", ID: "other", Name: "send_keys", Handle: "handle-other"}
	if _, err := storage.RunAppendEvent(ctx, response.JobID, "toolCall", func(seq uint64) ([]byte, error) {
		other.Seq = seq
		return json.Marshal(other)
	}); err != nil {
		t.Fatal(err)
	}

	restarted := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("done", nil)), deps, nil)
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	resumed := &SliceStream{}
	if err := restarted.ConfirmStream(context.Background(), Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}, StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, resumed)
	resultHandle, found := toolEventHandles(events, "toolResult", "keys")
	if !found || resultHandle != "handle-a" {
		t.Fatalf("recovered toolResult handle = %q found=%t, want its own handle-a", resultHandle, found)
	}
}
