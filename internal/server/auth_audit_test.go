package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type authAuditFixture struct {
	*accountFixture
	database *store.Store
}

// newAuthAuditFixture wires the server audit to the real store, as the
// server binary does, so tests read back what the audit view would show.
func newAuthAuditFixture(t *testing.T) *authAuditFixture {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	config := testConfig(t, false)
	config.Options.Auth = AuthOn
	accounts := account.New(database.GORM(), account.WithTOTPKeyFile(filepath.Join(t.TempDir(), "totp.key")), withTestTOTPClock())
	config.Accounts = accounts
	config.Settings = database
	config.AuditFunc = func(ctx context.Context, source, kind string, payload map[string]any) error {
		return database.AuditInsert(ctx, store.AuditInput{Source: source, Kind: kind, Payload: payload})
	}
	server, httpServer := newTestHTTP(t, config)
	return &authAuditFixture{
		accountFixture: &accountFixture{server: server, http: httpServer, accounts: accounts, db: database.DB(), client: &http.Client{}},
		database:       database,
	}
}

type authAuditRow struct {
	owner   string
	kind    string
	payload map[string]any
	raw     string
}

func (f *authAuditFixture) rows(t *testing.T) []authAuditRow {
	t.Helper()
	var rows []store.AuditRow
	if err := f.database.GORM().Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := make([]authAuditRow, 0, len(rows))
	for _, row := range rows {
		if row.Source != authAuditSource {
			continue
		}
		payload := map[string]any{}
		if err := json.Unmarshal([]byte(row.PayloadJSON), &payload); err != nil {
			t.Fatal(err)
		}
		out = append(out, authAuditRow{owner: row.OwnerID, kind: row.Kind, payload: payload, raw: row.PayloadJSON})
	}
	return out
}

func (f *authAuditFixture) only(t *testing.T, kind string) authAuditRow {
	t.Helper()
	var found []authAuditRow
	for _, row := range f.rows(t) {
		if row.kind == kind {
			found = append(found, row)
		}
	}
	if len(found) != 1 {
		t.Fatalf("audit %s rows = %d, want 1: %+v", kind, len(found), f.rows(t))
	}
	return found[0]
}

func expectAudit(t *testing.T, row authAuditRow, owner string, fields map[string]any) {
	t.Helper()
	if row.owner != owner {
		t.Fatalf("%s owner = %q, want %q", row.kind, row.owner, owner)
	}
	if row.payload["client_ip"] == "" || row.payload["client_ip"] == nil {
		t.Fatalf("%s missing client_ip: %v", row.kind, row.payload)
	}
	for key, want := range fields {
		got, _ := json.Marshal(row.payload[key])
		expected, _ := json.Marshal(want)
		if string(got) != string(expected) {
			t.Fatalf("%s %s = %s, want %s (payload %v)", row.kind, key, got, expected, row.payload)
		}
	}
}

