package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	core "github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

var (
	testPNGBytes  = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte("p"), 64)...)
	testJPEGBytes = append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF"), bytes.Repeat([]byte("j"), 64)...)
	testGIFBytes  = append([]byte("GIF89a"), bytes.Repeat([]byte("g"), 64)...)
	testWebPBytes = append(append([]byte("RIFF"), []byte("\x00\x00\x00\x00WEBPVP8 ")...), bytes.Repeat([]byte("w"), 64)...)
	testTextBytes = []byte("plain text, not an image")
)

type imageAuditRecord struct {
	source  string
	kind    string
	payload map[string]any
}

type imageFixture struct {
	*accountFixture
	database *store.Store

	mu     sync.Mutex
	audits []imageAuditRecord
}

func newImageFixture(t *testing.T, auth, publicBaseURL string, mutate func(*Config)) *imageFixture {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	config := testConfig(t, false)
	config.Options.Auth = auth
	config.Options.PublicBaseURL = publicBaseURL
	accounts := account.New(database.DB())
	config.Accounts = accounts
	config.Settings = database
	fixture := &imageFixture{accountFixture: &accountFixture{accounts: accounts, client: &http.Client{}}, database: database}
	config.AuditFunc = func(_ context.Context, source, kind string, payload map[string]any) error {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.audits = append(fixture.audits, imageAuditRecord{source: source, kind: kind, payload: payload})
		return nil
	}
	if mutate != nil {
		mutate(&config)
	}
	server, httpServer := newTestHTTP(t, config)
	fixture.server = server
	fixture.http = httpServer
	return fixture
}

func (f *imageFixture) callRaw(t *testing.T, method, path string, body []byte, session *accountTestSession, csrf string) (int, http.Header, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, f.http.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if session != nil && session.cookie != nil {
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
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, response.Header, raw
}

func (f *imageFixture) upload(t *testing.T, body []byte, session *accountTestSession, csrf string) (int, map[string]any) {
	t.Helper()
	status, _, raw := f.callRaw(t, http.MethodPost, "/files/image", body, session, csrf)
	var decoded map[string]any
	if len(raw) > 0 && strings.Contains(string(raw), "{") {
		_ = json.Unmarshal(raw, &decoded)
	}
	return status, decoded
}

func (f *imageFixture) auditKinds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	kinds := make([]string, 0, len(f.audits))
	for _, record := range f.audits {
		kinds = append(kinds, record.kind)
	}
	return kinds
}

func (f *imageFixture) findAudit(kind string) *imageAuditRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index := len(f.audits) - 1; index >= 0; index-- {
		if f.audits[index].kind == kind {
			return &f.audits[index]
		}
	}
	return nil
}

func uploadImageOK(t *testing.T, f *imageFixture, session *accountTestSession) map[string]any {
	t.Helper()
	status, body := f.upload(t, testPNGBytes, session, session.csrf)
	if status != http.StatusOK {
		t.Fatalf("upload status=%d body=%v", status, body)
	}
	return body
}

func TestImageUploadAuthAndCSRF(t *testing.T) {
	fixture := newImageFixture(t, AuthOn, "", nil)
	admin, _ := fixture.initSuperadmin(t, "root", "root-password-1")

	if status, _ := fixture.upload(t, testPNGBytes, nil, ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous upload status=%d, want 401", status)
	}
	if status, _ := fixture.upload(t, testPNGBytes, admin, ""); status != http.StatusForbidden {
		t.Fatalf("missing CSRF upload status=%d, want 403", status)
	}
	if status, _ := fixture.upload(t, testPNGBytes, admin, "deadbeef"); status != http.StatusForbidden {
		t.Fatalf("wrong CSRF upload status=%d, want 403", status)
	}

	body := uploadImageOK(t, fixture, admin)
	id, _ := body["id"].(string)
	if !ids.Valid(id) {
		t.Fatalf("upload id=%q, want ULID", id)
	}
	if body["url"] != "/files/image/"+id {
		t.Fatalf("upload url=%v, want relative link", body["url"])
	}
	if body["mime"] != "image/png" || body["bytes"] != float64(len(testPNGBytes)) {
		t.Fatalf("upload body=%v", body)
	}
	if body["expires_at"].(float64) <= body["created_at"].(float64) {
		t.Fatalf("expiry not after creation: %v", body)
	}
	record := fixture.findAudit("image_upload")
	if record == nil || record.source != "files" || record.payload["owner"] != admin.user["id"] || record.payload["by"] != admin.user["id"] {
		t.Fatalf("upload audit=%+v kinds=%v", record, fixture.auditKinds())
	}
}

