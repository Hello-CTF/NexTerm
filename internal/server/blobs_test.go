package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestBlobStageReserveStreamAndDelete(t *testing.T) {
	config := testConfig(t, false)
	store := NewBlobStore(config.Options.DataDir, testLogger())
	config.Blobs = store
	_, httpServer := newTestHTTP(t, config)
	payload := bytes.Repeat([]byte{0, 1, 2, 3, 0xff, 0xfe, 0x80, 0x7f}, 128*1024)

	response, err := httpServer.Client().Post(httpServer.URL+"/files/blob?name="+url.QueryEscape(`../../报表 2026.bin`), "application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	staged := decodeStagedBlob(t, response)
	if staged.ID == "" || staged.Bytes == nil || *staged.Bytes != int64(len(payload)) {
		t.Fatalf("staged = %+v", staged)
	}
	if staged.Path != staged.ID+"/报表 2026.bin" {
		t.Fatalf("staged path = %q", staged.Path)
	}
	if strings.Contains(staged.Path, config.Options.DataDir) || filepath.IsAbs(staged.Path) {
		t.Fatalf("staged path leaks server filesystem layout: %q", staged.Path)
	}
	response, err = httpServer.Client().Get(httpServer.URL + "/files/blob?id=" + url.QueryEscape(staged.ID))
	if err != nil {
		t.Fatal(err)
	}
	downloaded, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !bytes.Equal(downloaded, payload) {
		t.Fatalf("download status=%d bytes=%d", response.StatusCode, len(downloaded))
	}
	if disposition := response.Header.Get("Content-Disposition"); !strings.Contains(disposition, "filename*=UTF-8''") || !strings.Contains(disposition, "%E6%8A%A5%E8%A1%A8") {
		t.Fatalf("content disposition = %q", disposition)
	}

	request, err := http.NewRequest(http.MethodDelete, httpServer.URL+"/files/blob?id="+url.QueryEscape(staged.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", response.StatusCode)
	}
	response, err = httpServer.Client().Get(httpServer.URL + "/files/blob?id=" + url.QueryEscape(staged.ID))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted download status = %d", response.StatusCode)
	}

	response, err = httpServer.Client().Post(httpServer.URL+"/files/blob/reserve?name=report.txt", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	reserved := decodeStagedBlob(t, response)
	if reserved.Bytes != nil || reserved.Path != reserved.ID+"/report.txt" {
		t.Fatalf("reserved = %+v", reserved)
	}
	if err := os.WriteFile(filepath.Join(config.Options.DataDir, "blobs", reserved.ID, "report.txt"), []byte("written by shared fs core"), 0o600); err != nil {
		t.Fatal(err)
	}
	response, err = httpServer.Client().Get(httpServer.URL + "/files/blob?id=" + url.QueryEscape(reserved.ID))
	if err != nil {
		t.Fatal(err)
	}
	downloaded, err = io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(downloaded) != "written by shared fs core" {
		t.Fatalf("reserved download = %q, %v", downloaded, err)
	}
}

func TestBlobPersistenceTTLAndIDBoundary(t *testing.T) {
	config := testConfig(t, false)
	store := NewBlobStore(config.Options.DataDir, testLogger())
	store.diskUsage = func(string) (float64, error) { return 0, nil }
	config.Blobs = store
	_, httpServer := newTestHTTP(t, config)

	response, err := httpServer.Client().Post(httpServer.URL+"/files/blob?name=id_ed25519&persist=1", "application/octet-stream", strings.NewReader("private-key"))
	if err != nil {
		t.Fatal(err)
	}
	persistent := decodeStagedBlob(t, response)
	if persistent.Path != persistent.ID+"/id_ed25519" {
		t.Fatalf("persistent path = %q", persistent.Path)
	}
	persistentFile := filepath.Join(config.Options.DataDir, "files", persistent.ID, "id_ed25519")
	response, err = httpServer.Client().Post(httpServer.URL+"/files/blob?name=temporary.txt", "application/octet-stream", strings.NewReader("temporary"))
	if err != nil {
		t.Fatal(err)
	}
	temporary := decodeStagedBlob(t, response)
	temporaryFile := filepath.Join(config.Options.DataDir, "blobs", temporary.ID, "temporary.txt")

	removed, err := store.SweepOnce(context.Background())
	if err != nil || removed != 0 {
		t.Fatalf("fresh sweep = %d, %v", removed, err)
	}
	store.now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	removed, err = store.SweepOnce(context.Background())
	if err != nil || removed != 1 {
		t.Fatalf("expired sweep = %d, %v", removed, err)
	}
	if _, err := os.Stat(persistentFile); err != nil {
		t.Fatalf("persistent file was swept: %v", err)
	}
	if _, err := os.Stat(temporaryFile); !os.IsNotExist(err) {
		t.Fatalf("temporary file still exists: %v", err)
	}

	response, err = httpServer.Client().Get(httpServer.URL + "/files/blob?id=" + url.QueryEscape(persistent.ID))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("persistent blob was publicly readable: %d", response.StatusCode)
	}
	response, err = httpServer.Client().Get(httpServer.URL + "/files/blob?id=" + url.QueryEscape("../../etc/passwd"))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("traversal id status = %d", response.StatusCode)
	}
}

