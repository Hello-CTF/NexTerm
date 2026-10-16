package sync

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	stdsync "sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// remoteAPIFixture is a minimal faithful replica of the account + fleet/share routes: session issuing/validation and
// CSRF derivation go through the real account.Accounts, while business routes return canned payloads shaped like the server contracts.
type remoteAPIFixture struct {
	*httptest.Server
	db       *store.Store
	accounts *account.Accounts

	mu         stdsync.Mutex
	mfaTickets map[string]string
	logins     atomic.Int64

	failNextDevices401 atomic.Bool
	lastWriteCSRF      atomic.Value
}

func newRemoteAPIFixture(t *testing.T) *remoteAPIFixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fixture := &remoteAPIFixture{db: db, accounts: account.New(db.GORM(), account.WithTOTPKeyFile(filepath.Join(t.TempDir(), "totp.key"))), mfaTickets: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", fixture.serveLogin)
	mux.HandleFunc("POST /auth/totp/login", fixture.serveTOTPLogin)
	mux.Handle("GET /auth/me", fixture.session(http.HandlerFunc(fixture.serveMe)))
	mux.Handle("GET /auth/dek", fixture.session(http.HandlerFunc(fixture.serveDEK)))
	mux.Handle("POST /auth/password", fixture.session(fixture.csrf(http.HandlerFunc(fixture.servePassword))))
	mux.Handle("GET /auth/devices", fixture.session(http.HandlerFunc(fixture.serveAccountDevices)))
	mux.Handle("DELETE /auth/devices/{id}", fixture.session(fixture.csrf(http.HandlerFunc(fixture.serveAccountDeviceRevoke))))
	mux.Handle("GET /admin/users", fixture.session(http.HandlerFunc(fixture.serveAdminUsers)))
	mux.Handle("POST /admin/users", fixture.session(fixture.csrf(http.HandlerFunc(fixture.serveAdminUserCreate))))
	mux.Handle("POST /admin/users/{id}/disable", fixture.session(fixture.csrf(http.HandlerFunc(fixture.serveOK))))
	mux.Handle("POST /admin/users/{id}/reset", fixture.session(fixture.csrf(http.HandlerFunc(fixture.serveOK))))
	mux.Handle("GET /fleet/devices", fixture.session(http.HandlerFunc(fixture.serveFleetDevices)))
	mux.Handle("GET /fleet/devices/{id}/metrics", fixture.session(http.HandlerFunc(fixture.serveFleetMetrics)))
	mux.Handle("POST /fleet/devices/{id}/revoke", fixture.session(fixture.csrf(http.HandlerFunc(fixture.serveFleetRevoke))))
	mux.Handle("POST /fleet/devices/{id}/autostart", fixture.session(fixture.csrf(http.HandlerFunc(fixture.serveFleetAutostart))))
	mux.Handle("POST /device/enroll-codes", fixture.session(fixture.csrf(http.HandlerFunc(fixture.serveEnrollCode))))
	mux.Handle("GET /fleet/base-urls", fixture.session(http.HandlerFunc(fixture.serveBaseURLsGet)))
	mux.Handle("PUT /fleet/base-urls", fixture.session(fixture.csrf(http.HandlerFunc(fixture.serveBaseURLsPut))))
	mux.Handle("GET /share/host-shares", fixture.session(http.HandlerFunc(fixture.serveHostShares)))
	mux.Handle("POST /share/host-shares", fixture.session(fixture.csrf(http.HandlerFunc(fixture.serveHostShareCreate))))
	mux.Handle("POST /share/host-shares/{id}/revoke", fixture.session(fixture.csrf(http.HandlerFunc(fixture.serveOK))))
	mux.Handle("GET /share/links", fixture.session(http.HandlerFunc(fixture.serveShareLinks)))
	mux.Handle("POST /share/links", fixture.session(fixture.csrf(http.HandlerFunc(fixture.serveShareLinkCreate))))
	mux.Handle("POST /share/links/{id}/revoke", fixture.session(fixture.csrf(http.HandlerFunc(fixture.serveOK))))
	mux.Handle("GET /share/previews", fixture.session(http.HandlerFunc(fixture.servePreviewLinks)))
	mux.Handle("POST /share/previews/{id}/revoke", fixture.session(fixture.csrf(http.HandlerFunc(fixture.serveOK))))
	mux.Handle("POST /sync/v2/push", fixture.session(fixture.csrf(NewObjectHandler(db.GORM(), db.Backend(), nil))))
	mux.Handle("POST /sync/v2/pull", fixture.session(NewObjectHandler(db.GORM(), db.Backend(), nil)))
	mux.Handle("POST /sync/v2/ids", fixture.session(NewObjectHandler(db.GORM(), db.Backend(), nil)))
	mux.HandleFunc("GET /healthz", fixture.serveHealthz)
	mux.HandleFunc("GET /fail/{status}", fixture.serveFail)
	fixture.Server = httptest.NewServer(mux)
	t.Cleanup(fixture.Server.Close)
	return fixture
}

