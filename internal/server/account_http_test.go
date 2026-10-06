package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

type accountFixture struct {
	server   *Server
	http     *httptest.Server
	accounts *account.Accounts
	client   *http.Client
}

func newAccountFixture(t *testing.T, auth string) *accountFixture {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	config := testConfig(t, false)
	config.Options.Auth = auth
	accounts := account.New(database.DB())
	config.Accounts = accounts
	server, httpServer := newTestHTTP(t, config)
	return &accountFixture{
		server: server, http: httpServer, accounts: accounts,
		client: &http.Client{},
	}
}

type accountTestSession struct {
	cookie *http.Cookie
	csrf   string
	user   map[string]any
}

type accountCall struct {
	status  int
	body    map[string]any
	cookies []*http.Cookie
}

func (f *accountFixture) call(t *testing.T, method, path string, body any, session *accountTestSession, csrf string, headers map[string]string) accountCall {
	t.Helper()
	reader := strings.NewReader("")
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequest(method, f.http.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if session != nil && session.cookie != nil {
		request.AddCookie(session.cookie)
	}
	if csrf != "" {
		request.Header.Set(csrfHeaderName, csrf)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var decoded map[string]any
	if strings.Contains(response.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
			t.Fatalf("decode %s %s response: %v", method, path, err)
		}
	}
	return accountCall{status: response.StatusCode, body: decoded, cookies: response.Cookies()}
}

func captureSession(t *testing.T, call accountCall) *accountTestSession {
	t.Helper()
	session := &accountTestSession{csrf: call.body["csrf_token"].(string), user: call.body["user"].(map[string]any)}
	for _, cookie := range call.cookies {
		if cookie.Name == sessionCookieName {
			session.cookie = cookie
		}
	}
	if session.cookie == nil {
		t.Fatal("响应缺少会话 cookie")
	}
	return session
}

func (f *accountFixture) login(t *testing.T, username, password string) *accountTestSession {
	t.Helper()
	call := f.call(t, http.MethodPost, "/auth/login", map[string]any{"username": username, "password": password}, nil, "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("login status=%d body=%v", call.status, call.body)
	}
	return captureSession(t, call)
}

func envelopeBody(t *testing.T, password string) (map[string]any, string, []byte) {
	t.Helper()
	dek, envelopes, recoveryKey, err := vault.GenerateUserDEKEnvelopes(password)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"dek_envelope":      envelopes.DEKEnvelope,
		"kdf_salt":          envelopes.KDFSalt,
		"kdf_params":        envelopes.KDFParams,
		"recovery_envelope": envelopes.RecoveryEnvelope,
		"recovery_hash":     envelopes.RecoveryHash,
	}, recoveryKey, dek
}

func mergeBody(base map[string]any, extra map[string]any) map[string]any {
	merged := map[string]any{}
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

func (f *accountFixture) initSuperadmin(t *testing.T, username, password string) (*accountTestSession, string) {
	t.Helper()
	code, err := f.accounts.GenerateInitCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	envelopes, recoveryKey, _ := envelopeBody(t, password)
	initBody := mergeBody(envelopes, map[string]any{"code": code, "username": username, "password": password})
	call := f.call(t, http.MethodPost, "/auth/init", initBody, nil, "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("init status=%d body=%v", call.status, call.body)
	}
	return captureSession(t, call), recoveryKey
}

func createUserViaCore(t *testing.T, fixture *accountFixture, username, password string) {
	t.Helper()
	code, err := fixture.accounts.GenerateInitCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.accounts.InitSuperadmin(context.Background(), code, username, password); err != nil {
		t.Fatal(err)
	}
}

func TestAccountHTTPInitLifecycle(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)

	call := fixture.call(t, http.MethodGet, "/auth/status", nil, nil, "", nil)
	if call.status != http.StatusOK || call.body["initialized"] != false || call.body["registration_open"] != false || call.body["auth"] != AuthOn {
		t.Fatalf("uninitialized status=%d body=%v", call.status, call.body)
	}

	code, err := fixture.accounts.GenerateInitCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	envelopes, _, _ := envelopeBody(t, "init-password-1")
	wrong := mergeBody(envelopes, map[string]any{"code": "wrong-code", "username": "root", "password": "init-password-1"})
	if call = fixture.call(t, http.MethodPost, "/auth/init", wrong, nil, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("wrong init code status=%d body=%v", call.status, call.body)
	}

	initBody := mergeBody(envelopes, map[string]any{"code": code, "username": "root", "password": "init-password-1"})
	call = fixture.call(t, http.MethodPost, "/auth/init", initBody, nil, "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("init status=%d body=%v", call.status, call.body)
	}
	session := captureSession(t, call)
	if session.user["role"] != string(account.RoleSuperadmin) {
		t.Fatalf("init user role=%v", session.user["role"])
	}

	call = fixture.call(t, http.MethodGet, "/auth/me", nil, session, "", nil)
	if call.status != http.StatusOK || call.body["user"].(map[string]any)["username"] != "root" {
		t.Fatalf("me status=%d body=%v", call.status, call.body)
	}

	call = fixture.call(t, http.MethodGet, "/auth/status", nil, nil, "", nil)
	if call.status != http.StatusOK || call.body["initialized"] != true {
		t.Fatalf("initialized status=%d body=%v", call.status, call.body)
	}

	if call = fixture.call(t, http.MethodPost, "/auth/init", initBody, nil, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("re-init status=%d body=%v", call.status, call.body)
	}
}

