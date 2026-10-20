package store

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// TestEvidenceSignerKeyConcurrentFirstUse exercises the first-use creation
// race on a real store: when many callers race to create the same owner's
// signer key, the unique constraint picks one winner and every loser must
// still read back that winner's key — a loser that reads before the
// winner's insert is visible must retry, not fail the caller and drop the
// caller's capture.
func TestEvidenceSignerKeyConcurrentFirstUse(t *testing.T) {
	db := testStore(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")

	const workers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	keys := make(chan []byte, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			key, err := db.EvidenceSignerKey(ctx)
			if err != nil {
				errs <- err
				return
			}
			keys <- key
		}()
	}
	close(start)
	wg.Wait()
	close(keys)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent first use failed: %v", err)
	}

	var winner []byte
	received := 0
	for key := range keys {
		if len(key) == 0 {
			t.Fatal("caller received an empty key")
		}
		if winner == nil {
			winner = key
		} else if !bytes.Equal(winner, key) {
			t.Fatal("callers received different signer keys")
		}
		received++
	}
	if received != workers {
		t.Fatalf("received %d keys, want %d", received, workers)
	}

	// Exactly one key row exists for the owner, and it is the key every
	// caller received.
	var rows []AgentEvidenceKeyRow
	if err := db.db.WithContext(ctx).Where("owner_id = ?", "owner-a").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("persisted key rows = %d, want 1", len(rows))
	}
	if !bytes.Equal(rows[0].Key, winner) {
		t.Fatal("persisted key differs from the key callers received")
	}

	// Later callers keep loading the same persisted key.
	key, err := db.EvidenceSignerKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(key, winner) {
		t.Fatal("later caller received a different key")
	}
}
