package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ids"
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
	if usage, entries, err := store.stageUsage(owner.UserID, nil); err != nil || usage != 8 || entries != 1 {
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

func TestBlobReserveStagedWrite(t *testing.T) {
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
	limit, release, err := store.ReserveStagedWrite(first, owner.UserID)
	if err != nil || limit != 60 {
		t.Fatalf("budget with an empty quota = %d, %v; want the per-file cap 60", limit, err)
	}
	if err := os.WriteFile(first, make([]byte, 70), 0o600); err != nil {
		t.Fatal(err)
	}
	second := reserve()
	if limit, _, err := store.ReserveStagedWrite(second, owner.UserID); err != nil || limit != 40 {
		t.Fatalf("budget next to a live 60-byte reservation = %d, %v; want 40, the reservation counting instead of the file", limit, err)
	}
	if _, _, err := store.ReserveStagedWrite(second, owner.UserID); !errors.Is(err, errBlobStageQuota) {
		t.Fatalf("budget with the quota fully reserved = %v, want errBlobStageQuota", err)
	}
	if limit, release, err := store.ReserveStagedWrite(second, "user-other"); err != nil || limit != 60 {
		t.Fatalf("another owner's budget = %d, %v; want an untouched quota", limit, err)
	} else {
		release()
	}
	release()
	release()
	usage, _, err := func() (int64, int, error) {
		store.stageMu.Lock()
		defer store.stageMu.Unlock()
		return store.chargedUsageLocked(owner.UserID, "")
	}()
	if err != nil || usage != 70+40 {
		t.Fatalf("charged usage after releasing the first write = %d, %v; want its 70 bytes on disk plus the second budget", usage, err)
	}
	store.diskUsage = func(string) (float64, error) { return 0.99, nil }
	if _, _, err := store.ReserveStagedWrite(first, "user-third"); !errors.Is(err, errBlobStageDisk) {
		t.Fatalf("budget past the disk threshold = %v, want errBlobStageDisk", err)
	}
}

func TestBlobReserveStagedWriteConcurrentBudgetsStayWithinQuota(t *testing.T) {
	store := newQuotaTestBlobStore(t)
	store.stageQuota = 1000
	store.downloadMax = 300
	path := filepath.Join(store.stageRoot(), "target.bin")
	const writers = 16
	granted := make(chan int64, writers)
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	for range writers {
		ready.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			limit, _, err := store.ReserveStagedWrite(path, "user-owner")
			if err != nil {
				granted <- 0
				return
			}
			granted <- limit
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	close(granted)
	var total int64
	for limit := range granted {
		total += limit
	}
	if total != store.stageQuota {
		t.Fatalf("concurrent budgets total %d, want exactly the quota %d", total, store.stageQuota)
	}
}

func TestBlobConcurrentUnknownLengthUploadsShareTheQuota(t *testing.T) {
	store := newQuotaTestBlobStore(t)
	store.stageQuota = 64
	payload := strings.Repeat("x", 40)
	const uploads = 4
	codes := make(chan int, uploads)
	var done sync.WaitGroup
	for range uploads {
		done.Add(1)
		go func() {
			defer done.Done()
			recorder := httptest.NewRecorder()
			store.Stage(recorder, blobRequest(http.MethodPost, "/files/blob?name=u.bin", unknownLength{strings.NewReader(payload)}, nil))
			codes <- recorder.Code
		}()
	}
	done.Wait()
	close(codes)
	accepted := 0
	for code := range codes {
		switch code {
		case http.StatusOK:
			accepted++
		case http.StatusInsufficientStorage:
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	if usage, _, err := store.stageUsage("", nil); err != nil || usage > store.stageQuota || usage != int64(accepted*len(payload)) {
		t.Fatalf("staged %d bytes for %d accepted uploads (%v); the quota is %d", usage, accepted, err, store.stageQuota)
	}
}

func TestBlobSweepKeepsEntriesWithRecentWritesOrLiveReservations(t *testing.T) {
	store := newQuotaTestBlobStore(t)
	now := time.Now()
	store.now = func() time.Time { return now }
	old := now.Add(-3 * DefaultBlobTTL)
	entry := func(name string) string {
		t.Helper()
		dir := filepath.Join(store.stageRoot(), ids.New())
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
		return path
	}
	stale := entry("stale.bin")
	written := entry("written.bin")
	if err := os.Chtimes(written, now, now); err != nil {
		t.Fatal(err)
	}
	reserved := entry("reserved.bin")
	_, release, err := store.ReserveStagedWrite(reserved, "")
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := store.SweepOnce(t.Context()); err != nil || removed != 1 {
		t.Fatalf("sweep = %d, %v; want only the stale entry removed", removed, err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale entry survived: %v", err)
	}
	for _, path := range []string{written, reserved} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s was swept: %v", filepath.Base(path), err)
		}
	}
	release()
	if removed, err := store.SweepOnce(t.Context()); err != nil || removed != 1 {
		t.Fatalf("sweep after release = %d, %v; want the released stale entry removed", removed, err)
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
