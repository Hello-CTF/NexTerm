package production

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type accountCleanupKey struct {
	ownerID string
	eventID string
}

type accountCleanupEvent struct {
	deviceSessions []session.SessionInfo
	committed      bool
	aborted        bool
}

type accountCleanupState struct {
	mu      sync.Mutex
	events  map[accountCleanupKey]*accountCleanupEvent
	aliases map[accountCleanupKey]accountCleanupKey

	// runbook is the narrow owner-cleanup contract of the runbook service,
	// wired by the composition root (SetRunbookCleanup). Nil until wired.
	runbook runbookCleanup

	// Fault seams for deterministic tests: when non-nil, these replace the
	// generation-bound read and the owner device listing.
	cutoffBound       func(ctx context.Context, ownerID, cleanupID string) (*time.Time, error)
	listOwnerSessions func(ctx context.Context, ownerID string) ([]session.SessionInfo, error)
}

// runbookCleanup is the narrow slice of the runbook service's owner-cleanup
// contract the account cleanup lifecycle depends on; *runbookIPCService
// satisfies it.
type runbookCleanup interface {
	PrepareOwnerCleanup(ctx context.Context, ownerID, eventID string) (commit func(...string), rollback func())
}

// SetRunbookCleanup wires the runbook service into the account cleanup
// lifecycle so an owner cleanup also cancels that owner's live runbook runs.
// The composition root calls it once the runbook service is built (today a
// productionModules local; the ProductionServices.Runbook field lands with
// M15 and turns this into a direct field read).
func (p *Production) SetRunbookCleanup(r runbookCleanup) {
	p.accountCleanup.runbook = r
}

// CleanupAccount runs the cleanup for an already-committed revocation (the
// natural-expiry deferred path). A snapshot read failure is returned explicitly:
// nothing is committed, the event stays absent, and a later trigger with the
// same event id retries the snapshot.
func (p *Production) CleanupAccount(ctx context.Context, ownerID, cleanupID string, sessionIDs ...string) error {
	commit, _, err := p.PrepareAccountCleanup(ctx, ownerID, cleanupID)
	if err != nil {
		return err
	}
	commit(sessionIDs...)
	return nil
}

// PrepareAccountCleanup reserves the owner snapshot before revocation. The
// snapshot's generation-bound or owner-listing read failure is returned
// explicitly so the caller can abort: nothing is committed (no empty event,
// never an unbounded sweep), and a same-event retry takes a fresh snapshot.
func (p *Production) PrepareAccountCleanup(ctx context.Context, ownerID, cleanupID string) (func(...string), func(), error) {
	if p == nil || ownerID == "" || cleanupID == "" {
		return func(...string) {}, func() {}, nil
	}
	ctx = context.WithoutCancel(ctx)
	commitBatches := func(...string) {}
	rollbackBatches := func() {}
	if p.Services.ExecBatches != nil {
		commitBatches, rollbackBatches = p.Services.ExecBatches.PrepareOwnerCleanup(ctx, ownerID, cleanupID)
	}
	commitRunbook, rollbackRunbook := func(...string) {}, func() {}
	if p.accountCleanup.runbook != nil {
		commitRunbook, rollbackRunbook = p.accountCleanup.runbook.PrepareOwnerCleanup(ctx, ownerID, cleanupID)
	}
	key, event, err := p.prepareAccountDeviceSnapshot(ctx, ownerID, cleanupID)
	if err != nil {
		rollbackBatches()
		rollbackRunbook()
		return func(...string) {}, func() {}, err
	}
	return func(sessionIDs ...string) {
			if !p.commitAccountDeviceSnapshot(key, event, sessionIDs) {
				return
			}
			commitBatches(sessionIDs...)
			commitRunbook(sessionIDs...)
			p.cleanupAccountDeviceSnapshot(ctx, ownerID, event.deviceSessions)
			if p.Services.inventory != nil {
				_ = p.Services.inventory.stopWatch(ctx, ownerID)
			}
		}, func() {
			if p.rollbackAccountDeviceSnapshot(key, event) {
				rollbackBatches()
				rollbackRunbook()
			}
		}, nil
}

