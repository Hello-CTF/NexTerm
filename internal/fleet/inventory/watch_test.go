package inventory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeWatchStore struct {
	mu        sync.Mutex
	watches   map[string]SSHWatch
	saveErr   error
	deleteErr error
	listErr   error
}

func newFakeWatchStore() *fakeWatchStore {
	return &fakeWatchStore{watches: map[string]SSHWatch{}}
}

func (s *fakeWatchStore) SaveSSHWatch(_ context.Context, watch SSHWatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.watches[watch.OwnerID] = watch
	return nil
}

func (s *fakeWatchStore) DeleteSSHWatch(_ context.Context, ownerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.watches, ownerID)
	return nil
}

func (s *fakeWatchStore) ListSSHWatches(context.Context) ([]SSHWatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	watches := make([]SSHWatch, 0, len(s.watches))
	for _, watch := range s.watches {
		watches = append(watches, watch)
	}
	return watches, nil
}

func (s *fakeWatchStore) persistedOwners() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	owners := make([]string, 0, len(s.watches))
	for ownerID := range s.watches {
		owners = append(owners, ownerID)
	}
	return owners
}

type syncRecorder struct {
	calls    chan SSHWatch
	errors   chan error
	mu       sync.Mutex
	failures map[string]error
}

func newSyncRecorder() *syncRecorder {
	return &syncRecorder{calls: make(chan SSHWatch, 128), errors: make(chan error, 128), failures: map[string]error{}}
}

func (r *syncRecorder) sync(_ context.Context, watch SSHWatch) error {
	r.mu.Lock()
	err := r.failures[watch.Path]
	r.mu.Unlock()
	r.calls <- watch
	r.errors <- err
	return err
}

func (r *syncRecorder) fail(path string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures[path] = err
}

func (r *syncRecorder) waitSync(t *testing.T) (SSHWatch, error) {
	t.Helper()
	select {
	case watch := <-r.calls:
		return watch, <-r.errors
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a watch sync")
		return SSHWatch{}, nil
	}
}

func newTestWatchManager(t *testing.T, store WatchStore, recorder *syncRecorder) *WatchManager {
	t.Helper()
	manager, err := NewWatchManager(store, recorder.sync, WithWatchInterval(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	return manager
}

func TestWatchManagerStartPersistsAndSyncs(t *testing.T) {
	store := newFakeWatchStore()
	recorder := newSyncRecorder()
	manager := newTestWatchManager(t, store, recorder)
	status, err := manager.Start(context.Background(), "owner-a", "/home/a/.ssh/config", "group-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.waitSync(t); err != nil {
		t.Fatalf("initial sync err = %v", err)
	}
	if status.Watch.OwnerID != "owner-a" || status.Watch.Path != "/home/a/.ssh/config" || status.Watch.GroupID != "group-1" {
		t.Fatalf("status watch = %+v", status.Watch)
	}
	if status.LastSyncAt == nil || status.LastError != "" {
		t.Fatalf("status = %+v", status)
	}
	if owners := store.persistedOwners(); len(owners) != 1 || owners[0] != "owner-a" {
		t.Fatalf("persisted owners = %+v", owners)
	}
	current, active := manager.Status("owner-a")
	if !active || current.LastSyncAt == nil {
		t.Fatalf("runtime status = %+v active=%v", current, active)
	}
	if _, active := manager.Status("owner-b"); active {
		t.Fatal("unknown owner reported an active watch")
	}
}

func TestWatchManagerStartReplacesExistingWatch(t *testing.T) {
	store := newFakeWatchStore()
	recorder := newSyncRecorder()
	manager := newTestWatchManager(t, store, recorder)
	if _, err := manager.Start(context.Background(), "owner-a", "/first", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.waitSync(t); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), "owner-a", "/second", "group-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.waitSync(t); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case <-recorder.calls:
			<-recorder.errors
			continue
		default:
		}
		break
	}
	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case watch := <-recorder.calls:
			<-recorder.errors
			if watch.Path == "/first" {
				t.Fatalf("replaced watch kept syncing: %+v", watch)
			}
		case <-deadline:
			return
		}
	}
}

func TestWatchManagerStartRollsBackWhenInitialSyncFails(t *testing.T) {
	store := newFakeWatchStore()
	recorder := newSyncRecorder()
	recorder.fail("/broken", errors.New("config unreadable"))
	manager := newTestWatchManager(t, store, recorder)
	if _, err := manager.Start(context.Background(), "owner-a", "/broken", ""); err == nil || !strings.Contains(err.Error(), "config unreadable") {
		t.Fatalf("err = %v", err)
	}
	if owners := store.persistedOwners(); len(owners) != 0 {
		t.Fatalf("failed watch was persisted: %+v", owners)
	}
	if _, active := manager.Status("owner-a"); active {
		t.Fatal("failed watch is active")
	}
}