func TestSafeBlobName(t *testing.T) {
	for input, expected := range map[string]string{
		"report.txt":         "report.txt",
		"../../etc/passwd":   "passwd",
		`..\..\win.ini`:      "win.ini",
		"/absolute/path.log": "path.log",
		"..":                 "blob",
		"":                   "blob",
		"a\x00b":             "a_b",
	} {
		if actual := SafeBlobName(input); actual != expected {
			t.Errorf("SafeBlobName(%q) = %q, want %q", input, actual, expected)
		}
	}
	if actual, expected := SafeBlobName(strings.Repeat("界", 120)), strings.Repeat("界", 80); actual != expected {
		t.Errorf("long UTF-8 name has %d bytes, want %d: %q", len(actual), len(expected), actual)
	}
}

func TestBlobBodyLimitRejectsOversizedUploads(t *testing.T) {
	config := testConfig(t, false)
	store := NewBlobStore(config.Options.DataDir, testLogger())
	store.maxBytes = 64
	config.Blobs = store
	_, httpServer := newTestHTTP(t, config)

	response, err := httpServer.Client().Post(httpServer.URL+"/files/blob?name=a.bin", "application/octet-stream", bytes.NewReader(make([]byte, 64)))
	if err != nil {
		t.Fatal(err)
	}
	decodeStagedBlob(t, response)

	response, err = httpServer.Client().Post(httpServer.URL+"/files/blob?name=big.bin", "application/octet-stream", bytes.NewReader(make([]byte, 65)))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(body), "大小限制") {
		t.Fatalf("oversized status = %d: %s", response.StatusCode, body)
	}

	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/files/blob?name=chunked.bin", bytes.NewReader(make([]byte, 65)))
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = -1
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err = httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(body), "大小限制") {
		t.Fatalf("chunked oversized status = %d: %s", response.StatusCode, body)
	}

	response, err = httpServer.Client().Post(httpServer.URL+"/files/blob/reserve?name=r.txt", "application/octet-stream", bytes.NewReader(make([]byte, 65)))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("reserve oversized status = %d", response.StatusCode)
	}

	entries, err := os.ReadDir(filepath.Join(config.Options.DataDir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("staging dirs after rejects = %d, want 1", len(entries))
	}
}

func TestBlobPersistQuotaRejectsAndSurvivesSweep(t *testing.T) {
	config := testConfig(t, false)
	store := NewBlobStore(config.Options.DataDir, testLogger())
	store.persistQuota = 128
	store.diskUsage = func(string) (float64, error) { return 0, nil }
	config.Blobs = store
	_, httpServer := newTestHTTP(t, config)

	persist := func(name string, size int) *http.Response {
		t.Helper()
		response, err := httpServer.Client().Post(httpServer.URL+"/files/blob?name="+name+"&persist=1", "application/octet-stream", bytes.NewReader(make([]byte, size)))
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	first := decodeStagedBlob(t, persist("a.bin", 64))
	firstFile := filepath.Join(config.Options.DataDir, "files", first.ID, "a.bin")
	response := persist("big.bin", 65)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusInsufficientStorage || !strings.Contains(string(body), "配额已用尽") {
		t.Fatalf("known-length quota status = %d: %s", response.StatusCode, body)
	}
	second := decodeStagedBlob(t, persist("b.bin", 64))
	secondFile := filepath.Join(config.Options.DataDir, "files", second.ID, "b.bin")
	response = persist("c.bin", 1)
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusInsufficientStorage || !strings.Contains(string(body), "配额已用尽") {
		t.Fatalf("full quota status = %d: %s", response.StatusCode, body)
	}

	response, err := httpServer.Client().Post(httpServer.URL+"/files/blob?name=t.bin", "application/octet-stream", bytes.NewReader(make([]byte, 8)))
	if err != nil {
		t.Fatal(err)
	}
	temporary := decodeStagedBlob(t, response)
	temporaryFile := filepath.Join(config.Options.DataDir, "blobs", temporary.ID, "t.bin")

	store.now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	removed, err := store.SweepOnce(context.Background())
	if err != nil || removed != 1 {
		t.Fatalf("sweep = %d, %v", removed, err)
	}
	for _, path := range []string{firstFile, secondFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("persisted file was swept: %v", err)
		}
	}
	if _, err := os.Stat(temporaryFile); !os.IsNotExist(err) {
		t.Fatalf("staged file still exists: %v", err)
	}
}

