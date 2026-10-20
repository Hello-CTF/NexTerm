package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

type layoutCASReadBarrierKey struct{}

type layoutCASReadBarrier struct {
	arrived chan struct{}
	release chan struct{}
}

type layoutCASResult struct {
	writer   int
	saved    bool
	revision int64
	err      error
}

func raceLayoutCASPair(t *testing.T, db *store.Store, expected int64, owners, payloads [2]string) [2]layoutCASResult {
	t.Helper()
	barrier := &layoutCASReadBarrier{arrived: make(chan struct{}, len(owners)), release: make(chan struct{})}
	const callbackName = "test:layout_cas_read_barrier"
	if err := db.GORM().Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Context.Value(layoutCASReadBarrierKey{}) == barrier {
			barrier.arrived <- struct{}{}
			<-barrier.release
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.GORM().Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove layout read barrier: %v", err)
		}
	}()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(barrier.release) }) }
	defer release()

	results := make(chan layoutCASResult, len(owners))
	for i := range owners {
		go func() {
			ctx := context.WithValue(context.Background(), layoutCASReadBarrierKey{}, barrier)
			ctx = ipc.WithUserID(ctx, owners[i])
			saved, revision, err := db.LayoutSave(ctx, expected, payloads[i])
			results <- layoutCASResult{writer: i, saved: saved, revision: revision, err: err}
		}()
	}

	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for range owners {
		select {
		case <-barrier.arrived:
		case result := <-results:
			t.Fatalf("layout save finished before the read barrier: %+v", result)
		case <-timer.C:
			t.Fatal("timed out waiting for both layout reads")
		}
	}
	release()

	var pair [2]layoutCASResult
	for range owners {
		select {
		case result := <-results:
			pair[result.writer] = result
		case <-timer.C:
			t.Fatal("timed out waiting for layout writes")
		}
	}
	return pair
}

func requireLayoutCASWinner(t *testing.T, results [2]layoutCASResult, revision int64) int {
	t.Helper()
	winner := -1
	for i, result := range results {
		if result.err != nil {
			t.Errorf("writer %d: %v", i, result.err)
			continue
		}
		if result.revision != revision {
			t.Errorf("writer %d revision=%d, want %d", i, result.revision, revision)
		}
		if result.saved {
			if winner >= 0 {
				t.Fatalf("both writers succeeded: %+v", results)
			}
			winner = i
		}
	}
	if winner < 0 {
		t.Fatalf("neither writer succeeded: %+v", results)
	}
	return winner
}

func TestLayoutCASConcurrentInsertDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	payloads := [2]string{`{"writer":"a"}`, `{"writer":"b"}`}
	results := raceLayoutCASPair(t, db, 0, [2]string{}, payloads)
	winner := requireLayoutCASWinner(t, results, 1)
	revision, _, data, err := db.LayoutLoad(context.Background())
	if err != nil || revision != 1 || string(data) != payloads[winner] {
		t.Fatalf("stored layout revision=%d data=%s err=%v, want revision 1 and winner %d", revision, data, err, winner)
	}
}

func TestLayoutCASConcurrentUpdateDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)
	if saved, revision, err := db.LayoutSave(ctx, 0, `{"writer":"seed"}`); err != nil || !saved || revision != 1 {
		t.Fatalf("seed layout saved=%v revision=%d err=%v", saved, revision, err)
	}
	payloads := [2]string{`{"writer":"a"}`, `{"writer":"b"}`}
	results := raceLayoutCASPair(t, db, 1, [2]string{}, payloads)
	winner := requireLayoutCASWinner(t, results, 2)
	revision, _, data, err := db.LayoutLoad(ctx)
	if err != nil || revision != 2 || string(data) != payloads[winner] {
		t.Fatalf("stored layout revision=%d data=%s err=%v, want revision 2 and winner %d", revision, data, err, winner)
	}
}

func TestLayoutCASOwnerIsolationAndInvalidJSONDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	owners := [2]string{"user-a", "user-b"}
	payloads := [2]string{`{"owner":"a"}`, `{"owner":"b"}`}
	results := raceLayoutCASPair(t, db, 0, owners, payloads)
	updatedAt := [2]int64{}
	for i, result := range results {
		if result.err != nil || !result.saved || result.revision != 1 {
			t.Errorf("owner %s first save saved=%v revision=%d err=%v", owners[i], result.saved, result.revision, result.err)
		}
		ctx := ipc.WithUserID(context.Background(), owners[i])
		revision, ts, data, err := db.LayoutLoad(ctx)
		if err != nil || revision != 1 || string(data) != payloads[i] {
			t.Errorf("owner %s layout revision=%d data=%s err=%v", owners[i], revision, data, err)
		}
		updatedAt[i] = ts
	}

	ctx := ipc.WithUserID(context.Background(), owners[0])
	saved, revision, err := db.LayoutSave(ctx, 1, `{"broken"`)
	if saved || revision != 0 {
		t.Errorf("invalid JSON save saved=%v revision=%d", saved, revision)
	}
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam {
		t.Errorf("invalid JSON error=%v, want %s", err, ipc.CodeBadParam)
	}
	for i, owner := range owners {
		ctx := ipc.WithUserID(context.Background(), owner)
		revision, ts, data, err := db.LayoutLoad(ctx)
		if err != nil || revision != 1 || ts != updatedAt[i] || string(data) != payloads[i] {
			t.Errorf("owner %s layout after invalid JSON revision=%d updatedAt=%d data=%s err=%v", owner, revision, ts, data, err)
		}
	}
}