func TestAuthAuditRecordsAccountAndAdminActions(t *testing.T) {
	fixture := newAuthAuditFixture(t)
	admin, _ := fixture.initSuperadmin(t, "root", "password-root1")
	adminID := admin.user["id"].(string)
	secrets := []string{"password-root1", "password-bob1", "password-bob2", "password-carol1", "wrong-password-x", "typed-secret-name", admin.csrf, admin.cookie.Value}

	call := fixture.call(t, http.MethodPost, "/admin/users", map[string]any{"username": "bob", "password": "password-bob1", "display_name": "Bob"}, admin, admin.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("create status=%d body=%v", call.status, call.body)
	}
	bobID := call.body["user"].(map[string]any)["id"].(string)
	expectAudit(t, fixture.only(t, "admin_create_user"), adminID, map[string]any{
		"actor_id": adminID, "target_user_id": bobID, "fields": []string{"display_name", "password", "username"},
	})

	bob := fixture.login(t, "bob", "password-bob1")
	secrets = append(secrets, bob.csrf, bob.cookie.Value)
	expectAudit(t, fixture.only(t, "login_success"), bobID, map[string]any{"actor_id": bobID, "target_user_id": bobID, "method": "password"})
	if fixture.only(t, "login_success").payload["session_id"] == "" {
		t.Fatal("login_success must name the issued session")
	}

	if call := fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "bob", "password": "wrong-password-x"}, nil, "", map[string]string{"User-Agent": strings.Repeat("u", 400)}); call.status != http.StatusForbidden {
		t.Fatalf("wrong password status=%d", call.status)
	}
	failure := fixture.only(t, "login_failure")
	expectAudit(t, failure, bobID, map[string]any{"target_user_id": bobID, "reason": "rejected", "stage": "password", "failures": 1})
	if ua, _ := failure.payload["user_agent"].(string); len(ua) != authAuditUserAgentMax {
		t.Fatalf("user agent length = %d, want %d", len(ua), authAuditUserAgentMax)
	}

	if call := fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "typed-secret-name", "password": "wrong-password-x"}, nil, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("unknown user status=%d", call.status)
	}
	var unknown authAuditRow
	for _, row := range fixture.rows(t) {
		if row.kind == "login_failure" && row.payload["reason"] == "unknown_user" {
			unknown = row
		}
	}
	expectAudit(t, unknown, "", map[string]any{"username_hash": fixture.accounts.AuditPseudonym("typed-secret-name"), "username_length": len("typed-secret-name")})

	fixture.resetLoginThrottles()
	second := fixture.login(t, "bob", "password-bob1")
	if call := fixture.call(t, http.MethodDelete, "/auth/sessions/"+sessionIDOf(t, fixture, second), nil, bob, bob.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("session revoke status=%d body=%v", call.status, call.body)
	}
	expectAudit(t, fixture.only(t, "session_revoke"), bobID, map[string]any{"actor_id": bobID, "current": false})

	body, _, _ := envelopeBody(t, "password-bob2")
	body = mergeBody(body, map[string]any{"old_password": "password-bob1", "new_password": "password-bob2"})
	if call := fixture.call(t, http.MethodPost, "/auth/password", body, bob, bob.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("password change status=%d body=%v", call.status, call.body)
	}
	expectAudit(t, fixture.only(t, "password_change"), bobID, map[string]any{"actor_id": bobID})

	fixture.login(t, "bob", "password-bob2")
	if call := fixture.call(t, http.MethodPost, "/auth/logout-all", nil, bob, bob.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("logout-all status=%d body=%v", call.status, call.body)
	}
	expectAudit(t, fixture.only(t, "logout_all"), bobID, map[string]any{"revoked": 2})

	bob = fixture.login(t, "bob", "password-bob2")
	if call := fixture.call(t, http.MethodPost, "/auth/logout", nil, bob, bob.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("logout status=%d", call.status)
	}
	expectAudit(t, fixture.only(t, "logout"), bobID, map[string]any{"actor_id": bobID})

	if call := fixture.call(t, http.MethodPost, "/admin/users/"+bobID+"/disable", nil, admin, admin.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("disable status=%d", call.status)
	}
	expectAudit(t, fixture.only(t, "admin_disable_user"), adminID, map[string]any{"target_user_id": bobID, "fields": []string{"state"}})

	fixture.createUser(t, "carol", "password-carol1")
	carol, err := fixture.accounts.GetUserByUsername(context.Background(), "carol")
	if err != nil {
		t.Fatal(err)
	}
	if call := fixture.call(t, http.MethodPost, "/admin/users/"+carol.ID+"/reset", nil, admin, admin.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("reset status=%d", call.status)
	}
	expectAudit(t, fixture.only(t, "admin_reset_user"), adminID, map[string]any{"target_user_id": carol.ID})

	// A failed admin write records nothing.
	if call := fixture.call(t, http.MethodPost, "/admin/users/nonexistent/disable", nil, admin, admin.csrf, nil); call.status != http.StatusNotFound {
		t.Fatalf("disable unknown status=%d", call.status)
	}
	if rows := fixture.rows(t); countKind(rows, "admin_disable_user") != 1 {
		t.Fatalf("failed admin write was audited: %+v", rows)
	}

	if call := fixture.call(t, http.MethodPut, "/admin/settings", map[string]any{"registration_open": true, "mfa_required": false}, admin, admin.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("settings status=%d body=%v", call.status, call.body)
	}
	expectAudit(t, fixture.only(t, "admin_put_settings"), adminID, map[string]any{"fields": []string{"registration_open"}})

	if call := fixture.call(t, http.MethodPut, "/admin/preferences", map[string]any{"set": map[string]any{"appearance.terminalTheme": "dark"}}, admin, admin.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("preferences status=%d body=%v", call.status, call.body)
	}
	expectAudit(t, fixture.only(t, "admin_put_preferences"), adminID, map[string]any{"fields": []string{"appearance.terminalTheme"}})

	// Reads are not audited.
	fixture.call(t, http.MethodGet, "/admin/users", nil, admin, "", nil)
	if countKind(fixture.rows(t), "admin_list_users") != 0 {
		t.Fatal("admin reads must not be audited")
	}

	assertNoSecrets(t, fixture.rows(t), secrets)

	// Each user's audit view shows only their own account records.
	visible, err := fixture.database.AuditQuery(ipc.WithUserID(context.Background(), bobID), store.AuditQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range visible {
		if row.OwnerID != bobID {
			t.Fatalf("bob sees a record owned by %q", row.OwnerID)
		}
	}
	if len(visible) == 0 {
		t.Fatal("bob's audit view must list his account records")
	}
}

