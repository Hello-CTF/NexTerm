package sync

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// pushAllConcurrently simulates multiple devices pushing at once: each worker pushes a batch of distinct objects,
// re-reading and retrying on head conflict. All batches must eventually be applied.
func pushAllConcurrently(t *testing.T, ctx context.Context, objectStore *objectStore, userID string, workers, perWorker int) string {
	t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			objects := make([]WireObject, 0, perWorker)
			for i := 0; i < perWorker; i++ {
				objects = append(objects, WireObject{ID: fmt.Sprintf("worker-%d-obj-%d", w, i), Blob: []byte(fmt.Sprintf("blob-%d-%d", w, i))})
			}
			for attempt := 0; attempt < 200; attempt++ {
				head, err := objectStore.currentHead(ctx, userID)
				if err != nil {
					errs <- err
					return
				}
				if _, _, _, _, err := objectStore.push(ctx, userID, head, objects); err == nil {
					return
				} else if !errors.Is(err, errHeadMismatch) {
					errs <- err
					return
				}
			}
			errs <- fmt.Errorf("worker %d: push retries exhausted", w)
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	head, err := objectStore.currentHead(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	return head
}

// assertSeqContiguous validates the concurrent allocation result: all objects present, seq contiguous from 1 with no duplicates.
func assertSeqContiguous(t *testing.T, ctx context.Context, objectStore *objectStore, userID string, total int) {
	t.Helper()
	entries, _, maxSeq, err := objectStore.idList(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != total || maxSeq != int64(total) {
		t.Fatalf("entries=%d maxSeq=%d want %d", len(entries), maxSeq, total)
	}
	seen := make(map[int64]string, total)
	for _, entry := range entries {
		if prev, dup := seen[entry.Seq]; dup {
			t.Fatalf("duplicate seq %d (%s vs %s)", entry.Seq, prev, entry.ID)
		}
		seen[entry.Seq] = entry.ID
	}
	for seq := int64(1); seq <= int64(total); seq++ {
		if _, ok := seen[seq]; !ok {
			t.Fatalf("seq %d missing", seq)
		}
	}
}
