package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if filepath.Base(staged.Path) != "报表 2026.bin" || filepath.Dir(filepath.Dir(staged.Path)) != filepath.Join(config.Options.DataDir, "blobs") {
		t.Fatalf("staged path = %q", staged.Path)
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
	if reserved.Bytes != nil || filepath.Base(reserved.Path) != "report.txt" {
		t.Fatalf("reserved = %+v", reserved)
	}
	if err := os.WriteFile(reserved.Path, []byte("written by shared fs core"), 0o600); err != nil {
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
	config.Blobs = store
	_, httpServer := newTestHTTP(t, config)

	response, err := httpServer.Client().Post(httpServer.URL+"/files/blob?name=id_ed25519&persist=1", "application/octet-stream", strings.NewReader("private-key"))
	if err != nil {
		t.Fatal(err)
	}
	persistent := decodeStagedBlob(t, response)
	if filepath.Dir(filepath.Dir(persistent.Path)) != filepath.Join(config.Options.DataDir, "files") {
		t.Fatalf("persistent path = %q", persistent.Path)
	}
	response, err = httpServer.Client().Post(httpServer.URL+"/files/blob?name=temporary.txt", "application/octet-stream", strings.NewReader("temporary"))
	if err != nil {
		t.Fatal(err)
	}
	temporary := decodeStagedBlob(t, response)

	removed, err := store.SweepOnce(context.Background())
	if err != nil || removed != 0 {
		t.Fatalf("fresh sweep = %d, %v", removed, err)
	}
	store.now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	removed, err = store.SweepOnce(context.Background())
	if err != nil || removed != 1 {
		t.Fatalf("expired sweep = %d, %v", removed, err)
	}
	if _, err := os.Stat(persistent.Path); err != nil {
		t.Fatalf("persistent file was swept: %v", err)
	}
	if _, err := os.Stat(temporary.Path); !os.IsNotExist(err) {
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
	if response.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(body), "maximum allowed size") {
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
	if response.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(body), "maximum allowed size") {
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
	response := persist("big.bin", 65)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusInsufficientStorage || !strings.Contains(string(body), "quota exceeded") {
		t.Fatalf("known-length quota status = %d: %s", response.StatusCode, body)
	}
	second := decodeStagedBlob(t, persist("b.bin", 64))
	response = persist("c.bin", 1)
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusInsufficientStorage || !strings.Contains(string(body), "quota exceeded") {
		t.Fatalf("full quota status = %d: %s", response.StatusCode, body)
	}

	response, err := httpServer.Client().Post(httpServer.URL+"/files/blob?name=t.bin", "application/octet-stream", bytes.NewReader(make([]byte, 8)))
	if err != nil {
		t.Fatal(err)
	}
	temporary := decodeStagedBlob(t, response)

	store.now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	removed, err := store.SweepOnce(context.Background())
	if err != nil || removed != 1 {
		t.Fatalf("sweep = %d, %v", removed, err)
	}
	for _, path := range []string{first.Path, second.Path} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("persisted file was swept: %v", err)
		}
	}
	if _, err := os.Stat(temporary.Path); !os.IsNotExist(err) {
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
}

func TestNewBlobStoreEnvOverrides(t *testing.T) {
	t.Setenv("NEXTERM_BLOB_MAX_BYTES", "1024")
	t.Setenv("NEXTERM_BLOB_PERSIST_MAX_BYTES", "4096")
	store := NewBlobStore(t.TempDir(), testLogger())
	if store.maxBlobBytes() != 1024 {
		t.Fatalf("maxBlobBytes = %d", store.maxBlobBytes())
	}
	if store.persistQuotaBytes() != 4096 {
		t.Fatalf("persistQuotaBytes = %d", store.persistQuotaBytes())
	}

	t.Setenv("NEXTERM_BLOB_MAX_BYTES", "not-a-number")
	store = NewBlobStore(t.TempDir(), testLogger())
	if store.maxBlobBytes() != DefaultBlobMaxBytes {
		t.Fatalf("invalid env maxBlobBytes = %d", store.maxBlobBytes())
	}
	if store.persistQuotaBytes() != 4096 {
		t.Fatalf("persistQuotaBytes after invalid sibling env = %d", store.persistQuotaBytes())
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
