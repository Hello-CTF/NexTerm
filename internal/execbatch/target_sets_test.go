package execbatch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

var _ TargetSetStore = (*store.Store)(nil)

type fakeTargetSetStore struct {
	sets   map[string]map[string]*store.ExecTargetSet
	nextID int
	calls  int
	err    error
}

func newFakeTargetSetStore() *fakeTargetSetStore {
	return &fakeTargetSetStore{sets: map[string]map[string]*store.ExecTargetSet{}}
}

func (f *fakeTargetSetStore) bump() error {
	f.calls++
	return f.err
}

func (f *fakeTargetSetStore) ownerSets(ctx context.Context) map[string]*store.ExecTargetSet {
	owner := ipc.OwnerID(ctx)
	sets, exists := f.sets[owner]
	if !exists {
		sets = map[string]*store.ExecTargetSet{}
		f.sets[owner] = sets
	}
	return sets
}

func (f *fakeTargetSetStore) ExecTargetSetCreate(ctx context.Context, name string, sessionIDs []string) (store.ExecTargetSet, error) {
	if err := f.bump(); err != nil {
		return store.ExecTargetSet{}, err
	}
	f.nextID++
	set := store.ExecTargetSet{
		ID: fmt.Sprintf("set-%d", f.nextID), Name: name,
		SessionIDs: append([]string(nil), sessionIDs...),
		CreatedAt:  int64(f.nextID), UpdatedAt: int64(f.nextID),
	}
	f.ownerSets(ctx)[set.ID] = &set
	return set, nil
}

func (f *fakeTargetSetStore) ExecTargetSetList(ctx context.Context) ([]store.ExecTargetSet, error) {
	if err := f.bump(); err != nil {
		return nil, err
	}
	sets := f.ownerSets(ctx)
	ordered := make([]store.ExecTargetSet, 0, len(sets))
	for _, set := range sets {
		ordered = append(ordered, *set)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Name == ordered[j].Name {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Name < ordered[j].Name
	})
	return ordered, nil
}

func (f *fakeTargetSetStore) ExecTargetSetGet(ctx context.Context, id string) (store.ExecTargetSet, error) {
	if err := f.bump(); err != nil {
		return store.ExecTargetSet{}, err
	}
	set, exists := f.ownerSets(ctx)[id]
	if !exists {
		return store.ExecTargetSet{}, ipc.NewError(ipc.CodeNotFound, "目标集不存在")
	}
	return *set, nil
}

func (f *fakeTargetSetStore) ExecTargetSetUpdate(ctx context.Context, id, name string, sessionIDs []string) error {
	if err := f.bump(); err != nil {
		return err
	}
	set, exists := f.ownerSets(ctx)[id]
	if !exists {
		return ipc.NewError(ipc.CodeNotFound, "目标集不存在")
	}
	set.Name = name
	set.SessionIDs = append([]string(nil), sessionIDs...)
	set.UpdatedAt = int64(f.nextID + 1000)
	return nil
}

func (f *fakeTargetSetStore) ExecTargetSetDelete(ctx context.Context, id string) error {
	if err := f.bump(); err != nil {
		return err
	}
	sets := f.ownerSets(ctx)
	if _, exists := sets[id]; !exists {
		return ipc.NewError(ipc.CodeNotFound, "目标集不存在")
	}
	delete(sets, id)
	return nil
}

func requireTargetSetNotFound(t *testing.T, err error) {
	t.Helper()
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeNotFound {
		t.Fatalf("error = %v, want not found", err)
	}
}