func (f *remoteAPIFixture) createUser(t *testing.T, username, password string) string {
	t.Helper()
	_, envelopes, _, err := vault.GenerateUserDEKEnvelopes(password)
	if err != nil {
		t.Fatal(err)
	}
	user, err := f.accounts.CreateUserWithEnvelopes(context.Background(), username, username, password, envelopes)
	if err != nil {
		t.Fatal(err)
	}
	return user.ID
}

// bindTOTP completes a real TOTP binding for the user and returns the base32 secret (the test side generates TOTP codes from it).
func (f *remoteAPIFixture) bindTOTP(t *testing.T, username, password string) string {
	t.Helper()
	ctx := context.Background()
	user, err := f.accounts.Authenticate(ctx, username, password)
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := f.accounts.BeginTOTPSetup(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.accounts.ConfirmTOTPSetup(ctx, user.ID, testTOTPCode(t, secret, time.Now())); err != nil {
		t.Fatal(err)
	}
	return secret
}

func testTOTPCode(t *testing.T, secretBase32 string, at time.Time) string {
	t.Helper()
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretBase32)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha1.New, secret)
	var counterBytes [8]byte
	binary.BigEndian.PutUint64(counterBytes[:], uint64(at.Unix())/30)
	_, _ = mac.Write(counterBytes[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1_000_000)
}

func (f *remoteAPIFixture) userView(user *account.User) map[string]any {
	return map[string]any{
		"id": user.ID, "username": user.Username, "display_name": user.DisplayName,
		"role": user.Role, "state": user.State, "must_change_password": user.MustChangePassword,
		"mfa_enabled": false, "created_at": user.CreatedAt, "updated_at": user.UpdatedAt, "last_login_at": user.LastLoginAt,
	}
}

func (f *remoteAPIFixture) writeSession(w http.ResponseWriter, r *http.Request, user *account.User) {
	token, session, err := f.accounts.IssueSession(r.Context(), user.ID, "")
	if err != nil {
		writeSyncError(w, http.StatusInternalServerError, err)
		return
	}
	mfaEnabled, err := f.accounts.TOTPEnabled(r.Context(), user.ID)
	if err != nil {
		writeSyncError(w, http.StatusInternalServerError, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: token, Path: "/"})
	view := f.userView(user)
	view["mfa_enabled"] = mfaEnabled
	writeSyncJSON(w, http.StatusOK, map[string]any{
		"user": view, "csrf_token": testCSRFToken(session.ID), "mfa_required": false,
	})
}

