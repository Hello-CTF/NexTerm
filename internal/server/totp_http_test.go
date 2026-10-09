package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func testTOTPCode(t *testing.T, secretBase32 string, unixSeconds int64) string {
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

func (f *accountFixture) createUser(t *testing.T, username, password string) {
	t.Helper()
	if _, err := f.accounts.CreateUser(context.Background(), username, "", password); err != nil {
		t.Fatal(err)
	}
}

func TestAccountHTTPTOTPLifecycle(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	fixture.createUser(t, "root", "password-a1")

	session := fixture.login(t, "root", "password-a1")
	if session.user["mfa_enabled"] != false {
		t.Fatalf("fresh user mfa_enabled=%v", session.user["mfa_enabled"])
	}

	call := fixture.call(t, http.MethodGet, "/auth/totp", nil, session, "", nil)
	if call.status != http.StatusOK || call.body["enabled"] != false || call.body["mfa_required"] != false {
		t.Fatalf("initial totp status=%d body=%v", call.status, call.body)
	}

	call = fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("setup status=%d body=%v", call.status, call.body)
	}
	secret, _ := call.body["secret"].(string)
	uri, _ := call.body["otpauth_uri"].(string)
	if len(secret) != 32 || uri == "" {
		t.Fatalf("unexpected setup payload: %v", call.body)
	}
	call = fixture.call(t, http.MethodGet, "/auth/totp", nil, session, "", nil)
	if call.status != http.StatusOK || call.body["pending"] != true || call.body["enabled"] != false {
		t.Fatalf("pending status=%d body=%v", call.status, call.body)
	}

	call = fixture.call(t, http.MethodPost, "/auth/totp/confirm", map[string]any{"code": testTOTPCode(t, secret, time.Now().Unix())}, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("confirm status=%d body=%v", call.status, call.body)
	}
	recoveryCodes, _ := call.body["recovery_codes"].([]any)
	if len(recoveryCodes) != 8 {
		t.Fatalf("unexpected recovery codes: %v", call.body)
	}

	call = fixture.call(t, http.MethodGet, "/auth/totp", nil, session, "", nil)
	if call.status != http.StatusOK || call.body["enabled"] != true || call.body["recovery_codes_left"] != float64(8) {
		t.Fatalf("enabled status=%d body=%v", call.status, call.body)
	}

	call = fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "root", "password": "password-a1"}, nil, "", nil)
	if call.status != http.StatusOK || call.body["mfa_required"] != true {
		t.Fatalf("mfa begin status=%d body=%v", call.status, call.body)
	}
	if len(call.cookies) != 0 {
		t.Fatal("mfa challenge must not issue a session cookie")
	}
	ticket, _ := call.body["ticket"].(string)
	if ticket == "" {
		t.Fatalf("missing ticket: %v", call.body)
	}

	if call = fixture.call(t, http.MethodPost, "/auth/totp/login", map[string]any{"ticket": ticket, "code": "000000"}, nil, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("wrong totp status=%d body=%v", call.status, call.body)
	}
	if call = fixture.call(t, http.MethodPost, "/auth/totp/login", map[string]any{"ticket": ticket, "code": "000000"}, nil, "", nil); call.status != http.StatusTooManyRequests {
		t.Fatalf("totp throttle status=%d body=%v", call.status, call.body)
	}

	call = fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "root", "password": "password-a1"}, nil, "", nil)
	ticket = call.body["ticket"].(string)
	call = fixture.call(t, http.MethodPost, "/auth/totp/login", map[string]any{"ticket": ticket, "code": testTOTPCode(t, secret, time.Now().Unix())}, nil, "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("totp login status=%d body=%v", call.status, call.body)
	}
	mfaSession := captureSession(t, call)
	if mfaSession.user["mfa_enabled"] != true {
		t.Fatalf("mfa_enabled=%v", mfaSession.user["mfa_enabled"])
	}
	if call = fixture.call(t, http.MethodGet, "/auth/me", nil, mfaSession, "", nil); call.status != http.StatusOK {
		t.Fatalf("me status=%d body=%v", call.status, call.body)
	}

	if call = fixture.call(t, http.MethodPost, "/auth/totp/login", map[string]any{"ticket": ticket, "code": testTOTPCode(t, secret, time.Now().Unix())}, nil, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("ticket reuse status=%d body=%v", call.status, call.body)
	}

	call = fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "root", "password": "password-a1"}, nil, "", nil)
	ticket = call.body["ticket"].(string)
	recovery := recoveryCodes[0].(string)
	call = fixture.call(t, http.MethodPost, "/auth/totp/login", map[string]any{"ticket": ticket, "code": recovery}, nil, "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("recovery login status=%d body=%v", call.status, call.body)
	}
	call = fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "root", "password": "password-a1"}, nil, "", nil)
	ticket = call.body["ticket"].(string)
	if call = fixture.call(t, http.MethodPost, "/auth/totp/login", map[string]any{"ticket": ticket, "code": recovery}, nil, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("recovery reuse status=%d body=%v", call.status, call.body)
	}

	call = fixture.call(t, http.MethodDelete, "/auth/totp", map[string]any{"code": testTOTPCode(t, secret, time.Now().Unix())}, mfaSession, mfaSession.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("disable status=%d body=%v", call.status, call.body)
	}
	fixture.login(t, "root", "password-a1")
}

