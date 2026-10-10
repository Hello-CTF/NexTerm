package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

func TestMsgListPreservesInsertionOrder(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	conversation, err := db.ConvCreate(ctx, "order", nil)
	if err != nil {
		t.Fatal(err)
	}
	const total = 40
	for i := 0; i < total; i++ {
		content := map[string]any{"role": "user", "content": fmt.Sprintf("m%02d", i)}
		if err := db.MsgInsert(ctx, conversation.ID, "user", content, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.MsgList(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != total {
		t.Fatalf("rows=%d want %d", len(rows), total)
	}
	for i, row := range rows {
		var persisted struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(row.ContentJSON), &persisted); err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("m%02d", i); persisted.Content != want {
			t.Fatalf("row %d content=%q want %q — insertion order lost", i, persisted.Content, want)
		}
	}
}

func TestMsgInsertConcurrentAssignsUniqueSeq(t *testing.T) {
	ctx := context.Background()
	db, _ := fileStore(t)
	conversation, err := db.ConvCreate(ctx, "concurrent", nil)
	if err != nil {
		t.Fatal(err)
	}
	const writers, perWriter = 8, 10
	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				content := map[string]any{"role": "user", "content": fmt.Sprintf("w%d-%02d", w, i)}
				if err := db.MsgInsert(ctx, conversation.ID, "user", content, nil, nil); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	rows, err := db.MsgList(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != writers*perWriter {
		t.Fatalf("rows=%d want %d", len(rows), writers*perWriter)
	}
	position := map[string]int{}
	for i, row := range rows {
		var persisted struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(row.ContentJSON), &persisted); err != nil {
			t.Fatal(err)
		}
		position[persisted.Content] = i
	}
	for w := 0; w < writers; w++ {
		for i := 1; i < perWriter; i++ {
			previous := fmt.Sprintf("w%d-%02d", w, i-1)
			current := fmt.Sprintf("w%d-%02d", w, i)
			if position[previous] > position[current] {
				t.Fatalf("writer %d rows reordered: %s at %d after %s at %d", w, previous, position[previous], current, position[current])
			}
		}
	}
	var seqs []int64
	seqRows, err := db.DB().Query("SELECT seq FROM ai_message WHERE conversation_id = ? ORDER BY seq", conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer seqRows.Close()
	for seqRows.Next() {
		var seq int64
		if err := seqRows.Scan(&seq); err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, seq)
	}
	if err := seqRows.Err(); err != nil {
		t.Fatal(err)
	}
	for i, seq := range seqs {
		if seq != int64(i+1) {
			t.Fatalf("seq[%d]=%d — seq must be contiguous from 1, got %v", i, seq, seqs)
		}
	}
}
