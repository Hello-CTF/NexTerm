package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/adk"
)

func historyStore(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, ipc.WithUserID(context.Background(), "owner-a")
}

func insertHistoryRun(t *testing.T, database *store.Store, ctx context.Context, id, conversationID, status string, createdAt int64, finished bool, answer, errMsg string) {
	t.Helper()
	row := store.RunRow{
		ID: id, ConversationID: conversationID, Status: status, Source: "chat",
		Answer: answer, Error: errMsg, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if finished {
		finishedAt := createdAt + 1000
		row.FinishedAt = &finishedAt
	}
	if err := database.RunInsert(ctx, row); err != nil {
		t.Fatal(err)
	}
}

func historyConversation(t *testing.T, database *store.Store, ctx context.Context) string {
	t.Helper()
	conversation, err := database.ConvCreate(ctx, "chat", nil)
	if err != nil {
		t.Fatal(err)
	}
	return conversation.ID
}

func requireIPCCode(t *testing.T, err error, code ipc.Code) {
	t.Helper()
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("err = %v, want IPC code %s", err, code)
	}
}

// More than fifty runs restore as multiple backend pages: the cursor is
// opaque, chained verbatim, and every page lands exactly once in the stable
// (created_at DESC, id DESC) order.
func TestRunHistoryPagesMoreThanFiftyRunsByOpaqueCursor(t *testing.T) {
	database, ctx := historyStore(t)
	conversationID := historyConversation(t, database, ctx)
	for i := 0; i < 120; i++ {
		insertHistoryRun(t, database, ctx, fmt.Sprintf("run-%03d", i), conversationID, store.RunStatusCompleted, int64(1_760_000_000_000+i), true, fmt.Sprintf("answer-%03d", i), "")
	}
	runner := &Runner{runs: database}

	first, err := runner.RunHistory(ctx, conversationID, "all", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 50 || first.NextCursor == nil {
		t.Fatalf("first page = %d entries, cursor %v", len(first.Entries), first.NextCursor)
	}
	if first.Entries[0].RunID != "run-119" || first.Entries[49].RunID != "run-070" {
		t.Fatalf("first page boundary = %s..%s", first.Entries[0].RunID, first.Entries[49].RunID)
	}
	second, err := runner.RunHistory(ctx, conversationID, "all", *first.NextCursor, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) != 50 || second.NextCursor == nil {
		t.Fatalf("second page = %d entries, cursor %v", len(second.Entries), second.NextCursor)
	}
	if second.Entries[0].RunID != "run-069" || second.Entries[49].RunID != "run-020" {
		t.Fatalf("second page boundary = %s..%s", second.Entries[0].RunID, second.Entries[49].RunID)
	}
	third, err := runner.RunHistory(ctx, conversationID, "all", *second.NextCursor, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Entries) != 20 || third.NextCursor != nil {
		t.Fatalf("third page = %d entries, cursor %v", len(third.Entries), third.NextCursor)
	}
	if third.Entries[19].RunID != "run-000" {
		t.Fatalf("third page ends at %s", third.Entries[19].RunID)
	}

	seen := map[string]bool{}
	for _, page := range [][]RunHistoryEntry{first.Entries, second.Entries, third.Entries} {
		for _, entry := range page {
			if seen[entry.RunID] {
				t.Fatalf("run %s paged twice", entry.RunID)
			}
			seen[entry.RunID] = true
			if entry.State != "done" || entry.Evidence == nil || entry.Evidence.Kind != "output" {
				t.Fatalf("entry %s = %+v", entry.RunID, entry)
			}
		}
	}
	if len(seen) != 120 {
		t.Fatalf("paged %d runs", len(seen))
	}
}

// Status filters run server-side against the store statuses: idle keeps only
// unfinished interrupted runs, error keeps finished interrupted runs without
// absorbing the resumable idle ones, and queued matches nothing honestly.
func TestRunHistoryStatusFiltersAreServerSide(t *testing.T) {
	database, ctx := historyStore(t)
	conversationID := historyConversation(t, database, ctx)
	otherConversation := historyConversation(t, database, ctx)
	insertHistoryRun(t, database, ctx, "run-completed", conversationID, store.RunStatusCompleted, 1, true, "部署完成", "")
	insertHistoryRun(t, database, ctx, "run-idle", conversationID, store.RunStatusInterrupted, 2, false, "", "")
	insertHistoryRun(t, database, ctx, "run-interrupted-done", conversationID, store.RunStatusInterrupted, 3, true, "", "AI 任务因应用重启而中断")
	insertHistoryRun(t, database, ctx, "run-failed", conversationID, store.RunStatusFailed, 4, true, "", "boom")
	insertHistoryRun(t, database, ctx, "run-canceled", conversationID, store.RunStatusCanceled, 5, true, "", "")
	insertHistoryRun(t, database, ctx, "run-running", conversationID, store.RunStatusRunning, 6, false, "", "")
	if err := database.RunInsert(ctx, store.RunRow{
		ID: "run-title", ConversationID: conversationID, Status: store.RunStatusCompleted, Source: store.RunSourceTitle,
		Answer: "标题", CreatedAt: 7, UpdatedAt: 7,
	}); err != nil {
		t.Fatal(err)
	}
	insertHistoryRun(t, database, ctx, "run-other-conversation", otherConversation, store.RunStatusCompleted, 8, true, "别的会话", "")
	runner := &Runner{runs: database}

	for _, test := range []struct {
		filter string
		want   []string
	}{
		{"all", []string{"run-running", "run-canceled", "run-failed", "run-interrupted-done", "run-idle", "run-completed"}},
		{"done", []string{"run-completed"}},
		{"idle", []string{"run-idle"}},
		{"error", []string{"run-failed", "run-interrupted-done"}},
		{"canceled", []string{"run-canceled"}},
		{"running", []string{"run-running"}},
		{"queued", []string{}},
	} {
		page, err := runner.RunHistory(ctx, conversationID, test.filter, "", 50)
		if err != nil {
			t.Fatalf("filter %s: %v", test.filter, err)
		}
		if page.NextCursor != nil {
			t.Fatalf("filter %s cursor = %v, want nil", test.filter, *page.NextCursor)
		}
		got := make([]string, 0, len(page.Entries))
		for _, entry := range page.Entries {
			got = append(got, entry.RunID)
		}
		if fmt.Sprint(got) != fmt.Sprint(test.want) {
			t.Fatalf("filter %s = %v, want %v", test.filter, got, test.want)
		}
	}

	if _, err := runner.RunHistory(ctx, conversationID, "bogus", "", 50); err == nil {
		t.Fatal("unknown filter accepted")
	} else {
		requireIPCCode(t, err, ipc.CodeBadParam)
	}

	idlePage, err := runner.RunHistory(ctx, conversationID, "idle", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	idle := idlePage.Entries[0]
	if idle.State != "idle" || idle.Evidence != nil || idle.Summary != "中断待恢复" {
		t.Fatalf("idle entry = %+v", idle)
	}
	errorPage, err := runner.RunHistory(ctx, conversationID, "error", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]RunHistoryEntry{}
	for _, entry := range errorPage.Entries {
		byID[entry.RunID] = entry
	}
	if entry := byID["run-interrupted-done"]; entry.State != "error" || entry.Evidence == nil || entry.Evidence.Kind != "exit" || entry.Evidence.Summary != "AI 任务因应用重启而中断" {
		t.Fatalf("finished interrupted entry = %+v", entry)
	}
	if entry := byID["run-failed"]; entry.Evidence == nil || entry.Evidence.Summary != "boom" {
		t.Fatalf("failed entry = %+v", entry)
	}
}

func TestRunHistoryCursorValidationAndOwnerIsolation(t *testing.T) {
	database, ctx := historyStore(t)
	conversationID := historyConversation(t, database, ctx)
	insertHistoryRun(t, database, ctx, "run-a", conversationID, store.RunStatusCompleted, 1, true, "done", "")
	runner := &Runner{runs: database}

	if _, err := runner.RunHistory(ctx, conversationID, "all", "!!!not-a-cursor!!!", 50); err == nil {
		t.Fatal("garbage cursor accepted")
	} else {
		requireIPCCode(t, err, ipc.CodeBadParam)
	}
	if _, err := runner.RunHistory(ctx, "", "all", "", 50); err == nil {
		t.Fatal("empty conversation accepted")
	} else {
		requireIPCCode(t, err, ipc.CodeBadParam)
	}

	foreign := ipc.WithUserID(context.Background(), "owner-b")
	page, err := runner.RunHistory(foreign, conversationID, "all", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 0 || page.NextCursor != nil {
		t.Fatalf("foreign owner paged %+v", page.Entries)
	}
}

// Terminal evidence comes from the run's own durable finish record; a row
// that claims a terminal state without a finish timestamp degrades to no
// evidence instead of inventing one.
func TestRunHistoryEvidenceStaysHonest(t *testing.T) {
	database, ctx := historyStore(t)
	conversationID := historyConversation(t, database, ctx)
	longAnswer := strings.Repeat("很长", 60)
	insertHistoryRun(t, database, ctx, "run-long", conversationID, store.RunStatusCompleted, 1, true, longAnswer, "")
	insertHistoryRun(t, database, ctx, "run-empty", conversationID, store.RunStatusCompleted, 2, true, "  ", "")
	insertHistoryRun(t, database, ctx, "run-anomaly", conversationID, store.RunStatusCompleted, 3, false, "claims done", "")
	insertHistoryRun(t, database, ctx, "run-weird", conversationID, "weird", 4, true, "", "")
	runner := &Runner{runs: database}

	page, err := runner.RunHistory(ctx, conversationID, "all", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]RunHistoryEntry{}
	for _, entry := range page.Entries {
		byID[entry.RunID] = entry
	}
	long := byID["run-long"]
	if long.FinishedAt == nil || long.Evidence == nil || !strings.HasSuffix(long.Evidence.Summary, "…") || long.Evidence.ObservedAt != *long.FinishedAt {
		t.Fatalf("long answer evidence = %+v", long.Evidence)
	}
	if empty := byID["run-empty"]; empty.Evidence == nil || empty.Evidence.Summary != "已完成" || empty.Summary != "已完成" {
		t.Fatalf("empty answer entry = %+v", empty)
	}
	if anomaly := byID["run-anomaly"]; anomaly.State != "done" || anomaly.Evidence != nil {
		t.Fatalf("unfinished completed row must not fabricate evidence: %+v", anomaly)
	}
	if weird := byID["run-weird"]; weird.State != "error" || weird.Evidence == nil || !strings.Contains(weird.Summary, "未知运行状态") {
		t.Fatalf("unknown status entry = %+v", weird)
	}
}

// The recovery summary audits the conversation's newest runs: how many were
// restored, which of them stay interrupted, the single newest resumable run
// (the one a restore would actually accept), and whether older runs exist
// beyond the restored set.
func TestConversationRecoverySummary(t *testing.T) {
	database, ctx := historyStore(t)
	conversationID := historyConversation(t, database, ctx)
	emptyConversation := historyConversation(t, database, ctx)
	insertHistoryRun(t, database, ctx, "run-stuck", conversationID, store.RunStatusInterrupted, 1, false, "", "")
	insertHistoryRun(t, database, ctx, "run-dead", conversationID, store.RunStatusInterrupted, 2, true, "", "AI 任务因应用重启而中断")
	insertHistoryRun(t, database, ctx, "run-done", conversationID, store.RunStatusCompleted, 3, true, "完成", "")
	insertHistoryRun(t, database, ctx, "run-live", conversationID, store.RunStatusRunning, 4, false, "", "")
	insertHistoryRun(t, database, ctx, "run-resume", conversationID, store.RunStatusInterrupted, 5, false, "", "")

	runner := NewRunner(Config{Store: database, Runs: database})
	defer runner.Close()
	checkpointID := "checkpoint-resume"
	if err := runner.checkpoints.Set(ctx, checkpointID, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.hitl.RegisterRun(ctx, "run-resume", checkpointID); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.hitl.Interrupt(ctx, &adk.InterruptCtx{ID: "target-resume"}, hitl.InterruptInput{
		RunID: "run-resume", CheckpointID: checkpointID, CallID: "call-resume",
		Tool: "exec_commands", Kind: hitl.KindConfirm, Parameters: json.RawMessage(`{"command":"ls"}`),
	}); err != nil {
		t.Fatal(err)
	}

	summary, err := runner.ConversationRecoverySummary(ctx, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if summary == nil {
		t.Fatal("summary is null for a conversation with runs")
	}
	if summary.ConversationID != conversationID || summary.RestoredRuns != 5 || summary.Truncated {
		t.Fatalf("summary = %+v", summary)
	}
	if fmt.Sprint(summary.InterruptedRunIDs) != fmt.Sprint([]string{"run-resume", "run-stuck"}) {
		t.Fatalf("interrupted = %v", summary.InterruptedRunIDs)
	}
	if summary.ResumableRunID == nil || *summary.ResumableRunID != "run-resume" {
		t.Fatalf("resumable = %v", summary.ResumableRunID)
	}

	empty, err := runner.ConversationRecoverySummary(ctx, emptyConversation)
	if err != nil || empty != nil {
		t.Fatalf("empty conversation summary = %+v err=%v", empty, err)
	}

	// Without the HITL runtime the resumable run cannot be verified and stays
	// honestly absent instead of guessing.
	minimal := &Runner{runs: database}
	withoutHITL, err := minimal.ConversationRecoverySummary(ctx, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if withoutHITL.ResumableRunID != nil {
		t.Fatalf("unverifiable resumable = %v", *withoutHITL.ResumableRunID)
	}
	if withoutHITL.RestoredRuns != 5 || fmt.Sprint(withoutHITL.InterruptedRunIDs) != fmt.Sprint([]string{"run-resume", "run-stuck"}) {
		t.Fatalf("summary without HITL = %+v", withoutHITL)
	}
}

func TestConversationRecoverySummaryTruncatesBeyondRestoreCap(t *testing.T) {
	database, ctx := historyStore(t)
	conversationID := historyConversation(t, database, ctx)
	for i := 0; i < 205; i++ {
		insertHistoryRun(t, database, ctx, fmt.Sprintf("run-%03d", i), conversationID, store.RunStatusCompleted, int64(i+1), true, "done", "")
	}
	runner := &Runner{runs: database}
	summary, err := runner.ConversationRecoverySummary(ctx, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.RestoredRuns != 200 || !summary.Truncated {
		t.Fatalf("summary = %+v", summary)
	}
}

// RegisterCommands wires the roster surface and the AI-05 history surface for
// real: roster commands answer unsupported without a configured roster (never
// "unknown command"), and the legacy ai_run_list contract is unchanged.
func TestRegisterCommandsWiresRosterAndRunHistory(t *testing.T) {
	database, ctx := historyStore(t)
	conversationID := historyConversation(t, database, ctx)
	insertHistoryRun(t, database, ctx, "run-a", conversationID, store.RunStatusCompleted, 1, true, "done", "")

	minimal := &Runner{runs: database}
	dispatcher := ipc.NewDispatcher()
	if err := minimal.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	rosterResponse := dispatcher.Dispatch(ctx, ipc.Request{Command: "ai_roster_list", Args: json.RawMessage(`{"conversationId":"` + conversationID + `"}`)}, ipc.Environment{})
	if rosterResponse.OK {
		t.Fatal("roster command answered without a configured roster")
	}
	if rosterResponse.Error == nil || rosterResponse.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("roster error = %+v", rosterResponse.Error)
	}

	historyResponse := dispatcher.Dispatch(ctx, ipc.Request{Command: "ai_run_history", Args: json.RawMessage(`{"conversationId":"` + conversationID + `","filter":"all","limit":50}`)}, ipc.Environment{})
	if !historyResponse.OK {
		t.Fatalf("ai_run_history = %+v", historyResponse.Error)
	}
	var page RunHistoryPage
	if err := json.Unmarshal(historyResponse.Data, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].RunID != "run-a" || page.NextCursor != nil {
		t.Fatalf("history page = %+v", page)
	}

	summaryResponse := dispatcher.Dispatch(ctx, ipc.Request{Command: "ai_run_recovery_summary", Args: json.RawMessage(`{"conversationId":"` + conversationID + `"}`)}, ipc.Environment{})
	if !summaryResponse.OK {
		t.Fatalf("ai_run_recovery_summary = %+v", summaryResponse.Error)
	}
	var summary *ConversationRecoverySummary
	if err := json.Unmarshal(summaryResponse.Data, &summary); err != nil {
		t.Fatal(err)
	}
	if summary == nil || summary.RestoredRuns != 1 {
		t.Fatalf("summary = %+v", summary)
	}

	legacyResponse := dispatcher.Dispatch(ctx, ipc.Request{Command: "ai_run_list", Args: json.RawMessage(`{"conversationId":"` + conversationID + `","limit":10}`)}, ipc.Environment{})
	if !legacyResponse.OK {
		t.Fatalf("ai_run_list = %+v", legacyResponse.Error)
	}
	var legacy []RunDTO
	if err := json.Unmarshal(legacyResponse.Data, &legacy); err != nil {
		t.Fatal(err)
	}
	if len(legacy) != 1 || legacy[0].ID != "run-a" || legacy[0].Status != store.RunStatusCompleted {
		t.Fatalf("legacy runs = %+v", legacy)
	}

	withRoster := NewRunner(Config{Store: database, Runs: database, Roster: NewRoster(database, nil)})
	defer withRoster.Close()
	rosterDispatcher := ipc.NewDispatcher()
	if err := withRoster.RegisterCommands(rosterDispatcher); err != nil {
		t.Fatal(err)
	}
	rosterOK := rosterDispatcher.Dispatch(ctx, ipc.Request{Command: "ai_roster_list", Args: json.RawMessage(`{"conversationId":"` + conversationID + `"}`)}, ipc.Environment{})
	if !rosterOK.OK {
		t.Fatalf("configured roster list = %+v", rosterOK.Error)
	}
	var listed []RosterListEntry
	if err := json.Unmarshal(rosterOK.Data, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("roster = %+v", listed)
	}
}