func TestAuthAuditTOTPAndRecoveryCode(t *testing.T) {
	fixture := newAuthAuditFixture(t)
	fixture.createUser(t, "dana", "password-dana1")
	session := fixture.login(t, "dana", "password-dana1")
	userID := session.user["id"].(string)

	call := fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, session, session.csrf, nil)
	secret := call.body["secret"].(string)
	call = fixture.call(t, http.MethodPost, "/auth/totp/confirm", map[string]any{"code": testTOTPCode(t, secret, time.Now().Unix())}, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("confirm status=%d body=%v", call.status, call.body)
	}
	expectAudit(t, fixture.only(t, "totp_enable"), userID, map[string]any{"actor_id": userID, "rebind": false})
	codes := call.body["recovery_codes"].([]any)
	secrets := []string{secret, "password-dana1"}
	for _, code := range codes {
		secrets = append(secrets, code.(string), strings.ReplaceAll(code.(string), "-", ""))
	}

	call = fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "dana", "password": "password-dana1"}, nil, "", nil)
	ticket := call.body["ticket"].(string)
	if call := fixture.call(t, http.MethodPost, "/auth/totp/login", map[string]any{"ticket": ticket, "code": "000000"}, nil, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("wrong code status=%d", call.status)
	}
	expectAudit(t, fixture.only(t, "login_failure"), userID, map[string]any{"stage": "totp", "reason": "rejected"})

	call = fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "dana", "password": "password-dana1"}, nil, "", nil)
	ticket = call.body["ticket"].(string)
	call = fixture.call(t, http.MethodPost, "/auth/totp/login", map[string]any{"ticket": ticket, "code": codes[0].(string)}, nil, "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("recovery login status=%d body=%v", call.status, call.body)
	}
	expectAudit(t, fixture.only(t, "totp_recovery_code_used"), userID, map[string]any{"purpose": "login", "recovery_codes_left": len(codes) - 1})
	if rows := fixture.rows(t); rows[len(rows)-1].kind != "login_success" || rows[len(rows)-1].payload["method"] != "recovery_code" {
		t.Fatalf("recovery login must be audited as login_success by recovery_code: %+v", rows[len(rows)-1])
	}
	mfaSession := captureSession(t, call)

	call = fixture.call(t, http.MethodDelete, "/auth/totp", map[string]any{"code": codes[1].(string)}, mfaSession, mfaSession.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("disable status=%d body=%v", call.status, call.body)
	}
	expectAudit(t, fixture.only(t, "totp_disable"), userID, map[string]any{"method": "recovery_code"})
	if countKind(fixture.rows(t), "totp_recovery_code_used") != 2 {
		t.Fatal("recovery code use on disable must be audited")
	}
	assertNoSecrets(t, fixture.rows(t), secrets)
}