func (f *remoteAPIFixture) serveLogin(w http.ResponseWriter, r *http.Request) {
	f.logins.Add(1)
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeSyncError(w, http.StatusBadRequest, ipc.BadParam(err))
		return
	}
	user, err := f.accounts.Authenticate(r.Context(), request.Username, request.Password)
	if err != nil {
		writeSyncError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "用户名或密码错误"))
		return
	}
	mfaEnabled, err := f.accounts.TOTPEnabled(r.Context(), user.ID)
	if err != nil {
		writeSyncError(w, http.StatusInternalServerError, err)
		return
	}
	if mfaEnabled {
		ticket := ids.New()
		f.mu.Lock()
		f.mfaTickets[ticket] = user.ID
		f.mu.Unlock()
		writeSyncJSON(w, http.StatusOK, map[string]any{
			"mfa_required": true, "ticket": ticket, "expires_at": time.Now().Add(5 * time.Minute).UnixMilli(),
		})
		return
	}
	f.writeSession(w, r, user)
}

func (f *remoteAPIFixture) serveTOTPLogin(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Ticket string `json:"ticket"`
		Code   string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeSyncError(w, http.StatusBadRequest, ipc.BadParam(err))
		return
	}
	f.mu.Lock()
	userID, ok := f.mfaTickets[request.Ticket]
	f.mu.Unlock()
	if !ok {
		writeSyncError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "登录状态已过期，请重新登录"))
		return
	}
	valid, err := f.accounts.VerifyTOTPLoginCode(r.Context(), userID, request.Code)
	if err != nil {
		writeSyncError(w, http.StatusInternalServerError, err)
		return
	}
	if !valid {
		writeSyncError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "验证码错误"))
		return
	}
	f.mu.Lock()
	delete(f.mfaTickets, request.Ticket)
	f.mu.Unlock()
	user, err := f.accounts.GetUser(r.Context(), userID)
	if err != nil {
		writeSyncError(w, http.StatusInternalServerError, err)
		return
	}
	f.writeSession(w, r, user)
}

type remoteIdentityKey struct{}

func (f *remoteAPIFixture) session(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			writeSyncError(w, http.StatusUnauthorized, ipc.NewError(ipc.CodeForbidden, "会话无效或缺失，请重新登录"))
			return
		}
		identity, err := f.accounts.ValidateSession(r.Context(), cookie.Value)
		if err != nil {
			writeSyncError(w, http.StatusUnauthorized, ipc.NewError(ipc.CodeForbidden, "会话无效或缺失，请重新登录"))
			return
		}
		ctx := context.WithValue(r.Context(), remoteIdentityKey{}, identity)
		next.ServeHTTP(w, r.WithContext(WithUserID(ctx, identity.UserID)))
	})
}

func (f *remoteAPIFixture) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, _ := r.Context().Value(remoteIdentityKey{}).(*account.Identity)
		presented := r.Header.Get(csrfHeaderName)
		if identity == nil || presented != testCSRFToken(identity.SessionID) {
			writeSyncError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "CSRF 校验失败：页面已过期，请刷新后重试"))
			return
		}
		f.lastWriteCSRF.Store(presented)
		next.ServeHTTP(w, r)
	})
}

func (f *remoteAPIFixture) identity(r *http.Request) *account.Identity {
	identity, _ := r.Context().Value(remoteIdentityKey{}).(*account.Identity)
	return identity
}

func (f *remoteAPIFixture) serveMe(w http.ResponseWriter, r *http.Request) {
	user, err := f.accounts.GetUser(r.Context(), f.identity(r).UserID)
	if err != nil {
		writeSyncError(w, http.StatusInternalServerError, err)
		return
	}
	mfaEnabled, err := f.accounts.TOTPEnabled(r.Context(), user.ID)
	if err != nil {
		writeSyncError(w, http.StatusInternalServerError, err)
		return
	}
	view := f.userView(user)
	view["mfa_enabled"] = mfaEnabled
	writeSyncJSON(w, http.StatusOK, map[string]any{
		"user": view, "csrf_token": testCSRFToken(f.identity(r).SessionID), "mfa_required": false,
	})
}

