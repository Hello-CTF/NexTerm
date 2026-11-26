package production

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// TestProductionRestartRecoversMoreThanFiftyRunsThroughRealServer proves the
// real server/store restart recovery path handles more than fifty active
// runs: the restart finishes the non-resumable majority as interrupted, the
// genuinely pending run stays resumable through its real HITL blob, and the
// AI-05 surface — the recovery summary and the opaque-cursor run history —
// reports the recovered set through the real dispatcher.
func TestProductionRestartRecoversMoreThanFiftyRunsThroughRealServer(t *testing.T) {
	ctx := context.Background()
	// The desktop restart harness is ownerless: every store write and dispatch
	// shares the same empty identity, exactly like the shipped desktop path.

	path := filepath.Join(t.TempDir(), "data.db")
	providerServer := httptest.NewServer(&restartScriptServer{})
	t.Cleanup(providerServer.Close)

	firstStore, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	first, sessionID := composeRestartRuntime(t, firstStore, providerServer.URL)

	// A genuinely pending run: the provider stops at a question, so the HITL
	// blob is persisted by the real run path rather than fabricated.
	stream := &agent.SliceStream{}
	response, err := first.Agent.Start(ctx, agent.ChatArgs{Message: "go", Scope: tools.Scope{SessionID: sessionID}}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	question := waitOutcomeEvent(t, stream, "questionRequired")
	if question.Nonce == "" || question.RequestID == "" {
		t.Fatalf("questionRequired missing binding: %+v", question)
	}
	conversationID := response.ConversationID

	// More than fifty additional active runs in the same conversation, all
	// without a resumable HITL blob: the restart must finish every one as
	// interrupted through the atomic recovery write.
	const extraRuns = 75
	for index := 0; index < extraRuns; index++ {
		status := store.RunStatusRunning
		if index%2 == 1 {
			status = store.RunStatusInterrupted
		}
		run := store.RunRow{
			ID:             fmt.Sprintf("run-extra-%03d", index),
			ConversationID: conversationID,
			Status:         status,
			CreatedAt:      int64(1000 + index),
			UpdatedAt:      int64(1000 + index),
		}
		if err := firstStore.RunInsert(ctx, run); err != nil {
			t.Fatal(err)
		}
	}

	first.closeAIRuntime()
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	second, _ := composeRestartRuntime(t, secondStore, providerServer.URL, first.Sessions)

	// The restart recovered exactly the active set: every dummy run finished
	// as interrupted, the pending run restored as interrupted and unfinished.
	dummy, err := secondStore.RunGet(ctx, "run-extra-000")
	if err != nil || dummy.Status != store.RunStatusInterrupted || dummy.FinishedAt == nil {
		t.Fatalf("recovered dummy run = %+v err=%v", dummy, err)
	}
	resumed, err := secondStore.RunGet(ctx, response.JobID)
	if err != nil || resumed.Status != store.RunStatusInterrupted || resumed.FinishedAt != nil {
		t.Fatalf("resumable run = %+v err=%v", resumed, err)
	}

	dispatcher := ipc.NewDispatcher()
	if err := second.Agent.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	dispatch := func(command, args string) ipc.Response {
		t.Helper()
		return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
	}

	// The recovery summary audits more than fifty restored runs and names the
	// single genuinely resumable one, with no truncation below the cap.
	summaryResponse := dispatch("ai_run_recovery_summary", `{"conversationId":"`+conversationID+`"}`)
	if !summaryResponse.OK {
		t.Fatalf("ai_run_recovery_summary = %+v", summaryResponse.Error)
	}
	var summary agent.ConversationRecoverySummary
	if err := json.Unmarshal(summaryResponse.Data, &summary); err != nil {
		t.Fatal(err)
	}
	const totalRuns = extraRuns + 1
	if summary.RestoredRuns != totalRuns {
		t.Fatalf("restoredRuns = %d, want %d", summary.RestoredRuns, totalRuns)
	}
	if summary.ResumableRunID == nil || *summary.ResumableRunID != response.JobID {
		t.Fatalf("resumableRunId = %+v, want %s", summary.ResumableRunID, response.JobID)
	}
	if len(summary.InterruptedRunIDs) != 1 || summary.InterruptedRunIDs[0] != response.JobID {
		t.Fatalf("interruptedRunIds = %+v, want only the resumable run", summary.InterruptedRunIDs)
	}
	if summary.Truncated {
		t.Fatalf("summary must not truncate %d runs below the restore cap: %+v", totalRuns, summary)
	}

	// The history pages the recovered set through real opaque cursors: fifty
	// then the remainder, every run exactly once, with honest recovered
	// states — the resumable run idle, the restart-finished majority errors
	// carrying their exit evidence.
	seen := map[string]string{}
	cursor := ""
	for {
		if len(seen) >= totalRuns {
			t.Fatalf("history returned more than %d runs", totalRuns)
		}
		page := dispatch("ai_run_history", `{"conversationId":"`+conversationID+`","filter":"all","cursor":"`+cursor+`","limit":50}`)
		if !page.OK {
			t.Fatalf("ai_run_history = %+v", page.Error)
		}
		var decoded agent.RunHistoryPage
		if err := json.Unmarshal(page.Data, &decoded); err != nil {
			t.Fatal(err)
		}
		for _, entry := range decoded.Entries {
			if _, duplicate := seen[entry.RunID]; duplicate {
				t.Fatalf("run %s paged twice", entry.RunID)
			}
			seen[entry.RunID] = entry.State
			if entry.RunID == response.JobID {
				if entry.State != "idle" || entry.Evidence != nil {
					t.Fatalf("resumable run history = %+v, want evidence-less idle", entry)
				}
			} else {
				if entry.State != "error" || entry.Evidence == nil || entry.Evidence.Kind != "exit" {
					t.Fatalf("restart-finished run history = %+v, want exit-evidenced error", entry)
				}
			}
		}
		if decoded.NextCursor == nil {
			break
		}
		cursor = *decoded.NextCursor
	}
	if len(seen) != totalRuns {
		t.Fatalf("history paged %d runs, want %d", len(seen), totalRuns)
	}

	// The server-side idle filter isolates the single resumable run.
	idle := dispatch("ai_run_history", `{"conversationId":"`+conversationID+`","filter":"idle","cursor":"","limit":50}`)
	if !idle.OK {
		t.Fatalf("idle ai_run_history = %+v", idle.Error)
	}
	var idlePage agent.RunHistoryPage
	if err := json.Unmarshal(idle.Data, &idlePage); err != nil {
		t.Fatal(err)
	}
	if len(idlePage.Entries) != 1 || idlePage.Entries[0].RunID != response.JobID {
		t.Fatalf("idle page = %+v, want only the resumable run", idlePage.Entries)
	}
}