func (f *authAuditFixture) resetLoginThrottles() {
	f.server.accountThrottle.login = account.NewLoginThrottle()
	f.server.accountThrottle.loginIP = account.NewLoginThrottleWithPolicy(account.LoginIPThrottlePolicy)
	f.server.accountThrottle.loginUser = account.NewLoginThrottleWithPolicy(account.LoginUserThrottlePolicy)
}

func sessionIDOf(t *testing.T, fixture *authAuditFixture, session *accountTestSession) string {
	t.Helper()
	call := fixture.call(t, http.MethodGet, "/auth/sessions", nil, session, "", nil)
	for _, raw := range call.body["sessions"].([]any) {
		entry := raw.(map[string]any)
		if entry["current"] == true {
			return entry["id"].(string)
		}
	}
	t.Fatal("current session not listed")
	return ""
}

func countKind(rows []authAuditRow, kind string) int {
	n := 0
	for _, row := range rows {
		if row.kind == kind {
			n++
		}
	}
	return n
}

func assertNoSecrets(t *testing.T, rows []authAuditRow, secrets []string) {
	t.Helper()
	for _, row := range rows {
		for _, secret := range secrets {
			if secret != "" && strings.Contains(row.raw, secret) {
				t.Fatalf("audit %s leaks a secret: %s", row.kind, row.raw)
			}
		}
	}
}

func TestLoginFailureAuditAggregation(t *testing.T) {
	auditor := newLoginFailureAuditor()
	clock := time.Unix(1_700_000_000, 0)
	auditor.now = func() time.Time { return clock }

	recorded := 0
	for i := 0; i < loginFailureAuditPerKey+4; i++ {
		record, failures, suppressed := auditor.admit("k")
		if failures != i+1 || suppressed != 0 {
			t.Fatalf("attempt %d failures=%d suppressed=%d", i, failures, suppressed)
		}
		if record {
			recorded++
		}
	}
	if recorded != loginFailureAuditPerKey {
		t.Fatalf("recorded %d failures in one window, want %d", recorded, loginFailureAuditPerKey)
	}
	clock = clock.Add(loginFailureAuditWindow)
	record, failures, suppressed := auditor.admit("k")
	if !record || failures != 1 || suppressed != 4 {
		t.Fatalf("next window record=%v failures=%d suppressed=%d, want the 4 unrecorded failures carried", record, failures, suppressed)
	}

	// The global bound caps records across keys within one window.
	global := newLoginFailureAuditor()
	global.now = func() time.Time { return clock }
	total := 0
	for i := 0; i < loginFailureAuditGlobal+50; i++ {
		if record, _, _ := global.admit("key-" + time.Duration(i).String()); record {
			total++
		}
	}
	if total != loginFailureAuditGlobal {
		t.Fatalf("global records = %d, want %d", total, loginFailureAuditGlobal)
	}
	if len(global.entries) > loginFailureAuditMaxKeys {
		t.Fatalf("tracked keys = %d exceed bound", len(global.entries))
	}
}

func TestAuthAuditFailedLoginsDoNotFloodAudit(t *testing.T) {
	fixture := newAuthAuditFixture(t)
	fixture.createUser(t, "erin", "password-erin1")
	// Call the audit hook directly so the request throttle does not stop the attempts first.
	for i := 0; i < loginFailureAuditPerKey+5; i++ {
		request := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
		fixture.server.auditPasswordLoginFailure(request, "erin")
	}
	rows := fixture.rows(t)
	if got := countKind(rows, "login_failure"); got != loginFailureAuditPerKey {
		t.Fatalf("login_failure rows = %d, want %d", got, loginFailureAuditPerKey)
	}
}
