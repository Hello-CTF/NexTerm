package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestKnownHostMigrationReadDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "migration-owner-a")
	ownerB := ipc.WithUserID(ctx, "migration-owner-b")
	for _, acceptance := range []struct {
		ctx         context.Context
		fingerprint string
	}{
		{ctx: ctx, fingerprint: "SHA256:global"},
		{ctx: ownerA, fingerprint: "SHA256:owner-a"},
		{ctx: ownerB, fingerprint: "SHA256:owner-b"},
	} {
		if err := database.KnownHostAccept(acceptance.ctx, "migration.test", 22, "ssh-ed25519", acceptance.fingerprint); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := database.KnownHostListForMigration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("migration rows = %+v, want global and both owners", rows)
	}
	seen := make(map[string]string, len(rows))
	for _, row := range rows {
		seen[row.OwnerID] = row.Fingerprint
	}
	for ownerID, fingerprint := range map[string]string{"": "SHA256:global", "migration-owner-a": "SHA256:owner-a", "migration-owner-b": "SHA256:owner-b"} {
		if seen[ownerID] != fingerprint {
			t.Fatalf("migration owner %q fingerprint = %q, want %q; rows=%+v", ownerID, seen[ownerID], fingerprint, rows)
		}
	}
	for _, ownerCtx := range []context.Context{ownerA, ownerB} {
		owned, err := database.KnownHostList(ownerCtx)
		if err != nil || len(owned) != 1 || owned[0].OwnerID != ipc.OwnerID(ownerCtx) {
			t.Fatalf("ordinary owner list crossed its boundary: %+v, %v", owned, err)
		}
	}
}

func TestKnownHostOwnerIsolationDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "isolation-owner-a")
	ownerB := ipc.WithUserID(ctx, "isolation-owner-b")
	if err := database.KnownHostAccept(ownerA, "isolation.test", 22, "ssh-ed25519", "SHA256:a"); err != nil {
		t.Fatal(err)
	}
	if err := database.KnownHostAccept(ownerB, "isolation.test", 22, "ssh-ed25519", "SHA256:b"); err != nil {
		t.Fatal(err)
	}
	rowA, foundA, err := database.KnownHostGet(ownerA, "isolation.test", 22, "ssh-ed25519")
	if err != nil || !foundA || rowA.Fingerprint != "SHA256:a" || rowA.OwnerID != "isolation-owner-a" {
		t.Fatalf("owner A row = %+v, %v, %v", rowA, foundA, err)
	}
	rowB, foundB, err := database.KnownHostGet(ownerB, "isolation.test", 22, "ssh-ed25519")
	if err != nil || !foundB || rowB.Fingerprint != "SHA256:b" || rowB.OwnerID != "isolation-owner-b" {
		t.Fatalf("owner B row = %+v, %v, %v", rowB, foundB, err)
	}
	// An accept upsert replaces only the caller's own row.
	if err := database.KnownHostAccept(ownerA, "isolation.test", 22, "ssh-ed25519", "SHA256:a2"); err != nil {
		t.Fatal(err)
	}
	rowA, _, _ = database.KnownHostGet(ownerA, "isolation.test", 22, "ssh-ed25519")
	rowB, _, _ = database.KnownHostGet(ownerB, "isolation.test", 22, "ssh-ed25519")
	if rowA.Fingerprint != "SHA256:a2" || rowB.Fingerprint != "SHA256:b" {
		t.Fatalf("accept upsert crossed owners: A=%+v B=%+v", rowA, rowB)
	}
	// A removal carrying the other owner's ID deletes nothing.
	if err := database.KnownHostRemove(ownerA, rowB.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := database.KnownHostGet(ownerB, "isolation.test", 22, "ssh-ed25519"); err != nil || !found {
		t.Fatalf("cross-owner remove deleted owner B: found=%v err=%v", found, err)
	}
	if err := database.KnownHostRemove(ownerA, rowA.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := database.KnownHostGet(ownerA, "isolation.test", 22, "ssh-ed25519"); err != nil || found {
		t.Fatalf("owner A row survived its own removal: found=%v err=%v", found, err)
	}
	var tombstones int64
	if err := database.GORM().WithContext(ctx).Table("sync_tombstone").
		Where("id = ? AND owner_id = ?", rowA.ID, "isolation-owner-a").Count(&tombstones).Error; err != nil {
		t.Fatal(err)
	}
	if tombstones != 1 {
		t.Fatalf("owner A removal tombstones = %d, want 1", tombstones)
	}
}

func TestKnownHostCompensateAcceptDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "compensate-owner-a")
	ownerB := ipc.WithUserID(ctx, "compensate-owner-b")
	// A failed mirror write after an insert removes the freshly inserted row.
	if err := database.KnownHostAccept(ownerA, "insert.test", 22, "ssh-ed25519", "SHA256:insert"); err != nil {
		t.Fatal(err)
	}
	inserted, found, err := database.KnownHostGet(ownerA, "insert.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("insert accept = %+v, %v, %v", inserted, found, err)
	}
	if applied, err := database.KnownHostCompensateAccept(ownerA, "insert.test", 22, "ssh-ed25519", inserted, nil); err != nil || !applied {
		t.Fatalf("insert compensation applied=%v err=%v", applied, err)
	}
	if _, found, err := database.KnownHostGet(ownerA, "insert.test", 22, "ssh-ed25519"); err != nil || found {
		t.Fatalf("compensated insert left a ledger row: found=%v err=%v", found, err)
	}
	// A failed mirror write after a replacement restores the prior fingerprint and revision.
	if _, err := database.KnownHostAcceptWithKey(ownerA, "replace.test", 22, "ssh-ed25519", "SHA256:old", "b2xkLWtleQ=="); err != nil {
		t.Fatal(err)
	}
	previous, found, err := database.KnownHostGet(ownerA, "replace.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("previous row = %+v, %v, %v", previous, found, err)
	}
	if previous.Key != "b2xkLWtleQ==" {
		t.Fatalf("previous row lost its key material: %+v", previous)
	}
	if _, err := database.KnownHostAcceptWithKey(ownerA, "replace.test", 22, "ssh-ed25519", "SHA256:new", "bmV3LWtleQ=="); err != nil {
		t.Fatal(err)
	}
	accepted, found, err := database.KnownHostGet(ownerA, "replace.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("replacement accept = %+v, %v, %v", accepted, found, err)
	}
	if applied, err := database.KnownHostCompensateAccept(ownerA, "replace.test", 22, "ssh-ed25519", accepted, &previous); err != nil || !applied {
		t.Fatalf("replacement compensation applied=%v err=%v", applied, err)
	}
	restored, found, err := database.KnownHostGet(ownerA, "replace.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("restored row = %+v, %v, %v", restored, found, err)
	}
	if restored.ID != previous.ID || restored.Fingerprint != previous.Fingerprint || restored.Key != previous.Key || restored.AddedAt != previous.AddedAt {
		t.Fatalf("compensated replacement = %+v, want %+v", restored, previous)
	}
	// Compensation is owner-scoped: another owner cannot roll back someone else's accept.
	if applied, err := database.KnownHostCompensateAccept(ownerB, "replace.test", 22, "ssh-ed25519", restored, nil); err != nil || applied {
		t.Fatalf("cross-owner compensation applied=%v err=%v", applied, err)
	}
	if _, found, err := database.KnownHostGet(ownerA, "replace.test", 22, "ssh-ed25519"); err != nil || !found {
		t.Fatalf("cross-owner compensation deleted owner A: found=%v err=%v", found, err)
	}
}