func TestImageUploadMIMEAllowlist(t *testing.T) {
	fixture := newImageFixture(t, AuthOn, "", nil)
	admin, _ := fixture.initSuperadmin(t, "root", "root-password-1")

	cases := []struct {
		body []byte
		mime string
	}{
		{testPNGBytes, "image/png"},
		{testJPEGBytes, "image/jpeg"},
		{testGIFBytes, "image/gif"},
		{testWebPBytes, "image/webp"},
	}
	for _, test := range cases {
		status, body := fixture.upload(t, test.body, admin, admin.csrf)
		if status != http.StatusOK || body["mime"] != test.mime {
			t.Fatalf("upload %s status=%d body=%v", test.mime, status, body)
		}
	}
	if status, _ := fixture.upload(t, testTextBytes, admin, admin.csrf); status != http.StatusUnsupportedMediaType {
		t.Fatalf("text upload status=%d, want 415", status)
	}
	if status, _ := fixture.upload(t, []byte{0x00, 0x01, 0x02}, admin, admin.csrf); status != http.StatusUnsupportedMediaType {
		t.Fatalf("binary upload status=%d, want 415", status)
	}
}

func TestImageUploadSizeAndQuota(t *testing.T) {
	smallStore := func(maxBytes, quota int64) func(*Config) {
		return func(config *Config) {
			config.Images = &ImageStore{
				dataDir: t.TempDir(), ttl: DefaultImageTTL, sweepEvery: DefaultImageSweepInterval,
				maxBytes: maxBytes, ownerQuota: quota, newID: ids.New, now: time.Now, logger: testLogger(),
			}
		}
	}

	sizeFixture := newImageFixture(t, AuthOn, "", smallStore(64, DefaultImageOwnerQuota))
	admin, _ := sizeFixture.initSuperadmin(t, "root", "root-password-1")
	if status, _ := sizeFixture.upload(t, testPNGBytes, admin, admin.csrf); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload status=%d, want 413", status)
	}

	quotaFixture := newImageFixture(t, AuthOn, "", smallStore(DefaultImageMaxBytes, 150))
	admin, _ = quotaFixture.initSuperadmin(t, "root", "root-password-1")
	for range 2 {
		if status, _ := quotaFixture.upload(t, testPNGBytes, admin, admin.csrf); status != http.StatusOK {
			t.Fatalf("quota upload status=%d, want 200", status)
		}
	}
	if status, _ := quotaFixture.upload(t, testPNGBytes, admin, admin.csrf); status != http.StatusInsufficientStorage {
		t.Fatalf("quota exceeded upload status=%d, want 507", status)
	}
}

func TestImagePublicGet(t *testing.T) {
	fixture := newImageFixture(t, AuthOn, "", nil)
	admin, _ := fixture.initSuperadmin(t, "root", "root-password-1")
	uploaded := uploadImageOK(t, fixture, admin)
	id := uploaded["id"].(string)

	status, header, raw := fixture.callRaw(t, http.MethodGet, "/files/image/"+id, nil, nil, "")
	if status != http.StatusOK {
		t.Fatalf("public GET status=%d", status)
	}
	if header.Get("Content-Type") != "image/png" {
		t.Fatalf("Content-Type=%q", header.Get("Content-Type"))
	}
	if header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("X-Content-Type-Options=%q", header.Get("X-Content-Type-Options"))
	}
	if !strings.HasPrefix(header.Get("Content-Disposition"), "inline") {
		t.Fatalf("Content-Disposition=%q", header.Get("Content-Disposition"))
	}
	if !bytes.Equal(raw, testPNGBytes) {
		t.Fatalf("public GET body mismatch: %d bytes", len(raw))
	}
	record := fixture.findAudit("image_download")
	if record == nil || record.payload["by"] != "anonymous" {
		t.Fatalf("download audit=%+v", record)
	}

	if status, _, _ := fixture.callRaw(t, http.MethodGet, "/files/image/"+ids.New(), nil, nil, ""); status != http.StatusNotFound {
		t.Fatalf("unknown id GET status=%d, want 404", status)
	}
	if status, _, _ := fixture.callRaw(t, http.MethodGet, "/files/image/not-a-ulid", nil, nil, ""); status != http.StatusNotFound {
		t.Fatalf("invalid id GET status=%d, want 404", status)
	}
}

