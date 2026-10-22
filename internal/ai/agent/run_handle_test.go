package agent

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/cloudwego/eino/schema"
)

// The toolCall/toolResult events carry the permanent roster handle only when
// the call's actual target verifiably belongs to one live roster entry;
// non-roster, terminal-state, ambiguous, and non-terminal targets omit it.

func rosterHandleRunner(t *testing.T, scopeTab string, chatMessages ...*schema.Message) (*Runner, *Roster, string) {
	t.Helper()
	chat := sequenceModel(chatMessages...)
	deps := tools.Dependencies{
		Terminal:   ruleTerminal{},
		TabSession: func(string) string { return "session" },
	}
	runner, storage := testRunner(t, chat, deps, 0)
	roster := NewRoster(storage, nil)
	runner.config.Roster = roster
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	conversation, err := storage.ConvCreate(ctx, "handles", map[string]any{
		"scope": tools.Scope{SessionID: "session", TabID: scopeTab},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner, roster, conversation.ID
}

func toolEventHandles(events []Event, eventType, callID string) (string, bool) {
	for _, event := range events {
		if event.Type == eventType && event.ID == callID {
			return event.Handle, true
		}
	}
	return "", false
}

func runHandleJob(t *testing.T, runner *Runner, conversationID string) []Event {
	t.Helper()
	stream := &SliceStream{}
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	response, err := runner.Start(ctx, ChatArgs{Message: "go", ConversationID: conversationID}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Wait(context.Background(), response.JobID); err != nil {
		t.Fatal(err)
	}
	return waitClosed(t, stream)
}

func TestToolEventsCarryRosterHandleForRosterTarget(t *testing.T) {
	runner, roster, conversationID := rosterHandleRunner(t, "tab-roster",
		toolCallMessage(namedToolCall("c1", "send_keys", `{"keys":"ls"}`)),
		schema.AssistantMessage("done", nil),
	)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	if _, err := roster.Provision(ctx, conversationID, "handle-a", "agent-a", "tab-roster", nil); err != nil {
		t.Fatal(err)
	}

	events := runHandleJob(t, runner, conversationID)
	callHandle, found := toolEventHandles(events, "toolCall", "c1")
	if !found || callHandle != "handle-a" {
		t.Fatalf("toolCall handle = %q found=%t", callHandle, found)
	}
	resultHandle, found := toolEventHandles(events, "toolResult", "c1")
	if !found || resultHandle != "handle-a" {
		t.Fatalf("toolResult handle = %q found=%t", resultHandle, found)
	}
}

func TestToolEventsOmitHandleForNonRosterTarget(t *testing.T) {
	runner, _, conversationID := rosterHandleRunner(t, "tab-plain",
		toolCallMessage(namedToolCall("c1", "send_keys", `{"keys":"ls"}`)),
		schema.AssistantMessage("done", nil),
	)
	// The scope tab has no roster row: the target is not a roster tab.
	events := runHandleJob(t, runner, conversationID)
	for _, event := range events {
		if (event.Type == "toolCall" || event.Type == "toolResult") && event.Handle != "" {
			t.Fatalf("non-roster target emitted handle %q in %s", event.Handle, event.Type)
		}
	}
}

func TestToolEventsOmitHandleForTerminalStateRow(t *testing.T) {
	runner, roster, conversationID := rosterHandleRunner(t, "tab-roster",
		toolCallMessage(namedToolCall("c1", "send_keys", `{"keys":"ls"}`)),
		schema.AssistantMessage("done", nil),
	)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	entry, err := roster.Provision(ctx, conversationID, "handle-b", "agent-b", "tab-roster", nil)
	if err != nil {
		t.Fatal(err)
	}
	entry, err = roster.Activate(ctx, entry.ID, RosterIdentity{CreatedAt: entry.CreatedAt, Incarnation: entry.Incarnation}, "job")
	if err != nil {
		t.Fatal(err)
	}
	identity := RosterIdentity{CreatedAt: entry.CreatedAt, Incarnation: entry.Incarnation}
	if _, err := roster.RequestRelease(ctx, entry.Handle, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := roster.ConfirmRelease(ctx, entry.Handle, identity); err != nil {
		t.Fatal(err)
	}

	events := runHandleJob(t, runner, conversationID)
	for _, event := range events {
		if (event.Type == "toolCall" || event.Type == "toolResult") && event.Handle != "" {
			t.Fatalf("released row emitted handle %q in %s", event.Handle, event.Type)
		}
	}
}

func TestToolEventsOmitHandleForAmbiguousRows(t *testing.T) {
	runner, roster, conversationID := rosterHandleRunner(t, "tab-roster",
		toolCallMessage(namedToolCall("c1", "send_keys", `{"keys":"ls"}`)),
		schema.AssistantMessage("done", nil),
	)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	// Two live entries claiming the same tab cannot be told apart honestly.
	if _, err := roster.Provision(ctx, conversationID, "handle-c1", "agent-c1", "tab-roster", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := roster.Provision(ctx, conversationID, "handle-c2", "agent-c2", "tab-roster", nil); err != nil {
		t.Fatal(err)
	}

	events := runHandleJob(t, runner, conversationID)
	for _, event := range events {
		if (event.Type == "toolCall" || event.Type == "toolResult") && event.Handle != "" {
			t.Fatalf("ambiguous rows emitted handle %q in %s", event.Handle, event.Type)
		}
	}
}

func TestToolEventsOmitHandleForNonTerminalTool(t *testing.T) {
	runner, roster, conversationID := rosterHandleRunner(t, "tab-roster",
		toolCallMessage(namedToolCall("c1", "terminal_list", `{}`)),
		schema.AssistantMessage("done", nil),
	)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	if _, err := roster.Provision(ctx, conversationID, "handle-a", "agent-a", "tab-roster", nil); err != nil {
		t.Fatal(err)
	}

	events := runHandleJob(t, runner, conversationID)
	for _, event := range events {
		if (event.Type == "toolCall" || event.Type == "toolResult") && event.Handle != "" {
			t.Fatalf("non-terminal tool emitted handle %q in %s", event.Handle, event.Type)
		}
	}
}
