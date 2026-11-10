package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func newRealIPFixture(t *testing.T, trusted []string, trustForwardedFor bool) *accountFixture {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	config := testConfig(t, false)
	config.Options.Auth = AuthOn
	config.Options.TrustedProxyCIDRs = trusted
	config.Options.TrustForwardedFor = trustForwardedFor
	accounts := account.New(database.GORM(), account.WithTOTPKeyFile(filepath.Join(t.TempDir(), "totp.key")), withTestTOTPClock())
	config.Accounts = accounts
	server, httpServer := newTestHTTP(t, config)
	return &accountFixture{server: server, http: httpServer, accounts: accounts, db: database.DB(), client: &http.Client{}}
}

func (f *accountFixture) loginVia(t *testing.T, password, xff string) accountCall {
	t.Helper()
	headers := map[string]string{}
	if xff != "" {
		headers["X-Forwarded-For"] = xff
	}
	return f.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "root", "password": password}, nil, "", headers)
}

// failUntilThrottled returns once the source is rate limited; it fails the test if that never happens.
func (f *accountFixture) failUntilThrottled(t *testing.T, xff string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		call := f.loginVia(t, "wrong-password", xff)
		if call.status == http.StatusTooManyRequests {
			return
		}
		if call.status != http.StatusForbidden {
			t.Fatalf("failed login status = %d body = %v", call.status, call.body)
		}
	}
	t.Fatal("login was never rate limited")
}

func TestRealIPTrustedProxySeparatesLoginThrottle(t *testing.T) {
	fixture := newRealIPFixture(t, []string{"127.0.0.1/32"}, true)
	fixture.createUser(t, "root", "password-a1")

	fixture.failUntilThrottled(t, "203.0.113.9")
	if call := fixture.loginVia(t, "password-a1", "203.0.113.9"); call.status != http.StatusTooManyRequests {
		t.Fatalf("throttled client login status = %d", call.status)
	}
	if call := fixture.loginVia(t, "password-a1", "198.51.100.4"); call.status != http.StatusOK {
		t.Fatalf("other client behind the proxy login status = %d body = %v", call.status, call.body)
	}
}

func TestRealIPUntrustedProxyIgnoresForwardedFor(t *testing.T) {
	fixture := newRealIPFixture(t, nil, true)
	fixture.createUser(t, "root", "password-a1")

	fixture.failUntilThrottled(t, "203.0.113.9")
	if call := fixture.loginVia(t, "password-a1", "198.51.100.4"); call.status != http.StatusTooManyRequests {
		t.Fatalf("rotating X-Forwarded-For escaped the throttle: status = %d", call.status)
	}
}

func TestRealIPSessionOriginUsesForwardedClient(t *testing.T) {
	fixture := newRealIPFixture(t, []string{"127.0.0.1/32"}, true)
	fixture.createUser(t, "root", "password-a1")

	call := fixture.loginVia(t, "password-a1", "198.51.100.77, 127.0.0.1")
	if call.status != http.StatusOK {
		t.Fatalf("login status = %d body = %v", call.status, call.body)
	}
	session := captureSession(t, call)
	listed := fixture.call(t, http.MethodGet, "/auth/sessions", nil, session, "", nil)
	rows, _ := listed.body["sessions"].([]any)
	if listed.status != http.StatusOK || len(rows) != 1 {
		t.Fatalf("session list = %d %v", listed.status, listed.body)
	}
	if origin, _ := rows[0].(map[string]any)["origin"].(string); !strings.Contains(origin, "198.51.100.77") {
		t.Fatalf("session origin = %q, want the forwarded client address", origin)
	}
}

