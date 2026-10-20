package account

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// raceInsertUserDEKEnvelopes releases two racers at the same instant; the
// account write mutex serializes their INSERTs, and the unique constraint on
// user_id decides the loser with ErrDEKEnvelopesExist — exactly one success
// and one conflict regardless of driver RowsAffected/clientFoundRows
// semantics.
func raceInsertUserDEKEnvelopes(t *testing.T, a *Accounts, userID string, first, second *vault.UserDEKEnvelopes) (successes, conflicts int) {
	t.Helper()
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, envelopes := range []*vault.UserDEKEnvelopes{first, second} {
		wg.Add(1)
		go func(envelopes *vault.UserDEKEnvelopes) {
			defer wg.Done()
			<-start
			results <- a.InsertUserDEKEnvelopes(context.Background(), userID, envelopes)
		}(envelopes)
	}
	close(start)
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

// Two concurrent first uploads with identical content: the unique constraint
// on user_id decides the winner — exactly one success and one
// ErrDEKEnvelopesExist, never two successes, regardless of driver
// RowsAffected/clientFoundRows semantics.
func TestInsertUserDEKEnvelopesConcurrentIdenticalBarrier(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	a := New(database.GORM())
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "race-identical", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	_, envelopes, _, err := vault.GenerateUserDEKEnvelopes("synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}

	successes, conflicts := raceInsertUserDEKEnvelopes(t, a, user.ID, envelopes, envelopes)
	if successes != 1 || conflicts != 1 {
		t.Fatalf("identical concurrent upload: successes = %d, conflicts = %d, want exactly 1 and 1", successes, conflicts)
	}
	stored, err := a.GetUserDEKEnvelopes(ctx, user.ID)
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
	if epoch := a.Epoch(user.ID); epoch != 0 {
		t.Fatalf("epoch moved to %d, want 0 (no revocation)", epoch)
	}
}

// Two concurrent first uploads with different content: exactly one success and
// one conflict, and the stored row is one complete submitted set, never a mix.
func TestInsertUserDEKEnvelopesConcurrentDifferentBarrier(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	a := New(database.GORM())
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "race-different", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	_, first, _, err := vault.GenerateUserDEKEnvelopes("synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	_, second, _, err := vault.GenerateUserDEKEnvelopes("synthetic-password-2")
	if err != nil {
		t.Fatal(err)
	}

	successes, conflicts := raceInsertUserDEKEnvelopes(t, a, user.ID, first, second)
	if successes != 1 || conflicts != 1 {
		t.Fatalf("different concurrent upload: successes = %d, conflicts = %d, want exactly 1 and 1", successes, conflicts)
	}
	stored, err := a.GetUserDEKEnvelopes(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	matchesFirst := bytes.Equal(stored.DEKEnvelope, first.DEKEnvelope) &&
		bytes.Equal(stored.KDFSalt, first.KDFSalt) &&
		stored.KDFParams == first.KDFParams &&
		bytes.Equal(stored.RecoveryEnvelope, first.RecoveryEnvelope) &&
		stored.RecoveryHash == first.RecoveryHash
	matchesSecond := bytes.Equal(stored.DEKEnvelope, second.DEKEnvelope) &&
		bytes.Equal(stored.KDFSalt, second.KDFSalt) &&
		stored.KDFParams == second.KDFParams &&
		bytes.Equal(stored.RecoveryEnvelope, second.RecoveryEnvelope) &&
		stored.RecoveryHash == second.RecoveryHash
	if !matchesFirst && !matchesSecond {
		t.Fatal("stored envelopes are a mix of the two submitted sets, not one complete winner")
	}
	if epoch := a.Epoch(user.ID); epoch != 0 {
		t.Fatalf("epoch moved to %d, want 0 (no revocation)", epoch)
	}
}