func TestParseBlobLimitEnv(t *testing.T) {
	if value, err := ParseBlobMaxBytesEnv(func(string) string { return "" }); err != nil || value != 0 {
		t.Fatalf("empty max bytes = %d, %v", value, err)
	}
	if value, err := ParseBlobMaxBytesEnv(func(string) string { return "4096" }); err != nil || value != 4096 {
		t.Fatalf("valid max bytes = %d, %v", value, err)
	}
	for _, invalid := range []string{"abc", "0", "-3"} {
		if _, err := ParseBlobMaxBytesEnv(func(string) string { return invalid }); err == nil {
			t.Fatalf("ParseBlobMaxBytesEnv(%q) accepted", invalid)
		}
	}
	if value, err := ParseBlobPersistQuotaEnv(func(string) string { return "8192" }); err != nil || value != 8192 {
		t.Fatalf("valid persist quota = %d, %v", value, err)
	}
	if _, err := ParseBlobPersistQuotaEnv(func(string) string { return "1g" }); err == nil {
		t.Fatal("ParseBlobPersistQuotaEnv accepted 1g")
	}
	if value, err := ParseBlobDiskMaxPercentEnv(func(string) string { return "" }); err != nil || value != 0 {
		t.Fatalf("empty disk percent = %f, %v", value, err)
	}
	if value, err := ParseBlobDiskMaxPercentEnv(func(string) string { return "90.5" }); err != nil || value != 90.5 {
		t.Fatalf("valid disk percent = %f, %v", value, err)
	}
	for _, invalid := range []string{"abc", "0", "-1", "100.1"} {
		if _, err := ParseBlobDiskMaxPercentEnv(func(string) string { return invalid }); err == nil {
			t.Fatalf("ParseBlobDiskMaxPercentEnv(%q) accepted", invalid)
		}
	}
}

func TestNewBlobStoreEnvOverrides(t *testing.T) {
	t.Setenv("NEXTERM_BLOB_MAX_BYTES", "1024")
	t.Setenv("NEXTERM_BLOB_PERSIST_MAX_BYTES", "4096")
	t.Setenv("NEXTERM_BLOB_DISK_MAX_PERCENT", "85")
	store := NewBlobStore(t.TempDir(), testLogger())
	if store.maxBlobBytes() != 1024 {
		t.Fatalf("maxBlobBytes = %d", store.maxBlobBytes())
	}
	if store.persistQuotaBytes() != 4096 {
		t.Fatalf("persistQuotaBytes = %d", store.persistQuotaBytes())
	}
	if store.diskThresholdPercent() != 85 {
		t.Fatalf("diskThresholdPercent = %f", store.diskThresholdPercent())
	}

	t.Setenv("NEXTERM_BLOB_MAX_BYTES", "not-a-number")
	store = NewBlobStore(t.TempDir(), testLogger())
	if store.maxBlobBytes() != DefaultBlobMaxBytes {
		t.Fatalf("invalid env maxBlobBytes = %d", store.maxBlobBytes())
	}
	if store.persistQuotaBytes() != 4096 {
		t.Fatalf("persistQuotaBytes after invalid sibling env = %d", store.persistQuotaBytes())
	}
	if store.diskThresholdPercent() != 85 {
		t.Fatalf("diskThresholdPercent after invalid sibling env = %f", store.diskThresholdPercent())
	}
}