func TestAccountHTTPLoginThrottle(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	fixture.initSuperadmin(t, "root", "password-a1")

	if call := fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "root", "password": "wrong"}, nil, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("wrong password status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "root", "password": "password-a1"}, nil, "", nil); call.status != http.StatusTooManyRequests {
		t.Fatalf("throttled login status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "other", "password": "x"}, nil, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("independent throttle key status=%d", call.status)
	}
}

func TestAccountHTTPLogoutAndLogoutAll(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	fixture.initSuperadmin(t, "root", "password-a2")

	session := fixture.login(t, "root", "password-a2")
	second := fixture.login(t, "root", "password-a2")
	if session.cookie.Value == second.cookie.Value {
		t.Fatal("每次登录必须签发新会话")
	}

	if call := fixture.call(t, http.MethodPost, "/auth/logout", nil, session, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("logout without csrf status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodPost, "/auth/logout", nil, session, session.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("logout status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodGet, "/auth/me", nil, session, "", nil); call.status != http.StatusUnauthorized {
		t.Fatalf("me after logout status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "root", "password": "password-a2"}, session, "", nil); call.status != http.StatusOK {
		t.Fatalf("re-login with stale cookie status=%d", call.status)
	}

	if call := fixture.call(t, http.MethodPost, "/auth/logout-all", nil, second, "garbage", nil); call.status != http.StatusForbidden {
		t.Fatalf("logout-all with bad csrf status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodPost, "/auth/logout-all", nil, second, second.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("logout-all status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodGet, "/auth/me", nil, second, "", nil); call.status != http.StatusUnauthorized {
		t.Fatalf("initiator session after logout-all status=%d", call.status)
	}
	third := fixture.login(t, "root", "password-a2")
	if call := fixture.call(t, http.MethodGet, "/auth/me", nil, third, "", nil); call.status != http.StatusOK {
		t.Fatalf("fresh login after logout-all status=%d", call.status)
	}
}

func TestAccountHTTPCookieSecurityAttributes(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	code, err := fixture.accounts.GenerateInitCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	payload, _ := json.Marshal(map[string]any{"username": "root", "password": "password-b1", "code": code})
	request, err := http.NewRequest(http.MethodPost, fixture.http.URL+"/auth/init", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := fixture.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("init without envelopes status=%d", response.StatusCode)
	}

	envelopes, _, _ := envelopeBody(t, "password-b1")
	initBody := mergeBody(envelopes, map[string]any{"code": code, "username": "root", "password": "password-b1"})
	encoded, _ := json.Marshal(initBody)
	request, err = http.NewRequest(http.MethodPost, fixture.http.URL+"/auth/init", strings.NewReader(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err = fixture.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("init status=%d", response.StatusCode)
	}
	var cookie *http.Cookie
	for _, candidate := range response.Cookies() {
		if candidate.Name == sessionCookieName {
			cookie = candidate
		}
	}
	if cookie == nil {
		t.Fatal("缺少会话 cookie")
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Secure {
		t.Fatalf("cookie 属性不符: %+v", cookie)
	}

	tlsConfig := testConfig(t, false)
	tlsConfig.Options.Auth = AuthOn
	tlsConfig.Accounts = fixture.accounts
	tlsServer, err := New(tlsConfig)
	if err != nil {
		t.Fatal(err)
	}
	tlsHTTP := httptest.NewTLSServer(tlsServer.Handler())
	defer tlsHTTP.Close()
	request, err = http.NewRequest(http.MethodPost, tlsHTTP.URL+"/auth/login", strings.NewReader(`{"username":"root","password":"password-b1"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	tlsResponse, err := tlsHTTP.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer tlsResponse.Body.Close()
	if tlsResponse.StatusCode != http.StatusOK {
		t.Fatalf("tls login status=%d", tlsResponse.StatusCode)
	}
	secure := false
	for _, candidate := range tlsResponse.Cookies() {
		if candidate.Name == sessionCookieName && candidate.Secure {
			secure = true
		}
	}
	if !secure {
		t.Fatal("TLS 下会话 cookie 必须带 Secure")
	}
}

func TestAccountHTTPCSRFAndOriginRules(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	session, _ := fixture.initSuperadmin(t, "root", "password-c1")

	if call := fixture.call(t, http.MethodPost, "/auth/logout", nil, session, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("csrf missing status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodGet, "/auth/me", nil, session, "", nil); call.status != http.StatusOK {
		t.Fatalf("read exempt from csrf status=%d", call.status)
	}

	call := fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "root", "password": "password-c1"}, nil, "", map[string]string{"Origin": "http://evil.example"})
	if call.status != http.StatusForbidden {
		t.Fatalf("cross-origin login status=%d", call.status)
	}
	host := strings.TrimPrefix(fixture.http.URL, "http://")
	call = fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "root", "password": "password-c1"}, nil, "", map[string]string{"Origin": "http://" + host})
	if call.status != http.StatusOK {
		t.Fatalf("same-origin login status=%d", call.status)
	}
}

func TestAccountHTTPRegistrationClosedByDefault(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	adminSession, _ := fixture.initSuperadmin(t, "root", "password-d1")

	envelopes, _, _ := envelopeBody(t, "password-d2")
	registerBody := mergeBody(envelopes, map[string]any{"username": "alice", "password": "password-d2"})
	if call := fixture.call(t, http.MethodPost, "/auth/register", registerBody, nil, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("closed registration status=%d body=%v", call.status, call.body)
	}

	if call := fixture.call(t, http.MethodPut, "/admin/settings", map[string]any{"registration_open": true}, adminSession, adminSession.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("open registration status=%d", call.status)
	}
	call := fixture.call(t, http.MethodPost, "/auth/register", registerBody, nil, "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("register status=%d body=%v", call.status, call.body)
	}
	alice := captureSession(t, call)
	if alice.user["role"] != string(account.RoleUser) || alice.user["username"] != "alice" {
		t.Fatalf("registered user=%v", alice.user)
	}

	if call := fixture.call(t, http.MethodPost, "/auth/register", registerBody, nil, "", nil); call.status != http.StatusBadRequest {
		t.Fatalf("duplicate register status=%d", call.status)
	}

	if call := fixture.call(t, http.MethodPut, "/admin/settings", map[string]any{"registration_open": false}, alice, alice.csrf, nil); call.status != http.StatusForbidden {
		t.Fatalf("non-admin settings status=%d", call.status)
	}
	call = fixture.call(t, http.MethodGet, "/admin/settings", nil, adminSession, "", nil)
	if call.status != http.StatusOK || call.body["registration_open"] != true {
		t.Fatalf("settings after non-admin put status=%d body=%v", call.status, call.body)
	}
}

func TestAccountHTTPAdminUserLifecycle(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	adminSession, _ := fixture.initSuperadmin(t, "root", "password-e1")

	call := fixture.call(t, http.MethodPost, "/admin/users", map[string]any{"username": "bob", "password": "password-e2"}, adminSession, adminSession.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("admin create status=%d body=%v", call.status, call.body)
	}
	bob := fixture.login(t, "bob", "password-e2")

	if call := fixture.call(t, http.MethodGet, "/admin/users", nil, bob, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("user list as non-admin status=%d", call.status)
	}
	call = fixture.call(t, http.MethodGet, "/admin/users", nil, adminSession, "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("admin list status=%d", call.status)
	}
	if users := call.body["users"].([]any); len(users) != 2 {
		t.Fatalf("admin list users=%v", users)
	}

	bobID := bob.user["id"].(string)
	if call := fixture.call(t, http.MethodPost, "/admin/users/"+bobID+"/disable", nil, adminSession, adminSession.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("disable status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodGet, "/auth/me", nil, bob, "", nil); call.status != http.StatusUnauthorized {
		t.Fatalf("disabled session must be revoked immediately, status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "bob", "password": "password-e2"}, nil, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("disabled login status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodPost, "/admin/users/"+adminSession.user["id"].(string)+"/disable", nil, adminSession, adminSession.csrf, nil); call.status != http.StatusBadRequest {
		t.Fatalf("self disable status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodPost, "/admin/users/nonexistent/disable", nil, adminSession, adminSession.csrf, nil); call.status != http.StatusNotFound {
		t.Fatalf("disable unknown status=%d", call.status)
	}

	if call := fixture.call(t, http.MethodPost, "/admin/users", map[string]any{"username": "carol", "password": "password-e3"}, adminSession, adminSession.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("create carol status=%d", call.status)
	}
	carol := fixture.login(t, "carol", "password-e3")
	carolID := carol.user["id"].(string)
	if call := fixture.call(t, http.MethodPost, "/admin/users/"+carolID+"/reset", nil, adminSession, adminSession.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("reset status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodGet, "/auth/me", nil, carol, "", nil); call.status != http.StatusUnauthorized {
		t.Fatalf("reset must revoke sessions, status=%d", call.status)
	}
	call = fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "carol", "password": "password-e3"}, nil, "", nil)
	if call.status != http.StatusOK || call.body["user"].(map[string]any)["must_change_password"] != true {
		t.Fatalf("reset login status=%d body=%v", call.status, call.body)
	}
}

func TestAccountHTTPPasswordChange(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	session, _ := fixture.initSuperadmin(t, "root", "password-f1")
	other := fixture.login(t, "root", "password-f1")

	oldSalt := currentDEKSalt(t, fixture, session)
	wrongBody, _, _ := envelopeBody(t, "password-f2")
	wrongBody = mergeBody(wrongBody, map[string]any{"old_password": "wrong", "new_password": "password-f2"})
	if call := fixture.call(t, http.MethodPost, "/auth/password", wrongBody, session, session.csrf, nil); call.status != http.StatusForbidden {
		t.Fatalf("wrong old password status=%d", call.status)
	}

	changeBody, _, _ := envelopeBody(t, "password-f2")
	changeBody = mergeBody(changeBody, map[string]any{"old_password": "password-f1", "new_password": "password-f2"})
	if call := fixture.call(t, http.MethodPost, "/auth/password", changeBody, session, session.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("password change status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodGet, "/auth/me", nil, other, "", nil); call.status != http.StatusUnauthorized {
		t.Fatalf("other session after password change status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodGet, "/auth/me", nil, session, "", nil); call.status != http.StatusOK {
		t.Fatalf("current session after password change status=%d", call.status)
	}
	fixture.login(t, "root", "password-f2")

	newSalt := currentDEKSalt(t, fixture, session)
	if oldSalt == newSalt {
		t.Fatal("密码变更必须重包 DEK 信封")
	}
}

func currentDEKSalt(t *testing.T, fixture *accountFixture, session *accountTestSession) string {
	t.Helper()
	call := fixture.call(t, http.MethodGet, "/auth/dek", nil, session, "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("dek get status=%d body=%v", call.status, call.body)
	}
	salt, err := base64.StdEncoding.DecodeString(call.body["kdf_salt"].(string))
	if err != nil || len(salt) == 0 {
		t.Fatalf("dek salt invalid: %v", err)
	}
	return call.body["kdf_salt"].(string)
}

func TestAccountHTTPRecoveryReset(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	fixture.initSuperadmin(t, "root", "password-g1")

	resetBody, _, _ := envelopeBody(t, "password-g2")
	resetBody = mergeBody(resetBody, map[string]any{"username": "root", "recovery_key": "WRONG-KEY", "new_password": "password-g2"})
	if call := fixture.call(t, http.MethodPost, "/auth/recovery/reset", resetBody, nil, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("wrong recovery key status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodPost, "/auth/recovery/reset", resetBody, nil, "", nil); call.status != http.StatusTooManyRequests {
		t.Fatalf("recovery throttle status=%d", call.status)
	}

	other := newAccountFixture(t, AuthOn)
	otherSession, otherKey := other.initSuperadmin(t, "root", "password-g1")
	resetBody, _, _ = envelopeBody(t, "password-g2")
	resetBody = mergeBody(resetBody, map[string]any{"username": "root", "recovery_key": otherKey, "new_password": "password-g2"})
	if call := other.call(t, http.MethodPost, "/auth/recovery/reset", resetBody, nil, "", nil); call.status != http.StatusOK {
		t.Fatalf("recovery reset status=%d", call.status)
	}
	if call := other.call(t, http.MethodGet, "/auth/me", nil, otherSession, "", nil); call.status != http.StatusUnauthorized {
		t.Fatalf("session after recovery reset status=%d", call.status)
	}
	other.login(t, "root", "password-g2")
	if call := other.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "root", "password": "password-g1"}, nil, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("old password after recovery status=%d", call.status)
	}
}

func TestAccountHTTPDeviceEnrollment(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	session, _ := fixture.initSuperadmin(t, "root", "password-h1")

	if call := fixture.call(t, http.MethodPost, "/auth/devices/enroll-code", nil, session, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("enroll-code without csrf status=%d", call.status)
	}
	call := fixture.call(t, http.MethodPost, "/auth/devices/enroll-code", nil, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("enroll-code status=%d body=%v", call.status, call.body)
	}
	enrollCode := call.body["code"].(string)

	call = fixture.call(t, http.MethodPost, "/auth/devices/enroll", map[string]any{"code": enrollCode, "name": "laptop", "kind": "desktop"}, nil, "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("enroll status=%d body=%v", call.status, call.body)
	}
	deviceID := call.body["device"].(map[string]any)["id"].(string)
	if call := fixture.call(t, http.MethodPost, "/auth/devices/enroll", map[string]any{"code": enrollCode, "name": "again", "kind": "desktop"}, nil, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("enroll code reuse status=%d", call.status)
	}

	deviceSession := loginWithDevice(t, fixture, "root", "password-h1", deviceID)
	if call := fixture.call(t, http.MethodGet, "/auth/me", nil, deviceSession, "", nil); call.status != http.StatusOK {
		t.Fatalf("device session status=%d", call.status)
	}
	call = fixture.call(t, http.MethodGet, "/auth/devices", nil, session, "", nil)
	if call.status != http.StatusOK || len(call.body["devices"].([]any)) != 1 {
		t.Fatalf("device list status=%d body=%v", call.status, call.body)
	}

	if call := fixture.call(t, http.MethodDelete, "/auth/devices/"+deviceID, nil, session, session.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("device revoke status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodGet, "/auth/me", nil, deviceSession, "", nil); call.status != http.StatusUnauthorized {
		t.Fatalf("session on revoked device status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodDelete, "/auth/devices/"+deviceID, nil, session, session.csrf, nil); call.status != http.StatusForbidden {
		t.Fatalf("double revoke status=%d", call.status)
	}
}

func loginWithDevice(t *testing.T, fixture *accountFixture, username, password, deviceID string) *accountTestSession {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"username": username, "password": password, "device_id": deviceID})
	request, err := http.NewRequest(http.MethodPost, fixture.http.URL+"/auth/login", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := fixture.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("device login status=%d", response.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return captureSession(t, accountCall{status: response.StatusCode, body: body, cookies: response.Cookies()})
}

func TestAccountHTTPAuthOffBounds(t *testing.T) {
	fixture := newAccountFixture(t, AuthOff)

	if call := fixture.call(t, http.MethodGet, "/auth/status", nil, nil, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("auth=off status route status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "x", "password": "y"}, nil, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("auth=off login status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodGet, "/admin/users", nil, nil, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("auth=off admin status=%d", call.status)
	}

	status, body := postRPC(t, fixture.client, fixture.http.URL+"/rpc", "app_info", nil)
	if status != http.StatusOK || !body.OK {
		t.Fatalf("auth=off legacy rpc status=%d body=%v", status, body)
	}

	if err := ValidateAuthOffBounds(context.Background(), fixture.accounts); err != nil {
		t.Fatalf("auth=off without users must start: %v", err)
	}
	createUserViaCore(t, fixture, "root", "password-i1")
	if err := ValidateAuthOffBounds(context.Background(), fixture.accounts); err == nil {
		t.Fatal("auth=off with any user present must refuse startup")
	}
}

func TestAccountHTTPLoopbackExplicit(t *testing.T) {
	fixture := newAccountFixture(t, AuthLoopback)

	status, body := postRPC(t, fixture.client, fixture.http.URL+"/rpc", "app_info", nil)
	if status != http.StatusOK || !body.OK {
		t.Fatalf("loopback legacy rpc status=%d body=%v", status, body)
	}
	call := fixture.call(t, http.MethodGet, "/auth/status", nil, nil, "", nil)
	if call.status != http.StatusOK || call.body["auth"] != AuthLoopback {
		t.Fatalf("loopback account status=%d body=%v", call.status, call.body)
	}
	if call := fixture.call(t, http.MethodGet, "/auth/me", nil, nil, "", nil); call.status != http.StatusUnauthorized {
		t.Fatalf("loopback me without session status=%d", call.status)
	}
}

func TestAccountHTTPSessionAcceptedOnLegacyRPC(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	session, _ := fixture.initSuperadmin(t, "root", "password-j1")

	request, err := http.NewRequest(http.MethodPost, fixture.http.URL+"/rpc", strings.NewReader(`{"cmd":"app_info","args":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(session.cookie)
	response, err := fixture.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("session on legacy rpc status=%d", response.StatusCode)
	}

	status, _ := postRPC(t, fixture.client, fixture.http.URL+"/rpc", "app_info", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("no credential status=%d", status)
	}
	status, body := postRPC(t, fixture.client, fixture.http.URL+"/rpc", "app_info", map[string]string{TokenHeader: "secret"})
	if status != http.StatusOK || !body.OK {
		t.Fatalf("token-era staging path status=%d body=%v", status, body)
	}
}

func TestAccountHTTPNoRawDEK(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	code, err := fixture.accounts.GenerateInitCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	envelopes, _, dek := envelopeBody(t, "password-k1")
	initBody := mergeBody(envelopes, map[string]any{"code": code, "username": "root", "password": "password-k1"})
	call := fixture.call(t, http.MethodPost, "/auth/init", initBody, nil, "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("init status=%d body=%v", call.status, call.body)
	}
	session := captureSession(t, call)

	call = fixture.call(t, http.MethodGet, "/auth/dek", nil, session, "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("dek get status=%d body=%v", call.status, call.body)
	}
	raw := base64.StdEncoding.EncodeToString(dek)
	encoded, _ := json.Marshal(call.body)
	if strings.Contains(string(encoded), raw) {
		t.Fatal("响应不得包含明文 DEK")
	}
	if strings.Contains(string(encoded), "password_hash") {
		t.Fatal("响应不得包含密码哈希")
	}
	envelope, err := base64.StdEncoding.DecodeString(call.body["dek_envelope"].(string))
	if err != nil || len(envelope) == 0 {
		t.Fatalf("dek envelope invalid: %v", err)
	}
}

func TestAccountHTTPDEKUploadOnce(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	adminSession, _ := fixture.initSuperadmin(t, "root", "password-l1")

	call := fixture.call(t, http.MethodPost, "/admin/users", map[string]any{"username": "dave", "password": "password-l2"}, adminSession, adminSession.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("admin create status=%d body=%v", call.status, call.body)
	}
	dave := fixture.login(t, "dave", "password-l2")

	if call := fixture.call(t, http.MethodGet, "/auth/dek", nil, dave, "", nil); call.status != http.StatusNotFound {
		t.Fatalf("dek before upload status=%d", call.status)
	}
	upload, _, _ := envelopeBody(t, "password-l2")
	if call := fixture.call(t, http.MethodPost, "/auth/dek", upload, dave, dave.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("dek upload status=%d body=%v", call.status, call.body)
	}
	if call := fixture.call(t, http.MethodPost, "/auth/dek", upload, dave, dave.csrf, nil); call.status != http.StatusConflict {
		t.Fatalf("dek re-upload status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodGet, "/auth/dek", nil, dave, "", nil); call.status != http.StatusOK {
		t.Fatalf("dek after upload status=%d", call.status)
	}
}

func TestAccountHTTPConfigValidation(t *testing.T) {
	config := testConfig(t, false)
	config.Options.Auth = AuthOn
	config.Tokens = nil
	config.Accounts = nil
	if _, err := New(config); err == nil {
		t.Fatal("auth=on without tokens and accounts must fail")
	}

	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	config = testConfig(t, false)
	config.Options.Auth = AuthOn
	config.Tokens = nil
	config.Accounts = account.New(database.DB())
	server, err := New(config)
	if err != nil {
		t.Fatalf("auth=on with accounts only must start: %v", err)
	}
	_ = server.Close()
}