func TestKnownHostCompensateAcceptSkipsNewerDecisionDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "cas-owner-a")
	// An interleaved independent accept between the original accept and its compensation must survive:
	// the CAS guard never rolls back a newer trust decision. This mirrors the production PutHostKey write
	// path: the accepted outcome comes from the accept itself, never from a re-read.
	first, err := database.KnownHostAcceptWithKey(ownerA, "interleave.test", 22, "ssh-ed25519", "SHA256:first", "Zmlyc3Qta2V5")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.KnownHostAcceptWithKey(ownerA, "interleave.test", 22, "ssh-ed25519", "SHA256:newer", "bmV3ZXIta2V5"); err != nil {
		t.Fatal(err)
	}
	if applied, err := database.KnownHostCompensateAccept(ownerA, "interleave.test", 22, "ssh-ed25519", first, nil); err != nil || applied {
		t.Fatalf("interleaved compensation applied=%v err=%v", applied, err)
	}
	row, found, err := database.KnownHostGet(ownerA, "interleave.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("newer decision = %+v, %v, %v", row, found, err)
	}
	if row.Fingerprint != "SHA256:newer" || row.Key != "bmV3ZXIta2V5" {
		t.Fatalf("compensation rolled back a newer decision: %+v", row)
	}
	// The same CAS discipline applies to replacements: a stale accepted outcome must not restore the previous row.
	previous := row
	third, err := database.KnownHostAcceptWithKey(ownerA, "interleave.test", 22, "ssh-ed25519", "SHA256:third", "dGhpcmQta2V5")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.KnownHostAcceptWithKey(ownerA, "interleave.test", 22, "ssh-ed25519", "SHA256:fourth", "Zm91cnRoLWtleQ=="); err != nil {
		t.Fatal(err)
	}
	if applied, err := database.KnownHostCompensateAccept(ownerA, "interleave.test", 22, "ssh-ed25519", third, &previous); err != nil || applied {
		t.Fatalf("stale replacement compensation applied=%v err=%v", applied, err)
	}
	if row, _, _ := database.KnownHostGet(ownerA, "interleave.test", 22, "ssh-ed25519"); row.Fingerprint != "SHA256:fourth" || row.Key != "Zm91cnRoLWtleQ==" {
		t.Fatalf("stale replacement compensation changed the row: %+v", row)
	}
	// Compensating with the exact current outcome still applies.
	fourth, err := database.KnownHostAcceptWithKey(ownerA, "interleave.test", 22, "ssh-ed25519", "SHA256:fourth", "Zm91cnRoLWtleQ==")
	if err != nil {
		t.Fatal(err)
	}
	if applied, err := database.KnownHostCompensateAccept(ownerA, "interleave.test", 22, "ssh-ed25519", fourth, nil); err != nil || !applied {
		t.Fatalf("current-state compensation applied=%v err=%v", applied, err)
	}
	if _, found, err := database.KnownHostGet(ownerA, "interleave.test", 22, "ssh-ed25519"); err != nil || found {
		t.Fatalf("current-state compensation left a row: found=%v err=%v", found, err)
	}
}

func TestKnownHostAcceptForMirrorAtomicDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "atomic-owner-a")
	// previous=nil with an interleaved independent accept after this accept: the compensation must skip.
	acceptedA, previous, err := database.KnownHostAcceptForMirror(ownerA, "atomic.test", 22, "ssh-ed25519", "SHA256:a", "YS1rZXk=", false)
	if err != nil || previous != nil {
		t.Fatalf("first accept = %+v, %+v, %v", acceptedA, previous, err)
	}
	if _, err := database.KnownHostAcceptWithKey(ownerA, "atomic.test", 22, "ssh-ed25519", "SHA256:b", "Yi1rZXk="); err != nil {
		t.Fatal(err)
	}
	if applied, err := database.KnownHostCompensateAccept(ownerA, "atomic.test", 22, "ssh-ed25519", acceptedA, nil); err != nil || applied {
		t.Fatalf("interleaved insert compensation applied=%v err=%v", applied, err)
	}
	if row, found, err := database.KnownHostGet(ownerA, "atomic.test", 22, "ssh-ed25519"); err != nil || !found || row.Fingerprint != "SHA256:b" {
		t.Fatalf("interleaved decision = %+v, %v, %v", row, found, err)
	}
	// An independent accept between the previous read and this accept becomes the actual previous:
	// the compensation restores it, not the state from before it.
	if _, err := database.KnownHostAcceptWithKey(ownerA, "atomic.test", 22, "ssh-ed25519", "SHA256:c", "Yy1rZXk="); err != nil {
		t.Fatal(err)
	}
	acceptedD, previousC, err := database.KnownHostAcceptForMirror(ownerA, "atomic.test", 22, "ssh-ed25519", "SHA256:d", "ZC1rZXk=", true)
	if err != nil {
		t.Fatal(err)
	}
	if previousC == nil || previousC.Fingerprint != "SHA256:c" || previousC.Key != "Yy1rZXk=" {
		t.Fatalf("actual previous = %+v, want the interleaved C row", previousC)
	}
	if applied, err := database.KnownHostCompensateAccept(ownerA, "atomic.test", 22, "ssh-ed25519", acceptedD, previousC); err != nil || !applied {
		t.Fatalf("replacement compensation applied=%v err=%v", applied, err)
	}
	if row, found, err := database.KnownHostGet(ownerA, "atomic.test", 22, "ssh-ed25519"); err != nil || !found || row.Fingerprint != "SHA256:c" || row.Key != "Yy1rZXk=" || row.AddedAt != previousC.AddedAt {
		t.Fatalf("restored row = %+v, %v, %v", row, found, err)
	}
	// Without replace, an existing row with a different fingerprint fails with ErrKnownHostConflict and stays untouched.
	if _, _, err := database.KnownHostAcceptForMirror(ownerA, "atomic.test", 22, "ssh-ed25519", "SHA256:e", "ZS1rZXk=", false); !errors.Is(err, store.ErrKnownHostConflict) {
		t.Fatalf("conflicting accept error = %v", err)
	}
	if row, _, _ := database.KnownHostGet(ownerA, "atomic.test", 22, "ssh-ed25519"); row.Fingerprint != "SHA256:c" || row.Key != "Yy1rZXk=" {
		t.Fatalf("conflicting accept changed the row: %+v", row)
	}
}