func TestBlobPersistDiskThresholdRejects(t *testing.T) {
	config := testConfig(t, false)
	store := NewBlobStore(config.Options.DataDir, testLogger())
	store.diskUsage = func(string) (float64, error) { return 0.95, nil }
	config.Blobs = store
	_, httpServer := newTestHTTP(t, config)

	response, err := httpServer.Client().Post(httpServer.URL+"/files/blob?name=p.bin&persist=1", "application/octet-stream", bytes.NewReader(make([]byte, 8)))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusInsufficientStorage || !strings.Contains(string(body), "磁盘使用率超过阈值") {
		t.Fatalf("disk threshold status = %d: %s", response.StatusCode, body)
	}

	response, err = httpServer.Client().Post(httpServer.URL+"/files/blob?name=s.bin", "application/octet-stream", bytes.NewReader(make([]byte, 8)))
	if err != nil {
		t.Fatal(err)
	}
	decodeStagedBlob(t, response)

	store.diskUsage = func(string) (float64, error) { return 0.5, nil }
	response, err = httpServer.Client().Post(httpServer.URL+"/files/blob?name=p.bin&persist=1", "application/octet-stream", bytes.NewReader(make([]byte, 8)))
	if err != nil {
		t.Fatal(err)
	}
	decodeStagedBlob(t, response)

	store.diskUsage = func(string) (float64, error) { return 0, errors.New("statfs failed") }
	response, err = httpServer.Client().Post(httpServer.URL+"/files/blob?name=p.bin&persist=1", "application/octet-stream", bytes.NewReader(make([]byte, 8)))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError || !strings.Contains(string(body), "磁盘用量获取失败") {
		t.Fatalf("disk stat failure status = %d: %s", response.StatusCode, body)
	}
}

func TestDiskUsageRate(t *testing.T) {
	usage, err := diskUsageRate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if usage < 0 || usage > 1 {
		t.Fatalf("disk usage rate = %f", usage)
	}
	if usage, err := diskUsageRate(filepath.Join(t.TempDir(), "missing")); err == nil || usage != 0 {
		t.Fatalf("missing path = %f, %v", usage, err)
	}
}

func TestBlobOwnerEnforcedOnDownloadAndDelete(t *testing.T) {
	store := NewBlobStore(t.TempDir(), testLogger())
	owner := &account.Identity{UserID: "user-owner", Role: account.RoleUser, State: account.StateActive}
	other := &account.Identity{UserID: "user-other", Role: account.RoleUser, State: account.StateActive}
	admin := &account.Identity{UserID: "user-admin", Role: account.RoleSuperadmin, State: account.StateActive}

	request := func(method, target string, body io.Reader, identity *account.Identity) *http.Request {
		t.Helper()
		r := httptest.NewRequest(method, target, body)
		if identity != nil {
			r = r.WithContext(withAccountIdentity(r.Context(), identity))
		}
		return r
	}
	stage := func(name, content string, identity *account.Identity) stagedBlob {
		t.Helper()
		recorder := httptest.NewRecorder()
		store.Stage(recorder, request(http.MethodPost, "/files/blob?name="+name, strings.NewReader(content), identity))
		return decodeStagedBlob(t, recorder.Result())
	}
	download := func(id string, identity *account.Identity) (int, string) {
		t.Helper()
		recorder := httptest.NewRecorder()
		store.Download(recorder, request(http.MethodGet, "/files/blob?id="+id, nil, identity))
		body, _ := io.ReadAll(recorder.Result().Body)
		return recorder.Code, string(body)
	}
	remove := func(id string, identity *account.Identity) int {
		t.Helper()
		recorder := httptest.NewRecorder()
		store.Delete(recorder, request(http.MethodDelete, "/files/blob?id="+id, nil, identity))
		return recorder.Code
	}

	owned := stage("owned.bin", "owned-by-user", owner)
	if status, body := download(owned.ID, owner); status != http.StatusOK || body != "owned-by-user" {
		t.Fatalf("owner download = %d %q", status, body)
	}
	if status, _ := download(owned.ID, other); status != http.StatusNotFound {
		t.Fatalf("non-owner download status = %d, want 404", status)
	}
	if status, _ := download(owned.ID, nil); status != http.StatusNotFound {
		t.Fatalf("anonymous download status = %d, want 404", status)
	}
	if status, body := download(owned.ID, admin); status != http.StatusOK || body != "owned-by-user" {
		t.Fatalf("superadmin download = %d %q", status, body)
	}
	if status := remove(owned.ID, other); status != http.StatusNotFound {
		t.Fatalf("non-owner delete status = %d, want 404", status)
	}
	if _, err := os.Stat(filepath.Join(store.stageRoot(), owned.ID)); err != nil {
		t.Fatalf("non-owner delete removed the blob: %v", err)
	}
	if status := remove(owned.ID, admin); status != http.StatusNoContent {
		t.Fatalf("superadmin delete status = %d", status)
	}

	reservedRecorder := httptest.NewRecorder()
	store.Reserve(reservedRecorder, request(http.MethodPost, "/files/blob/reserve?name=reserved.bin", nil, owner))
	reserved := decodeStagedBlob(t, reservedRecorder.Result())
	if status, _ := download(reserved.ID, other); status != http.StatusNotFound {
		t.Fatalf("non-owner reserved download status = %d, want 404", status)
	}
	if status, body := download(reserved.ID, owner); status != http.StatusOK || body != "" {
		t.Fatalf("owner reserved download = %d %q", status, body)
	}
	if status := remove(reserved.ID, owner); status != http.StatusNoContent {
		t.Fatalf("owner delete status = %d", status)
	}

	legacy := stage("legacy.bin", "no-owner-recorded", nil)
	if status, _ := download(legacy.ID, other); status != http.StatusNotFound {
		t.Fatalf("blob without owner metadata must be inaccessible: %d", status)
	}
	if status, _ := download(legacy.ID, nil); status != http.StatusNotFound {
		t.Fatalf("anonymous ownerless download status = %d, want 404", status)
	}
}

