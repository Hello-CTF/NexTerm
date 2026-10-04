package agent

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/hitl"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func restartStore(t *testing.T) *store.Store {
	t.Helper()
	storage, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	return storage
}

func durableRunner(t *testing.T, storage *store.Store, chat model.BaseChatModel, deps tools.Dependencies, manager *hitl.Manager) *Runner {
	t.Helper()
	config := Config{
		Model:       func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Tools:       tools.NewRegistry(deps),
		Store:       storage,
		Runs:        storage,
		Checkpoints: NewStoreCheckpoints(storage),
		HITL:        manager,
	}
	runner := NewRunner(config)
	t.Cleanup(func() { _ = runner.Close() })
	return runner
}

func runEventsOf(t *testing.T, storage *store.Store, jobID string) []store.RunEventRow {
	t.Helper()
	events, err := storage.RunEventsAfter(context.Background(), jobID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func requireContiguousSeq(t *testing.T, events []store.RunEventRow) {
	t.Helper()
	for index, event := range events {
		if event.Seq != uint64(index+1) {
			t.Fatalf("event %d has seq %d: %+v", index, event.Seq, events)
		}
	}
}

func waitRunStatus(t *testing.T, storage *store.Store, jobID, status string) store.RunRow {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		row, err := storage.RunGet(context.Background(), jobID)
		if err == nil && row.Status == status {
			return row
		}
		time.Sleep(5 * time.Millisecond)
	}
	row, _ := storage.RunGet(context.Background(), jobID)
	t.Fatalf("run %s did not reach status %q: %+v", jobID, status, row)
	return store.RunRow{}
}

func TestRestartRecoversRunningRunAsInterrupted(t *testing.T) {
	storage := restartStore(t)
	var calls atomic.Int64
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("loop", "todo_write", `{"todos":[]}`))}), nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	runner := durableRunner(t, storage, chat, tools.Dependencies{}, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	_ = waitEvent(t, stream, "toolResult")

	restarted := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("ok", nil)), tools.Dependencies{}, nil)
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	row := waitRunStatus(t, storage, response.JobID, store.RunStatusInterrupted)
	if row.FinishedAt == nil || !strings.Contains(row.Error, "重启") {
		t.Fatalf("recovered row = %+v", row)
	}
	events := runEventsOf(t, storage, response.JobID)
	requireContiguousSeq(t, events)
	last := events[len(events)-1]
	if last.Type != "error" || !strings.Contains(last.PayloadJSON, "重启") {
		t.Fatalf("terminal journal event = %+v", last)
	}
	found := false
	for _, event := range events {
		if event.Type == "toolResult" {
			found = true
		}
	}
	if !found {
		t.Fatalf("transcript tool result missing from journal: %+v", events)
	}
	if _, found, err := storage.CheckpointGet(context.Background(), response.JobID); err != nil || found {
		t.Fatalf("checkpoint of interrupted run survived recovery: found=%v err=%v", found, err)
	}
}

func TestRecoverRunsIsIdempotentAcrossRestarts(t *testing.T) {
	storage := restartStore(t)
	var calls atomic.Int64
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("loop", "todo_write", `{"todos":[]}`))}), nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	runner := durableRunner(t, storage, chat, tools.Dependencies{}, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	_ = waitEvent(t, stream, "toolResult")

	first := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("ok", nil)), tools.Dependencies{}, nil)
	if err := first.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitRunStatus(t, storage, response.JobID, store.RunStatusInterrupted)
	eventsAfterFirst := runEventsOf(t, storage, response.JobID)

	if err := first.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("ok", nil)), tools.Dependencies{}, nil)
	if err := second.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventsAfterSecond := runEventsOf(t, storage, response.JobID)
	if len(eventsAfterSecond) != len(eventsAfterFirst) {
		t.Fatalf("repeat recovery appended events: first=%d second=%d", len(eventsAfterFirst), len(eventsAfterSecond))
	}
	terminal := 0
	for _, event := range eventsAfterSecond {
		if event.Type == "error" && strings.Contains(event.PayloadJSON, "重启") {
			terminal++
		}
	}
	if terminal != 1 {
		t.Fatalf("terminal restart events = %d, want exactly 1", terminal)
	}
	after, err := storage.RunGet(context.Background(), response.JobID)
	if err != nil || after.Status != store.RunStatusInterrupted || after.FinishedAt == nil {
		t.Fatalf("row changed on repeat recovery: %+v err=%v", after, err)
	}
}

