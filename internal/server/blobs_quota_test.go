package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
)

func newQuotaTestBlobStore(t *testing.T) *BlobStore {
	t.Helper()
	store := NewBlobStore(t.TempDir(), testLogger())
	store.diskUsage = func(string) (float64, error) { return 0, nil }
	return store
}

func blobRequest(method, target string, body io.Reader, identity *account.Identity) *http.Request {
	r := httptest.NewRequest(method, target, body)
	if identity != nil {
		r = r.WithContext(withAccountIdentity(r.Context(), identity))
	}
	return r
}

// unknownLength hides the body length so Stage cannot reject it up front.
type unknownLength struct{ io.Reader }

func TestBlobStageQuotaIsPerOwner(t *testing.T) {
	store := newQuotaTestBlobStore(t)
	store.stageQuota = 10
	owner := &account.Identity{UserID: "user-owner", Role: account.RoleUser, State: account.StateActive}
	other := &account.Identity{UserID: "user-other", Role: account.RoleUser, State: account.StateActive}
	stage := func(content string, identity *account.Identity, declared bool) *httptest.ResponseRecorder {
		t.Helper()
		var body io.Reader = strings.NewReader(content)
		if !declared {
			body = unknownLength{body}
		}
		recorder := httptest.NewRecorder()
		store.Stage(recorder, blobRequest(http.MethodPost, "/files/blob?name=part.bin", body, identity))
		return recorder
	}

	if recorder := stage("12345678", owner, true); recorder.Code != http.StatusOK {
		t.Fatalf("stage within quota = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := stage("abcd", owner, true); recorder.Code != http.StatusInsufficientStorage || !strings.Contains(recorder.Body.String(), "暂存空间已达上限") {
		t.Fatalf("declared stage past quota = %d %q, want 507", recorder.Code, recorder.Body.String())
	}
	if recorder := stage("abcd", owner, false); recorder.Code != http.StatusInsufficientStorage {
		t.Fatalf("unknown-length stage past quota = %d, want 507", recorder.Code)
	}
	if usage, entries, err := store.stageUsage(owner.UserID, ""); err != nil || usage != 8 || entries != 1 {
		t.Fatalf("owner usage after rejections = %d bytes, %d entries, %v; want the rejected uploads removed", usage, entries, err)
	}
	if recorder := stage("abcd", other, true); recorder.Code != http.StatusOK {
		t.Fatalf("another owner's stage = %d, want its own quota", recorder.Code)
	}

	reserve := httptest.NewRecorder()
	store.stageQuota = 8
	store.Reserve(reserve, blobRequest(http.MethodPost, "/files/blob/reserve?name=r.bin", nil, owner))
	if reserve.Code != http.StatusInsufficientStorage {
		t.Fatalf("reserve with an exhausted quota = %d, want 507", reserve.Code)
	}
}

func TestBlobStageEntryLimitRejectsReservations(t *testing.T) {
	store := newQuotaTestBlobStore(t)
	store.stageEntries = 2
	owner := &account.Identity{UserID: "user-owner", Role: account.RoleUser, State: account.StateActive}
	for i := range 3 {
		recorder := httptest.NewRecorder()
		store.Reserve(recorder, blobRequest(http.MethodPost, "/files/blob/reserve?name=r.bin", nil, owner))
		want := http.StatusOK
		if i == 2 {
			want = http.StatusInsufficientStorage
		}
		if recorder.Code != want {
			t.Fatalf("reserve %d = %d, want %d", i, recorder.Code, want)
		}
	}
}

func TestBlobStageAndReserveHonorDiskThreshold(t *testing.T) {
	store := newQuotaTestBlobStore(t)
	store.diskUsage = func(string) (float64, error) { return 0.95, nil }
	stage := httptest.NewRecorder()
	store.Stage(stage, blobRequest(http.MethodPost, "/files/blob?name=a.bin", strings.NewReader("abc"), nil))
	if stage.Code != http.StatusInsufficientStorage || !strings.Contains(stage.Body.String(), "磁盘使用率超过阈值") {
		t.Fatalf("stage past disk threshold = %d %q, want 507", stage.Code, stage.Body.String())
	}
	reserve := httptest.NewRecorder()
	store.Reserve(reserve, blobRequest(http.MethodPost, "/files/blob/reserve?name=a.bin", nil, nil))
	if reserve.Code != http.StatusInsufficientStorage {
		t.Fatalf("reserve past disk threshold = %d, want 507", reserve.Code)
	}
	if entries, err := os.ReadDir(store.stageRoot()); err == nil && len(entries) != 0 {
		t.Fatalf("rejected requests left staged entries: %v", entries)
	}
}

func TestBlobStagedWriteLimit(t *testing.T) {
	store := newQuotaTestBlobStore(t)
	store.stageQuota = 100
	store.downloadMax = 60
	owner := &account.Identity{UserID: "user-owner", Role: account.RoleUser, State: account.StateActive}
	reserve := func() string {
		t.Helper()
		recorder := httptest.NewRecorder()
		store.Reserve(recorder, blobRequest(http.MethodPost, "/files/blob/reserve?name=r.bin", nil, owner))
		blob := decodeStagedBlob(t, recorder.Result())
		path, err := store.ResolveStagedPath(blob.Path, owner.UserID)
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	first := reserve()
	if limit, err := store.StagedWriteLimit(first, owner.UserID); err != nil || limit != 60 {
		t.Fatalf("limit with an empty quota = %d, %v; want the per-file cap 60", limit, err)
	}
	if err := os.WriteFile(first, make([]byte, 70), 0o600); err != nil {
		t.Fatal(err)
	}
	if limit, err := store.StagedWriteLimit(first, owner.UserID); err != nil || limit != 60 {
		t.Fatalf("rewriting the same file = %d, %v; its own bytes must not count", limit, err)
	}
	second := reserve()
	if limit, err := store.StagedWriteLimit(second, owner.UserID); err != nil || limit != 30 {
		t.Fatalf("limit next to a 70-byte file = %d, %v; want the remaining 30", limit, err)
	}
	if limit, err := store.StagedWriteLimit(second, "user-other"); err != nil || limit != 60 {
		t.Fatalf("another owner's limit = %d, %v; want an untouched quota", limit, err)
	}
	if err := os.WriteFile(second, make([]byte, 30), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StagedWriteLimit(filepath.Join(filepath.Dir(second), "other.bin"), owner.UserID); !errors.Is(err, errBlobStageQuota) {
		t.Fatalf("limit with the quota used up = %v, want errBlobStageQuota", err)
	}
	store.diskUsage = func(string) (float64, error) { return 0.99, nil }
	if _, err := store.StagedWriteLimit(first, owner.UserID); !errors.Is(err, errBlobStageDisk) {
		t.Fatalf("limit past the disk threshold = %v, want errBlobStageDisk", err)
	}
}

func TestParseBlobStagingEnv(t *testing.T) {
	env := map[string]string{"NEXTERM_BLOB_STAGE_MAX_BYTES": "1024", "NEXTERM_BLOB_DOWNLOAD_MAX_BYTES": "512"}
	getenv := func(key string) string { return env[key] }
	if quota, err := ParseBlobStageQuotaEnv(getenv); err != nil || quota != 1024 {
		t.Fatalf("stage quota = %d, %v", quota, err)
	}
	if limit, err := ParseBlobDownloadMaxBytesEnv(getenv); err != nil || limit != 512 {
		t.Fatalf("download limit = %d, %v", limit, err)
	}
	env["NEXTERM_BLOB_STAGE_MAX_BYTES"] = "-1"
	if _, err := ParseBlobStageQuotaEnv(getenv); err == nil {
		t.Fatal("a negative staging quota must be rejected")
	}
	t.Setenv("NEXTERM_BLOB_STAGE_MAX_BYTES", "4096")
	t.Setenv("NEXTERM_BLOB_DOWNLOAD_MAX_BYTES", "")
	store := NewBlobStore(t.TempDir(), testLogger())
	if store.stageQuotaBytes() != 4096 || store.downloadMaxBytes() != DefaultBlobDownloadMax {
		t.Fatalf("env overrides = %d, %d", store.stageQuotaBytes(), store.downloadMaxBytes())
	}
}
