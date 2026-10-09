package fleetserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type httpFixture struct {
	service  *Service
	accounts *account.Accounts
	http     *httptest.Server
	client   *http.Client
}

func newHTTPFixture(t *testing.T, authOff bool, options ...Option) *httpFixture {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	accounts := account.New(database.DB(), account.WithTOTPKeyFile(filepath.Join(t.TempDir(), "totp.key")))
	service, err := New(Config{DB: database.DB(), Accounts: accounts, AuthOff: authOff}, options...)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(service.Handler())
	t.Cleanup(httpServer.Close)
	return &httpFixture{service: service, accounts: accounts, http: httpServer, client: &http.Client{}}
}

type httpSession struct {
	cookie *http.Cookie
	csrf   string
	user   *account.User
}

func (f *httpFixture) session(t *testing.T, user *account.User) *httpSession {
	t.Helper()
	token, session, err := f.accounts.IssueSession(context.Background(), user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	return &httpSession{
		cookie: &http.Cookie{Name: sessionCookieName, Value: token},
		csrf:   fleetCSRFToken(session.ID),
		user:   user,
	}
}

type httpCall struct {
	status  int
	body    map[string]any
	cookies []*http.Cookie
}

func (f *httpFixture) call(t *testing.T, method, path string, body any, session *httpSession, csrf string) httpCall {
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
	if session != nil {
		request.AddCookie(session.cookie)
	}
	if csrf != "" {
		request.Header.Set(csrfHeaderName, csrf)
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
	} else {
		_, _ = io.Copy(io.Discard, response.Body)
	}
	return httpCall{status: response.StatusCode, body: decoded, cookies: response.Cookies()}
}

func (f *httpFixture) createUser(t *testing.T, username string) *account.User {
	t.Helper()
	user, err := f.accounts.CreateUser(context.Background(), username, "", "password-"+username)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func totpCodeForTest(t *testing.T, secretBase32 string, unixSeconds int64) string {
	t.Helper()
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretBase32)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha1.New, secret)
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(unixSeconds)/30)
	_, _ = mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1_000_000)
}