func TestAccountHTTPTOTPRouteGuards(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	fixture.createUser(t, "root", "password-a1")

	if call := fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, nil, "", nil); call.status != http.StatusUnauthorized {
		t.Fatalf("setup without session status=%d", call.status)
	}
	session := fixture.login(t, "root", "password-a1")
	if call := fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, session, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("setup without csrf status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodDelete, "/auth/totp", map[string]any{"code": "000000"}, session, session.csrf, nil); call.status != http.StatusForbidden {
		t.Fatalf("disable unbound status=%d body=%v", call.status, call.body)
	}
}

func TestAccountHTTPAdminMFARequired(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	admin, _ := fixture.initSuperadmin(t, "root", "password-a1")
	fixture.createUser(t, "bob", "password-b1")

	call := fixture.call(t, http.MethodGet, "/admin/settings", nil, admin, "", nil)
	if call.status != http.StatusOK || call.body["mfa_required"] != false {
		t.Fatalf("default settings status=%d body=%v", call.status, call.body)
	}
	call = fixture.call(t, http.MethodPut, "/admin/settings", map[string]any{"registration_open": false, "mfa_required": true}, admin, admin.csrf, nil)
	if call.status != http.StatusOK || call.body["mfa_required"] != true {
		t.Fatalf("put settings status=%d body=%v", call.status, call.body)
	}

	bob := fixture.login(t, "bob", "password-b1")
	if call = fixture.call(t, http.MethodPut, "/admin/settings", map[string]any{"registration_open": false, "mfa_required": false}, bob, bob.csrf, nil); call.status != http.StatusForbidden {
		t.Fatalf("non-admin put status=%d", call.status)
	}

	// 策略开启后未绑定会话被锁到只能绑定: /auth/totp 可用, 其余路由 403 mfa_enrollment_required。
	call = fixture.call(t, http.MethodGet, "/auth/totp", nil, bob, "", nil)
	if call.status != http.StatusOK || call.body["mfa_required"] != true || call.body["enabled"] != false {
		t.Fatalf("bob totp status=%d body=%v", call.status, call.body)
	}
	call = fixture.call(t, http.MethodGet, "/auth/devices", nil, bob, "", nil)
	if call.status != http.StatusForbidden || call.body["error"].(map[string]any)["code"] != "mfa_enrollment_required" {
		t.Fatalf("bob locked status=%d body=%v", call.status, call.body)
	}
	call = fixture.call(t, http.MethodGet, "/admin/users", nil, admin, "", nil)
	if call.status != http.StatusForbidden || call.body["error"].(map[string]any)["code"] != "mfa_enrollment_required" {
		t.Fatalf("admin locked until bound status=%d body=%v", call.status, call.body)
	}

	// 超管完成绑定后管理面恢复, 用户列表如实汇报绑定状态。
	call = fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, admin, admin.csrf, nil)
	adminSecret := call.body["secret"].(string)
	call = fixture.call(t, http.MethodPost, "/auth/totp/confirm", map[string]any{"code": testTOTPCode(t, adminSecret, time.Now().Unix())}, admin, admin.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("admin confirm status=%d body=%v", call.status, call.body)
	}
	call = fixture.call(t, http.MethodGet, "/admin/users", nil, admin, "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("user list status=%d body=%v", call.status, call.body)
	}
	users, _ := call.body["users"].([]any)
	if len(users) != 2 {
		t.Fatalf("unexpected users: %v", call.body)
	}
	flags := map[string]bool{}
	for _, entry := range users {
		user := entry.(map[string]any)
		flags[user["username"].(string)] = user["mfa_enabled"] == true
	}
	if !flags["root"] || flags["bob"] {
		t.Fatalf("unexpected mfa_enabled flags: %v", flags)
	}
}