func (f *remoteAPIFixture) serveDEK(w http.ResponseWriter, r *http.Request) {
	envelopes, err := f.accounts.GetUserDEKEnvelopes(r.Context(), f.identity(r).UserID)
	if err != nil {
		var appErr *ipc.Error
		if errors.As(err, &appErr) && appErr.Code == ipc.CodeNotFound {
			writeSyncError(w, http.StatusNotFound, appErr)
			return
		}
		writeSyncError(w, http.StatusInternalServerError, err)
		return
	}
	writeSyncJSON(w, http.StatusOK, map[string]any{
		"dek_envelope": envelopes.DEKEnvelope, "kdf_salt": envelopes.KDFSalt, "kdf_params": envelopes.KDFParams,
		"recovery_envelope": envelopes.RecoveryEnvelope, "recovery_hash": envelopes.RecoveryHash,
	})
}

func (f *remoteAPIFixture) servePassword(w http.ResponseWriter, r *http.Request) {
	var request struct {
		OldPassword      string `json:"old_password"`
		NewPassword      string `json:"new_password"`
		DEKEnvelope      []byte `json:"dek_envelope"`
		KDFSalt          []byte `json:"kdf_salt"`
		KDFParams        string `json:"kdf_params"`
		RecoveryEnvelope []byte `json:"recovery_envelope"`
		RecoveryHash     string `json:"recovery_hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeSyncError(w, http.StatusBadRequest, ipc.BadParam(err))
		return
	}
	envelopes := &vault.UserDEKEnvelopes{
		DEKEnvelope: request.DEKEnvelope, KDFSalt: request.KDFSalt, KDFParams: request.KDFParams,
		RecoveryEnvelope: request.RecoveryEnvelope, RecoveryHash: request.RecoveryHash,
	}
	identity := f.identity(r)
	if err := f.accounts.ChangePassword(r.Context(), identity.UserID, request.OldPassword, request.NewPassword, envelopes, identity.SessionID); err != nil {
		var appErr *ipc.Error
		if errors.As(err, &appErr) && appErr.Code == ipc.CodeForbidden {
			writeSyncError(w, http.StatusForbidden, appErr)
			return
		}
		writeSyncError(w, http.StatusInternalServerError, err)
		return
	}
	writeSyncJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (f *remoteAPIFixture) serveAccountDevices(w http.ResponseWriter, _ *http.Request) {
	writeSyncJSON(w, http.StatusOK, map[string]any{"devices": []map[string]any{
		{"id": "dev-1", "name": "work-laptop", "kind": "desktop", "created_at": 1, "last_seen_at": 2, "revoked_at": 0},
	}})
}

func (f *remoteAPIFixture) serveAccountDeviceRevoke(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("id") == "missing" {
		writeSyncError(w, http.StatusNotFound, ipc.NewError(ipc.CodeNotFound, "设备不存在"))
		return
	}
	writeSyncJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (f *remoteAPIFixture) serveAdminUsers(w http.ResponseWriter, r *http.Request) {
	if f.identity(r).Role != account.RoleSuperadmin {
		writeSyncError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "需要超级管理员权限"))
		return
	}
	writeSyncJSON(w, http.StatusOK, map[string]any{"users": []map[string]any{
		{"id": "u-1", "username": "alice", "display_name": "", "role": "user", "state": "active",
			"must_change_password": false, "mfa_enabled": false, "created_at": 1, "updated_at": 1, "last_login_at": 1},
	}})
}

func (f *remoteAPIFixture) serveAdminUserCreate(w http.ResponseWriter, r *http.Request) {
	if f.identity(r).Role != account.RoleSuperadmin {
		writeSyncError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "需要超级管理员权限"))
		return
	}
	var request struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeSyncError(w, http.StatusBadRequest, ipc.BadParam(err))
		return
	}
	writeSyncJSON(w, http.StatusOK, map[string]any{"user": map[string]any{
		"id": "u-new", "username": request.Username, "display_name": request.DisplayName, "role": "user", "state": "reset_required",
		"must_change_password": true, "mfa_enabled": false, "created_at": 1, "updated_at": 1, "last_login_at": 0,
	}})
}

func (f *remoteAPIFixture) serveOK(w http.ResponseWriter, _ *http.Request) {
	writeSyncJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (f *remoteAPIFixture) serveFleetDevices(w http.ResponseWriter, _ *http.Request) {
	if f.failNextDevices401.CompareAndSwap(true, false) {
		writeSyncError(w, http.StatusUnauthorized, ipc.NewError(ipc.CodeForbidden, "会话无效或缺失，请重新登录"))
		return
	}
	writeSyncJSON(w, http.StatusOK, map[string]any{"devices": []map[string]any{
		{"id": "d-1", "name": "edge-01", "kind": "agent", "created_at": 1, "last_seen_at": 2, "revoked_at": 0,
			"owner": map[string]any{"id": "u-1", "username": "alice"},
			"agent": map[string]any{"platform": "linux/amd64", "app_version": "1.0.0", "desired_autostart": true,
				"terminal_enabled": true, "current_url": "https://edge-01", "service_state": map[string]any{"installed": true, "enabled": true, "active": true},
				"last_seen_at": 2, "state_digest": "digest"}},
	}})
}

func (f *remoteAPIFixture) serveFleetMetrics(w http.ResponseWriter, r *http.Request) {
	sample := map[string]any{"ts": 10, "cpu_pct": 12.5, "mem_used": 1, "mem_total": 2, "disk_used": 3, "disk_total": 4, "uptime_s": 5}
	response := map[string]any{
		"samples": []map[string]any{sample}, "latest": sample,
		"windowStart": 1, "windowEnd": 20, "sampleCount": 100, "downsampled": true, "sampleIntervalMs": 5000,
	}
	if r.URL.Query().Get("since_ms") != "" {
		response["samples"] = []map[string]any{}
		response["sampleCount"] = 0
		response["downsampled"] = false
		response["seriesError"] = "trend timeout"
	}
	writeSyncJSON(w, http.StatusOK, response)
}

func (f *remoteAPIFixture) serveFleetRevoke(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("id") == "missing" {
		writeSyncError(w, http.StatusNotFound, ipc.NewError(ipc.CodeNotFound, "设备不存在"))
		return
	}
	writeSyncJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (f *remoteAPIFixture) serveFleetAutostart(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Desired bool `json:"desired"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeSyncError(w, http.StatusBadRequest, ipc.BadParam(err))
		return
	}
	writeSyncJSON(w, http.StatusOK, map[string]any{"ok": true, "desired_autostart": request.Desired})
}

