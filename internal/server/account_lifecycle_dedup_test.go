package server

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type accountRevalidationTransport struct {
	started chan context.Context
	alive   atomic.Bool
}

func newAccountRevalidationTransport() *accountRevalidationTransport {
	transport := &accountRevalidationTransport{started: make(chan context.Context, 1)}
	transport.alive.Store(true)
	return transport
}

func (t *accountRevalidationTransport) Kind() string       { return "ssh" }
func (t *accountRevalidationTransport) Generation() uint64 { return 1 }
func (t *accountRevalidationTransport) IsAlive() bool      { return t.alive.Load() }
func (t *accountRevalidationTransport) Close() error {
	t.alive.Store(false)
	return nil
}
func (t *accountRevalidationTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}
func (t *accountRevalidationTransport) Exec(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
	t.started <- ctx
	<-ctx.Done()
	return base.ExecResult{}, ctx.Err()
}

func TestAccountLifecycleConcurrentLateRevalidationDoesNotCancelNewBatch(t *testing.T) {
	oldTransport := newAccountRevalidationTransport()
	newTransport := newAccountRevalidationTransport()
	targets := map[string]execbatch.Target{
		"old-target": {SessionID: "old-target", Transport: oldTransport, Generation: oldTransport.Generation()},
		"new-target": {SessionID: "new-target", Transport: newTransport, Generation: newTransport.Generation()},
	}
	batches := execbatch.New(execbatch.Config{Resolver: execbatch.ResolverFunc(func(_ context.Context, sessionID string) (execbatch.Target, error) {
		target, ok := targets[sessionID]
		if !ok {
			return execbatch.Target{}, execbatch.ErrDisconnected
		}
		return target, nil
	})})
	fixture := newAccountExecLifecycleFixture(t, batches)
	alice, _ := fixture.initSuperadmin(t, "alice", "password-alice")
	alicePeer := fixture.login(t, "alice", "password-alice")

	execute := func(session *accountTestSession, request execbatch.Request) chan accountCall {
		done := make(chan accountCall, 1)
		go func() {
			done <- fixture.call(t, http.MethodPost, "/rpc", map[string]any{
				"cmd": "exec_batch_execute", "args": request,
			}, session, session.csrf, nil)
		}()
		return done
	}
	oldBatchDone := execute(alicePeer, execbatch.Request{
		BatchID: "before-old-session-logout", SessionIDs: []string{"old-target"}, Command: "wait",
	})
	var oldExecCtx context.Context
	select {
	case oldExecCtx = <-oldTransport.started:
	case <-time.After(2 * time.Second):
		t.Fatal("old batch did not start")
	}

	gates := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var gateOnce [2]sync.Once
	releaseGate := func(index int) {
		gateOnce[index].Do(func() { close(gates[index]) })
	}
	t.Cleanup(func() {
		releaseGate(0)
		releaseGate(1)
	})
	entered := make(chan int, 2)
	var requestIndex atomic.Int32
	if err := fixture.server.dispatcher.RegisterRaw("test_concurrent_account_recheck", func(ctx context.Context, _ *ipc.Call) (any, error) {
		index := int(requestIndex.Add(1)) - 1
		entered <- index
		<-gates[index]
		return nil, ipc.CheckAccountSession(ctx)
	}); err != nil {
		t.Fatal(err)
	}
	oldRequests := []chan accountCall{make(chan accountCall, 1), make(chan accountCall, 1)}
	for index := range oldRequests {
		index := index
		go func() {
			oldRequests[index] <- fixture.call(t, http.MethodPost, "/rpc", map[string]any{
				"cmd": "test_concurrent_account_recheck", "args": map[string]any{},
			}, alice, alice.csrf, nil)
		}()
	}
	for range 2 {
		select {
		case index := <-entered:
			if index < 0 || index >= len(gates) {
				t.Fatalf("unexpected revalidation index %d", index)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("old requests were not admitted")
		}
	}

	if call := fixture.call(t, http.MethodPost, "/auth/logout", nil, alice, alice.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("logout status = %d body = %v", call.status, call.body)
	}
	select {
	case call := <-oldBatchDone:
		batch := decodeAccountExecBatch(t, call)
		if len(batch.Results) != 1 || batch.Results[0].Status != execbatch.StatusCanceled {
			t.Fatalf("first cleanup did not cancel the old batch: %+v", batch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old batch did not finish after logout")
	}
	if oldExecCtx.Err() == nil {
		t.Fatal("first cleanup did not cancel the old Exec context")
	}

	fresh := fixture.login(t, "alice", "password-alice")
	newBatchDone := execute(fresh, execbatch.Request{
		BatchID: "after-old-session-logout", SessionIDs: []string{"new-target"}, Command: "wait",
	})
	var newExecCtx context.Context
	select {
	case newExecCtx = <-newTransport.started:
	case <-time.After(2 * time.Second):
		t.Fatal("new batch did not start after the first cleanup")
	}
	releaseGate(0)
	releaseGate(1)
	for index, done := range oldRequests {
		select {
		case call := <-done:
			if call.status != http.StatusOK || call.body["ok"] != false {
				t.Fatalf("old request %d status = %d body = %v", index, call.status, call.body)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("old request %d did not finish revalidation", index)
		}
	}
	if newExecCtx.Err() != nil {
		t.Fatal("late revalidation of the old session canceled the new batch")
	}

	if call := fixture.call(t, http.MethodPost, "/auth/logout", nil, fresh, fresh.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("fresh session logout status = %d body = %v", call.status, call.body)
	}
	select {
	case call := <-newBatchDone:
		batch := decodeAccountExecBatch(t, call)
		if len(batch.Results) != 1 || batch.Results[0].Status != execbatch.StatusCanceled {
			t.Fatalf("fresh session cleanup did not cancel its new batch: %+v", batch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("new batch did not finish after the fresh session logout")
	}
}
