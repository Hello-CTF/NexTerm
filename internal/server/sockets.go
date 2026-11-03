package server

import (
	"context"
	"errors"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/account"
)

var errServerClosed = errors.New("server is closed")

// socketEntry is one tracked connection. The account fields stay empty for
// open-access and gateway connections, which never close per user or session.
type socketEntry struct {
	cancel   context.CancelFunc
	userID   string
	session  string
	deviceID string
	revoked  chan struct{}
	revoke   sync.Once
}

func (e *socketEntry) signalRevoked() { e.revoke.Do(func() { close(e.revoked) }) }

type socketTracker struct {
	mu      sync.Mutex
	nextID  uint64
	entries map[uint64]*socketEntry
	closed  bool
	wg      sync.WaitGroup
}

func (t *socketTracker) track(parent context.Context) (context.Context, func(), error) {
	ctx, _, release, err := t.trackAccount(parent, nil)
	return ctx, release, err
}

// trackAccount registers a connection. The returned channel closes when the
// connection's login session or device is revoked; the owner is expected to
// close the socket with a close code instead of just dropping it, so the
// context is deliberately not cancelled by a revocation.
func (t *socketTracker) trackAccount(parent context.Context, identity *account.Identity) (context.Context, <-chan struct{}, func(), error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, nil, nil, errServerClosed
	}
	t.nextID++
	id := t.nextID
	ctx, cancel := context.WithCancel(parent)
	entry := &socketEntry{cancel: cancel, revoked: make(chan struct{})}
	if identity != nil {
		entry.userID, entry.session, entry.deviceID = identity.UserID, identity.SessionID, identity.DeviceID
	}
	if t.entries == nil {
		t.entries = make(map[uint64]*socketEntry)
	}
	t.entries[id] = entry
	t.wg.Add(1)
	t.mu.Unlock()

	var once sync.Once
	return ctx, entry.revoked, func() {
		once.Do(func() {
			cancel()
			t.mu.Lock()
			delete(t.entries, id)
			t.mu.Unlock()
			t.wg.Done()
		})
	}, nil
}

func (t *socketTracker) revokeMatching(match func(*socketEntry) bool) {
	t.mu.Lock()
	var hit []*socketEntry
	for _, entry := range t.entries {
		if entry.userID != "" && match(entry) {
			hit = append(hit, entry)
		}
	}
	t.mu.Unlock()
	for _, entry := range hit {
		entry.signalRevoked()
	}
}

// closeSession signals every connection opened under the login session.
func (t *socketTracker) closeSession(sessionID string) {
	if sessionID == "" {
		return
	}
	t.revokeMatching(func(e *socketEntry) bool { return e.session == sessionID })
}

// closeUser signals every connection of the user except those of exceptSession.
func (t *socketTracker) closeUser(userID, exceptSession string) {
	if userID == "" {
		return
	}
	t.revokeMatching(func(e *socketEntry) bool {
		return e.userID == userID && (exceptSession == "" || e.session != exceptSession)
	})
}

// closeDevice signals every connection of the user's sessions bound to the device.
func (t *socketTracker) closeDevice(userID, deviceID string) {
	if userID == "" || deviceID == "" {
		return
	}
	t.revokeMatching(func(e *socketEntry) bool { return e.userID == userID && e.deviceID == deviceID })
}

func (t *socketTracker) closeAndWait(ctx context.Context) error {
	t.mu.Lock()
	t.closed = true
	cancels := make([]context.CancelFunc, 0, len(t.entries))
	for _, entry := range t.entries {
		cancels = append(cancels, entry.cancel)
	}
	t.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