func (f *remoteAPIFixture) serveEnrollCode(w http.ResponseWriter, r *http.Request) {
	var request struct {
		TTLMS int64 `json:"ttl_ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeSyncError(w, http.StatusBadRequest, ipc.BadParam(err))
		return
	}
	writeSyncJSON(w, http.StatusOK, map[string]any{"code": "NX-ENROLL-1", "expires_at": time.Now().Add(time.Duration(request.TTLMS) * time.Millisecond).UnixMilli()})
}

func (f *remoteAPIFixture) serveBaseURLsGet(w http.ResponseWriter, _ *http.Request) {
	writeSyncJSON(w, http.StatusOK, map[string]any{"base_urls": []map[string]any{{"url": "https://sync.example.com"}}})
}

func (f *remoteAPIFixture) serveBaseURLsPut(w http.ResponseWriter, r *http.Request) {
	var request struct {
		BaseURLs []RemoteBaseURLEntry `json:"base_urls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeSyncError(w, http.StatusBadRequest, ipc.BadParam(err))
		return
	}
	writeSyncJSON(w, http.StatusOK, map[string]any{"base_urls": request.BaseURLs})
}

func (f *remoteAPIFixture) serveHostShares(w http.ResponseWriter, _ *http.Request) {
	writeSyncJSON(w, http.StatusOK, map[string]any{"shares": []map[string]any{
		{"id": "hs-1", "owner_id": "u-1", "owner_username": "alice", "device_id": "d-1", "recipient_id": "u-2",
			"recipient_username": "bob", "permission": "read_write", "created_at": 1, "expires_at": 2},
	}})
}

