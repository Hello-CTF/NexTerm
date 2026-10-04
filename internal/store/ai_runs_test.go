package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func insertTestRun(t *testing.T, db *Store, id, status string) string {
	t.Helper()
	conversation, err := db.ConvCreate(context.Background(), "t", map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RunInsert(context.Background(), RunRow{ID: id, ConversationID: conversation.ID, Status: status}); err != nil {
		t.Fatal(err)
	}
	return conversation.ID
}

func TestRunLifecycleAndEventPaging(t *testing.T) {
	db := testStore(t)
	insertTestRun(t, db, "run-1", RunStatusRunning)
	row, err := db.RunGet(context.Background(), "run-1")
	if err != nil || row.Status != RunStatusRunning || row.Seq != 0 {
		t.Fatalf("row = %+v err=%v", row, err)
	}
	for i := 1; i <= 3; i++ {
		seq, err := db.RunAppendEvent(context.Background(), "run-1", "delta", func(seq uint64) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"type":"delta","seq":%d,"text":"%d"}`, seq, i)), nil
		})
		if err != nil || seq != uint64(i) {
			t.Fatalf("append %d: seq=%d err=%v", i, seq, err)
		}
	}
	events, err := db.RunEventsAfter(context.Background(), "run-1", 1)
	if err != nil || len(events) != 2 || events[0].Seq != 2 || events[1].Seq != 3 {
		t.Fatalf("events = %+v err=%v", events, err)
	}
	if events[0].PayloadJSON != `{"type":"delta","seq":2,"text":"2"}` {
		t.Fatalf("payload = %q", events[0].PayloadJSON)
	}
	if err := db.RunUpdateStatus(context.Background(), "run-1", RunStatusInterrupted); err != nil {
		t.Fatal(err)
	}
	active, err := db.RunsActive(context.Background())
	if err != nil || len(active) != 1 || active[0].ID != "run-1" {
		t.Fatalf("active = %+v err=%v", active, err)
	}
	if err := db.RunFinish(context.Background(), "run-1", RunStatusCompleted, "answer", "", 2, 10, 20); err != nil {
		t.Fatal(err)
	}
	row, err = db.RunGet(context.Background(), "run-1")
	if err != nil || row.Status != RunStatusCompleted || row.Answer != "answer" || row.TokensIn != 10 || row.FinishedAt == nil {
		t.Fatalf("finished row = %+v err=%v", row, err)
	}
	if active, err = db.RunsActive(context.Background()); err != nil || len(active) != 0 {
		t.Fatalf("active after finish = %+v err=%v", active, err)
	}
	if _, err := db.RunAppendEvent(context.Background(), "missing", "delta", func(uint64) ([]byte, error) {
		return []byte(`{}`), nil
	}); err == nil {
		t.Fatal("append to missing run succeeded")
	}
}

func TestRunAppendEventConcurrentMonotonic(t *testing.T) {
	db, _ := fileStore(t)
	insertTestRun(t, db, "run-c", RunStatusRunning)
	const writers = 8
	const perWriter = 10
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if _, err := db.RunAppendEvent(context.Background(), "run-c", "status", func(seq uint64) ([]byte, error) {
					return []byte(`{"type":"status"}`), nil
				}); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	events, err := db.RunEventsAfter(context.Background(), "run-c", 0)
	if err != nil || len(events) != writers*perWriter {
		t.Fatalf("events = %d err=%v", len(events), err)
	}
	for index, event := range events {
		if event.Seq != uint64(index+1) {
			t.Fatalf("event %d has seq %d", index, event.Seq)
		}
	}
}

func TestRunListFiltersByConversation(t *testing.T) {
	db := testStore(t)
	first, err := db.ConvCreate(context.Background(), "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.ConvCreate(context.Background(), "b", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RunInsert(context.Background(), RunRow{ID: "r1", ConversationID: first.ID, Status: RunStatusCompleted}); err != nil {
		t.Fatal(err)
	}
	if err := db.RunInsert(context.Background(), RunRow{ID: "r2", ConversationID: second.ID, Status: RunStatusCompleted}); err != nil {
		t.Fatal(err)
	}
	runs, err := db.RunList(context.Background(), first.ID, 0)
	if err != nil || len(runs) != 1 || runs[0].ID != "r1" {
		t.Fatalf("runs = %+v err=%v", runs, err)
	}
	all, err := db.RunList(context.Background(), "", 1)
	if err != nil || len(all) != 1 {
		t.Fatalf("limited runs = %+v err=%v", all, err)
	}
	if err := db.ConvDelete(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RunGet(context.Background(), "r1"); err == nil {
		t.Fatal("run survived conversation cascade delete")
	}
}

func TestCheckpointAndHitlRunRoundtrip(t *testing.T) {
	db := testStore(t)
	if _, found, err := db.CheckpointGet(context.Background(), "cp"); err != nil || found {
		t.Fatalf("missing checkpoint: found=%v err=%v", found, err)
	}
	if err := db.CheckpointSet(context.Background(), "cp", []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	data, found, err := db.CheckpointGet(context.Background(), "cp")
	if err != nil || !found || len(data) != 3 || data[0] != 1 {
		t.Fatalf("checkpoint = %v found=%v err=%v", data, found, err)
	}
	data[0] = 9
	again, _, _ := db.CheckpointGet(context.Background(), "cp")
	if again[0] != 1 {
		t.Fatal("checkpoint get aliased the stored blob")
	}
	if err := db.CheckpointSet(context.Background(), "cp", []byte{7}); err != nil {
		t.Fatal(err)
	}
	if data, _, _ = db.CheckpointGet(context.Background(), "cp"); len(data) != 1 || data[0] != 7 {
		t.Fatalf("upserted checkpoint = %v", data)
	}
	if err := db.CheckpointDelete(context.Background(), "cp"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ = db.CheckpointGet(context.Background(), "cp"); found {
		t.Fatal("checkpoint survived delete")
	}

	if err := db.HitlRunSave(context.Background(), "h1", []byte(`{"status":"interrupted"}`)); err != nil {
		t.Fatal(err)
	}
	if err := db.HitlRunSave(context.Background(), "h1", []byte(`{"status":"running"}`)); err != nil {
		t.Fatal(err)
	}
	runs, err := db.HitlRunList(context.Background())
	if err != nil || len(runs) != 1 || runs[0].ID != "h1" || string(runs[0].Data) != `{"status":"running"}` {
		t.Fatalf("hitl runs = %+v err=%v", runs, err)
	}
	if err := db.HitlRunDelete(context.Background(), "h1"); err != nil {
		t.Fatal(err)
	}
	if runs, err = db.HitlRunList(context.Background()); err != nil || len(runs) != 0 {
		t.Fatalf("hitl runs after delete = %+v err=%v", runs, err)
	}
}