func TestImageOwnerIsolationAndAdminDelete(t *testing.T) {
	fixture := newImageFixture(t, AuthOn, "", nil)
	admin, _ := fixture.initSuperadmin(t, "root", "root-password-1")
	if _, err := fixture.accounts.CreateUser(context.Background(), "alice", "Alice", "alice-password-1"); err != nil {
		t.Fatal(err)
	}
	alice := fixture.login(t, "alice", "alice-password-1")

	ownedByAdmin := uploadImageOK(t, fixture, admin)
	if status, _, _ := fixture.callRaw(t, http.MethodDelete, "/files/image/"+ownedByAdmin["id"].(string), nil, alice, alice.csrf); status != http.StatusNotFound {
		t.Fatalf("non-owner delete status=%d, want 404", status)
	}
	if status, _, _ := fixture.callRaw(t, http.MethodGet, "/files/image/"+ownedByAdmin["id"].(string), nil, nil, ""); status != http.StatusOK {
		t.Fatalf("public GET after rejected delete status=%d", status)
	}

	ownedByAlice := uploadImageOK(t, fixture, alice)
	if status, _, _ := fixture.callRaw(t, http.MethodDelete, "/files/image/"+ownedByAlice["id"].(string), nil, admin, admin.csrf); status != http.StatusNoContent {
		t.Fatalf("superadmin delete status=%d, want 204", status)
	}
	if status, _, _ := fixture.callRaw(t, http.MethodGet, "/files/image/"+ownedByAlice["id"].(string), nil, nil, ""); status != http.StatusNotFound {
		t.Fatalf("GET after admin delete status=%d, want 404", status)
	}
	if status, _, _ := fixture.callRaw(t, http.MethodDelete, "/files/image/"+ownedByAlice["id"].(string), nil, admin, admin.csrf); status != http.StatusNotFound {
		t.Fatalf("repeat delete status=%d, want 404", status)
	}

	own := uploadImageOK(t, fixture, alice)
	if status, _, _ := fixture.callRaw(t, http.MethodDelete, "/files/image/"+own["id"].(string), nil, alice, alice.csrf); status != http.StatusNoContent {
		t.Fatalf("owner delete status=%d, want 204", status)
	}
	record := fixture.findAudit("image_delete")
	if record == nil || record.payload["by"] != alice.user["id"] {
		t.Fatalf("delete audit=%+v", record)
	}
}

func TestImageAuthOffSharedWorkspace(t *testing.T) {
	fixture := newImageFixture(t, AuthOff, "", nil)

	status, body := fixture.upload(t, testPNGBytes, nil, "")
	if status != http.StatusOK {
		t.Fatalf("off-mode upload status=%d body=%v", status, body)
	}
	record := fixture.findAudit("image_upload")
	if record == nil || record.payload["owner"] != ImageSharedOwner {
		t.Fatalf("off-mode upload audit=%+v", record)
	}
	id := body["id"].(string)
	if status, _, _ := fixture.callRaw(t, http.MethodGet, "/files/image/"+id, nil, nil, ""); status != http.StatusOK {
		t.Fatalf("off-mode GET status=%d", status)
	}
	if status, _, _ := fixture.callRaw(t, http.MethodDelete, "/files/image/"+id, nil, nil, ""); status != http.StatusNoContent {
		t.Fatalf("off-mode delete status=%d, want 204", status)
	}

	if status, _, _ := fixture.callRaw(t, http.MethodGet, "/auth/status", nil, nil, ""); status != http.StatusForbidden {
		t.Fatalf("off-mode /auth/status status=%d, want 403", status)
	}
	if status, _, _ := fixture.callRaw(t, http.MethodGet, "/admin/users", nil, nil, ""); status != http.StatusForbidden {
		t.Fatalf("off-mode /admin/users status=%d, want 403 (no implicit superadmin)", status)
	}
}