func TestWatchManagerStartValidatesAndRejectsStoreFailure(t *testing.T) {
	store := newFakeWatchStore()
	recorder := newSyncRecorder()
	manager := newTestWatchManager(t, store, recorder)
	if _, err := manager.Start(context.Background(), "", "/path", ""); err == nil {
		t.Fatal("empty owner was accepted")
	}
	if _, err := manager.Start(context.Background(), "owner-a", " ", ""); err == nil {
		t.Fatal("empty path was accepted")
	}
	store.saveErr = errors.New("disk full")
	if _, err := manager.Start(context.Background(), "owner-a", "/path", ""); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("store failure err = %v", err)
	}
	if _, active := manager.Status("owner-a"); active {
		t.Fatal("watch active despite store failure")
	}
}

func TestWatchManagerStopCancelsAndDeletes(t *testing.T) {
	store := newFakeWatchStore()
	recorder := newSyncRecorder()
	manager := newTestWatchManager(t, store, recorder)
	if _, err := manager.Start(context.Background(), "owner-a", "/path", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.waitSync(t); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(context.Background(), "owner-a"); err != nil {
		t.Fatal(err)
	}
	if owners := store.persistedOwners(); len(owners) != 0 {
		t.Fatalf("persisted owners after stop = %+v", owners)
	}
	if _, active := manager.Status("owner-a"); active {
		t.Fatal("watch still active after stop")
	}
	if err := manager.Stop(context.Background(), "owner-a"); err != nil {
		t.Fatalf("stop is not idempotent: %v", err)
	}
	if err := manager.Stop(context.Background(), "owner-unknown"); err != nil {
		t.Fatalf("stop of unknown owner: %v", err)
	}
}

func TestWatchManagerStopIsolatesOwners(t *testing.T) {
	store := newFakeWatchStore()
	recorder := newSyncRecorder()
	manager := newTestWatchManager(t, store, recorder)
	if _, err := manager.Start(context.Background(), "owner-a", "/a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), "owner-b", "/b", ""); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(context.Background(), "owner-a"); err != nil {
		t.Fatal(err)
	}
	if _, active := manager.Status("owner-b"); !active {
		t.Fatal("stopping owner-a stopped owner-b")
	}
	if owners := store.persistedOwners(); len(owners) != 1 || owners[0] != "owner-b" {
		t.Fatalf("persisted owners = %+v", owners)
	}
}

func TestWatchManagerRestoreRelaunchesPersistedWatches(t *testing.T) {
	store := newFakeWatchStore()
	store.watches["owner-a"] = SSHWatch{OwnerID: "owner-a", Path: "/a", GroupID: "group-a"}
	store.watches["owner-b"] = SSHWatch{OwnerID: "owner-b", Path: "/b"}
	recorder := newSyncRecorder()
	manager := newTestWatchManager(t, store, recorder)
	if err := manager.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	synced := map[string]bool{}
	for range 2 {
		watch, err := recorder.waitSync(t)
		if err != nil {
			t.Fatalf("restored sync err = %v", err)
		}
		synced[watch.OwnerID] = true
	}
	if !synced["owner-a"] || !synced["owner-b"] {
		t.Fatalf("restored owners = %+v", synced)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		statusA, activeA := manager.Status("owner-a")
		statusB, activeB := manager.Status("owner-b")
		if activeA && activeB && statusA.LastSyncAt != nil && statusB.LastSyncAt != nil &&
			statusA.Watch.GroupID == "group-a" && statusA.LastError == "" && statusB.LastError == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restored watches never became ready: %+v %+v", statusA, statusB)
		}
		time.Sleep(time.Millisecond)
	}
	if owners := store.persistedOwners(); len(owners) != 2 {
		t.Fatalf("restore dropped persisted records: %+v", owners)
	}
	if err := manager.Restore(context.Background()); err != nil {
		t.Fatalf("second restore: %v", err)
	}
	if _, active := manager.Status("owner-a"); !active {
		t.Fatal("second restore replaced the active watch")
	}
}

func TestWatchManagerRestoreKeepsRecordWhenSyncFails(t *testing.T) {
	store := newFakeWatchStore()
	store.watches["owner-a"] = SSHWatch{OwnerID: "owner-a", Path: "/broken"}
	recorder := newSyncRecorder()
	recorder.fail("/broken", errors.New("still broken"))
	manager := newTestWatchManager(t, store, recorder)
	if err := manager.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.waitSync(t); err == nil {
		t.Fatal("expected the restored sync to fail")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, active := manager.Status("owner-a")
		if active && status.LastError != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restored watch never recorded the error: %+v", status)
		}
		time.Sleep(time.Millisecond)
	}
	if owners := store.persistedOwners(); len(owners) != 1 {
		t.Fatalf("failed restored watch was dropped: %+v", owners)
	}
}

