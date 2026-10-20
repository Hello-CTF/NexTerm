package server

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestAccountLifecycleLogoutAllSharesCleanupAcrossRevokedSessions(t *testing.T) {
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
	s1, _ := fixture.initSuperadmin(t, "alice", "password-alice")
	s2 := fixture.login(t, "alice", "password-alice")
	s1ID := currentAccountSessionID(t, fixture, s1)
	s2ID := currentAccountSessionID(t, fixture, s2)

	oldBatchDone := make(chan accountCall, 1)
	go func() {
		oldBatchDone <- fixture.call(t, http.MethodPost, "/rpc", map[string]any{
			"cmd":  "exec_batch_execute",
			"args": execbatch.Request{BatchID: "before-logout-all", SessionIDs: []string{"old-target"}, Command: "wait"},
		}, s2, s2.csrf, nil)
	}()
	var oldExecCtx context.Context
	select {
	case oldExecCtx = <-oldTransport.started:
	case <-time.After(2 * time.Second):
		t.Fatal("old batch did not start")
	}

	gates := map[string]chan struct{}{s1ID: make(chan struct{}), s2ID: make(chan struct{})}
	releases := make(map[string]func(), len(gates))
	for sessionID, gate := range gates {
		sessionID, gate := sessionID, gate
		var once sync.Once
		releases[sessionID] = func() { once.Do(func() { close(gate) }) }
	}
	t.Cleanup(func() {
		releases[s1ID]()
		releases[s2ID]()
	})
	entered := make(chan string, 2)
	if err := fixture.server.dispatcher.RegisterRaw("test_logout_all_recheck", func(ctx context.Context, _ *ipc.Call) (any, error) {
		sessionID := ipc.AccountSessionID(ctx)
		gate, ok := gates[sessionID]
		if !ok {
			return nil, ipc.NewError(ipc.CodeBadParam, "unexpected account session")
		}
		entered <- sessionID
		<-gate
		return nil, ipc.CheckAccountSession(ctx)
	}); err != nil {
		t.Fatal(err)
	}
	oldRequests := make(map[string]chan accountCall, 2)
	for _, session := range []*accountTestSession{s1, s2} {
		session := session
		done := make(chan accountCall, 1)
		oldRequests[currentAccountSessionID(t, fixture, session)] = done
		go func() {
			done <- fixture.call(t, http.MethodPost, "/rpc", map[string]any{
				"cmd": "test_logout_all_recheck", "args": map[string]any{},
			}, session, session.csrf, nil)
		}()
	}
	seen := make(map[string]bool, 2)
	for range 2 {
		select {
		case sessionID := <-entered:
			seen[sessionID] = true
		case <-time.After(2 * time.Second):
			t.Fatal("old sessions were not admitted")
		}
	}
	if !seen[s1ID] || !seen[s2ID] {
		t.Fatalf("admitted sessions = %v, want S1 and S2", seen)
	}

	if call := fixture.call(t, http.MethodPost, "/auth/logout-all", nil, s1, s1.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("logout-all status = %d body = %v", call.status, call.body)
	}
	select {
	case call := <-oldBatchDone:
		batch := decodeAccountExecBatch(t, call)
		if len(batch.Results) != 1 || batch.Results[0].Status != execbatch.StatusCanceled {
			t.Fatalf("logout-all did not cancel the old batch: %+v", batch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old batch did not finish after logout-all")
	}
	if oldExecCtx.Err() == nil {
		t.Fatal("logout-all did not cancel the old Exec context")
	}

	s3 := fixture.login(t, "alice", "password-alice")
	newBatchDone := make(chan accountCall, 1)
	go func() {
		newBatchDone <- fixture.call(t, http.MethodPost, "/rpc", map[string]any{
			"cmd":  "exec_batch_execute",
			"args": execbatch.Request{BatchID: "after-logout-all", SessionIDs: []string{"new-target"}, Command: "wait"},
		}, s3, s3.csrf, nil)
	}()
	var newExecCtx context.Context
	select {
	case newExecCtx = <-newTransport.started:
	case <-time.After(2 * time.Second):
		t.Fatal("S3 batch did not start")
	}

	for _, sessionID := range []string{s2ID, s1ID} {
		releases[sessionID]()
		select {
		case call := <-oldRequests[sessionID]:
			if call.status != http.StatusOK || call.body["ok"] != false {
				t.Fatalf("old session %s status = %d body = %v", sessionID, call.status, call.body)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("old session %s did not finish revalidation", sessionID)
		}
		if newExecCtx.Err() != nil {
			t.Fatalf("late revalidation of old session %s canceled the S3 batch", sessionID)
		}
	}

	if call := fixture.call(t, http.MethodPost, "/auth/logout", nil, s3, s3.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("S3 logout status = %d body = %v", call.status, call.body)
	}
	select {
	case call := <-newBatchDone:
		batch := decodeAccountExecBatch(t, call)
		if len(batch.Results) != 1 || batch.Results[0].Status != execbatch.StatusCanceled {
			t.Fatalf("S3 cleanup did not cancel its batch: %+v", batch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("S3 batch did not finish after its own logout")
	}
}
