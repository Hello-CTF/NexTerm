package execbatch

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestCancelOwnerCancelsOnlyThatOwnersActiveBatches(t *testing.T) {
	started := map[string]chan context.Context{
		"a-one": make(chan context.Context, 1),
		"a-two": make(chan context.Context, 1),
		"b":     make(chan context.Context, 1),
		"open":  make(chan context.Context, 1),
	}
	resolver := &fakeResolver{targets: make(map[string]Target)}
	for id := range started {
		id := id
		resolver.targets[id] = targetFor(id, newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
			started[id] <- ctx
			<-ctx.Done()
			return base.ExecResult{}, ctx.Err()
		}))
	}
	service := New(Config{Resolver: resolver})
	type outcome struct {
		batch Batch
		err   error
	}
	done := map[string]chan outcome{
		"a-one": make(chan outcome, 1),
		"a-two": make(chan outcome, 1),
		"b":     make(chan outcome, 1),
		"open":  make(chan outcome, 1),
	}
	owners := map[string]string{"a-one": "owner-a", "a-two": "owner-a", "b": "owner-b", "open": ""}
	for id := range done {
		id := id
		go func() {
			batch, err := service.Execute(t.Context(), owners[id], Request{BatchID: id, SessionIDs: []string{id}, Command: "wait"})
			done[id] <- outcome{batch: batch, err: err}
		}()
	}
	observed := make(map[string]context.Context, len(started))
	for id, ch := range started {
		select {
		case observed[id] = <-ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s did not start", id)
		}
	}

	service.CancelOwner(t.Context(), "", "open-session")
	service.CancelOwner(t.Context(), "unknown", "unknown-session")
	for id, ctx := range observed {
		if ctx.Err() != nil {
			t.Fatalf("empty or unknown owner cleanup canceled %s", id)
		}
	}

	service.CancelOwner(t.Context(), "owner-a", "session-a")
	for _, id := range []string{"a-one", "a-two"} {
		select {
		case result := <-done[id]:
			if result.err != nil || len(result.batch.Results) != 1 || result.batch.Results[0].Status != StatusCanceled {
				t.Fatalf("%s outcome = %+v, %v", id, result.batch, result.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s did not finish after owner cleanup", id)
		}
	}
	for _, id := range []string{"b", "open"} {
		if observed[id].Err() != nil {
			t.Fatalf("owner A cleanup canceled %s", id)
		}
	}

	service.CancelOwner(t.Context(), "owner-b", "session-b")
	select {
	case result := <-done["b"]:
		if result.err != nil || result.batch.Results[0].Status != StatusCanceled {
			t.Fatalf("owner B outcome = %+v, %v", result.batch, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owner B batch did not finish")
	}
	if _, err := service.Cancel(t.Context(), "", "open"); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done["open"]:
		if result.err != nil || result.batch.Results[0].Status != StatusCanceled {
			t.Fatalf("open batch outcome = %+v, %v", result.batch, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("open batch did not finish")
	}
}

func TestCancelOwnerDeduplicatesConcurrentAndLateSessionCleanup(t *testing.T) {
	oldStarted := make(chan context.Context, 1)
	newStarted := make(chan context.Context, 1)
	allowOldReturn := make(chan struct{})
	resolver := &fakeResolver{targets: map[string]Target{
		"old": targetFor("old", newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
			oldStarted <- ctx
			<-ctx.Done()
			<-allowOldReturn
			return base.ExecResult{}, ctx.Err()
		})),
		"new": targetFor("new", newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
			newStarted <- ctx
			<-ctx.Done()
			return base.ExecResult{}, ctx.Err()
		})),
	}}
	service := New(Config{Resolver: resolver})
	type outcome struct {
		batch Batch
		err   error
	}
	oldDone := make(chan outcome, 1)
	go func() {
		batch, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "old-batch", SessionIDs: []string{"old"}, Command: "wait"})
		oldDone <- outcome{batch: batch, err: err}
	}()
	var oldCtx context.Context
	select {
	case oldCtx = <-oldStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("old batch did not start")
	}
	firstCleanup := make(chan struct{})
	go func() {
		service.CancelOwner(t.Context(), "owner-a", "logout-all:session-s1", "session-s1", "session-s2")
		close(firstCleanup)
	}()
	select {
	case <-oldCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("first cleanup did not cancel the old Exec")
	}

	newDone := make(chan outcome, 1)
	go func() {
		batch, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "new-batch", SessionIDs: []string{"new"}, Command: "wait"})
		newDone <- outcome{batch: batch, err: err}
	}()
	var newCtx context.Context
	select {
	case newCtx = <-newStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("new batch did not start after the first snapshot")
	}
	secondCalling := make(chan struct{})
	secondCleanup := make(chan struct{})
	go func() {
		close(secondCalling)
		service.CancelOwner(t.Context(), "owner-a", "session-s2")
		close(secondCleanup)
	}()
	<-secondCalling
	close(allowOldReturn)
	for name, done := range map[string]chan struct{}{"first": firstCleanup, "second": secondCleanup} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s cleanup did not finish", name)
		}
	}
	select {
	case result := <-oldDone:
		if result.err != nil || len(result.batch.Results) != 1 || result.batch.Results[0].Status != StatusCanceled {
			t.Fatalf("old batch outcome = %+v, %v", result.batch, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old batch did not finish")
	}

	service.CancelOwner(t.Context(), "owner-a", "session-s1")
	if newCtx.Err() != nil {
		t.Fatal("concurrent or late cleanup of the old session canceled the new batch")
	}
	service.CancelOwner(t.Context(), "owner-a", "session-new")
	select {
	case result := <-newDone:
		if result.err != nil || len(result.batch.Results) != 1 || result.batch.Results[0].Status != StatusCanceled {
			t.Fatalf("new session cleanup outcome = %+v, %v", result.batch, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("different session cleanup did not cancel the new batch")
	}
}
