package server

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type accountAtomicGate struct {
	bySession map[*accountTestSession]string
	requests  map[string]chan accountCall
	releases  map[string]func()
}

func newAccountAtomicGate(t *testing.T, fixture *accountFixture, command string, sessions ...*accountTestSession) *accountAtomicGate {
	t.Helper()
	gate := &accountAtomicGate{
		bySession: make(map[*accountTestSession]string, len(sessions)),
		requests:  make(map[string]chan accountCall, len(sessions)),
		releases:  make(map[string]func(), len(sessions)),
	}
	gates := make(map[string]chan struct{}, len(sessions))
	for _, session := range sessions {
		sessionID := currentAccountSessionID(t, fixture, session)
		gate.bySession[session] = sessionID
		gates[sessionID] = make(chan struct{})
		gate.requests[sessionID] = make(chan accountCall, 1)
	}
	for sessionID, channel := range gates {
		sessionID, channel := sessionID, channel
		var once sync.Once
		gate.releases[sessionID] = func() { once.Do(func() { close(channel) }) }
	}
	t.Cleanup(func() {
		for _, release := range gate.releases {
			release()
		}
	})
	entered := make(chan string, len(sessions))
	if err := fixture.server.dispatcher.RegisterRaw(command, func(ctx context.Context, _ *ipc.Call) (any, error) {
		sessionID := ipc.AccountSessionID(ctx)
		channel, ok := gates[sessionID]
		if !ok {
			return nil, ipc.NewError(ipc.CodeBadParam, "unexpected account session")
		}
		entered <- sessionID
		<-channel
		return nil, ipc.CheckAccountSession(ctx)
	}); err != nil {
		t.Fatal(err)
	}
	for _, session := range sessions {
		session := session
		go func() {
			gate.requests[gate.bySession[session]] <- fixture.call(t, http.MethodPost, "/rpc", map[string]any{
				"cmd": command, "args": map[string]any{},
			}, session, session.csrf, nil)
		}()
	}
	seen := make(map[string]bool, len(sessions))
	for range len(sessions) {
		select {
		case sessionID := <-entered:
			seen[sessionID] = true
		case <-time.After(2 * time.Second):
			t.Fatal("old requests were not admitted")
		}
	}
	for _, sessionID := range gate.bySession {
		if !seen[sessionID] {
			t.Fatalf("session %s was not admitted", sessionID)
		}
	}
	return gate
}

func (g *accountAtomicGate) release(session *accountTestSession) {
	g.releases[g.bySession[session]]()
}

func (g *accountAtomicGate) wait(t *testing.T, session *accountTestSession) accountCall {
	t.Helper()
	select {
	case call := <-g.requests[g.bySession[session]]:
		if call.status != http.StatusOK || call.body["ok"] != false {
			t.Fatalf("old request status = %d body = %v", call.status, call.body)
		}
		return call
	case <-time.After(2 * time.Second):
		t.Fatal("old request did not finish revalidation")
		return accountCall{}
	}
}

func newAccountAtomicFixture(t *testing.T, oldTransport, newTransport *accountRevalidationTransport) *accountFixture {
	t.Helper()
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
	return newAccountExecLifecycleFixture(t, batches)
}

func startAccountAtomicBatch(t *testing.T, fixture *accountFixture, session *accountTestSession, batchID, targetID string, transport *accountRevalidationTransport) (chan accountCall, context.Context) {
	t.Helper()
	done := make(chan accountCall, 1)
	go func() {
		done <- fixture.call(t, http.MethodPost, "/rpc", map[string]any{
			"cmd":  "exec_batch_execute",
			"args": execbatch.Request{BatchID: batchID, SessionIDs: []string{targetID}, Command: "wait"},
		}, session, session.csrf, nil)
	}()
	select {
	case execCtx := <-transport.started:
		return done, execCtx
	case <-time.After(2 * time.Second):
		t.Fatal("batch did not start")
		return nil, nil
	}
}

