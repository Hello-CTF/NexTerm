package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
)

// I-3: async boundaries re-check the account epoch. A login whose epoch went
// stale between authentication and issuance fails closed; issuance waits for
// in-flight revocations; DEK uploads and MFA confirms crossing a revocation
// fail closed instead of committing under a stale generation.

func TestAccountLoginEpochRecheckAfterRevocation(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	alice, _ := fixture.initSuperadmin(t, "alice", "password-alice")
	aliceID := alice.user["id"].(string)
	ctx := context.Background()

	user, err := fixture.accounts.Authenticate(ctx, "alice", "password-alice")
	if err != nil {
		t.Fatal(err)
	}
	epoch := fixture.accounts.Epoch(aliceID)

	// A revocation lands between authentication and issuance (logout-all from
	// another device): the captured generation is stale.
	fixture.login(t, "alice", "password-alice")
	if _, err := fixture.accounts.RevokeUserSessionsWithIDs(ctx, aliceID, ""); err != nil {
		t.Fatal(err)
	}
	if fixture.accounts.CheckEpoch(aliceID, epoch) {
		t.Fatal("epoch still current after a revocation")
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	fixture.server.writeAccountSession(recorder, request, user, "", epoch)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("stale-epoch issuance status = %d body = %s, want 403", recorder.Code, recorder.Body.String())
	}

	// A login admitted under the current generation succeeds.
	recorder = httptest.NewRecorder()
	fixture.server.writeAccountSession(recorder, request, user, "", fixture.accounts.Epoch(aliceID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("fresh-epoch issuance status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestAccountLoginIssuanceWaitsForInFlightRevocation(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	_, _ = fixture.initSuperadmin(t, "alice", "password-alice")

	fixture.server.accountRevocationMu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"username":"alice","password":"password-alice"}`))
		request.Header.Set("Content-Type", "application/json")
		fixture.server.serveAccountLogin(recorder, request)
	}()
	select {
	case <-done:
		fixture.server.accountRevocationMu.Unlock()
		t.Fatal("login did not wait for the in-flight revocation")
	case <-time.After(100 * time.Millisecond):
	}
	fixture.server.accountRevocationMu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("login did not resume after the revocation lock was released")
	}
}

// epochGatedReader blocks the first body read until released, parking a
// handler between its epoch capture and the revocation-locked commit.
type epochGatedReader struct {
	released chan struct{}
	data     *strings.Reader
	once     sync.Once
}

func (g *epochGatedReader) Read(p []byte) (int, error) {
	g.once.Do(func() { <-g.released })
	return g.data.Read(p)
}

func (g *epochGatedReader) Close() error { return nil }

func TestAccountDEKUploadEpochRecheck(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	fixture.createUser(t, "bob", "password-bob")
	bob := fixture.login(t, "bob", "password-bob")
	bobID := bob.user["id"].(string)

	body, _, _ := envelopeBody(t, "password-bob")
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	identity := &account.Identity{UserID: bobID, SessionID: "epoch-dek-test", Role: account.RoleUser, State: account.StateActive}

	upload := func(reader *strings.Reader) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/auth/dek", reader).WithContext(withAccountIdentity(context.Background(), identity))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		fixture.server.serveAccountDEKUpload(recorder, request)
		return recorder
	}

	// The handler captured bob's epoch and is parked on the request body; a
	// revocation landing now must fail the commit closed.
	gated := &epochGatedReader{released: make(chan struct{}), data: strings.NewReader(string(encoded))}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPost, "/auth/dek", gated).WithContext(withAccountIdentity(context.Background(), identity))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		fixture.server.serveAccountDEKUpload(recorder, request)
		done <- recorder
	}()
	sessionID := currentAccountSessionID(t, fixture, bob)
	if err := fixture.accounts.RevokeUserSession(context.Background(), bobID, sessionID); err != nil {
		t.Fatal(err)
	}
	close(gated.released)
	recorder := <-done
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("stale-epoch DEK upload status = %d body = %s, want 403", recorder.Code, recorder.Body.String())
	}
	if _, err := fixture.accounts.GetUserDEKEnvelopes(context.Background(), bobID); err == nil {
		t.Fatal("stale-epoch DEK upload committed an envelope")
	}

	// The same upload under the current generation succeeds.
	recorder = upload(strings.NewReader(string(encoded)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("fresh-epoch DEK upload status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if _, err := fixture.accounts.GetUserDEKEnvelopes(context.Background(), bobID); err != nil {
		t.Fatalf("fresh-epoch DEK upload missing envelope: %v", err)
	}
}

func TestAccountMFATicketEpochRecheck(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	fixture.createUser(t, "root", "password-a1")
	session := fixture.login(t, "root", "password-a1")

	setup := fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, session, session.csrf, nil)
	if setup.status != http.StatusOK {
		t.Fatalf("setup status = %d body = %v", setup.status, setup.body)
	}
	secret, _ := setup.body["secret"].(string)
	confirm := fixture.call(t, http.MethodPost, "/auth/totp/confirm", map[string]any{"code": testTOTPCode(t, secret, time.Now().Unix())}, session, session.csrf, nil)
	if confirm.status != http.StatusOK {
		t.Fatalf("confirm status = %d body = %v", confirm.status, confirm.body)
	}

	begin := fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "root", "password": "password-a1"}, nil, "", nil)
	if begin.status != http.StatusOK || begin.body["mfa_required"] != true {
		t.Fatalf("mfa begin status = %d body = %v", begin.status, begin.body)
	}
	ticket, _ := begin.body["ticket"].(string)
	if ticket == "" {
		t.Fatalf("missing ticket: %v", begin.body)
	}

	// A revocation lands between the ticket and the confirm (the still-valid
	// session revokes itself): the ticket belongs to a stale generation.
	sessionID := currentAccountSessionID(t, fixture, session)
	revoke := fixture.call(t, http.MethodDelete, "/auth/sessions/"+sessionID, nil, session, session.csrf, nil)
	if revoke.status != http.StatusOK {
		t.Fatalf("session revoke status = %d body = %v", revoke.status, revoke.body)
	}
	login := fixture.call(t, http.MethodPost, "/auth/totp/login", map[string]any{"ticket": ticket, "code": testTOTPCode(t, secret, time.Now().Unix())}, nil, "", nil)
	if login.status != http.StatusForbidden {
		t.Fatalf("stale-ticket confirm status = %d body = %v, want 403", login.status, login.body)
	}

	// A fresh password login issues a ticket at the current generation and confirms.
	begin = fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "root", "password": "password-a1"}, nil, "", nil)
	ticket, _ = begin.body["ticket"].(string)
	login = fixture.call(t, http.MethodPost, "/auth/totp/login", map[string]any{"ticket": ticket, "code": testTOTPCode(t, secret, time.Now().Unix())}, nil, "", nil)
	if login.status != http.StatusOK {
		t.Fatalf("fresh-ticket confirm status = %d body = %v", login.status, login.body)
	}
}

func TestAccountTOTPSetupConfirmEpochRecheck(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	fixture.createUser(t, "root", "password-a1")
	first := fixture.login(t, "root", "password-a1")
	second := fixture.login(t, "root", "password-a1")

	setup := fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, first, first.csrf, nil)
	if setup.status != http.StatusOK {
		t.Fatalf("setup status = %d body = %v", setup.status, setup.body)
	}
	secret, _ := setup.body["secret"].(string)

	// The second session revokes the first: the pending binding's generation is gone.
	firstSessionID := currentAccountSessionID(t, fixture, first)
	revoke := fixture.call(t, http.MethodDelete, "/auth/sessions/"+firstSessionID, nil, second, second.csrf, nil)
	if revoke.status != http.StatusOK {
		t.Fatalf("session revoke status = %d body = %v", revoke.status, revoke.body)
	}

	// The second session's confirm with a valid code must fail closed.
	confirm := fixture.call(t, http.MethodPost, "/auth/totp/confirm", map[string]any{"code": testTOTPCode(t, secret, time.Now().Unix())}, second, second.csrf, nil)
	if confirm.status == http.StatusOK {
		t.Fatalf("stale-generation TOTP confirm succeeded: %v", confirm.body)
	}
	status := fixture.call(t, http.MethodGet, "/auth/totp", nil, second, "", nil)
	if status.status != http.StatusOK || status.body["enabled"] != false {
		t.Fatalf("stale-generation confirm changed TOTP state: %v", status.body)
	}
}

// The confirm's epoch check and its transaction commit span the same
// revocation boundary as an epoch bump: a revocation attempted mid-confirm is
// serialized to after the commit, never landing between the check and the
// commit.
func TestAccountTOTPConfirmEpochCheckCommitAtomic(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	fixture.createUser(t, "root", "password-a1")
	first := fixture.login(t, "root", "password-a1")
	second := fixture.login(t, "root", "password-a1")

	setup := fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, first, first.csrf, nil)
	if setup.status != http.StatusOK {
		t.Fatalf("setup status = %d body = %v", setup.status, setup.body)
	}
	secret, _ := setup.body["secret"].(string)

	confirmEntered := make(chan struct{})
	releaseConfirm := make(chan struct{})
	restoreGate := account.SetTOTPConfirmGateForTest(func(context.Context, string) {
		close(confirmEntered)
		<-releaseConfirm
	})
	defer restoreGate()

	confirmDone := make(chan accountCall, 1)
	go func() {
		confirmDone <- fixture.call(t, http.MethodPost, "/auth/totp/confirm", map[string]any{"code": testTOTPCode(t, secret, time.Now().Unix())}, second, second.csrf, nil)
	}()
	select {
	case <-confirmEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("confirm did not reach the epoch-check gate")
	}

	// A revocation attempted now must wait for the in-flight confirm; it cannot
	// land between the epoch check and the commit.
	firstSessionID := currentAccountSessionID(t, fixture, first)
	revokeDone := make(chan accountCall, 1)
	go func() {
		revokeDone <- fixture.call(t, http.MethodDelete, "/auth/sessions/"+firstSessionID, nil, first, first.csrf, nil)
	}()
	select {
	case call := <-revokeDone:
		t.Fatalf("revocation completed mid-confirm (epoch window is not atomic): %v", call.body)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseConfirm)
	confirm := <-confirmDone
	if confirm.status != http.StatusOK {
		t.Fatalf("in-flight confirm status = %d body = %v", confirm.status, confirm.body)
	}
	if codes, _ := confirm.body["recovery_codes"].([]any); len(codes) != 8 {
		t.Fatalf("in-flight confirm returned no recovery codes: %v", confirm.body)
	}
	revoke := <-revokeDone
	if revoke.status != http.StatusOK {
		t.Fatalf("revocation after confirm status = %d body = %v", revoke.status, revoke.body)
	}
	if _, err := fixture.accounts.ValidateSession(context.Background(), first.cookie.Value); err == nil {
		t.Fatal("revocation after the confirm did not revoke the first session")
	}
	status := fixture.call(t, http.MethodGet, "/auth/totp", nil, second, "", nil)
	if status.status != http.StatusOK || status.body["enabled"] != true {
		t.Fatalf("TOTP state after the atomic confirm = %v", status.body)
	}
}

// Admin disable shares the revocation boundary with the TOTP confirm: a
// disable attempted mid-confirm is serialized to after the commit, and a
// disable that completed first fails the stale confirm with TOTP unchanged.
func TestAccountTOTPConfirmEpochAdminDisableAtomic(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	_, _ = fixture.initSuperadmin(t, "alice", "password-alice")
	adminSession := fixture.login(t, "alice", "password-alice")
	fixture.createUser(t, "root", "password-a1")
	target := fixture.login(t, "root", "password-a1")
	targetID := target.user["id"].(string)

	setup := fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, target, target.csrf, nil)
	if setup.status != http.StatusOK {
		t.Fatalf("setup status = %d body = %v", setup.status, setup.body)
	}
	secret, _ := setup.body["secret"].(string)

	confirmEntered := make(chan struct{})
	releaseConfirm := make(chan struct{})
	restoreGate := account.SetTOTPConfirmGateForTest(func(context.Context, string) {
		close(confirmEntered)
		<-releaseConfirm
	})
	defer restoreGate()

	confirmDone := make(chan accountCall, 1)
	go func() {
		confirmDone <- fixture.call(t, http.MethodPost, "/auth/totp/confirm", map[string]any{"code": testTOTPCode(t, secret, time.Now().Unix())}, target, target.csrf, nil)
	}()
	select {
	case <-confirmEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("confirm did not reach the epoch-check gate")
	}

	// The admin disable attempted now must wait for the in-flight confirm.
	disableDone := make(chan accountCall, 1)
	go func() {
		disableDone <- fixture.call(t, http.MethodPost, "/admin/users/"+targetID+"/disable", nil, adminSession, adminSession.csrf, nil)
	}()
	select {
	case call := <-disableDone:
		t.Fatalf("admin disable completed mid-confirm (epoch window is not atomic): %v", call.body)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseConfirm)
	confirm := <-confirmDone
	if confirm.status != http.StatusOK {
		t.Fatalf("in-flight confirm status = %d body = %v", confirm.status, confirm.body)
	}
	if codes, _ := confirm.body["recovery_codes"].([]any); len(codes) != 8 {
		t.Fatalf("in-flight confirm returned no recovery codes: %v", confirm.body)
	}
	disable := <-disableDone
	if disable.status != http.StatusOK {
		t.Fatalf("admin disable after confirm status = %d body = %v", disable.status, disable.body)
	}
	if enabled, err := fixture.accounts.TOTPEnabled(context.Background(), targetID); err != nil || !enabled {
		t.Fatalf("TOTP enabled after the atomic confirm = %v, %v", enabled, err)
	}
}

func TestAccountTOTPConfirmEpochAdminDisableFirstFails(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	_, _ = fixture.initSuperadmin(t, "alice", "password-alice")
	adminSession := fixture.login(t, "alice", "password-alice")
	fixture.createUser(t, "root", "password-a1")
	target := fixture.login(t, "root", "password-a1")
	targetID := target.user["id"].(string)

	setup := fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, target, target.csrf, nil)
	if setup.status != http.StatusOK {
		t.Fatalf("setup status = %d body = %v", setup.status, setup.body)
	}
	secret, _ := setup.body["secret"].(string)

	// The admin disable completes first: the pending binding's generation is gone.
	disable := fixture.call(t, http.MethodPost, "/admin/users/"+targetID+"/disable", nil, adminSession, adminSession.csrf, nil)
	if disable.status != http.StatusOK {
		t.Fatalf("admin disable status = %d body = %v", disable.status, disable.body)
	}

	// The stale confirm with a valid code must fail closed with no new recovery codes.
	confirm := fixture.call(t, http.MethodPost, "/auth/totp/confirm", map[string]any{"code": testTOTPCode(t, secret, time.Now().Unix())}, target, target.csrf, nil)
	if confirm.status == http.StatusOK {
		t.Fatalf("stale-generation TOTP confirm succeeded: %v", confirm.body)
	}
	if _, present := confirm.body["recovery_codes"]; present {
		t.Fatalf("stale-generation confirm returned recovery codes: %v", confirm.body)
	}
	if enabled, err := fixture.accounts.TOTPEnabled(context.Background(), targetID); err != nil || enabled {
		t.Fatalf("stale-generation confirm changed TOTP state: enabled=%v err=%v", enabled, err)
	}
}

// A password change that completed before the login's Authenticate fails the
// old-password login closed; the new password still works.
func TestAccountLoginAfterPasswordChangeFails(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	alice, _ := fixture.initSuperadmin(t, "alice", "password-alice")
	aliceSession := fixture.login(t, "alice", "password-alice")
	_ = aliceSession

	body, _, _ := envelopeBody(t, "password-alice-2")
	change := fixture.call(t, http.MethodPost, "/auth/password", mergeBody(body, map[string]any{
		"old_password": "password-alice", "new_password": "password-alice-2",
	}), aliceSession, aliceSession.csrf, nil)
	if change.status != http.StatusOK {
		t.Fatalf("password change status = %d body = %v", change.status, change.body)
	}

	// The old password no longer authenticates (checked at the account layer to
	// avoid tripping the HTTP login throttle).
	if _, err := fixture.accounts.Authenticate(context.Background(), "alice", "password-alice"); err == nil {
		t.Fatal("old password still authenticates after the change")
	}
	fresh := fixture.login(t, "alice", "password-alice-2")
	if fresh.user["id"] != alice.user["id"] {
		t.Fatalf("new-password login user = %v, want %s", fresh.user["id"], alice.user["id"])
	}
}

// The login's epoch is captured at the authentication boundary under the
// revocation lock: a password change attempted mid-login is serialized to
// after the issuance, never landing between Authenticate and the epoch capture.
func TestAccountLoginEpochCapturedAtAuthenticationAtomic(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	fixture.initSuperadmin(t, "alice", "password-alice")
	aliceSession := fixture.login(t, "alice", "password-alice")

	loginEntered := make(chan struct{})
	releaseLogin := make(chan struct{})
	restoreGate := SetAccountLoginGateForTest(func(context.Context, *account.User) {
		close(loginEntered)
		<-releaseLogin
	})
	defer restoreGate()

	loginDone := make(chan accountCall, 1)
	go func() {
		loginDone <- fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "alice", "password": "password-alice"}, nil, "", nil)
	}()
	select {
	case <-loginEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("login did not reach the post-authenticate gate")
	}

	// A password change attempted now must wait for the in-flight login.
	body, _, _ := envelopeBody(t, "password-alice-2")
	changeDone := make(chan accountCall, 1)
	go func() {
		changeDone <- fixture.call(t, http.MethodPost, "/auth/password", mergeBody(body, map[string]any{
			"old_password": "password-alice", "new_password": "password-alice-2",
		}), aliceSession, aliceSession.csrf, nil)
	}()
	select {
	case call := <-changeDone:
		t.Fatalf("password change completed mid-login (epoch window is not atomic): %v", call.body)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseLogin)
	login := <-loginDone
	if login.status != http.StatusOK {
		t.Fatalf("in-flight login status = %d body = %v", login.status, login.body)
	}
	change := <-changeDone
	if change.status != http.StatusOK {
		t.Fatalf("password change after login status = %d body = %v", change.status, change.body)
	}
	// The password change revoked every session except its own, including the
	// just-issued login session.
	var revoked bool
	for _, cookie := range login.cookies {
		if cookie.Name == sessionCookieName {
			if _, err := fixture.accounts.ValidateSession(context.Background(), cookie.Value); err != nil {
				revoked = true
			}
		}
	}
	if !revoked {
		t.Fatal("password change after the login did not revoke the new session")
	}
	if _, err := fixture.accounts.ValidateSession(context.Background(), aliceSession.cookie.Value); err != nil {
		t.Fatalf("password change revoked its own session: %v", err)
	}
}