func TestBlobOpenAccessOwnerlessEntries(t *testing.T) {
	store := NewBlobStore(t.TempDir(), testLogger())
	store.openAccess = true
	owner := &account.Identity{UserID: "user-owner", Role: account.RoleUser, State: account.StateActive}
	other := &account.Identity{UserID: "user-other", Role: account.RoleUser, State: account.StateActive}

	request := func(method, target string, body io.Reader, identity *account.Identity) *http.Request {
		t.Helper()
		r := httptest.NewRequest(method, target, body)
		if identity != nil {
			r = r.WithContext(withAccountIdentity(r.Context(), identity))
		}
		return r
	}
	stage := func(name, content string, identity *account.Identity) stagedBlob {
		t.Helper()
		recorder := httptest.NewRecorder()
		store.Stage(recorder, request(http.MethodPost, "/files/blob?name="+name, strings.NewReader(content), identity))
		return decodeStagedBlob(t, recorder.Result())
	}
	download := func(id string, identity *account.Identity) (int, string) {
		t.Helper()
		recorder := httptest.NewRecorder()
		store.Download(recorder, request(http.MethodGet, "/files/blob?id="+id, nil, identity))
		body, _ := io.ReadAll(recorder.Result().Body)
		return recorder.Code, string(body)
	}

	ownerless := stage("ownerless.bin", "no-owner-recorded", nil)
	if status, body := download(ownerless.ID, nil); status != http.StatusOK || body != "no-owner-recorded" {
		t.Fatalf("open-access ownerless download = %d %q", status, body)
	}

	owned := stage("owned.bin", "owned-by-user", owner)
	if status, _ := download(owned.ID, nil); status != http.StatusNotFound {
		t.Fatalf("open-access anonymous owned download status = %d, want 404", status)
	}
	if status, _ := download(owned.ID, other); status != http.StatusNotFound {
		t.Fatalf("open-access non-owner download status = %d, want 404", status)
	}
	if status, body := download(owned.ID, owner); status != http.StatusOK || body != "owned-by-user" {
		t.Fatalf("open-access owner download = %d %q", status, body)
	}
}

func TestBlobOpenAccessFollowsDeploymentForm(t *testing.T) {
	newWithForm := func(t *testing.T, mutate func(*Config)) *BlobStore {
		t.Helper()
		blobs := NewBlobStore(t.TempDir(), testLogger())
		config := testConfig(t, false)
		config.Blobs = blobs
		mutate(&config)
		server, err := New(config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = server.Close() })
		return blobs
	}

	if blobs := newWithForm(t, func(*Config) {}); !blobs.openAccess {
		t.Fatal("token form (no accounts) must open ownerless blobs")
	}
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	accounts := account.New(database.DB())
	if blobs := newWithForm(t, func(config *Config) {
		config.Options.Auth = AuthOn
		config.Accounts = accounts
		config.Tokens = nil
	}); blobs.openAccess {
		t.Fatal("account form must keep ownerless blobs closed")
	}
	if blobs := newWithForm(t, func(config *Config) {
		config.Options.Auth = AuthOff
		config.Accounts = accounts
	}); !blobs.openAccess {
		t.Fatal("auth=off must open ownerless blobs")
	}
}