func TestAccountHTTPMFARequiredEnrollmentFlow(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	admin, _ := fixture.initSuperadmin(t, "root", "password-a1")
	fixture.createUser(t, "bob", "password-b1")
	call := fixture.call(t, http.MethodPut, "/admin/settings", map[string]any{"registration_open": false, "mfa_required": true}, admin, admin.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("put settings status=%d body=%v", call.status, call.body)
	}

	// 未绑定用户仍可密码登录拿会话(否则永远无法绑定), 但响应带 mfa_required, 且会话被锁。
	bob := fixture.login(t, "bob", "password-b1")
	call = fixture.call(t, http.MethodGet, "/auth/me", nil, bob, "", nil)
	if call.status != http.StatusOK || call.body["mfa_required"] != true || call.body["user"].(map[string]any)["mfa_enabled"] != false {
		t.Fatalf("me status=%d body=%v", call.status, call.body)
	}
	call = fixture.call(t, http.MethodGet, "/auth/dek", nil, bob, "", nil)
	if call.status != http.StatusForbidden || call.body["error"].(map[string]any)["code"] != "mfa_enrollment_required" {
		t.Fatalf("dek locked status=%d body=%v", call.status, call.body)
	}
	if call = fixture.call(t, http.MethodPost, "/auth/logout", map[string]any{}, bob, bob.csrf, nil); call.status != http.StatusOK {
		t.Fatalf("logout must stay available: %d", call.status)
	}

	// 完成绑定后锁解除: 其余路由恢复。
	bob = fixture.login(t, "bob", "password-b1")
	call = fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, bob, bob.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("setup status=%d body=%v", call.status, call.body)
	}
	secret := call.body["secret"].(string)
	call = fixture.call(t, http.MethodPost, "/auth/totp/confirm", map[string]any{"code": testTOTPCode(t, secret, time.Now().Unix())}, bob, bob.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("confirm status=%d body=%v", call.status, call.body)
	}
	call = fixture.call(t, http.MethodGet, "/auth/dek", nil, bob, "", nil)
	if call.status != http.StatusNotFound {
		t.Fatalf("dek unlocked status=%d body=%v", call.status, call.body)
	}
}