func (f *remoteAPIFixture) serveHostShareCreate(w http.ResponseWriter, r *http.Request) {
	var request struct {
		DeviceID          string `json:"device_id"`
		RecipientUsername string `json:"recipient_username"`
		Write             bool   `json:"write"`
		TTLMS             int64  `json:"ttl_ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeSyncError(w, http.StatusBadRequest, ipc.BadParam(err))
		return
	}
	permission := "read"
	if request.Write {
		permission = "read_write"
	}
	writeSyncJSON(w, http.StatusOK, map[string]any{
		"id": "hs-new", "owner_id": "u-1", "owner_username": "alice", "device_id": request.DeviceID,
		"recipient_id": "u-2", "recipient_username": request.RecipientUsername, "permission": permission, "created_at": 1, "expires_at": 2,
	})
}

func (f *remoteAPIFixture) serveShareLinks(w http.ResponseWriter, _ *http.Request) {
	writeSyncJSON(w, http.StatusOK, map[string]any{"links": []map[string]any{
		{"id": "sl-1", "owner_id": "u-1", "device_id": "d-1", "session_id": "s-1", "permission": "read", "created_at": 1, "expires_at": 2},
	}})
}

func (f *remoteAPIFixture) serveShareLinkCreate(w http.ResponseWriter, r *http.Request) {
	var request struct {
		DeviceID  string `json:"device_id"`
		SessionID string `json:"session_id"`
		Write     bool   `json:"write"`
		TTLMS     int64  `json:"ttl_ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeSyncError(w, http.StatusBadRequest, ipc.BadParam(err))
		return
	}
	writeSyncJSON(w, http.StatusOK, map[string]any{
		"id": "sl-new", "owner_id": "u-1", "device_id": request.DeviceID, "session_id": request.SessionID,
		"permission": "read", "created_at": 1, "expires_at": 2, "token": "one-shot-token",
	})
}

func (f *remoteAPIFixture) servePreviewLinks(w http.ResponseWriter, _ *http.Request) {
	writeSyncJSON(w, http.StatusOK, map[string]any{"links": []map[string]any{
		{"id": "pl-1", "owner_id": "u-1", "session_id": "s-1", "created_at": 1, "expires_at": 2},
	}})
}

func (f *remoteAPIFixture) serveHealthz(w http.ResponseWriter, _ *http.Request) {
	writeSyncJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "nexterm-server", "version": "9.9.9-test"})
}

// serveFail returns a failure with the status code from the path: with ?rpc=bad_param it carries an rpcErr shape, otherwise a bare status code.
func (f *remoteAPIFixture) serveFail(w http.ResponseWriter, r *http.Request) {
	var status int
	if _, err := fmt.Sscanf(r.PathValue("status"), "%d", &status); err != nil {
		status = http.StatusInternalServerError
	}
	if code := r.URL.Query().Get("rpc"); code != "" {
		writeSyncError(w, status, ipc.NewError(ipc.Code(code), "服务端业务失败"))
		return
	}
	http.Error(w, "plain failure", status)
}

