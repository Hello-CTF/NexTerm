package store_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// TestAgentEvidenceContractsDatabase exercises the signer key and anchor
// persistence against every real configured backend (PostgreSQL/MySQL/
// MariaDB via NEXTERM_TEST_DB_*), not just SQLite.
func TestAgentEvidenceContractsDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA := ipc.WithUserID(context.Background(), "evidence-owner-a")
	ctxB := ipc.WithUserID(context.Background(), "evidence-owner-b")

	// The signer key is created once and reloaded identically afterwards.
	key, err := db.EvidenceSignerKey(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) == 0 {
		t.Fatal("empty signer key")
	}
	reloaded, err := db.EvidenceSignerKey(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(key, reloaded) {
		t.Fatal("signer key changed between loads")
	}
	// A different owner gets a different key.
	otherKey, err := db.EvidenceSignerKey(ctxB)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(key, otherKey) {
		t.Fatal("cross-owner key reuse")
	}

	// Anchors resolve by the exact (capture, chain head) pair only.
	if err := db.PutEvidenceAnchor(ctxA, "cap-1", "head-1", "envelope-1", "mac-1"); err != nil {
		t.Fatal(err)
	}
	// Re-putting the identical anchor is idempotent; a different envelope
	// or MAC under the same key is a conflict, never a silent success.
	if err := db.PutEvidenceAnchor(ctxA, "cap-1", "head-1", "envelope-1", "mac-1"); err != nil {
		t.Fatalf("idempotent re-put failed: %v", err)
	}
	if err := db.PutEvidenceAnchor(ctxA, "cap-1", "head-1", "envelope-2", "mac-2"); err == nil {
		t.Fatal("conflicting anchor was accepted as success")
	}
	envelopeHash, mac, err := db.EvidenceAnchor(ctxA, "cap-1", "head-1")
	if err != nil || envelopeHash != "envelope-1" || mac != "mac-1" {
		t.Fatalf("anchor = %q %q err=%v", envelopeHash, mac, err)
	}
	if _, _, err := db.EvidenceAnchor(ctxA, "cap-1", "head-moved"); !errors.Is(err, store.ErrEvidenceAnchorNotFound) {
		t.Fatalf("moved head err=%v", err)
	}
	if _, _, err := db.EvidenceAnchor(ctxA, "cap-2", "head-1"); !errors.Is(err, store.ErrEvidenceAnchorNotFound) {
		t.Fatalf("unknown capture err=%v", err)
	}
	// Owner isolation on anchors.
	if _, _, err := db.EvidenceAnchor(ctxB, "cap-1", "head-1"); !errors.Is(err, store.ErrEvidenceAnchorNotFound) {
		t.Fatalf("cross-owner anchor resolution err=%v", err)
	}
	if err := db.PutEvidenceAnchor(ctxB, "cap-1", "head-1", "envelope-b", "mac-b"); err != nil {
		t.Fatal(err)
	}
	envelopeHash, _, err = db.EvidenceAnchor(ctxA, "cap-1", "head-1")
	if err != nil || envelopeHash != "envelope-1" {
		t.Fatalf("owner A anchor disturbed: %q err=%v", envelopeHash, err)
	}
}

// TestAgentEvidenceSignerKeyConcurrentFirstUseDatabase exercises the
// first-use creation race on every real configured backend: concurrent
// callers of the same owner's initial EvidenceSignerKey must all obtain the
// single persisted winner key, never a not-found error.
func TestAgentEvidenceSignerKeyConcurrentFirstUseDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := ipc.WithUserID(context.Background(), "evidence-race-owner")

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
}