func TestKnownHostAcceptForMirrorConcurrentInsertDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "race-owner-a")
	// Two concurrent accepts for the same absent endpoint: exactly one may report a fresh insert, and
	// the other must observe the winner's row as its actual previous — never a stale empty read.
	for iteration := 0; iteration < 20; iteration++ {
		host := fmt.Sprintf("race-%d.test", iteration)
		fingerprintA := fmt.Sprintf("SHA256:race-a-%d", iteration)
		fingerprintB := fmt.Sprintf("SHA256:race-b-%d", iteration)
		start := make(chan struct{})
		type outcome struct {
			accepted store.KnownHostRow
			previous *store.KnownHostRow
			err      error
		}
		outcomes := make([]outcome, 2)
		var wait sync.WaitGroup
		for index, fingerprint := range []string{fingerprintA, fingerprintB} {
			wait.Add(1)
			go func(index int, fingerprint string) {
				defer wait.Done()
				<-start
				accepted, previous, err := database.KnownHostAcceptForMirror(ownerA, host, 22, "ssh-ed25519", fingerprint, fingerprint+"-a2V5", true)
				outcomes[index] = outcome{accepted: accepted, previous: previous, err: err}
			}(index, fingerprint)
		}
		close(start)
		wait.Wait()
		for _, outcome := range outcomes {
			if outcome.err != nil {
				t.Fatalf("iteration %d accept = %v", iteration, outcome.err)
			}
		}
		fresh := 0
		for _, outcome := range outcomes {
			if outcome.previous == nil {
				fresh++
			}
		}
		if fresh != 1 {
			t.Fatalf("iteration %d fresh inserts = %d, outcomes = %+v", iteration, fresh, outcomes)
		}
		var winner, loser outcome
		if outcomes[0].previous == nil {
			winner, loser = outcomes[0], outcomes[1]
		} else {
			winner, loser = outcomes[1], outcomes[0]
		}
		if loser.previous.Fingerprint != winner.accepted.Fingerprint || loser.previous.Key != winner.accepted.Key {
			t.Fatalf("iteration %d loser previous = %+v, want winner outcome %+v", iteration, loser.previous, winner.accepted)
		}
		row, found, err := database.KnownHostGet(ownerA, host, 22, "ssh-ed25519")
		if err != nil || !found {
			t.Fatalf("iteration %d final row = %+v, %v, %v", iteration, row, found, err)
		}
		if row.Fingerprint != fingerprintA && row.Fingerprint != fingerprintB {
			t.Fatalf("iteration %d final row = %+v, want one of the accepted outcomes", iteration, row)
		}
	}
}