func TestImageLoopbackSharedAndGuarding(t *testing.T) {
	loopback := newImageFixture(t, AuthLoopback, "", nil)
	if status, _ := loopback.upload(t, testPNGBytes, nil, ""); status != http.StatusOK {
		t.Fatalf("loopback anonymous upload status=%d, want 200", status)
	}
	if record := loopback.findAudit("image_upload"); record == nil || record.payload["owner"] != ImageSharedOwner {
		t.Fatalf("loopback upload audit=%+v", record)
	}

	exposed := newImageFixture(t, AuthLoopback, "", func(config *Config) {
		config.Options.Listen = "0.0.0.0:0"
	})
	if status, _ := exposed.upload(t, testPNGBytes, nil, ""); status != http.StatusUnauthorized {
		t.Fatalf("non-loopback loopback-mode upload status=%d, want 401", status)
	}
}

func TestImageResetRequiredLocked(t *testing.T) {
	fixture := newImageFixture(t, AuthOn, "", nil)
	fixture.initSuperadmin(t, "root", "root-password-1")
	user, err := fixture.accounts.CreateUser(context.Background(), "bob", "Bob", "bob-password-1")
	if err != nil {
		t.Fatal(err)
	}
	bob := fixture.login(t, "bob", "bob-password-1")
	uploadImageOK(t, fixture, bob)
	if err := fixture.accounts.AdminResetUser(context.Background(), user.ID); err != nil {
		t.Fatal(err)
	}
	bob = fixture.login(t, "bob", "bob-password-1")
	if status, _ := fixture.upload(t, testPNGBytes, bob, bob.csrf); status != http.StatusForbidden {
		t.Fatalf("reset_required upload status=%d, want 403", status)
	}
	if status, _, _ := fixture.callRaw(t, http.MethodDelete, "/files/image/"+ids.New(), nil, bob, bob.csrf); status != http.StatusForbidden {
		t.Fatalf("reset_required delete status=%d, want 403", status)
	}
}

