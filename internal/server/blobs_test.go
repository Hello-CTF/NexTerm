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