func TestRestartReplaysCompleteTranscriptWithoutGaps(t *testing.T) {
	storage := restartStore(t)
	first := schema.AssistantMessage("完", nil)
	first.ReasoningContent = "分析"
	second := assistantWithUsage("成", 3, 2)
	chat := &fakeModel{steps: []fakeStep{{chunks: []*schema.Message{first, second}}}}
	runner := durableRunner(t, storage, chat, tools.Dependencies{}, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	live := waitClosed(t, stream)

	row := waitRunStatus(t, storage, response.JobID, store.RunStatusCompleted)
	if row.Answer != "完成" || row.TokensIn != 3 || row.TokensOut != 2 || row.Turns != 1 {
		t.Fatalf("completed row = %+v", row)
	}
	journal := runEventsOf(t, storage, response.JobID)
	requireContiguousSeq(t, journal)
	if len(journal) != len(live) {
		t.Fatalf("journal has %d events, live stream had %d", len(journal), len(live))
	}
	for index := range live {
		if journal[index].Seq != live[index].Seq || journal[index].Type != live[index].Type {
			t.Fatalf("journal[%d] = seq %d type %s, live = seq %d type %s", index, journal[index].Seq, journal[index].Type, live[index].Seq, live[index].Type)
		}
	}
	if journal[len(journal)-1].Type != "done" {
		t.Fatalf("last journal event = %+v", journal[len(journal)-1])
	}
}

func TestRestartResumesPendingHITL(t *testing.T) {
	storage := restartStore(t)
	var actions atomic.Int64
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { actions.Add(1); return nil }}
	runner := durableRunner(t, storage, dockerConfirmChat(), deps, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")
	preRestart := runEventsOf(t, storage, response.JobID)
	requireContiguousSeq(t, preRestart)

	restarted := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("done", nil)), deps, nil)
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	row, err := storage.RunGet(context.Background(), response.JobID)
	if err != nil || row.Status != store.RunStatusInterrupted {
		t.Fatalf("row after recovery = %+v err=%v", row, err)
	}
	snapshot, err := restarted.HITLSnapshot(response.JobID)
	if err != nil || snapshot.Terminal != nil || len(snapshot.Pending) != 1 {
		t.Fatalf("restored snapshot = %+v err=%v", snapshot, err)
	}

	resumed := &SliceStream{}
	if err := restarted.ConfirmStream(context.Background(), Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}, StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, resumed)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("resumed terminal counts done=%d error=%d events=%+v", done, failed, events)
	}
	if actions.Load() != 1 {
		t.Fatalf("tool actions = %d", actions.Load())
	}
	waitRunStatus(t, storage, response.JobID, store.RunStatusCompleted)
	journal := runEventsOf(t, storage, response.JobID)
	requireContiguousSeq(t, journal)
	if uint64(len(preRestart)) >= journal[len(journal)-1].Seq {
		t.Fatalf("journal seq did not advance across the restart: pre=%d post=%d", len(preRestart), journal[len(journal)-1].Seq)
	}
	if journal[len(journal)-1].Type != "done" {
		t.Fatalf("last journal event = %+v", journal[len(journal)-1])
	}
}

func TestRestartExpiresPendingHITL(t *testing.T) {
	storage := restartStore(t)
	frozen := time.Now()
	manager, err := hitl.NewManager(hitl.Config{
		Checkpoints: NewStoreCheckpoints(storage),
		Store:       hitlStoreBridge{storage},
		TTL:         60 * time.Millisecond,
		Now:         func() time.Time { return frozen },
	})
	if err != nil {
		t.Fatal(err)
	}
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	runner := durableRunner(t, storage, dockerConfirmChat(), deps, manager)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")

	restarted := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("done", nil)), deps, nil)
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	row := waitRunStatus(t, storage, response.JobID, store.RunStatusExpired)
	if !strings.Contains(row.Error, "过期") {
		t.Fatalf("expired row = %+v", row)
	}
	snapshot, err := restarted.HITLSnapshot(response.JobID)
	if err != nil || snapshot.Terminal == nil || snapshot.Terminal.Reason != hitl.TerminalExpired {
		t.Fatalf("snapshot = %+v err=%v", snapshot, err)
	}
	if err := restarted.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err == nil {
		t.Fatal("expired confirmation accepted after restart")
	}
	events := runEventsOf(t, storage, response.JobID)
	requireContiguousSeq(t, events)
	last := events[len(events)-1]
	if last.Type != "error" || !strings.Contains(last.PayloadJSON, "过期") {
		t.Fatalf("terminal journal event = %+v", last)
	}
}

func TestRestartCancelsRestoredRun(t *testing.T) {
	storage := restartStore(t)
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	runner := durableRunner(t, storage, dockerConfirmChat(), deps, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	_ = waitEvent(t, stream, "confirmRequired")

	restarted := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("done", nil)), deps, nil)
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	waitRunStatus(t, storage, response.JobID, store.RunStatusCanceled)
	snapshot, err := restarted.HITLSnapshot(response.JobID)
	if err != nil || snapshot.Terminal == nil || snapshot.Terminal.Reason != hitl.TerminalCanceled {
		t.Fatalf("snapshot = %+v err=%v", snapshot, err)
	}
}

