package main

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/forward"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

type fakeAccountSessions struct {
	mu          sync.Mutex
	owner       map[string]string
	tabs        map[string][]string
	killed      []string
	disconnects map[string]int
}

func (f *fakeAccountSessions) ListSessionsFor(ctx context.Context) ([]session.SessionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var infos []session.SessionInfo
	for id, owner := range f.owner {
		if owner == ipc.OwnerID(ctx) {
			infos = append(infos, session.SessionInfo{ID: id, Tabs: f.tabs[id]})
		}
	}
	return infos, nil
}

func (f *fakeAccountSessions) KillTabFor(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, id)
	return nil
}

func (f *fakeAccountSessions) DisconnectFor(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.owner[id] != ipc.OwnerID(ctx) {
		return session.ErrSessionNotFound
	}
	f.disconnects[id]++
	if f.disconnects[id] == 2 {
		delete(f.owner, id)
	}
	return nil
}

type fakeAccountForwards struct {
	specs   map[string]string
	removed []string
}

func (f *fakeAccountForwards) ListFor(ctx context.Context) []forward.Spec {
	var specs []forward.Spec
	for id, owner := range f.specs {
		if owner == ipc.OwnerID(ctx) {
			specs = append(specs, forward.Spec{ID: id})
		}
	}
	return specs
}

func (f *fakeAccountForwards) RemoveFor(_ context.Context, id string) error {
	f.removed = append(f.removed, id)
	if id == "broken" {
		return errors.New("listener busy")
	}
	return nil
}

func TestAccountPurgeStopsOnlyTheOwnersLiveState(t *testing.T) {
	sessions := &fakeAccountSessions{
		owner:       map[string]string{"s-bob": "bob", "s-alice": "alice"},
		tabs:        map[string][]string{"s-bob": {"t-bob"}, "s-alice": {"t-alice"}},
		disconnects: map[string]int{},
	}
	forwards := &fakeAccountForwards{specs: map[string]string{"f-bob": "bob", "f-alice": "alice"}}
	if err := accountPurgeFor(sessions, forwards)(context.Background(), "bob"); err != nil {
		t.Fatal(err)
	}
	if len(sessions.killed) != 1 || sessions.killed[0] != "t-bob" {
		t.Fatalf("killed tabs = %v", sessions.killed)
	}
	if _, ok := sessions.owner["s-bob"]; ok {
		t.Fatal("the owner's session record remains")
	}
	if _, ok := sessions.owner["s-alice"]; !ok || sessions.disconnects["s-alice"] != 0 {
		t.Fatal("another owner's session was touched")
	}
	if len(forwards.removed) != 1 || forwards.removed[0] != "f-bob" {
		t.Fatalf("removed forwards = %v", forwards.removed)
	}
}

func TestAccountPurgeReportsFailuresAndIgnoresEmptyOwner(t *testing.T) {
	forwards := &fakeAccountForwards{specs: map[string]string{"broken": "bob"}}
	purge := accountPurgeFor(nil, forwards)
	if err := purge(context.Background(), "bob"); err == nil {
		t.Fatal("a failed forward removal was not reported")
	}
	forwards.removed = nil
	if err := purge(context.Background(), ""); err != nil || len(forwards.removed) != 0 {
		t.Fatalf("empty owner purge err=%v removed=%v", err, forwards.removed)
	}
}