func requireAccountAtomicBatch(t *testing.T, done chan accountCall, want execbatch.Status) {
	t.Helper()
	select {
	case call := <-done:
		batch := decodeAccountExecBatch(t, call)
		if len(batch.Results) != 1 || batch.Results[0].Status != want {
			t.Fatalf("batch results = %+v, want %s", batch.Results, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("batch did not finish")
	}
}

func TestAccountLifecycleLogoutAllCommitWindowKeepsUnifiedEvent(t *testing.T) {
	oldTransport := newAccountRevalidationTransport()
	newTransport := newAccountRevalidationTransport()
	fixture := newAccountAtomicFixture(t, oldTransport, newTransport)
	s1, _ := fixture.initSuperadmin(t, "alice", "password-alice")
	s2 := fixture.login(t, "alice", "password-alice")
	oldBatchDone, oldExecCtx := startAccountAtomicBatch(t, fixture, s2, "atomic-old", "old-target", oldTransport)
	gate := newAccountAtomicGate(t, fixture, "test_atomic_logout_all_window", s1, s2)
	originalRevoke := fixture.server.revokeUserSessions
	committed := make(chan struct{})
	allowCommit := make(chan struct{})
	var committedOnce, allowOnce sync.Once
	allow := func() { allowOnce.Do(func() { close(allowCommit) }) }
	t.Cleanup(allow)
	fixture.server.revokeUserSessions = func(ctx context.Context, ownerID, exceptSessionID string, beforeCommit ...func() error) ([]string, error) {
		ids, err := originalRevoke(ctx, ownerID, exceptSessionID, beforeCommit...)
		if err == nil {
			committedOnce.Do(func() { close(committed) })
			<-allowCommit
		}
		return ids, err
	}
	logoutDone := make(chan accountCall, 1)
	go func() {
		logoutDone <- fixture.call(t, http.MethodPost, "/auth/logout-all", nil, s1, s1.csrf, nil)
	}()
	select {
	case <-committed:
	case <-time.After(2 * time.Second):
		t.Fatal("logout-all did not commit the account transaction")
	}
	if oldExecCtx.Err() != nil {
		t.Fatal("prepared cleanup canceled the old batch before commit")
	}

	s3 := fixture.login(t, "alice", "password-alice")
	s3ID := currentAccountSessionID(t, fixture, s3)
	admissionStarted := make(chan struct{}, 1)
	originalAdmission := fixture.server.accountExecutionAdmission
	fixture.server.accountExecutionAdmission = func(ctx context.Context, token string, identity *account.Identity, fn func() error) error {
		if identity.SessionID == s3ID {
			select {
			case admissionStarted <- struct{}{}:
			default:
			}
		}
		return originalAdmission(ctx, token, identity, fn)
	}
	newBatchDone := make(chan accountCall, 1)
	go func() {
		newBatchDone <- fixture.call(t, http.MethodPost, "/rpc", map[string]any{
			"cmd":  "exec_batch_execute",
			"args": execbatch.Request{BatchID: "atomic-new-in-window", SessionIDs: []string{"new-target"}, Command: "wait"},
		}, s3, s3.csrf, nil)
	}()
	select {
	case <-admissionStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("S3 did not reach the execution admission barrier")
	}
	gate.release(s2)
	gate.release(s1)
	for _, session := range []*accountTestSession{s2, s1} {
		select {
		case call := <-gate.requests[gate.bySession[session]]:
			t.Fatalf("old revalidation returned before alias commit: %+v", call)
		case <-time.After(25 * time.Millisecond):
		}
	}
	allow()
	select {
	case call := <-logoutDone:
		if call.status != http.StatusOK {
			t.Fatalf("logout-all status = %d body = %v", call.status, call.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("logout-all did not finish")
	}
	requireAccountAtomicBatch(t, oldBatchDone, execbatch.StatusCanceled)
	gate.wait(t, s2)
	gate.wait(t, s1)
	var newExecCtx context.Context
	select {
	case newExecCtx = <-newTransport.started:
	case <-time.After(2 * time.Second):
		t.Fatal("S3 batch did not start after the commit barrier")
	}
	if newExecCtx.Err() != nil {
		t.Fatal("S3 batch created in the commit window was canceled by the old event")
	}
	if call := fixture.call(t, http.MethodPost, "/auth/logout", nil, s3, s3.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("S3 logout status = %d body = %v", call.status, call.body)
	}
	requireAccountAtomicBatch(t, newBatchDone, execbatch.StatusCanceled)
}

func TestAccountLifecycleLogoutAllIncludesExpiredSession(t *testing.T) {
	oldTransport := newAccountRevalidationTransport()
	newTransport := newAccountRevalidationTransport()
	fixture := newAccountAtomicFixture(t, oldTransport, newTransport)
	s1, _ := fixture.initSuperadmin(t, "alice", "password-alice")
	s2 := fixture.login(t, "alice", "password-alice")
	gate := newAccountAtomicGate(t, fixture, "test_atomic_expired_session", s2)
	if _, err := fixture.db.Exec("UPDATE user_session SET expires_at = 0 WHERE id = ?", gate.bySession[s2]); err != nil {
		t.Fatal(err)
	}
	call := fixture.call(t, http.MethodPost, "/auth/logout-all", nil, s1, s1.csrf, nil)
	if call.status != http.StatusOK || call.body["revoked"] != float64(2) {
		t.Fatalf("logout-all with expired session = %d %v", call.status, call.body)
	}
	s3 := fixture.login(t, "alice", "password-alice")
	newBatchDone, newExecCtx := startAccountAtomicBatch(t, fixture, s3, "atomic-expired-new", "new-target", newTransport)
	gate.release(s2)
	gate.wait(t, s2)
	if newExecCtx.Err() != nil {
		t.Fatal("expired old session revalidation canceled the S3 batch")
	}
	if call := fixture.call(t, http.MethodPost, "/auth/logout", nil, s3, s3.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("S3 logout status = %d body = %v", call.status, call.body)
	}
	requireAccountAtomicBatch(t, newBatchDone, execbatch.StatusCanceled)
}

func TestAccountLifecycleLogoutAllFailureRollsBackPreparedEvent(t *testing.T) {
	oldTransport := newAccountRevalidationTransport()
	newTransport := newAccountRevalidationTransport()
	fixture := newAccountAtomicFixture(t, oldTransport, newTransport)
	s1, _ := fixture.initSuperadmin(t, "alice", "password-alice")
	s2 := fixture.login(t, "alice", "password-alice")
	oldBatchDone, oldExecCtx := startAccountAtomicBatch(t, fixture, s2, "atomic-rollback-old", "old-target", oldTransport)
	originalRevoke := fixture.server.revokeUserSessions
	fixture.server.revokeUserSessions = func(_ context.Context, _, _ string, beforeCommit ...func() error) ([]string, error) {
		for _, prepare := range beforeCommit {
			if prepare != nil {
				if err := prepare(); err != nil {
					return nil, err
				}
			}
		}
		return nil, errors.New("injected revoke failure")
	}
	if call := fixture.call(t, http.MethodPost, "/auth/logout-all", nil, s1, s1.csrf, nil); call.status != http.StatusInternalServerError {
		t.Fatalf("failed logout-all status = %d body = %v", call.status, call.body)
	}
	if oldExecCtx.Err() != nil {
		t.Fatal("failed revocation canceled the old batch")
	}
	fixture.server.revokeUserSessions = originalRevoke
	if call := fixture.call(t, http.MethodPost, "/auth/logout-all", nil, s1, s1.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("logout-all retry status = %d body = %v", call.status, call.body)
	}
	requireAccountAtomicBatch(t, oldBatchDone, execbatch.StatusCanceled)
}