func (f *remoteAPIFixture) loginClient(t *testing.T, username, password string) *remoteClient {
	t.Helper()
	client, err := newRemoteClient(RemoteConfig{URL: f.URL, Username: username, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := client.login(context.Background(), username, password)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.established {
		t.Fatalf("login outcome = %+v, want established session", outcome)
	}
	return client
}

func TestRemoteAPIListDevicesSendsSessionCookie(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	client := fixture.loginClient(t, "alice", "alice-pw-123")

	devices, err := client.ListDevices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(devices.Devices) != 1 || devices.Devices[0].ID != "d-1" {
		t.Fatalf("devices = %+v", devices)
	}
	agent := devices.Devices[0].Agent
	if agent == nil || !agent.DesiredAutostart || agent.StateDigest != "digest" || !agent.ServiceState.Active {
		t.Fatalf("agent = %+v", agent)
	}
	if devices.Devices[0].Owner == nil || devices.Devices[0].Owner.Username != "alice" {
		t.Fatalf("owner = %+v", devices.Devices[0].Owner)
	}

	// A bare client without a session cookie must be rejected with 401 and mapped to errSessionExpired.
	bare := &remoteClient{base: fixture.URL}
	if _, err := bare.ListDevices(context.Background()); !errors.Is(err, errSessionExpired) {
		t.Fatalf("bare ListDevices err = %v, want errSessionExpired", err)
	}
}

func TestRemoteAPIMetricsSinceQuery(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	client := fixture.loginClient(t, "alice", "alice-pw-123")

	metrics, err := client.DeviceMetrics(context.Background(), "d-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics.Samples) != 1 || metrics.Samples[0].CPUPct != 12.5 {
		t.Fatalf("samples = %+v", metrics.Samples)
	}
	if metrics.Latest == nil || metrics.Latest.CPUPct != 12.5 || metrics.WindowStart != 1 || metrics.WindowEnd != 20 ||
		metrics.SampleCount != 100 || !metrics.Downsampled || metrics.SampleIntervalMS != 5000 || metrics.SeriesError != "" {
		t.Fatalf("metrics metadata = %+v", metrics)
	}
	partial, err := client.DeviceMetrics(context.Background(), "d-1", 60000)
	if err != nil {
		t.Fatal(err)
	}
	if len(partial.Samples) != 0 || partial.Latest == nil || partial.SeriesError != "trend timeout" {
		t.Fatalf("since_ms query or partial metadata was not forwarded: %+v", partial)
	}
}

func TestRemoteAPIWriteCarriesCSRF(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	client := fixture.loginClient(t, "alice", "alice-pw-123")

	entries := []RemoteBaseURLEntry{{URL: "https://a.example.com"}, {URL: "https://b.example.com", Insecure: true}}
	updated, err := client.PutBaseURLs(context.Background(), entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.BaseURLs) != 2 || !updated.BaseURLs[1].Insecure {
		t.Fatalf("base_urls = %+v", updated)
	}
	seen, _ := fixture.lastWriteCSRF.Load().(string)
	if seen == "" || seen != client.csrfToken {
		t.Fatalf("write CSRF header = %q, want session token", seen)
	}

	// A write request with the cookie but no CSRF token must be rejected by the server with 403 and mapped to forbidden.
	stale := &remoteClient{base: fixture.URL, cookieToken: client.cookieToken}
	if _, err := stale.PutBaseURLs(context.Background(), entries); err == nil {
		t.Fatal("write without CSRF token must fail")
	} else {
		requireCode(t, err, ipc.CodeForbidden)
	}
}

func TestRemoteAPIErrorMapping(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	client := &remoteClient{base: fixture.URL}
	ctx := context.Background()

	cases := []struct {
		path string
		code ipc.Code
	}{
		{"/fail/400?rpc=bad_param", ipc.CodeBadParam},
		{"/fail/403?rpc=forbidden", ipc.CodeForbidden},
		{"/fail/404?rpc=not_found", ipc.CodeNotFound},
		{"/fail/404", ipc.CodeNotFound},
		{"/fail/429", ipc.CodeForbidden},
		{"/fail/500", ipc.CodeDisconnected},
		{"/fail/503", ipc.CodeDisconnected},
	}
	for _, tc := range cases {
		err := client.api(ctx, http.MethodGet, tc.path, nil, nil)
		if err == nil {
			t.Fatalf("%s: expected error", tc.path)
		}
		requireCode(t, err, tc.code)
	}

	// rpcErr passes through the server message; 401 maps to errSessionExpired.
	err := client.api(ctx, http.MethodGet, "/fail/403?rpc=forbidden", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "服务端业务失败") {
		t.Fatalf("rpcErr message not propagated: %v", err)
	}
	if err := client.api(ctx, http.MethodGet, "/fail/401", nil, nil); !errors.Is(err, errSessionExpired) {
		t.Fatalf("401 err = %v, want errSessionExpired", err)
	}
}

func TestRemoteAPITypedMethodsDecode(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	client := fixture.loginClient(t, "alice", "alice-pw-123")
	ctx := context.Background()

	code, err := client.CreateEnrollCode(ctx, 60000, "")
	if err != nil || code.Code != "NX-ENROLL-1" || code.ExpiresAt == 0 {
		t.Fatalf("enroll code = %+v, %v", code, err)
	}
	share, err := client.CreateHostShare(ctx, RemoteHostShareInput{DeviceID: "d-1", RecipientUsername: "bob", Write: true, TTLMS: 60000})
	if err != nil || share.Permission != "read_write" || share.RecipientUsername != "bob" {
		t.Fatalf("host share = %+v, %v", share, err)
	}
	link, err := client.CreateShareLink(ctx, RemoteShareLinkInput{DeviceID: "d-1", SessionID: "s-1", TTLMS: 60000})
	if err != nil || link.Token != "one-shot-token" {
		t.Fatalf("share link = %+v, %v", link, err)
	}
	links, err := client.ListShareLinks(ctx)
	if err != nil || len(links.Links) != 1 || links.Links[0].Token != "" {
		t.Fatalf("share links = %+v, %v", links, err)
	}
	previews, err := client.ListPreviewLinks(ctx)
	if err != nil || len(previews.Links) != 1 || previews.Links[0].ID != "pl-1" {
		t.Fatalf("previews = %+v, %v", previews, err)
	}
	myDevices, err := client.ListMyDevices(ctx)
	if err != nil || len(myDevices.Devices) != 1 || myDevices.Devices[0].Name != "work-laptop" {
		t.Fatalf("account devices = %+v, %v", myDevices, err)
	}
	if err := client.RevokeMyDevice(ctx, "dev-1"); err != nil {
		t.Fatal(err)
	}
	requireCode(t, client.RevokeMyDevice(ctx, "missing"), ipc.CodeNotFound)
	requireCode(t, client.RevokeDevice(ctx, "missing"), ipc.CodeNotFound)
	if err := client.SetDeviceAutostart(ctx, "d-1", true); err != nil {
		t.Fatal(err)
	}
	if err := client.RevokeHostShare(ctx, "hs-1"); err != nil {
		t.Fatal(err)
	}
	if err := client.RevokeShareLink(ctx, "sl-1"); err != nil {
		t.Fatal(err)
	}
	if err := client.RevokePreviewLink(ctx, "pl-1"); err != nil {
		t.Fatal(err)
	}
	version, err := client.Healthz(ctx)
	if err != nil || version != "9.9.9-test" {
		t.Fatalf("healthz = %q, %v", version, err)
	}

	// A non-admin calling an admin route: the server 403 passes through.
	requireCode(t, func() error {
		_, err := client.AdminListUsers(ctx)
		return err
	}(), ipc.CodeForbidden)
	if err := client.AdminSetUserState(ctx, "u-1", "bogus"); err == nil {
		t.Fatal("bogus state must be rejected client-side")
	} else {
		requireCode(t, err, ipc.CodeBadParam)
	}
}

func TestRemoteCallReloginOn401(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	device := newTestDevice(t)
	config := RemoteConfig{URL: fixture.URL, Username: "alice", Password: "alice-pw-123"}

	// The first fleet call gets a 401: remoteCall must dropSession and re-login with the saved password, then succeed.
	fixture.failNextDevices401.Store(true)
	var devices *RemoteFleetDevices
	err := device.engine.remoteCall(context.Background(), config, func(ctx context.Context, client *remoteClient) error {
		result, err := client.ListDevices(ctx)
		if err != nil {
			return err
		}
		devices = result
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(devices.Devices) != 1 {
		t.Fatalf("devices = %+v", devices)
	}
	if got := fixture.logins.Load(); got != 2 {
		t.Fatalf("logins = %d, want 2 (initial + re-login after 401)", got)
	}

	// The session is cached: the second call does not re-login.
	err = device.engine.remoteCall(context.Background(), config, func(ctx context.Context, client *remoteClient) error {
		_, err := client.ListDevices(ctx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := fixture.logins.Load(); got != 2 {
		t.Fatalf("logins = %d, want 2 (cached session reused)", got)
	}
}