func TestGracefulShutdownParksPendingHITLForRestart(t *testing.T) {
	storage := restartStore(t)
	var actions atomic.Int64
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { actions.Add(1); return nil }}
	runner := durableRunner(t, storage, dockerConfirmChat(), deps, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")
	if err := runner.Close(); err != nil {
		t.Fatal(err)
	}

	row, err := storage.RunGet(context.Background(), response.JobID)
	if err != nil || row.Status != store.RunStatusInterrupted || row.FinishedAt != nil {
		t.Fatalf("parked row = %+v err=%v", row, err)
	}
	if _, found, err := storage.CheckpointGet(context.Background(), response.JobID); err != nil || !found {
		t.Fatalf("parked checkpoint missing: found=%v err=%v", found, err)
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
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("resumed terminal counts done=%d error=%d", done, failed)
	}
	if actions.Load() != 1 {
		t.Fatalf("tool actions = %d", actions.Load())
	}
	waitRunStatus(t, storage, response.JobID, store.RunStatusCompleted)
}

func TestRestartWithoutStoreKeepsMemoryBehavior(t *testing.T) {
	runner, _ := testRunner(t, sequenceModel(schema.AssistantMessage("ok", nil)), tools.Dependencies{}, 0)
	if err := runner.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	runs, err := runner.RunList(context.Background(), "", 0)
	if err != nil || len(runs) != 0 {
		t.Fatalf("runs = %+v err=%v", runs, err)
	}
	events, err := runner.RunEvents(context.Background(), "missing", 0)
	if err != nil || len(events) != 0 {
		t.Fatalf("events = %+v err=%v", events, err)
	}
	if err := runner.Confirm(Confirmation{JobID: "missing", CallID: "call", Nonce: "nonce", Decision: "allow"}); err != ErrJobNotFound {
		t.Fatalf("confirm without store error = %v", err)
	}
}

func hitlBlobOf(t *testing.T, storage *store.Store, id string) []byte {
	t.Helper()
	rows, err := storage.HitlRunList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == id {
			return row.Data
		}
	}
	t.Fatalf("hitl blob %s not found", id)
	return nil
}

func TestCloseStopsParkedHitlTimerWithoutTerminalWrites(t *testing.T) {
	storage := restartStore(t)
	manager, err := hitl.NewManager(hitl.Config{
		Checkpoints: NewStoreCheckpoints(storage),
		Store:       hitlStoreBridge{storage},
		TTL:         200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	runner := durableRunner(t, storage, dockerConfirmChat(), deps, manager)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	_ = waitEvent(t, stream, "confirmRequired")
	if err := runner.Close(); err != nil {
		t.Fatal(err)
	}

	blobAfterClose := hitlBlobOf(t, storage, response.JobID)
	eventsAfterClose := len(runEventsOf(t, storage, response.JobID))
	time.Sleep(400 * time.Millisecond)

	if blob := hitlBlobOf(t, storage, response.JobID); !bytes.Equal(blob, blobAfterClose) {
		t.Fatal("parked hitl blob was rewritten after Close")
	}
	if events := len(runEventsOf(t, storage, response.JobID)); events != eventsAfterClose {
		t.Fatalf("journal grew after Close: %d -> %d", eventsAfterClose, events)
	}
	row, err := storage.RunGet(context.Background(), response.JobID)
	if err != nil || row.Status != store.RunStatusInterrupted || row.FinishedAt != nil {
		t.Fatalf("parked row after Close = %+v err=%v", row, err)
	}
	if _, found, err := storage.CheckpointGet(context.Background(), response.JobID); err != nil || !found {
		t.Fatalf("parked checkpoint deleted after Close: found=%v err=%v", found, err)
	}
}

func TestCloseQuiescesRestoredWatcherWithoutTerminalWrites(t *testing.T) {
	storage := restartStore(t)
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	runner := durableRunner(t, storage, dockerConfirmChat(), deps, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	_ = waitEvent(t, stream, "confirmRequired")

	restarted := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("done", nil)), deps, nil)
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}

	blobAfterClose := hitlBlobOf(t, storage, response.JobID)
	eventsAfterClose := len(runEventsOf(t, storage, response.JobID))
	time.Sleep(50 * time.Millisecond)

	if blob := hitlBlobOf(t, storage, response.JobID); !bytes.Equal(blob, blobAfterClose) {
		t.Fatal("restored hitl blob was rewritten after Close")
	}
	if events := len(runEventsOf(t, storage, response.JobID)); events != eventsAfterClose {
		t.Fatalf("journal grew after Close: %d -> %d", eventsAfterClose, events)
	}
	row, err := storage.RunGet(context.Background(), response.JobID)
	if err != nil || row.Status != store.RunStatusInterrupted || row.FinishedAt != nil {
		t.Fatalf("restored row after Close = %+v err=%v", row, err)
	}
	if err := restarted.Cancel(response.JobID); err != ErrJobNotFound {
		t.Fatalf("cancel after Close error = %v", err)
	}
}