func enrollAttempt(t *testing.T, base, xff string) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, base+"/device/enroll", strings.NewReader(`{"code":"bad","name":"n"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Forwarded-For", xff)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

func TestRealIPFleetEnrollThrottleFollowsForwardedClient(t *testing.T) {
	config := testConfig(t, false)
	config.Fleet = newTestFleet(t, false)
	config.Options.TrustedProxyCIDRs = []string{"127.0.0.1/32"}
	config.Options.TrustForwardedFor = true
	_, httpServer := newTestHTTP(t, config)

	throttled := false
	for i := 0; i < 100 && !throttled; i++ {
		throttled = enrollAttempt(t, httpServer.URL, "203.0.113.9") == http.StatusTooManyRequests
	}
	if !throttled {
		t.Fatal("enroll was never rate limited")
	}
	if status := enrollAttempt(t, httpServer.URL, "198.51.100.4"); status == http.StatusTooManyRequests {
		t.Fatal("a different client behind the proxy was throttled")
	}
}

func TestRealIPTransportGuardResolvesBeforePreflight(t *testing.T) {
	config := testConfig(t, false)
	config.Options.TrustedProxyCIDRs = []string{"127.0.0.1/32"}
	config.Options.TrustForwardedFor = true
	server, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	handler := server.transportGuard(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = clientIP(r)
	}))
	request := httptest.NewRequest(http.MethodGet, "/x", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	request.Host = "127.0.0.1"
	request.Header.Set("X-Forwarded-For", "203.0.113.9")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if seen != "203.0.113.9" {
		t.Fatalf("clientIP behind guard = %q", seen)
	}
}

func TestRealIPTrustedCIDRsAloneIgnoreForwardedFor(t *testing.T) {
	fixture := newRealIPFixture(t, []string{"127.0.0.1/32"}, false)
	fixture.createUser(t, "root", "password-a1")

	fixture.failUntilThrottled(t, "203.0.113.9")
	if call := fixture.loginVia(t, "password-a1", "198.51.100.4"); call.status != http.StatusTooManyRequests {
		t.Fatalf("rotating X-Forwarded-For escaped the throttle: status = %d", call.status)
	}
}

func TestRealIPSessionOriginIsPeerWithoutForwardedForOptIn(t *testing.T) {
	fixture := newRealIPFixture(t, []string{"127.0.0.1/32"}, false)
	fixture.createUser(t, "root", "password-a1")

	call := fixture.loginVia(t, "password-a1", "198.51.100.77")
	if call.status != http.StatusOK {
		t.Fatalf("login status = %d body = %v", call.status, call.body)
	}
	session := captureSession(t, call)
	listed := fixture.call(t, http.MethodGet, "/auth/sessions", nil, session, "", nil)
	rows, _ := listed.body["sessions"].([]any)
	if listed.status != http.StatusOK || len(rows) != 1 {
		t.Fatalf("session list = %d %v", listed.status, listed.body)
	}
	origin, _ := rows[0].(map[string]any)["origin"].(string)
	if strings.Contains(origin, "198.51.100.77") || !strings.Contains(origin, "127.0.0.1") {
		t.Fatalf("session origin = %q, want the peer address", origin)
	}
}

func TestRealIPForwardedProtoStillTrustedWithoutForwardedForOptIn(t *testing.T) {
	fixture := newRealIPFixture(t, []string{"127.0.0.1/32"}, false)
	fixture.createUser(t, "root", "password-a1")

	request, err := http.NewRequest(http.MethodPost, fixture.http.URL+"/auth/login", strings.NewReader(`{"username":"root","password":"password-a1"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Forwarded-Proto", "https")
	response, err := fixture.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", response.StatusCode)
	}
	cookies := response.Cookies()
	if len(cookies) == 0 {
		t.Fatal("login set no cookies")
	}
	for _, cookie := range cookies {
		if !cookie.Secure {
			t.Fatalf("cookie %s = %+v, want Secure", cookie.Name, cookie)
		}
	}
}

// One address spraying throwaway usernames is throttled by the per-address
// bucket even though each username+address key sees a single failure.
func TestLoginThrottlePerAddressAcrossUsernames(t *testing.T) {
	fixture := newRealIPFixture(t, []string{"127.0.0.1/32"}, true)
	fixture.createUser(t, "root", "password-a1")
	headers := map[string]string{"X-Forwarded-For": "203.0.113.9"}
	throttled := false
	for i := 0; i < 200 && !throttled; i++ {
		call := fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "spray-" + strconv.Itoa(i), "password": "wrong-password"}, nil, "", headers)
		switch call.status {
		case http.StatusTooManyRequests:
			throttled = true
		case http.StatusForbidden:
		default:
			t.Fatalf("spray login status = %d body = %v", call.status, call.body)
		}
	}
	if !throttled {
		t.Fatal("an address spraying distinct usernames was never throttled")
	}
	if call := fixture.loginVia(t, "password-a1", "203.0.113.9"); call.status != http.StatusTooManyRequests {
		t.Fatalf("throttled address login status = %d", call.status)
	}
	if call := fixture.loginVia(t, "password-a1", "198.51.100.4"); call.status != http.StatusOK {
		t.Fatalf("other address login status = %d body = %v", call.status, call.body)
	}
}