func TestBlobResolveStagedPath(t *testing.T) {
	store := NewBlobStore(t.TempDir(), testLogger())
	store.diskUsage = func(string) (float64, error) { return 0, nil }
	owner := &account.Identity{UserID: "user-owner", Role: account.RoleUser, State: account.StateActive}
	other := &account.Identity{UserID: "user-other", Role: account.RoleUser, State: account.StateActive}

	stage := func(name, content string, identity *account.Identity) stagedBlob {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/files/blob?name="+name, strings.NewReader(content))
		if identity != nil {
			r = r.WithContext(withAccountIdentity(r.Context(), identity))
		}
		recorder := httptest.NewRecorder()
		store.Stage(recorder, r)
		return decodeStagedBlob(t, recorder.Result())
	}

	first := stage("first.bin", "first-content", owner)
	resolved, err := store.ResolveStagedPath(first.Path, owner.UserID)
	if err != nil {
		t.Fatalf("owner resolve: %v", err)
	}
	if resolved != filepath.Join(store.stageRoot(), first.ID, "first.bin") {
		t.Fatalf("resolved path = %q", resolved)
	}
	if data, err := os.ReadFile(resolved); err != nil || string(data) != "first-content" {
		t.Fatalf("resolved file = %q, %v", data, err)
	}

	ownerless := stage("ownerless.bin", "ownerless-content", nil)
	if _, err := store.ResolveStagedPath(ownerless.Path, ""); !errors.Is(err, errStagedNotFound) {
		t.Fatalf("ownerless resolve = %v, want errStagedNotFound", err)
	}

	spaced := stage(url.QueryEscape("my file.bin"), "spaced-content", owner)
	if spaced.Path != spaced.ID+"/my file.bin" {
		t.Fatalf("spaced staged path = %q", spaced.Path)
	}
	if path, err := store.ResolveStagedPath(spaced.Path, owner.UserID); err != nil || path == "" {
		t.Fatalf("resolve name kept by SafeBlobName = %q, %v", path, err)
	}

	owned := stage("owned.bin", "owned-content", owner)
	if path, err := store.ResolveStagedPath(owned.Path, owner.UserID); err != nil || path == "" {
		t.Fatalf("owner resolve = %q, %v", path, err)
	}
	for _, userID := range []string{"", other.UserID} {
		if _, err := store.ResolveStagedPath(owned.Path, userID); !errors.Is(err, errStagedNotFound) {
			t.Fatalf("resolve as %q = %v, want errStagedNotFound", userID, err)
		}
	}

	persistRecorder := httptest.NewRecorder()
	store.Stage(persistRecorder, httptest.NewRequest(http.MethodPost, "/files/blob?name=key.pem&persist=1", strings.NewReader("key")))
	persist := decodeStagedBlob(t, persistRecorder.Result())
	if _, err := store.ResolveStagedPath(persist.Path, ""); !errors.Is(err, errStagedNotFound) {
		t.Fatalf("persist resolve = %v, want errStagedNotFound", err)
	}

	for _, malformed := range []string{
		"", "no-slash", "short/name", first.ID + "/..", first.ID + "/.meta.json",
		first.ID + "/.META.JSON", first.ID + "/.meta.json ", first.ID + "/.hidden",
		first.ID + "/na\x01me.bin", first.ID + "/name.bin ", first.ID + "/nested/name.bin",
		first.ID + `\name.bin`, "/abs/name.bin", ids.New() + "/missing.bin",
	} {
		if _, err := store.ResolveStagedPath(malformed, ""); !errors.Is(err, errStagedNotFound) {
			t.Fatalf("ResolveStagedPath(%q) = %v, want errStagedNotFound", malformed, err)
		}
	}
}

func decodeStagedBlob(t *testing.T, response *http.Response) stagedBlob {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	var body struct {
		OK   bool       `json:"ok"`
		Data stagedBlob `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.OK {
		t.Fatalf("response = %+v", body)
	}
	return body.Data
}