func TestImageTTLExpiryAndSweep(t *testing.T) {
	images := &ImageStore{
		dataDir: t.TempDir(), ttl: DefaultImageTTL, sweepEvery: DefaultImageSweepInterval,
		maxBytes: DefaultImageMaxBytes, ownerQuota: DefaultImageOwnerQuota, newID: ids.New, now: time.Now, logger: testLogger(),
	}
	fixture := newImageFixture(t, AuthOn, "", func(config *Config) { config.Images = images })
	admin, _ := fixture.initSuperadmin(t, "root", "root-password-1")

	expired := uploadImageOK(t, fixture, admin)
	fresh := uploadImageOK(t, fixture, admin)
	expiredPath := filepath.Join(images.root(), expired["id"].(string), imageMetaName)
	raw, err := os.ReadFile(expiredPath)
	if err != nil {
		t.Fatal(err)
	}
	var meta ImageMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	meta.ExpiresAt = time.Now().Add(-time.Hour).UnixMilli()
	encoded, _ := json.Marshal(&meta)
	if err := os.WriteFile(expiredPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	if status, _, _ := fixture.callRaw(t, http.MethodGet, "/files/image/"+expired["id"].(string), nil, nil, ""); status != http.StatusNotFound {
		t.Fatalf("expired GET status=%d, want 404", status)
	}
	if status, _, _ := fixture.callRaw(t, http.MethodGet, "/files/image/"+fresh["id"].(string), nil, nil, ""); status != http.StatusOK {
		t.Fatalf("fresh GET status=%d, want 200", status)
	}

	second := uploadImageOK(t, fixture, admin)
	secondPath := filepath.Join(images.root(), second["id"].(string), imageMetaName)
	raw, _ = os.ReadFile(secondPath)
	_ = json.Unmarshal(raw, &meta)
	meta.ExpiresAt = time.Now().Add(-time.Hour).UnixMilli()
	encoded, _ = json.Marshal(&meta)
	if err := os.WriteFile(secondPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(images.root(), ids.New()), 0o700); err != nil {
		t.Fatal(err)
	}
	removed, err := images.SweepOnce(context.Background())
	if err != nil || removed != 2 {
		t.Fatalf("SweepOnce removed=%d err=%v, want 2 (expired + corrupt dir)", removed, err)
	}
	if status, _, _ := fixture.callRaw(t, http.MethodGet, "/files/image/"+fresh["id"].(string), nil, nil, ""); status != http.StatusOK {
		t.Fatalf("fresh GET after sweep status=%d", status)
	}
}

func TestParsePublicBaseURL(t *testing.T) {
	valid := map[string]string{
		"https://example.com":            "https://example.com",
		"https://example.com/":           "https://example.com",
		"https://example.com/nexterm":    "https://example.com/nexterm",
		"https://example.com/nexterm/":   "https://example.com/nexterm",
		"http://127.0.0.1:8080":          "http://127.0.0.1:8080",
		"  https://example.com  ":        "https://example.com",
		"":                               "",
		"https://[::1]:8443/prefix/path": "https://[::1]:8443/prefix/path",
	}
	for raw, want := range valid {
		got, err := core.ParsePublicBaseURL(raw)
		if err != nil || got != want {
			t.Fatalf("ParsePublicBaseURL(%q)=%q err=%v, want %q", raw, got, err, want)
		}
	}
	invalid := []string{
		"https://user:pass@example.com",
		"https://example.com/?q=1",
		"https://example.com/#frag",
		"ftp://example.com",
		"example.com",
		"https://",
		"javascript:alert(1)",
	}
	for _, raw := range invalid {
		if got, err := core.ParsePublicBaseURL(raw); err == nil {
			t.Fatalf("ParsePublicBaseURL(%q)=%q, want error", raw, got)
		}
	}
}

func TestImagePublicBaseURLPrecedence(t *testing.T) {
	fixture := newImageFixture(t, AuthOn, "https://cli.example.com/nexterm", nil)
	admin, _ := fixture.initSuperadmin(t, "root", "root-password-1")

	body := uploadImageOK(t, fixture, admin)
	if want := "https://cli.example.com/nexterm/files/image/" + body["id"].(string); body["url"] != want {
		t.Fatalf("CLI base url=%v, want %v", body["url"], want)
	}

	if err := fixture.database.SettingSet(context.Background(), core.FilePublicBaseURLSettingKey, "https://files.example.com"); err != nil {
		t.Fatal(err)
	}
	body = uploadImageOK(t, fixture, admin)
	if want := "https://files.example.com/files/image/" + body["id"].(string); body["url"] != want {
		t.Fatalf("setting base url=%v, want %v", body["url"], want)
	}

	status, _, raw := fixture.callRaw(t, http.MethodGet, "/healthz", nil, nil, "")
	if status != http.StatusOK {
		t.Fatalf("healthz status=%d", status)
	}
	var health Health
	if err := json.Unmarshal(raw, &health); err != nil {
		t.Fatal(err)
	}
	if health.ImageLinks == nil || !health.ImageLinks.PublicBaseURLConfigured ||
		health.ImageLinks.MaxBytes != DefaultImageMaxBytes ||
		health.ImageLinks.OwnerQuotaBytes != DefaultImageOwnerQuota ||
		health.ImageLinks.TTLSeconds != int64(DefaultImageTTL.Seconds()) {
		t.Fatalf("health imageLinks=%+v", health.ImageLinks)
	}
}

func TestImageAdminSettingsPublicBaseURL(t *testing.T) {
	fixture := newImageFixture(t, AuthOn, "", nil)
	admin, _ := fixture.initSuperadmin(t, "root", "root-password-1")

	status, _, raw := fixture.callRaw(t, http.MethodGet, "/admin/settings", nil, admin, "")
	if status != http.StatusOK || !strings.Contains(string(raw), `"public_base_url":""`) {
		t.Fatalf("settings get status=%d body=%s", status, raw)
	}

	call := func(payload string) (int, []byte) {
		request, err := http.NewRequest(http.MethodPut, fixture.http.URL+"/admin/settings", strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(admin.cookie)
		request.Header.Set(csrfHeaderName, admin.csrf)
		response, err := fixture.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		return response.StatusCode, raw
	}

	if status, raw := call(`{"registration_open":false,"public_base_url":"https://example.com/nexterm/"}`); status != http.StatusOK ||
		!strings.Contains(string(raw), `"public_base_url":"https://example.com/nexterm"`) {
		t.Fatalf("settings put status=%d body=%s", status, raw)
	}
	if status, _ := call(`{"registration_open":false,"public_base_url":"https://user:pw@example.com"}`); status != http.StatusBadRequest {
		t.Fatalf("invalid settings put status=%d, want 400", status)
	}
	if status, raw := call(`{"registration_open":false}`); status != http.StatusOK ||
		!strings.Contains(string(raw), `"public_base_url":"https://example.com/nexterm"`) {
		t.Fatalf("absent public_base_url must preserve stored value, status=%d body=%s", status, raw)
	}
	if status, raw := call(`{"registration_open":false,"public_base_url":""}`); status != http.StatusOK ||
		!strings.Contains(string(raw), `"public_base_url":""`) {
		t.Fatalf("clear settings put status=%d body=%s", status, raw)
	}
}

func TestImageCLIOption(t *testing.T) {
	getenv := func(string) string { return "" }
	invocation, err := core.ParseCLI([]string{"--public-base-url", "https://example.com/nexterm/"}, core.CommandServe, getenv)
	if err != nil || invocation.PublicBaseURL != "https://example.com/nexterm" {
		t.Fatalf("flag parse=%q err=%v", invocation.PublicBaseURL, err)
	}
	invocation, err = core.ParseCLI(nil, core.CommandServe, func(key string) string {
		if key == "NEXTERM_PUBLIC_BASE_URL" {
			return "https://env.example.com"
		}
		return ""
	})
	if err != nil || invocation.PublicBaseURL != "https://env.example.com" {
		t.Fatalf("env parse=%q err=%v", invocation.PublicBaseURL, err)
	}
	if _, err := core.ParseCLI([]string{"--public-base-url", "https://user:pw@example.com"}, core.CommandServe, getenv); err == nil {
		t.Fatal("invalid flag accepted")
	}
	if _, err := core.ParseCLI(nil, core.CommandServe, func(key string) string {
		if key == "NEXTERM_PUBLIC_BASE_URL" {
			return "not a url"
		}
		return ""
	}); err == nil {
		t.Fatal("invalid env accepted")
	}
}

func TestImageEnvOverrides(t *testing.T) {
	getenv := func(key string) string {
		switch key {
		case "NEXTERM_IMAGE_MAX_BYTES":
			return "1048576"
		case "NEXTERM_IMAGE_OWNER_MAX_BYTES":
			return "2097152"
		case "NEXTERM_IMAGE_TTL":
			return "720h"
		}
		return ""
	}
	maxBytes, err := ParseImageMaxBytesEnv(getenv)
	if err != nil || maxBytes != 1048576 {
		t.Fatalf("max bytes=%d err=%v", maxBytes, err)
	}
	quota, err := ParseImageOwnerQuotaEnv(getenv)
	if err != nil || quota != 2097152 {
		t.Fatalf("quota=%d err=%v", quota, err)
	}
	ttl, err := ParseImageTTLEnv(getenv)
	if err != nil || ttl != MaxImageTTL {
		t.Fatalf("ttl=%v err=%v, want clamp to 7d", ttl, err)
	}
	if _, err := ParseImageTTLEnv(func(string) string { return "0s" }); err == nil {
		t.Fatal("zero TTL accepted")
	}
	if _, err := ParseImageTTLEnv(func(string) string { return "nonsense" }); err == nil {
		t.Fatal("invalid TTL accepted")
	}
}

func TestImageDoesNotExposePersistedFiles(t *testing.T) {
	fixture := newImageFixture(t, AuthOn, "", nil)
	persistID := ids.New()
	secretDir := filepath.Join(fixture.server.options.DataDir, "files", persistID)
	if err := os.MkdirAll(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretDir, "secret.key"), []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := fixture.callRaw(t, http.MethodGet, "/files/image/"+persistID, nil, nil, ""); status != http.StatusNotFound {
		t.Fatalf("persisted file via image route status=%d, want 404", status)
	}
	if status, _, raw := fixture.callRaw(t, http.MethodGet, "/files/image/"+persistID+"/secret.key", nil, nil, ""); status != http.StatusNotFound {
		t.Fatalf("persisted path via image route status=%d body=%s", status, raw)
	}
}