func loginDeviceCookie(t *testing.T, call accountCall) string {
	t.Helper()
	for _, cookie := range call.cookies {
		if cookie.Name == loginDeviceCookieName {
			return cookie.Value
		}
	}
	t.Fatalf("login response is missing the login device cookie: %v", call.cookies)
	return ""
}

func (f *accountFixture) loginWithDevice(t *testing.T, password, xff, device string) accountCall {
	t.Helper()
	headers := map[string]string{"X-Forwarded-For": xff}
	if device != "" {
		headers["Cookie"] = loginDeviceCookieName + "=" + device
	}
	return f.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "root", "password": password}, nil, "", headers)
}

// backOffRoot spreads failed guesses for root over many addresses until the
// per-username bucket backs it off.
func (f *accountFixture) backOffRoot(t *testing.T, third int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		call := f.loginVia(t, "wrong-password", "203.0."+strconv.Itoa(third)+"."+strconv.Itoa(i+1))
		switch call.status {
		case http.StatusTooManyRequests:
			return
		case http.StatusForbidden:
		default:
			t.Fatalf("distributed login status = %d body = %v", call.status, call.body)
		}
	}
	t.Fatal("a username guessed from many addresses was never throttled")
}

// Failures against one username from many addresses back the username off for
// clients without a login device token, so distributed guessing cannot bypass
// the per-address buckets. A client holding the token from an earlier login
// is exempt from any address, so the guessing cannot lock the user out even
// after an address change.
func TestLoginThrottlePerUsernameAcrossAddresses(t *testing.T) {
	fixture := newRealIPFixture(t, []string{"127.0.0.1/32"}, true)
	fixture.createUser(t, "root", "password-a1")
	fixture.createUser(t, "other", "password-b1")
	first := fixture.loginVia(t, "password-a1", "198.51.100.10")
	if first.status != http.StatusOK {
		t.Fatalf("first login status = %d body = %v", first.status, first.body)
	}
	device := loginDeviceCookie(t, first)

	fixture.backOffRoot(t, 113)
	if call := fixture.loginWithDevice(t, "password-a1", "198.51.100.4", ""); call.status != http.StatusTooManyRequests {
		t.Fatalf("backed-off username login without a device token status = %d", call.status)
	}
	if call := fixture.loginWithDevice(t, "password-a1", "192.0.2.77", device); call.status != http.StatusOK {
		t.Fatalf("login with a device token from a new address status = %d body = %v", call.status, call.body)
	}
	// The success leaves the per-username streak in place for strangers.
	if call := fixture.loginWithDevice(t, "password-a1", "198.51.100.5", ""); call.status != http.StatusTooManyRequests {
		t.Fatalf("a successful login reset the per-username backoff: status = %d", call.status)
	}
	// A token is bound to its username.
	other := fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "other", "password": "password-b1"}, nil, "", map[string]string{"X-Forwarded-For": "198.51.100.4"})
	if other.status != http.StatusOK {
		t.Fatalf("unrelated username login status = %d body = %v", other.status, other.body)
	}
	otherDevice := loginDeviceCookie(t, other)
	if call := fixture.loginWithDevice(t, "password-a1", "198.51.100.6", otherDevice); call.status != http.StatusTooManyRequests {
		t.Fatalf("another account's device token exempted root: status = %d", call.status)
	}
	if call := fixture.loginWithDevice(t, "password-a1", "198.51.100.6", device+"x"); call.status != http.StatusTooManyRequests {
		t.Fatalf("a tampered device token exempted root: status = %d", call.status)
	}
}