func (p *Production) prepareAccountDeviceSnapshot(ctx context.Context, ownerID, cleanupID string) (accountCleanupKey, *accountCleanupEvent, error) {
	key := accountCleanupKey{ownerID: ownerID, eventID: cleanupID}
	state := &p.accountCleanup
	state.mu.Lock()
	if state.events == nil {
		state.events = make(map[accountCleanupKey]*accountCleanupEvent)
		state.aliases = make(map[accountCleanupKey]accountCleanupKey)
	}
	if canonical, ok := state.aliases[key]; ok {
		key = canonical
	}
	if event, ok := state.events[key]; ok {
		state.mu.Unlock()
		return key, event, nil
	}
	state.mu.Unlock()

	snapshot, err := p.accountDeviceSnapshot(ctx, ownerID, key.eventID)
	if err != nil {
		return key, nil, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if canonical, ok := state.aliases[key]; ok {
		key = canonical
	}
	if event, ok := state.events[key]; ok {
		return key, event, nil
	}
	event := &accountCleanupEvent{deviceSessions: snapshot}
	state.events[key] = event
	return key, event, nil
}

func (p *Production) commitAccountDeviceSnapshot(key accountCleanupKey, event *accountCleanupEvent, sessionIDs []string) bool {
	state := &p.accountCleanup
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.events[key] != event || event.aborted {
		return false
	}
	for _, sessionID := range sessionIDs {
		if sessionID == "" || sessionID == key.eventID {
			continue
		}
		alias := accountCleanupKey{ownerID: key.ownerID, eventID: sessionID}
		if _, exists := state.aliases[alias]; !exists {
			state.aliases[alias] = key
		}
	}
	event.committed = true
	return true
}

func (p *Production) rollbackAccountDeviceSnapshot(key accountCleanupKey, event *accountCleanupEvent) bool {
	state := &p.accountCleanup
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.events[key] != event || event.committed || event.aborted {
		return false
	}
	event.aborted = true
	delete(state.events, key)
	for alias, canonical := range state.aliases {
		if canonical == key {
			delete(state.aliases, alias)
		}
	}
	return true
}

// Natural expiry has no pre-revocation snapshot; the stored expiry or revocation time bounds the device generation a late cleanup may touch.
func (p *Production) accountCleanupCutoff(ctx context.Context, ownerID, cleanupID string) (*time.Time, error) {
	if p.Services.Store == nil || !ids.Valid(cleanupID) {
		return nil, nil
	}
	var row store.UserSession
	if err := p.Services.Store.GORM().WithContext(ctx).
		Select("expires_at", "revoked_at").
		Where("id = ? AND user_id = ?", cleanupID, ownerID).
		Take(&row).Error; err != nil {
		return nil, err
	}
	cutoff := time.UnixMilli(row.ExpiresAt)
	if row.RevokedAt != nil && *row.RevokedAt > 0 && (row.ExpiresAt <= 0 || *row.RevokedAt < row.ExpiresAt) {
		cutoff = time.UnixMilli(*row.RevokedAt)
	}
	return &cutoff, nil
}

// accountDeviceSnapshot lists the owner's device sessions created before the
// cleanup event's generation bound. A failed bound or listing read is returned
// explicitly: the caller must never substitute an unbounded snapshot.
func (p *Production) accountDeviceSnapshot(ctx context.Context, ownerID, cleanupID string) ([]session.SessionInfo, error) {
	if p.Services.Sessions == nil {
		return nil, nil
	}
	state := &p.accountCleanup
	cutoffFn := state.cutoffBound
	if cutoffFn == nil {
		cutoffFn = p.accountCleanupCutoff
	}
	listFn := state.listOwnerSessions
	if listFn == nil {
		listFn = func(ctx context.Context, ownerID string) ([]session.SessionInfo, error) {
			return p.Services.Sessions.ListSessionsFor(ipc.WithUserID(ctx, ownerID))
		}
	}
	cutoff, err := cutoffFn(ctx, ownerID, cleanupID)
	if err != nil {
		return nil, fmt.Errorf("resolve account device cleanup generation: %w", err)
	}
	infos, err := listFn(ctx, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list account device sessions for cleanup: %w", err)
	}
	devices := make([]session.SessionInfo, 0, len(infos))
	for _, info := range infos {
		if info.Kind != session.KindDevice || cutoff != nil && !info.CreatedAt.Before(*cutoff) {
			continue
		}
		info.Tabs = append([]string(nil), info.Tabs...)
		devices = append(devices, info)
	}
	return devices, nil
}

func (p *Production) cleanupAccountDeviceSnapshot(ctx context.Context, ownerID string, snapshot []session.SessionInfo) {
	if p.Services.Sessions == nil {
		return
	}
	ownerCtx := ipc.WithUserID(ctx, ownerID)
	for _, info := range snapshot {
		for _, tabID := range info.Tabs {
			err := p.Services.Sessions.CloseTabFor(ownerCtx, tabID)
			if err != nil && !errors.Is(err, session.ErrTabNotFound) && !errors.Is(err, session.ErrSessionNotFound) {
				slog.Warn("close account device terminal", "session", info.ID, "tab", tabID, "error", err)
			}
		}
		if err := p.Services.Sessions.DisconnectFor(ownerCtx, info.ID); err != nil && !errors.Is(err, session.ErrSessionNotFound) {
			slog.Warn("disconnect account device session", "session", info.ID, "error", err)
		}
	}
}