func TestTargetSetServiceCreateTrimsAndScopesOwner(t *testing.T) {
	fake := newFakeTargetSetStore()
	service := NewTargetSetService(fake)

	created, err := service.Create(context.Background(), "owner-a", "  web fleet  ", []string{"sess-b", "sess-a"})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Name != "web fleet" || created.CreatedAt == 0 || created.UpdatedAt == 0 {
		t.Fatalf("created = %+v", created)
	}
	if strings.Join(created.SessionIDs, ",") != "sess-b,sess-a" {
		t.Fatalf("created sessions = %+v", created.SessionIDs)
	}
	loaded, err := service.Get(context.Background(), "owner-a", created.ID)
	if err != nil || loaded.Name != created.Name || strings.Join(loaded.SessionIDs, ",") != "sess-b,sess-a" {
		t.Fatalf("loaded = %+v, %v", loaded, err)
	}
	if fake.sets["owner-b"] != nil && len(fake.sets["owner-b"]) != 0 {
		t.Fatal("create leaked into another owner namespace")
	}
}

func TestTargetSetServiceOwnerIsolationSameID(t *testing.T) {
	fake := newFakeTargetSetStore()
	service := NewTargetSetService(fake)
	ctx := context.Background()

	setA, err := service.Create(ctx, "owner-a", "fleet", []string{"sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	setB, err := service.Create(ctx, "owner-b", "fleet", []string{"sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	if setA.ID == setB.ID {
		t.Fatalf("fake generated the same id twice: %q", setA.ID)
	}

	_, err = service.Get(ctx, "owner-b", setA.ID)
	requireTargetSetNotFound(t, err)
	requireTargetSetNotFound(t, service.Update(ctx, "owner-b", setA.ID, "hijack", []string{"other"}))
	requireTargetSetNotFound(t, service.Delete(ctx, "owner-b", setA.ID))

	loadedA, err := service.Get(ctx, "owner-a", setA.ID)
	if err != nil || loadedA.Name != "fleet" || len(loadedA.SessionIDs) != 1 || loadedA.SessionIDs[0] != "sess-1" {
		t.Fatalf("cross-owner write touched owner A row: %+v, %v", loadedA, err)
	}
	listB, err := service.List(ctx, "owner-b")
	if err != nil || len(listB) != 1 || listB[0].ID != setB.ID {
		t.Fatalf("owner B list = %+v, %v", listB, err)
	}
	listA, err := service.List(ctx, "owner-a")
	if err != nil || len(listA) != 1 || listA[0].ID != setA.ID {
		t.Fatalf("owner A list = %+v, %v", listA, err)
	}
}

func TestTargetSetServiceListEmptyAndOrdering(t *testing.T) {
	fake := newFakeTargetSetStore()
	service := NewTargetSetService(fake)
	ctx := context.Background()

	empty, err := service.List(ctx, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty list = %#v, want empty non-nil", empty)
	}
	for _, name := range []string{"zeta", "alpha", "mid"} {
		if _, err := service.Create(ctx, "owner-a", name, []string{"sess"}); err != nil {
			t.Fatal(err)
		}
	}
	sets, err := service.List(ctx, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 3 || sets[0].Name != "alpha" || sets[1].Name != "mid" || sets[2].Name != "zeta" {
		t.Fatalf("picker ordering = %+v", sets)
	}
}

func TestTargetSetServiceUpdateAndDelete(t *testing.T) {
	fake := newFakeTargetSetStore()
	service := NewTargetSetService(fake)
	ctx := context.Background()

	created, err := service.Create(ctx, "owner-a", "fleet", []string{"sess-1", "sess-2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Update(ctx, "owner-a", created.ID, "renamed", []string{"sess-9"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := service.Get(ctx, "owner-a", created.ID)
	if err != nil || loaded.Name != "renamed" || len(loaded.SessionIDs) != 1 || loaded.SessionIDs[0] != "sess-9" {
		t.Fatalf("updated = %+v, %v", loaded, err)
	}
	if loaded.UpdatedAt == created.UpdatedAt {
		t.Fatal("update did not touch updated_at")
	}
	if err := service.Delete(ctx, "owner-a", created.ID); err != nil {
		t.Fatal(err)
	}
	_, err = service.Get(ctx, "owner-a", created.ID)
	requireTargetSetNotFound(t, err)
	requireTargetSetNotFound(t, service.Delete(ctx, "owner-a", created.ID))
	if sets, err := service.List(ctx, "owner-a"); err != nil || len(sets) != 0 {
		t.Fatalf("list after delete = %+v, %v", sets, err)
	}
}

func TestTargetSetServiceValidationRejectsBeforeStore(t *testing.T) {
	fake := newFakeTargetSetStore()
	service := NewTargetSetService(fake)
	ctx := context.Background()

	if _, err := service.Create(ctx, "owner-a", "   ", []string{"sess"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank name = %v", err)
	}
	if _, err := service.Create(ctx, "owner-a", strings.Repeat("n", store.ExecTargetSetMaxNameBytes+1), []string{"sess"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize name = %v", err)
	}
	if _, err := service.Create(ctx, "owner-a", "fleet", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty sessions = %v", err)
	}
	if _, err := service.Create(ctx, "owner-a", "fleet", []string{"sess", "sess"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate sessions = %v", err)
	}
	if _, err := service.Create(ctx, "owner-a", "fleet", []string{" lead"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank-padded session = %v", err)
	}
	overflow := make([]string, store.ExecTargetSetMaxTargets+1)
	for index := range overflow {
		overflow[index] = strings.Repeat("s", index+1)
	}
	if _, err := service.Create(ctx, "owner-a", "fleet", overflow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overflow sessions = %v", err)
	}
	if _, err := service.Get(ctx, "owner-a", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank id get = %v", err)
	}
	if err := service.Update(ctx, "owner-a", " lead", "fleet", []string{"sess"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("padded id update = %v", err)
	}
	if err := service.Delete(ctx, "owner-a", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank id delete = %v", err)
	}
	if fake.calls != 0 {
		t.Fatalf("store was called %d times for invalid input", fake.calls)
	}
}

func TestTargetSetServicePropagatesStoreErrors(t *testing.T) {
	injected := errors.New("database exploded")
	fake := newFakeTargetSetStore()
	fake.err = injected
	service := NewTargetSetService(fake)
	ctx := context.Background()

	if _, err := service.Create(ctx, "owner-a", "fleet", []string{"sess"}); !errors.Is(err, injected) {
		t.Fatalf("create = %v", err)
	}
	if _, err := service.List(ctx, "owner-a"); !errors.Is(err, injected) {
		t.Fatalf("list = %v", err)
	}
	if _, err := service.Get(ctx, "owner-a", "set-1"); !errors.Is(err, injected) {
		t.Fatalf("get = %v", err)
	}
	if err := service.Update(ctx, "owner-a", "set-1", "fleet", []string{"sess"}); !errors.Is(err, injected) {
		t.Fatalf("update = %v", err)
	}
	if err := service.Delete(ctx, "owner-a", "set-1"); !errors.Is(err, injected) {
		t.Fatalf("delete = %v", err)
	}
}

func TestTargetSetServiceContextCancellation(t *testing.T) {
	fake := newFakeTargetSetStore()
	service := NewTargetSetService(fake)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := service.Create(canceled, "owner-a", "fleet", []string{"sess"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("create = %v", err)
	}
	if _, err := service.List(canceled, "owner-a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("list = %v", err)
	}
	if _, err := service.Get(canceled, "owner-a", "set-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("get = %v", err)
	}
	if err := service.Update(canceled, "owner-a", "set-1", "fleet", []string{"sess"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("update = %v", err)
	}
	if err := service.Delete(canceled, "owner-a", "set-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("delete = %v", err)
	}
	if fake.calls != 0 {
		t.Fatalf("store was called %d times with a canceled context", fake.calls)
	}
}

func TestTargetSetServiceMissingStore(t *testing.T) {
	service := NewTargetSetService(nil)
	if _, err := service.Create(context.Background(), "owner-a", "fleet", []string{"sess"}); !errors.Is(err, errTargetSetStoreMissing) {
		t.Fatalf("create = %v", err)
	}
	if _, err := service.List(context.Background(), "owner-a"); !errors.Is(err, errTargetSetStoreMissing) {
		t.Fatalf("list = %v", err)
	}
}