func TestAccountHTTPMFARequiredLocksRPCAndWebSocket(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	fixture.createUser(t, "bob", "password-b1")
	session := fixture.login(t, "bob", "password-b1")

	rpcWithSession := func() (int, map[string]any) {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, fixture.http.URL+"/rpc", strings.NewReader(`{"cmd":"app_info","args":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(session.cookie)
		request.Header.Set(csrfHeaderName, session.csrf)
		response, err := fixture.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var decoded map[string]any
		_ = json.NewDecoder(response.Body).Decode(&decoded)
		return response.StatusCode, decoded
	}
	wsGet := func(path string) (int, map[string]any) {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, fixture.http.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.AddCookie(session.cookie)
		response, err := fixture.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var decoded map[string]any
		if strings.Contains(response.Header.Get("Content-Type"), "application/json") {
			_ = json.NewDecoder(response.Body).Decode(&decoded)
		}
		return response.StatusCode, decoded
	}

	// 策略默认关闭: /rpc 与会话 cookie 正常可用。
	if status, _ := rpcWithSession(); status != http.StatusOK {
		t.Fatalf("default rpc status=%d", status)
	}

	if err := fixture.accounts.SetMFARequired(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	// mfa_required 未绑定: /rpc 与 /ws/* 全部 403 mfa_enrollment_required, 绑定必需路由不受影响。
	status, body := rpcWithSession()
	if status != http.StatusForbidden || body["error"].(map[string]any)["code"] != "mfa_enrollment_required" {
		t.Fatalf("locked rpc status=%d body=%v", status, body)
	}
	for _, path := range []string{"/ws/events", "/ws/channel/c1"} {
		status, body := wsGet(path)
		if status != http.StatusForbidden || body["error"].(map[string]any)["code"] != "mfa_enrollment_required" {
			t.Fatalf("locked %s status=%d body=%v", path, status, body)
		}
	}
	call := fixture.call(t, http.MethodGet, "/auth/totp", nil, session, "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("totp status must stay available: %d", call.status)
	}

	// 完成绑定后各面解锁。
	call = fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, session, session.csrf, nil)
	secret := call.body["secret"].(string)
	call = fixture.call(t, http.MethodPost, "/auth/totp/confirm", map[string]any{"code": testTOTPCode(t, secret, time.Now().Unix())}, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("confirm status=%d body=%v", call.status, call.body)
	}
	if status, _ := rpcWithSession(); status != http.StatusOK {
		t.Fatalf("unlocked rpc status=%d", status)
	}
	if status, _ := wsGet("/ws/events"); status == http.StatusForbidden {
		t.Fatal("ws events must be unlocked after binding")
	}
}

func TestAccountHTTPTOTPRebindRequiresReverify(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	fixture.createUser(t, "root", "password-a1")
	session := fixture.login(t, "root", "password-a1")

	call := fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, session, session.csrf, nil)
	secret := call.body["secret"].(string)
	call = fixture.call(t, http.MethodPost, "/auth/totp/confirm", map[string]any{"code": testTOTPCode(t, secret, time.Now().Unix())}, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("confirm status=%d body=%v", call.status, call.body)
	}

	// 已绑定: 无重验凭据直接 403(不计限流), 原绑定不受影响。
	call = fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, session, session.csrf, nil)
	if call.status != http.StatusForbidden {
		t.Fatalf("rebind without reverify status=%d body=%v", call.status, call.body)
	}
	call = fixture.call(t, http.MethodGet, "/auth/totp", nil, session, "", nil)
	if call.status != http.StatusOK || call.body["enabled"] != true || call.body["pending"] != false {
		t.Fatalf("binding must stay intact: %d %v", call.status, call.body)
	}

	// 密码重验通过, 换绑进入 pending; 确认新码后新绑定生效。
	call = fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{"reverify": "password-a1"}, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("rebind with password status=%d body=%v", call.status, call.body)
	}
	secret2 := call.body["secret"].(string)
	if secret2 == secret {
		t.Fatal("rebind reused the same secret")
	}
	call = fixture.call(t, http.MethodPost, "/auth/totp/confirm", map[string]any{"code": testTOTPCode(t, secret2, time.Now().Unix())}, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("rebind confirm status=%d body=%v", call.status, call.body)
	}
	call = fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "root", "password": "password-a1"}, nil, "", nil)
	ticket := call.body["ticket"].(string)
	call = fixture.call(t, http.MethodPost, "/auth/totp/login", map[string]any{"ticket": ticket, "code": testTOTPCode(t, secret2, time.Now().Unix())}, nil, "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("login with new secret status=%d body=%v", call.status, call.body)
	}

	// 错误重验凭据 403 并触发限流, 再试直接 429。
	if call = fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{"reverify": "wrong-credential"}, session, session.csrf, nil); call.status != http.StatusForbidden {
		t.Fatalf("rebind with wrong reverify status=%d body=%v", call.status, call.body)
	}
	if call = fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{"reverify": "wrong-credential"}, session, session.csrf, nil); call.status != http.StatusTooManyRequests {
		t.Fatalf("rebind throttle status=%d body=%v", call.status, call.body)
	}
}

func TestAccountHTTPTOTPDisabledUserBlocked(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	fixture.createUser(t, "root", "password-a1")
	session := fixture.login(t, "root", "password-a1")
	call := fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, session, session.csrf, nil)
	secret := call.body["secret"].(string)
	call = fixture.call(t, http.MethodPost, "/auth/totp/confirm", map[string]any{"code": testTOTPCode(t, secret, time.Now().Unix())}, session, session.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("confirm status=%d body=%v", call.status, call.body)
	}

	if err := fixture.accounts.SetUserDisabled(context.Background(), session.user["id"].(string), true); err != nil {
		t.Fatal(err)
	}
	call = fixture.call(t, http.MethodPost, "/auth/login", map[string]any{"username": "root", "password": "password-a1"}, nil, "", nil)
	if call.status != http.StatusForbidden {
		t.Fatalf("disabled login status=%d body=%v", call.status, call.body)
	}
}

func TestAccountHTTPTOTPVerifyThrottle(t *testing.T) {
	fixture := newAccountFixture(t, AuthOn)
	fixture.createUser(t, "root", "password-a1")
	session := fixture.login(t, "root", "password-a1")
	call := fixture.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, session, session.csrf, nil)
	secret := call.body["secret"].(string)

	if call = fixture.call(t, http.MethodPost, "/auth/totp/confirm", map[string]any{"code": "000000"}, session, session.csrf, nil); call.status != http.StatusForbidden {
		t.Fatalf("wrong confirm status=%d body=%v", call.status, call.body)
	}
	if call = fixture.call(t, http.MethodPost, "/auth/totp/confirm", map[string]any{"code": testTOTPCode(t, secret, time.Now().Unix())}, session, session.csrf, nil); call.status != http.StatusTooManyRequests {
		t.Fatalf("confirm throttle status=%d body=%v", call.status, call.body)
	}

	other := newAccountFixture(t, AuthOn)
	other.createUser(t, "root", "password-a1")
	otherSession := other.login(t, "root", "password-a1")
	call = other.call(t, http.MethodPost, "/auth/totp/setup", map[string]any{}, otherSession, otherSession.csrf, nil)
	otherSecret := call.body["secret"].(string)
	call = other.call(t, http.MethodPost, "/auth/totp/confirm", map[string]any{"code": testTOTPCode(t, otherSecret, time.Now().Unix())}, otherSession, otherSession.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("other fixture confirm status=%d body=%v", call.status, call.body)
	}
	if call = other.call(t, http.MethodDelete, "/auth/totp", map[string]any{"code": "000000"}, otherSession, otherSession.csrf, nil); call.status != http.StatusForbidden {
		t.Fatalf("wrong disable status=%d body=%v", call.status, call.body)
	}
	if call = other.call(t, http.MethodDelete, "/auth/totp", map[string]any{"code": testTOTPCode(t, otherSecret, time.Now().Unix())}, otherSession, otherSession.csrf, nil); call.status != http.StatusTooManyRequests {
		t.Fatalf("disable throttle status=%d body=%v", call.status, call.body)
	}
}