func TestWatchManagerShutdownKeepsRecordsAndRejectsNewWork(t *testing.T) {
	store := newFakeWatchStore()
	recorder := newSyncRecorder()
	manager := newTestWatchManager(t, store, recorder)
	if _, err := manager.Start(context.Background(), "owner-a", "/a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.waitSync(t); err != nil {
		t.Fatal(err)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown is not idempotent: %v", err)
	}
	if _, active := manager.Status("owner-a"); active {
		t.Fatal("watch active after shutdown")
	}
	if owners := store.persistedOwners(); len(owners) != 1 {
		t.Fatalf("shutdown dropped persisted records: %+v", owners)
	}
	if _, err := manager.Start(context.Background(), "owner-b", "/b", ""); err == nil {
		t.Fatal("start after shutdown was accepted")
	}
	if err := manager.Restore(context.Background()); err == nil {
		t.Fatal("restore after shutdown was accepted")
	}
}

func TestWatchManagerRestoresAfterRestart(t *testing.T) {
	store := newFakeWatchStore()
	recorder := newSyncRecorder()
	first := newTestWatchManager(t, store, recorder)
	if _, err := first.Start(context.Background(), "owner-a", "/a", "group-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.waitSync(t); err != nil {
		t.Fatal(err)
	}
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := newTestWatchManager(t, store, recorder)
	if err := second.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	watch, err := recorder.waitSync(t)
	if err != nil {
		t.Fatal(err)
	}
	if watch.OwnerID != "owner-a" || watch.Path != "/a" || watch.GroupID != "group-a" {
		t.Fatalf("restored watch = %+v", watch)
	}
}

func TestWatchManagerRecordsSyncErrorsAndRecovers(t *testing.T) {
	store := newFakeWatchStore()
	recorder := newSyncRecorder()
	recorder.fail("/flaky", errors.New("temporary"))
	manager := newTestWatchManager(t, store, recorder)
	if _, err := manager.Start(context.Background(), "owner-a", "/flaky", ""); err == nil {
		t.Fatal("start with failing sync was accepted")
	}
	recorder.fail("/flaky", nil)
	if _, err := manager.Start(context.Background(), "owner-a", "/flaky", ""); err != nil {
		t.Fatal(err)
	}
	recorder.fail("/flaky", errors.New("tick failure"))
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, _ := manager.Status("owner-a")
		if status.LastError != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("tick error never surfaced")
		}
		time.Sleep(time.Millisecond)
	}
	recorder.fail("/flaky", nil)
	deadline = time.Now().Add(5 * time.Second)
	for {
		status, _ := manager.Status("owner-a")
		if status.LastError == "" && status.LastSyncAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watch never recovered: %+v", status)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestNewWatchManagerRequiresStoreAndSync(t *testing.T) {
	if _, err := NewWatchManager(nil, newSyncRecorder().sync); err == nil {
		t.Fatal("nil store was accepted")
	}
	if _, err := NewWatchManager(newFakeWatchStore(), nil); err == nil {
		t.Fatal("nil sync was accepted")
	}
}

func TestFileWatchStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "ssh-watches.json")
	store := NewFileWatchStore(path)
	watches, err := store.ListSSHWatches(context.Background())
	if err != nil || len(watches) != 0 {
		t.Fatalf("missing file watches = %+v err = %v", watches, err)
	}
	if err := store.SaveSSHWatch(context.Background(), SSHWatch{OwnerID: "owner-b", Path: "/b"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSSHWatch(context.Background(), SSHWatch{OwnerID: "owner-a", Path: "/a", GroupID: "group-a"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSSHWatch(context.Background(), SSHWatch{OwnerID: "owner-a", Path: "/a2", GroupID: "group-a"}); err != nil {
		t.Fatal(err)
	}
	watches, err = store.ListSSHWatches(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(watches) != 2 || watches[0].OwnerID != "owner-a" || watches[0].Path != "/a2" || watches[1].OwnerID != "owner-b" {
		t.Fatalf("watches = %+v", watches)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("file mode = %o", info.Mode().Perm())
		}
	}
	if err := store.DeleteSSHWatch(context.Background(), "owner-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSSHWatch(context.Background(), "owner-a"); err != nil {
		t.Fatalf("delete is not idempotent: %v", err)
	}
	watches, err = store.ListSSHWatches(context.Background())
	if err != nil || len(watches) != 1 || watches[0].OwnerID != "owner-b" {
		t.Fatalf("watches = %+v err = %v", watches, err)
	}
	if err := store.SaveSSHWatch(context.Background(), SSHWatch{OwnerID: " "}); err == nil {
		t.Fatal("invalid watch was saved")
	}
}

func TestFileWatchStoreRejectsCorruptAndDuplicateRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ssh-watches.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewFileWatchStore(path)
	if _, err := store.ListSSHWatches(context.Background()); err == nil {
		t.Fatal("corrupt store was accepted")
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"watches":[{"ownerId":"dup","path":"/a"},{"ownerId":"dup","path":"/b"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListSSHWatches(context.Background()); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate err = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"watches":[{"ownerId":"","path":"/a"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListSSHWatches(context.Background()); err == nil {
		t.Fatal("ownerless record was accepted")
	}
}