// Regression for the sustained username backoff: a trickle of failures from a
// dozen addresses keeps root backed off for a day, yet root logs in with its
// device token from an address it never used.
func TestLoginThrottleSustainedUsernameBackoffSparesDeviceToken(t *testing.T) {
	fixture := newRealIPFixture(t, []string{"127.0.0.1/32"}, true)
	fixture.createUser(t, "root", "password-a1")
	first := fixture.loginVia(t, "password-a1", "198.51.100.10")
	if first.status != http.StatusOK {
		t.Fatalf("first login status = %d body = %v", first.status, first.body)
	}
	device := loginDeviceCookie(t, first)
	now := time.Now()
	fixture.server.accountThrottle.loginUser.SetNowForTest(func() time.Time { return now })
	for round := 0; round < 24*12; round++ {
		if round > 0 {
			now = now.Add(5 * time.Minute)
		}
		for i := 0; i < 12; i++ {
			fixture.loginVia(t, "wrong-password", "10."+strconv.Itoa(round/200)+"."+strconv.Itoa(round%200)+"."+strconv.Itoa(i+1))
		}
	}
	if call := fixture.loginWithDevice(t, "password-a1", "192.0.2.200", ""); call.status != http.StatusTooManyRequests {
		t.Fatalf("sustained guessing did not back the username off: status = %d", call.status)
	}
	if call := fixture.loginWithDevice(t, "password-a1", "192.0.2.201", device); call.status != http.StatusOK {
		t.Fatalf("device token login during sustained guessing status = %d body = %v", call.status, call.body)
	}
}

// The login device cookie only marks the client for the throttle: it is
// scoped to /auth, HttpOnly and Lax, and authenticates nothing.
func TestLoginDeviceCookieGrantsNoAccess(t *testing.T) {
	fixture := newRealIPFixture(t, []string{"127.0.0.1/32"}, true)
	fixture.createUser(t, "root", "password-a1")
	login := fixture.loginVia(t, "password-a1", "198.51.100.10")
	var device *http.Cookie
	for _, cookie := range login.cookies {
		if cookie.Name == loginDeviceCookieName {
			device = cookie
		}
	}
	if device == nil || device.Path != "/auth" || !device.HttpOnly || device.SameSite != http.SameSiteLaxMode {
		t.Fatalf("login device cookie = %+v", device)
	}
	me := fixture.call(t, http.MethodGet, "/auth/me", nil, nil, "", map[string]string{"Cookie": loginDeviceCookieName + "=" + device.Value})
	if me.status != http.StatusUnauthorized {
		t.Fatalf("/auth/me with only the device cookie status = %d", me.status)
	}
}

// One address hammering a username is refused by its own narrow bucket; those
// refused requests must not spend the per-username bucket, so the user still
// logs in from any other address.
func TestLoginThrottleRefusedAttemptsDoNotLockUsernameOut(t *testing.T) {
	fixture := newRealIPFixture(t, []string{"127.0.0.1/32"}, true)
	fixture.createUser(t, "root", "password-a1")
	for i := 0; i < 200; i++ {
		call := fixture.loginVia(t, "wrong-password", "203.0.113.9")
		if call.status != http.StatusForbidden && call.status != http.StatusTooManyRequests {
			t.Fatalf("hammering login status = %d body = %v", call.status, call.body)
		}
	}
	if call := fixture.loginVia(t, "password-a1", "198.51.100.4"); call.status != http.StatusOK {
		t.Fatalf("login from a fresh address after one address hammered the username status = %d body = %v", call.status, call.body)
	}
}

// Addresses inside one IPv6 /64 share the per-address bucket.
func TestLoginThrottleGroupsIPv6By64(t *testing.T) {
	fixture := newRealIPFixture(t, []string{"127.0.0.1/32"}, true)
	fixture.createUser(t, "root", "password-a1")
	throttled := false
	for i := 0; i < 200 && !throttled; i++ {
		address := "2001:db8:1:2::" + strconv.FormatInt(int64(i+1), 16)
		call := fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "spray-" + strconv.Itoa(i), "password": "wrong-password"}, nil, "", map[string]string{"X-Forwarded-For": address})
		switch call.status {
		case http.StatusTooManyRequests:
			throttled = true
		case http.StatusForbidden:
		default:
			t.Fatalf("spray login status = %d body = %v", call.status, call.body)
		}
	}
	if !throttled {
		t.Fatal("rotating addresses inside one /64 escaped the per-address bucket")
	}
	if call := fixture.loginVia(t, "password-a1", "2001:db8:1:3::1"); call.status != http.StatusOK {
		t.Fatalf("login from another /64 status = %d body = %v", call.status, call.body)
	}
}
