package account

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/vault"
	"gorm.io/gorm"
)

// gateBeforeCreate pauses every CREATE on the user_dek table until both racers
// have arrived, so the two INSERTs hit the unique constraint at the same
// instant. Setup creates on other tables pass through untouched.
func gateBeforeCreate(t *testing.T, db *gorm.DB, arrived chan<- struct{}, release <-chan struct{}) {
	t.Helper()
	db.Callback().Create().Before("gorm:create").Register("test:dek_gate", func(tx *gorm.DB) {
		if tx.Statement.Table != "user_dek" {
			return
		}
		arrived <- struct{}{}
		<-release
	})
	t.Cleanup(func() {
		db.Callback().Create().Remove("test:dek_gate")
	})
}

// raceInsertUserDEKEnvelopes runs the same envelope upload through two
// independent Accounts instances (each with its own write mutex, so nothing
// serializes the racers before the database) and releases them at the same
// instant through the gate above. The unique constraint on user_id alone must
// decide the loser with ErrDEKEnvelopesExist — exactly one success and one
// conflict, never two successes and never a RowsAffected/clientFoundRows
// dependent outcome.
func raceInsertUserDEKEnvelopes(t *testing.T, first, second *Accounts, userID string, firstEnvelope, secondEnvelope *vault.UserDEKEnvelopes) (successes, conflicts int) {
	t.Helper()
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	gateBeforeCreate(t, first.db, arrived, release)

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	racers := []struct {
		accounts  *Accounts
		envelopes *vault.UserDEKEnvelopes
	}{{first, firstEnvelope}, {second, secondEnvelope}}
	for _, racer := range racers {
		wg.Add(1)
		go func(accounts *Accounts, envelopes *vault.UserDEKEnvelopes) {
			defer wg.Done()
			<-start
			results <- accounts.InsertUserDEKEnvelopes(context.Background(), userID, envelopes)
		}(racer.accounts, racer.envelopes)
	}
	close(start)
	for range 2 {
		<-arrived
	}
	close(release)
	wg.Wait()
	close(results)
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrDEKEnvelopesExist) {
			conflicts++
		} else {
			t.Fatalf("unexpected insert error: %v", err)
		}
	}
	return successes, conflicts
}

// Two concurrent first uploads with identical content through independent
// connections: exactly one success and one ErrDEKEnvelopesExist, never two
// successes, regardless of driver RowsAffected/clientFoundRows semantics.
func TestInsertUserDEKEnvelopesConcurrentIdenticalBarrier(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	first := New(database.GORM())
	second := New(database.GORM())
	ctx := context.Background()
	user, err := first.CreateUser(ctx, "race-identical", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	_, envelopes, _, err := vault.GenerateUserDEKEnvelopes("synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}

	successes, conflicts := raceInsertUserDEKEnvelopes(t, first, second, user.ID, envelopes, envelopes)
	if successes != 1 || conflicts != 1 {
		t.Fatalf("identical concurrent upload: successes = %d, conflicts = %d, want exactly 1 and 1", successes, conflicts)
	}
	stored, err := first.GetUserDEKEnvelopes(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored.DEKEnvelope, envelopes.DEKEnvelope) ||
		!bytes.Equal(stored.KDFSalt, envelopes.KDFSalt) ||
		stored.KDFParams != envelopes.KDFParams ||
		!bytes.Equal(stored.RecoveryEnvelope, envelopes.RecoveryEnvelope) ||
		stored.RecoveryHash != envelopes.RecoveryHash {
		t.Fatal("stored envelopes mismatch: the winner's content changed")
	}
	if epoch := first.Epoch(user.ID); epoch != 0 {
		t.Fatalf("epoch moved to %d, want 0 (no revocation)", epoch)
	}
}

// Two concurrent first uploads with different content through independent
// connections: exactly one success and one conflict, and the stored row is one
// complete submitted set, never a mix.
func TestInsertUserDEKEnvelopesConcurrentDifferentBarrier(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	first := New(database.GORM())
	second := New(database.GORM())
	ctx := context.Background()
	user, err := first.CreateUser(ctx, "race-different", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	_, firstEnvelope, _, err := vault.GenerateUserDEKEnvelopes("synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	_, secondEnvelope, _, err := vault.GenerateUserDEKEnvelopes("synthetic-password-2")
	if err != nil {
		t.Fatal(err)
	}

	successes, conflicts := raceInsertUserDEKEnvelopes(t, first, second, user.ID, firstEnvelope, secondEnvelope)
	if successes != 1 || conflicts != 1 {
		t.Fatalf("different concurrent upload: successes = %d, conflicts = %d, want exactly 1 and 1", successes, conflicts)
	}
	stored, err := first.GetUserDEKEnvelopes(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	matchesFirst := bytes.Equal(stored.DEKEnvelope, firstEnvelope.DEKEnvelope) &&
		bytes.Equal(stored.KDFSalt, firstEnvelope.KDFSalt) &&
		stored.KDFParams == firstEnvelope.KDFParams &&
		bytes.Equal(stored.RecoveryEnvelope, firstEnvelope.RecoveryEnvelope) &&
		stored.RecoveryHash == firstEnvelope.RecoveryHash
	matchesSecond := bytes.Equal(stored.DEKEnvelope, secondEnvelope.DEKEnvelope) &&
		bytes.Equal(stored.KDFSalt, secondEnvelope.KDFSalt) &&
		stored.KDFParams == secondEnvelope.KDFParams &&
		bytes.Equal(stored.RecoveryEnvelope, secondEnvelope.RecoveryEnvelope) &&
		stored.RecoveryHash == secondEnvelope.RecoveryHash
	if !matchesFirst && !matchesSecond {
		t.Fatal("stored envelopes are a mix of the two submitted sets, not one complete winner")
	}
	if epoch := first.Epoch(user.ID); epoch != 0 {
		t.Fatalf("epoch moved to %d, want 0 (no revocation)", epoch)
	}
}
