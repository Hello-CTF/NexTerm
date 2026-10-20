package server

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/execbatch"
)

func TestAccountLifecycleLogoutAllPreTransactionAdmission(t *testing.T) {
	oldTransport := newAccountRevalidationTransport()
	newTransport := newAccountRevalidationTransport()
	fixture := newAccountAtomicFixture(t, oldTransport, newTransport)
	s1, _ := fixture.initSuperadmin(t, "alice", "password-alice")
	s2 := fixture.login(t, "alice", "password-alice")
	s2ID := currentAccountSessionID(t, fixture, s2)
	oldBatchDone, _ := startAccountAtomicBatch(t, fixture, s2, "precommit-old", "old-target", oldTransport)
	originalRevoke := fixture.server.revokeUserSessions
	revokeStarted := make(chan struct{})
	allowRevoke := make(chan struct{})
	var startOnce, allowOnce sync.Once
	allow := func() { allowOnce.Do(func() { close(allowRevoke) }) }
	t.Cleanup(allow)
	fixture.server.revokeUserSessions = func(ctx context.Context, ownerID, exceptSessionID string, beforeCommit ...func() error) ([]string, error) {
		startOnce.Do(func() { close(revokeStarted) })
		<-allowRevoke
		return originalRevoke(ctx, ownerID, exceptSessionID, beforeCommit...)
	}
	// S3 exists before logout-all starts: with issuance serialized under the
	// revocation lock, a session can no longer be created inside the window.
	s3 := fixture.login(t, "alice", "password-alice")
	s3ID := currentAccountSessionID(t, fixture, s3)
	logoutDone := make(chan accountCall, 1)
	go func() {
		logoutDone <- fixture.call(t, http.MethodPost, "/auth/logout-all", nil, s1, s1.csrf, nil)
	}()
	select {
	case <-revokeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("logout-all did not reach the pre-transaction barrier")
	}

	admissionAttempts := make(chan string, 2)
	originalAdmission := fixture.server.accountExecutionAdmission
	fixture.server.accountExecutionAdmission = func(ctx context.Context, token string, identity *account.Identity, fn func() error) error {
		admissionAttempts <- identity.SessionID
		return originalAdmission(ctx, token, identity, fn)
	}
	startExecute := func(session *accountTestSession, batchID string) chan accountCall {
		done := make(chan accountCall, 1)
		go func() {
			done <- fixture.call(t, http.MethodPost, "/rpc", map[string]any{
				"cmd":  "exec_batch_execute",
				"args": execbatch.Request{BatchID: batchID, SessionIDs: []string{"new-target"}, Command: "wait"},
			}, session, session.csrf, nil)
		}()
		return done
	}
	doneS3 := startExecute(s3, "precommit-s3")
	doneS2 := startExecute(s2, "precommit-old-request")
	seen := make(map[string]bool, 2)
	for range 2 {
		select {
		case sessionID := <-admissionAttempts:
			seen[sessionID] = true
		case <-time.After(2 * time.Second):
			t.Fatal("Execute did not reach the pre-transaction admission barrier")
		}
	}
	if !seen[s2ID] || !seen[s3ID] {
		t.Fatalf("admission sessions = %v, want old S2 and new S3", seen)
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
	for name, done := range map[string]chan accountCall{"S3": doneS3, "old request": doneS2} {
		select {
		case call := <-done:
			if call.status != http.StatusOK || call.body["ok"] != false {
				t.Fatalf("%s Execute status = %d body = %v", name, call.status, call.body)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s Execute did not finish after revocation", name)
		}
	}
	select {
	case <-newTransport.started:
		t.Fatal("a revoked session reached SSH after the pre-transaction barrier")
	default:
	}
	if call := fixture.call(t, http.MethodGet, "/auth/me", nil, s3, "", nil); call.status != http.StatusUnauthorized {
		t.Fatalf("S3 was not included in the revoked set: status = %d", call.status)
	}

	s4 := fixture.login(t, "alice", "password-alice")
	newBatchDone, newExecCtx := startAccountAtomicBatch(t, fixture, s4, "precommit-new-era", "new-target", newTransport)
	if newExecCtx.Err() != nil {
		t.Fatal("post-revocation session inherited the old cleanup event")
	}
	if call := fixture.call(t, http.MethodPost, "/auth/logout", nil, s4, s4.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("S4 logout status = %d body = %v", call.status, call.body)
	}
	requireAccountAtomicBatch(t, newBatchDone, execbatch.StatusCanceled)
}
