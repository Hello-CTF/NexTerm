package execbatch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestOwnerCleanupAliasFailureRetriesSameEvent(t *testing.T) {
	started := make(chan context.Context, 1)
	transport := newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
		started <- ctx
		<-ctx.Done()
		return base.ExecResult{}, ctx.Err()
	})
	service := New(Config{Resolver: &fakeResolver{targets: map[string]Target{
		"old": targetFor("old", transport),
	}}})
	type outcome struct {
		batch Batch
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		batch, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "alias-failure-old", SessionIDs: []string{"old"}, Command: "wait"})
		done <- outcome{batch: batch, err: err}
	}()
	var execCtx context.Context
	select {
	case execCtx = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("old batch did not start")
	}
	service.beforeCleanupCommit = func(accountCleanupKey) error {
		return errors.New("injected alias registration failure")
	}
	commit, _ := service.PrepareOwnerCleanup(t.Context(), "owner-a", "logout-all:s1", nil)
	commit("session-s1", "session-s2")
	if execCtx.Err() != nil {
		t.Fatal("injected alias failure canceled the batch")
	}

	service.beforeCleanupCommit = nil
	service.CancelOwner(t.Context(), "owner-a", "logout-all:s1", nil, "session-s1", "session-s2")
	select {
	case result := <-done:
		if result.err != nil || len(result.batch.Results) != 1 || result.batch.Results[0].Status != StatusCanceled {
			t.Fatalf("same-event retry outcome = %+v, %v", result.batch, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("same-event retry did not cancel the old batch")
	}
}