func TestKnownHostRemoveForMirrorConcurrentUpdateDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "remove-race-owner-a")
	// A fingerprint-changing accept races a mirror removal: whichever schedule the backend picks, the
	// independent decision survives — the locked snapshot makes the restored row the actual deleted one.
	for iteration := 0; iteration < 20; iteration++ {
		host := fmt.Sprintf("remove-race-%d.test", iteration)
		if _, err := database.KnownHostAcceptWithKey(ownerA, host, 22, "ssh-ed25519", "SHA256:seed", "c2VlZC1rZXk="); err != nil {
			t.Fatal(err)
		}
		seed, found, err := database.KnownHostGet(ownerA, host, 22, "ssh-ed25519")
		if err != nil || !found {
			t.Fatalf("iteration %d seed row = %+v, %v, %v", iteration, seed, found, err)
		}
		fingerprintB := fmt.Sprintf("SHA256:b-%d", iteration)
		keyB := fmt.Sprintf("Yi1rZXk=%d", iteration)
		start := make(chan struct{})
		var wait sync.WaitGroup
		removedCh := make(chan store.KnownHostRow, 1)
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			removed, _, err := database.KnownHostRemoveForMirror(ownerA, seed.ID)
			if err != nil {
				t.Errorf("iteration %d removal = %v", iteration, err)
			}
			removedCh <- removed
		}()
		go func() {
			defer wait.Done()
			<-start
			if _, err := database.KnownHostAcceptWithKey(ownerA, host, 22, "ssh-ed25519", fingerprintB, keyB); err != nil {
				t.Errorf("iteration %d concurrent accept = %v", iteration, err)
			}
		}()
		close(start)
		wait.Wait()
		removed := <-removedCh
		if err := database.KnownHostRestoreRemoved(ownerA, removed); err != nil {
			t.Fatalf("iteration %d restore = %v", iteration, err)
		}
		row, found, err := database.KnownHostGet(ownerA, host, 22, "ssh-ed25519")
		if err != nil || !found {
			t.Fatalf("iteration %d final row = %+v, %v, %v", iteration, row, found, err)
		}
		if row.Fingerprint != fingerprintB || row.Key != keyB {
			t.Fatalf("iteration %d independent decision lost: %+v, want fingerprint %q key %q", iteration, row, fingerprintB, keyB)
		}
	}
}

func TestKnownHostCompensateAcceptTokenABADatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "aba-owner-a")
	// Insert ABA: A accepts F/K, an independent B accepts the identical F/K in the same revision, and
	// A's mirror-failure compensation must not match B's row even with colliding added_at.
	acceptedA, previous, err := database.KnownHostAcceptForMirror(ownerA, "aba-insert.test", 22, "ssh-ed25519", "SHA256:aba", "YWJhLWtleQ==", false)
	if err != nil || previous != nil {
		t.Fatalf("A accept = %+v, %+v, %v", acceptedA, previous, err)
	}
	acceptedB, err := database.KnownHostAcceptWithKey(ownerA, "aba-insert.test", 22, "ssh-ed25519", "SHA256:aba", "YWJhLWtleQ==")
	if err != nil {
		t.Fatal(err)
	}
	if acceptedB.OpToken == acceptedA.OpToken {
		t.Fatal("accepts shared an operation token")
	}
	if err := database.GORM().WithContext(ctx).Model(&store.KnownHostRow{}).
		Where("host = ? AND port = ? AND key_type = ? AND owner_id = ?", "aba-insert.test", 22, "ssh-ed25519", "aba-owner-a").
		Update("added_at", acceptedA.AddedAt).Error; err != nil {
		t.Fatal(err)
	}
	if applied, err := database.KnownHostCompensateAccept(ownerA, "aba-insert.test", 22, "ssh-ed25519", acceptedA, nil); err != nil || applied {
		t.Fatalf("ABA insert compensation applied=%v err=%v", applied, err)
	}
	if row, found, err := database.KnownHostGet(ownerA, "aba-insert.test", 22, "ssh-ed25519"); err != nil || !found || row.OpToken != acceptedB.OpToken || row.AddedAt != acceptedA.AddedAt {
		t.Fatalf("B decision = %+v, %v, %v", row, found, err)
	}
	// Replacement ABA: A replaces P with F/K, an independent B re-accepts the identical F/K in the
	// same revision, and A's compensation must not restore stale P over B.
	if _, err := database.KnownHostAcceptWithKey(ownerA, "aba-replace.test", 22, "ssh-ed25519", "SHA256:previous", "cHJldmlvdXMta2V5"); err != nil {
		t.Fatal(err)
	}
	previousRow, _, _ := database.KnownHostGet(ownerA, "aba-replace.test", 22, "ssh-ed25519")
	acceptedR, previous, err := database.KnownHostAcceptForMirror(ownerA, "aba-replace.test", 22, "ssh-ed25519", "SHA256:aba", "YWJhLWtleQ==", true)
	if err != nil || previous == nil || previous.ID != previousRow.ID {
		t.Fatalf("replacement accept = %+v, %+v, %v", acceptedR, previous, err)
	}
	acceptedB2, err := database.KnownHostAcceptWithKey(ownerA, "aba-replace.test", 22, "ssh-ed25519", "SHA256:aba", "YWJhLWtleQ==")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GORM().WithContext(ctx).Model(&store.KnownHostRow{}).
		Where("host = ? AND port = ? AND key_type = ? AND owner_id = ?", "aba-replace.test", 22, "ssh-ed25519", "aba-owner-a").
		Update("added_at", acceptedR.AddedAt).Error; err != nil {
		t.Fatal(err)
	}
	if applied, err := database.KnownHostCompensateAccept(ownerA, "aba-replace.test", 22, "ssh-ed25519", acceptedR, previous); err != nil || applied {
		t.Fatalf("ABA replacement compensation applied=%v err=%v", applied, err)
	}
	if row, found, err := database.KnownHostGet(ownerA, "aba-replace.test", 22, "ssh-ed25519"); err != nil || !found || row.Fingerprint != "SHA256:aba" || row.OpToken != acceptedB2.OpToken {
		t.Fatalf("B replacement decision overwritten by stale previous: %+v, %v, %v", row, found, err)
	}
}

