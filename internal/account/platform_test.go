package account

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"gorm.io/gorm"
)

type platformOwnerRaceBarrier struct {
	arrived chan struct{}
	release chan struct{}
}

type platformOwnerQueryState struct {
	barrier *platformOwnerRaceBarrier
	once    sync.Once
}

type platformOwnerQueryContextKey struct{}

func TestEnsurePlatformOwnerUniqueRaceDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	t.Logf("platform owner unique race backend: %s", database.Backend())
	accounts := New(database.GORM())

	const callers = 2
	barrier := &platformOwnerRaceBarrier{arrived: make(chan struct{}, callers), release: make(chan struct{})}
	const callbackName = "test:platform_owner_query_barrier"
	if err := database.GORM().Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		state, _ := tx.Statement.Context.Value(platformOwnerQueryContextKey{}).(*platformOwnerQueryState)
		if state == nil {
			return
		}
		state.once.Do(func() {
			state.barrier.arrived <- struct{}{}
			<-state.barrier.release
		})
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.GORM().Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove platform owner query barrier: %v", err)
		}
	}()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(barrier.release) }) }
	defer release()

	type outcome struct {
		owner *User
		err   error
	}
	results := make(chan outcome, callers)
	for range callers {
		state := &platformOwnerQueryState{barrier: barrier}
		ctx := context.WithValue(context.Background(), platformOwnerQueryContextKey{}, state)
		go func() {
			owner, err := accounts.EnsurePlatformOwner(ctx)
			results <- outcome{owner: owner, err: err}
		}()
	}

	arrivalTimeout := time.NewTimer(15 * time.Second)
	defer arrivalTimeout.Stop()
	for range callers {
		select {
		case <-barrier.arrived:
		case result := <-results:
			t.Fatalf("platform owner call finished before both initial reads: %+v", result)
		case <-arrivalTimeout.C:
			t.Fatal("timed out waiting for concurrent platform owner reads")
		}
	}
	release()

	resultTimeout := time.NewTimer(30 * time.Second)
	defer resultTimeout.Stop()
	ownerID := ""
	for range callers {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatalf("concurrent EnsurePlatformOwner: %v", result.err)
			}
			if result.owner == nil || result.owner.Username != PlatformOwnerUsername || result.owner.Role != RoleSuperadmin {
				t.Fatalf("unexpected platform owner: %+v", result.owner)
			}
			if ownerID == "" {
				ownerID = result.owner.ID
			} else if result.owner.ID != ownerID {
				t.Fatalf("concurrent calls returned different owners: %q and %q", ownerID, result.owner.ID)
			}
		case <-resultTimeout.C:
			t.Fatal("timed out waiting for platform owner creation")
		}
	}
	if count, err := accounts.CountUsers(context.Background()); err != nil || count != 1 {
		t.Fatalf("platform owner count = %d, err=%v, want exactly one", count, err)
	}
}