func (f *httpFixture) createSuperadmin(t *testing.T, username string) *account.User {
	t.Helper()
	code, err := f.accounts.GenerateInitCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	user, err := f.accounts.InitSuperadmin(context.Background(), code, username, "password-"+username)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func TestFleetHTTPOffMode(t *testing.T) {
	fixture := newHTTPFixture(t, true)
	root := fixture.createSuperadmin(t, "root")
	session := fixture.session(t, root)

	calls := []httpCall{
		fixture.call(t, http.MethodPost, "/device/enroll", map[string]any{"code": "x", "name": "n"}, nil, ""),
		fixture.call(t, http.MethodPost, "/device/enroll-codes", map[string]any{}, session, session.csrf),
		fixture.call(t, http.MethodGet, "/fleet/devices", nil, session, ""),
		fixture.call(t, http.MethodGet, "/fleet/base-urls", nil, session, ""),
		fixture.call(t, http.MethodPut, "/fleet/base-urls", map[string]any{"base_urls": []any{}}, session, session.csrf),
		fixture.call(t, http.MethodPost, "/fleet/devices/d1/revoke", map[string]any{}, session, session.csrf),
	}
	for i, call := range calls {
		if call.status != http.StatusForbidden {
			t.Fatalf("call %d status=%d body=%v", i, call.status, call.body)
		}
	}
}

// mfa_required 策略下未绑定会话在 fleet 面同样被锁: 绑定只能走主服务 /auth/totp*。
func TestFleetHTTPMFAEnrollLock(t *testing.T) {
	fixture := newHTTPFixture(t, false)
	alice := fixture.createUser(t, "alice")
	session := fixture.session(t, alice)

	call := fixture.call(t, http.MethodGet, "/fleet/devices", nil, session, "")
	if call.status != http.StatusOK {
		t.Fatalf("default status=%d body=%v", call.status, call.body)
	}
	if err := fixture.accounts.SetMFARequired(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	call = fixture.call(t, http.MethodGet, "/fleet/devices", nil, session, "")
	if call.status != http.StatusForbidden || call.body["error"].(map[string]any)["code"] != "mfa_enrollment_required" {
		t.Fatalf("locked status=%d body=%v", call.status, call.body)
	}

	secret, _, err := fixture.accounts.BeginTOTPSetup(context.Background(), alice.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	code := totpCodeForTest(t, secret, time.Now().Unix())
	if _, err := fixture.accounts.ConfirmTOTPSetup(context.Background(), alice.ID, code); err != nil {
		t.Fatal(err)
	}
	call = fixture.call(t, http.MethodGet, "/fleet/devices", nil, session, "")
	if call.status != http.StatusOK {
		t.Fatalf("unlocked after binding status=%d body=%v", call.status, call.body)
	}
}

func TestFleetHTTPEnrollFlow(t *testing.T) {
	fixture := newHTTPFixture(t, false)
	root := fixture.createSuperadmin(t, "root")
	alice := fixture.createUser(t, "alice")
	aliceSession := fixture.session(t, alice)

	call := fixture.call(t, http.MethodPost, "/device/enroll-codes", map[string]any{}, aliceSession, aliceSession.csrf)
	if call.status != http.StatusOK {
		t.Fatalf("issue status=%d body=%v", call.status, call.body)
	}
	code, _ := call.body["code"].(string)
	if code == "" || call.body["expires_at"].(float64) <= 0 {
		t.Fatalf("issue body=%v", call.body)
	}

	call = fixture.call(t, http.MethodPost, "/device/enroll", map[string]any{"code": code, "name": "agent-host", "platform": "linux", "app_version": "1.0.0"}, nil, "")
	if call.status != http.StatusOK {
		t.Fatalf("enroll status=%d body=%v", call.status, call.body)
	}
	deviceID, _ := call.body["device_id"].(string)
	secret, _ := call.body["secret"].(string)
	if deviceID == "" || secret == "" || call.body["metrics_interval_ms"].(float64) != DefaultMetricsIntervalMS ||
		call.body["desired_autostart"] != true || call.body["terminal_enabled"] != true {
		t.Fatalf("enroll body=%v", call.body)
	}
	if _, ok := call.body["base_urls"].([]any); !ok {
		t.Fatalf("enroll base_urls missing: %v", call.body)
	}

	call = fixture.call(t, http.MethodPost, "/device/enroll", map[string]any{"code": code, "name": "again"}, nil, "")
	if call.status != http.StatusForbidden {
		t.Fatalf("replay status=%d body=%v", call.status, call.body)
	}

	call = fixture.call(t, http.MethodGet, "/fleet/devices", nil, aliceSession, "")
	if call.status != http.StatusOK {
		t.Fatalf("list status=%d body=%v", call.status, call.body)
	}
	devices := call.body["devices"].([]any)
	if len(devices) != 1 {
		t.Fatalf("devices=%v", devices)
	}
	device := devices[0].(map[string]any)
	if device["id"] != deviceID || device["kind"] != agentDeviceKind || device["owner"] != nil {
		t.Fatalf("device view=%v", device)
	}
	agent := device["agent"].(map[string]any)
	if agent["platform"] != "linux" || agent["desired_autostart"] != true {
		t.Fatalf("agent view=%v", agent)
	}

	rootSession := fixture.session(t, root)
	call = fixture.call(t, http.MethodGet, "/fleet/devices", nil, rootSession, "")
	if call.status != http.StatusOK {
		t.Fatalf("admin list status=%d", call.status)
	}
	adminDevices := call.body["devices"].([]any)
	if len(adminDevices) != 1 {
		t.Fatalf("admin devices=%v", adminDevices)
	}
	adminDevice := adminDevices[0].(map[string]any)
	owner := adminDevice["owner"].(map[string]any)
	if owner["username"] != "alice" {
		t.Fatalf("admin owner view=%v", owner)
	}
}

func TestFleetHTTPEnrollThrottle(t *testing.T) {
	fixture := newHTTPFixture(t, false)

	call := fixture.call(t, http.MethodPost, "/device/enroll", map[string]any{"code": "wrong", "name": "n"}, nil, "")
	if call.status != http.StatusForbidden {
		t.Fatalf("wrong code status=%d body=%v", call.status, call.body)
	}
	call = fixture.call(t, http.MethodPost, "/device/enroll", map[string]any{"code": "wrong", "name": "n"}, nil, "")
	if call.status != http.StatusTooManyRequests {
		t.Fatalf("backoff status=%d body=%v", call.status, call.body)
	}
}

func TestFleetHTTPAuthAndCSRF(t *testing.T) {
	fixture := newHTTPFixture(t, false)
	alice := fixture.createUser(t, "alice")
	aliceSession := fixture.session(t, alice)

	if call := fixture.call(t, http.MethodGet, "/fleet/devices", nil, nil, ""); call.status != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodPost, "/device/enroll-codes", map[string]any{}, aliceSession, ""); call.status != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d", call.status)
	} else if message, _ := call.body["error"].(map[string]any)["message"].(string); !strings.Contains(message, "CSRF") {
		t.Fatalf("missing csrf message=%q, want CSRF keyword", message)
	}
	if call := fixture.call(t, http.MethodPost, "/device/enroll-codes", map[string]any{}, aliceSession, "bad-csrf"); call.status != http.StatusForbidden {
		t.Fatalf("bad csrf status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodPost, "/device/enroll-codes", map[string]any{}, aliceSession, aliceSession.csrf); call.status != http.StatusOK {
		t.Fatalf("valid csrf status=%d body=%v", call.status, call.body)
	}
}

func TestFleetHTTPBaseURLs(t *testing.T) {
	fixture := newHTTPFixture(t, false)
	root := fixture.createSuperadmin(t, "root")
	alice := fixture.createUser(t, "alice")
	aliceSession := fixture.session(t, alice)
	rootSession := fixture.session(t, root)

	put := map[string]any{"base_urls": []any{map[string]any{"url": "http://public.example.com"}}}
	if call := fixture.call(t, http.MethodPut, "/fleet/base-urls", put, aliceSession, aliceSession.csrf); call.status != http.StatusForbidden {
		t.Fatalf("user put status=%d body=%v", call.status, call.body)
	}

	put = map[string]any{"base_urls": []any{
		map[string]any{"url": "https://fleet.example.com/"},
		map[string]any{"url": "http://192.168.1.10:9000"},
	}}
	call := fixture.call(t, http.MethodPut, "/fleet/base-urls", put, rootSession, rootSession.csrf)
	if call.status != http.StatusOK {
		t.Fatalf("admin put status=%d body=%v", call.status, call.body)
	}
	urls := call.body["base_urls"].([]any)
	if len(urls) != 2 || urls[0].(map[string]any)["url"] != "https://fleet.example.com" {
		t.Fatalf("put body=%v", call.body)
	}

	call = fixture.call(t, http.MethodGet, "/fleet/base-urls", nil, aliceSession, "")
	if call.status != http.StatusOK {
		t.Fatalf("user get status=%d", call.status)
	}
	if got := call.body["base_urls"].([]any); len(got) != 2 {
		t.Fatalf("get body=%v", call.body)
	}

	bad := map[string]any{"base_urls": []any{map[string]any{"url": "http://public.example.com"}}}
	if call := fixture.call(t, http.MethodPut, "/fleet/base-urls", bad, rootSession, rootSession.csrf); call.status != http.StatusForbidden {
		t.Fatalf("insecure public http status=%d body=%v", call.status, call.body)
	}
}

func TestFleetHTTPRoleIsolation(t *testing.T) {
	fixture := newHTTPFixture(t, false)
	alice := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")
	aliceSession := fixture.session(t, alice)
	bobSession := fixture.session(t, bob)

	call := fixture.call(t, http.MethodPost, "/device/enroll-codes", map[string]any{}, aliceSession, aliceSession.csrf)
	if call.status != http.StatusOK {
		t.Fatalf("issue status=%d", call.status)
	}
	code := call.body["code"].(string)
	call = fixture.call(t, http.MethodPost, "/device/enroll", map[string]any{"code": code, "name": "alice-host"}, nil, "")
	if call.status != http.StatusOK {
		t.Fatalf("enroll status=%d body=%v", call.status, call.body)
	}
	deviceID := call.body["device_id"].(string)

	call = fixture.call(t, http.MethodPost, "/fleet/devices/"+deviceID+"/revoke", map[string]any{}, bobSession, bobSession.csrf)
	if call.status != http.StatusForbidden {
		t.Fatalf("bob revoke status=%d body=%v", call.status, call.body)
	}
	call = fixture.call(t, http.MethodPost, "/fleet/devices/"+deviceID+"/autostart", map[string]any{"desired": false}, bobSession, bobSession.csrf)
	if call.status != http.StatusForbidden {
		t.Fatalf("bob autostart status=%d body=%v", call.status, call.body)
	}
	call = fixture.call(t, http.MethodGet, "/fleet/devices/"+deviceID+"/metrics", nil, bobSession, "")
	if call.status != http.StatusForbidden {
		t.Fatalf("bob metrics status=%d body=%v", call.status, call.body)
	}

	call = fixture.call(t, http.MethodPost, "/fleet/devices/"+deviceID+"/autostart", map[string]any{"desired": false}, aliceSession, aliceSession.csrf)
	if call.status != http.StatusOK {
		t.Fatalf("alice autostart status=%d body=%v", call.status, call.body)
	}
	call = fixture.call(t, http.MethodPost, "/fleet/devices/"+deviceID+"/revoke", map[string]any{}, aliceSession, aliceSession.csrf)
	if call.status != http.StatusOK {
		t.Fatalf("alice revoke status=%d body=%v", call.status, call.body)
	}

	call = fixture.call(t, http.MethodGet, "/fleet/devices/"+deviceID+"/metrics", nil, aliceSession, "")
	if call.status != http.StatusForbidden {
		t.Fatalf("revoked device metrics status=%d body=%v", call.status, call.body)
	}
}

func TestFleetHTTPDeviceListStateDigest(t *testing.T) {
	fixture := newHTTPFixture(t, false)
	alice := fixture.createUser(t, "alice")
	aliceSession := fixture.session(t, alice)

	call := fixture.call(t, http.MethodPost, "/device/enroll-codes", map[string]any{}, aliceSession, aliceSession.csrf)
	code, _ := call.body["code"].(string)
	call = fixture.call(t, http.MethodPost, "/device/enroll", map[string]any{"code": code, "name": "agent-host", "platform": "linux"}, nil, "")
	if call.status != http.StatusOK {
		t.Fatalf("enroll status=%d body=%v", call.status, call.body)
	}
	deviceID, _ := call.body["device_id"].(string)

	agentViewOf := func() map[string]any {
		call = fixture.call(t, http.MethodGet, "/fleet/devices", nil, aliceSession, "")
		if call.status != http.StatusOK {
			t.Fatalf("list status=%d body=%v", call.status, call.body)
		}
		devices := call.body["devices"].([]any)
		if len(devices) != 1 {
			t.Fatalf("devices=%v", devices)
		}
		return devices[0].(map[string]any)["agent"].(map[string]any)
	}

	// 控制通道未注册 (设备离线) 时不下发摘要
	if agent := agentViewOf(); agent["state_digest"] != nil {
		t.Fatalf("offline agent view carries state_digest: %v", agent)
	}

	// 控制通道 hello 上报摘要后随列表下发 (浏览器 supervisor hello 的唯一来源)
	_, controlServer := wsPair(t)
	fixture.service.registry.RegisterControl(deviceID, controlServer, "digest-abc")
	if agent := agentViewOf(); agent["state_digest"] != "digest-abc" {
		t.Fatalf("agent view state_digest=%v", agent)
	}
}