func TestKnownHostAcceptForMirrorRetryResetsPreviousDatabase(t *testing.T) {
	fixture := dbtest.NewFixture(t)
	if fixture.Backend != store.BackendPostgres {
		t.Skipf("retry-previous regression requires the PostgreSQL 40P01 trigger schedule, backend = %s", fixture.Backend)
	}
	database := fixture.OpenStore(t)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "retry-owner-a")
	if _, err := database.KnownHostAcceptWithKey(ownerA, "retry.test", 22, "ssh-ed25519", "SHA256:previous", "cHJldmlvdXMta2V5"); err != nil {
		t.Fatal(err)
	}
	row, found, err := database.KnownHostGet(ownerA, "retry.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("seed row = %+v, %v, %v", row, found, err)
	}
	// Hold the first transaction open after its locked read so an independent delete queues on the row
	// lock and commits right after the forced serialization failure rolls the transaction back.
	if _, err := database.DB().ExecContext(ctx, `
		CREATE OR REPLACE FUNCTION fail_known_host_update() RETURNS trigger AS $$
		BEGIN
			PERFORM pg_sleep(0.5);
			RAISE EXCEPTION 'forced serialization failure' USING ERRCODE = '40P01';
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER fail_known_host_update BEFORE UPDATE ON known_host
		FOR EACH ROW EXECUTE FUNCTION fail_known_host_update()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.DB().ExecContext(context.Background(), `DROP TRIGGER IF EXISTS fail_known_host_update ON known_host; DROP FUNCTION IF EXISTS fail_known_host_update()`)
	})
	type acceptResult struct {
		accepted store.KnownHostRow
		previous *store.KnownHostRow
		err      error
	}
	acceptedCh := make(chan acceptResult, 1)
	go func() {
		accepted, previous, err := database.KnownHostAcceptForMirror(ownerA, "retry.test", 22, "ssh-ed25519", "SHA256:replacement", "cmVwbGFjZW1lbnQta2V5", true)
		acceptedCh <- acceptResult{accepted: accepted, previous: previous, err: err}
	}()
	// Let the first transaction take the row lock, then queue an independent identity-less delete.
	time.Sleep(50 * time.Millisecond)
	removedCh := make(chan error, 1)
	go func() {
		removedCh <- database.KnownHostRemove(context.Background(), row.ID)
	}()
	result := <-acceptedCh
	if result.err != nil {
		t.Fatalf("accept after retry = %v", result.err)
	}
	if err := <-removedCh; err != nil {
		t.Fatalf("independent removal = %v", err)
	}
	// The retry was a fresh insert after the independent delete: the returned previous may only come
	// from the successful transaction, so it must be nil — never the rolled-back row.
	if result.previous != nil {
		t.Fatalf("stale previous survived the retry: %+v", result.previous)
	}
	// Mirror-failure compensation with the fresh-insert identity deletes the new row; it must not
	// restore the independently deleted trust.
	if applied, err := database.KnownHostCompensateAccept(ownerA, "retry.test", 22, "ssh-ed25519", result.accepted, nil); err != nil || !applied {
		t.Fatalf("fresh-insert compensation applied=%v err=%v", applied, err)
	}
	if _, found, err := database.KnownHostGet(ownerA, "retry.test", 22, "ssh-ed25519"); err != nil || found {
		t.Fatalf("stale trust restored after fresh insert: found=%v err=%v", found, err)
	}
}

func TestKnownHostRestoreRemovedKeepsNewerDecisionDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "guard-owner-a")
	if _, err := database.KnownHostAcceptWithKey(ownerA, "guard.test", 22, "ssh-ed25519", "SHA256:old", "b2xkLWtleQ=="); err != nil {
		t.Fatal(err)
	}
	row, found, err := database.KnownHostGet(ownerA, "guard.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("seed row = %+v, %v, %v", row, found, err)
	}
	removed, found, err := database.KnownHostRemoveForMirror(ownerA, row.ID)
	if err != nil || !found {
		t.Fatalf("removal = %+v, %v, %v", removed, found, err)
	}
	// An independent accept lands before the failed mirror rewrite is compensated.
	if _, err := database.KnownHostAcceptWithKey(ownerA, "guard.test", 22, "ssh-ed25519", "SHA256:newer", "bmV3ZXIta2V5"); err != nil {
		t.Fatal(err)
	}
	if err := database.KnownHostRestoreRemoved(ownerA, removed); err != nil {
		t.Fatal(err)
	}
	current, found, err := database.KnownHostGet(ownerA, "guard.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("newer decision = %+v, %v, %v", current, found, err)
	}
	if current.Fingerprint != "SHA256:newer" || current.Key != "bmV3ZXIta2V5" {
		t.Fatalf("restore rolled back a newer decision: %+v", current)
	}
	// The tombstone of the removed row stands so the deletion still converges.
	var tombstones int64
	if err := database.GORM().WithContext(ctx).Table("sync_tombstone").
		Where("id = ? AND owner_id = ?", row.ID, "guard-owner-a").Count(&tombstones).Error; err != nil {
		t.Fatal(err)
	}
	if tombstones != 1 {
		t.Fatalf("removed row tombstones = %d, want 1", tombstones)
	}
}

func TestKnownHostRemoveForMirrorRestoreDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "mirror-owner-a")
	ownerB := ipc.WithUserID(ctx, "mirror-owner-b")
	if err := database.KnownHostAccept(ownerA, "mirror.test", 22, "ssh-ed25519", "SHA256:mirror"); err != nil {
		t.Fatal(err)
	}
	row, found, err := database.KnownHostGet(ownerA, "mirror.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("seed row = %+v, %v, %v", row, found, err)
	}
	// The removal is owner-scoped: another owner neither sees nor removes the row.
	if _, found, err := database.KnownHostRemoveForMirror(ownerB, row.ID); err != nil || found {
		t.Fatalf("cross-owner removal found=%v err=%v", found, err)
	}
	removed, found, err := database.KnownHostRemoveForMirror(ownerA, row.ID)
	if err != nil || !found {
		t.Fatalf("removal = %+v, %v, %v", removed, found, err)
	}
	if removed != row {
		t.Fatalf("removed row = %+v, want %+v", removed, row)
	}
	if _, found, err := database.KnownHostGet(ownerA, "mirror.test", 22, "ssh-ed25519"); err != nil || found {
		t.Fatalf("removed row still present: found=%v err=%v", found, err)
	}
	var tombstones int64
	if err := database.GORM().WithContext(ctx).Table("sync_tombstone").
		Where("id = ? AND owner_id = ?", row.ID, "mirror-owner-a").Count(&tombstones).Error; err != nil {
		t.Fatal(err)
	}
	if tombstones != 1 {
		t.Fatalf("removal tombstones = %d, want 1", tombstones)
	}
	// Restoring after a failed mirror rewrite brings the exact row back and drops the tombstone.
	if err := database.KnownHostRestoreRemoved(ownerA, removed); err != nil {
		t.Fatal(err)
	}
	restored, found, err := database.KnownHostGet(ownerA, "mirror.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("restored row = %+v, %v, %v", restored, found, err)
	}
	if restored != row {
		t.Fatalf("restored row = %+v, want %+v", restored, row)
	}
	if err := database.GORM().WithContext(ctx).Table("sync_tombstone").
		Where("id = ? AND owner_id = ?", row.ID, "mirror-owner-a").Count(&tombstones).Error; err != nil {
		t.Fatal(err)
	}
	if tombstones != 0 {
		t.Fatalf("restore left %d tombstones, want 0", tombstones)
	}
	// With opt-in disabled the removal writes no tombstone and the restore still round-trips.
	if err := database.SetKnownHostSyncOptIn(ownerA, "mirror-owner-a", false); err != nil {
		t.Fatal(err)
	}
	removed, found, err = database.KnownHostRemoveForMirror(ownerA, row.ID)
	if err != nil || !found {
		t.Fatalf("opt-out removal = %+v, %v, %v", removed, found, err)
	}
	if err := database.KnownHostRestoreRemoved(ownerA, removed); err != nil {
		t.Fatal(err)
	}
	if restored, found, err := database.KnownHostGet(ownerA, "mirror.test", 22, "ssh-ed25519"); err != nil || !found || restored != row {
		t.Fatalf("opt-out restored row = %+v, %v, %v", restored, found, err)
	}
}

func TestKnownHostKeyMaterialDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "material-owner-a")
	ownerB := ipc.WithUserID(ctx, "material-owner-b")
	// Accepting with complete key material stores it on the ledger row.
	if _, err := database.KnownHostAcceptWithKey(ownerA, "material.test", 22, "ssh-ed25519", "SHA256:material", "bWF0ZXJpYWwta2V5"); err != nil {
		t.Fatal(err)
	}
	row, found, err := database.KnownHostGet(ownerA, "material.test", 22, "ssh-ed25519")
	if err != nil || !found || row.Key != "bWF0ZXJpYWwta2V5" {
		t.Fatalf("key material row = %+v, %v, %v", row, found, err)
	}
	// A fingerprint-only re-accept keeps the recorded material.
	if err := database.KnownHostAccept(ownerA, "material.test", 22, "ssh-ed25519", "SHA256:material"); err != nil {
		t.Fatal(err)
	}
	if row, _, _ = database.KnownHostGet(ownerA, "material.test", 22, "ssh-ed25519"); row.Key != "bWF0ZXJpYWwta2V5" {
		t.Fatalf("fingerprint-only re-accept lost material: %+v", row)
	}
	// A replacement with new material updates fingerprint and key together.
	if _, err := database.KnownHostAcceptWithKey(ownerA, "material.test", 22, "ssh-ed25519", "SHA256:rotated", "cm90YXRlZC1rZXk="); err != nil {
		t.Fatal(err)
	}
	if row, _, _ = database.KnownHostGet(ownerA, "material.test", 22, "ssh-ed25519"); row.Fingerprint != "SHA256:rotated" || row.Key != "cm90YXRlZC1rZXk=" {
		t.Fatalf("material replacement = %+v", row)
	}
	// A fingerprint-only replacement clears stale material: the old key must not verify under the new fingerprint.
	if err := database.KnownHostAccept(ownerA, "material.test", 22, "ssh-ed25519", "SHA256:cleared"); err != nil {
		t.Fatal(err)
	}
	if row, _, _ = database.KnownHostGet(ownerA, "material.test", 22, "ssh-ed25519"); row.Fingerprint != "SHA256:cleared" || row.Key != "" {
		t.Fatalf("fingerprint-only replacement kept stale material: %+v", row)
	}
	// Key material is owner-scoped: owners hold independent material for the same endpoint.
	if _, err := database.KnownHostAcceptWithKey(ownerB, "material.test", 22, "ssh-ed25519", "SHA256:material-b", "b3duZXItYi1rZXk="); err != nil {
		t.Fatal(err)
	}
	rowA, _, _ := database.KnownHostGet(ownerA, "material.test", 22, "ssh-ed25519")
	rowB, _, _ := database.KnownHostGet(ownerB, "material.test", 22, "ssh-ed25519")
	if rowA.Fingerprint != "SHA256:cleared" || rowA.Key != "" || rowB.Fingerprint != "SHA256:material-b" || rowB.Key != "b3duZXItYi1rZXk=" {
		t.Fatalf("owner materials crossed: A=%+v B=%+v", rowA, rowB)
	}
}
