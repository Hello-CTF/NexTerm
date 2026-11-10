package production

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type fsEventRecorder struct {
	mu     sync.Mutex
	events []ipc.Event
}

func (r *fsEventRecorder) Emit(_ context.Context, event ipc.Event) error {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	return nil
}

func (r *fsEventRecorder) count(topic ipc.Topic) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, event := range r.events {
		if event.Event == topic {
			count++
		}
	}
	return count
}

type fakeStagedBlobResolver struct {
	dir     string
	userIDs []string
	// writeLimit is the budget ReserveStagedWrite grants; writeErr refuses the write instead. reserved counts the
	// budgets granted and not yet released.
	writeLimit int64
	writeErr   error
	reserved   atomic.Int32
}

func (f *fakeStagedBlobResolver) ReserveStagedWrite(string, string) (int64, func(), error) {
	if f.writeErr != nil {
		return 0, nil, f.writeErr
	}
	f.reserved.Add(1)
	var once sync.Once
	return f.writeLimit, func() { once.Do(func() { f.reserved.Add(-1) }) }, nil
}

func (f *fakeStagedBlobResolver) ResolveStagedPath(rel, userID string) (string, error) {
	f.userIDs = append(f.userIDs, userID)
	id, name, found := strings.Cut(rel, "/")
	if !found || id != "staged-id" || name == "" {
		return "", errors.New("staged blob missing")
	}
	return filepath.Join(f.dir, name), nil
}
