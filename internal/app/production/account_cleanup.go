package production

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// accountCleanupLogoutAllPrefix namespaces the logout-all cleanup event key
// (constructed by the server's logout-all handler). The event keeps the
// prefixed key so alias dedup is unchanged; the cutoff read normalizes the
// prefix back to the canonical session ID so the lookup cannot silently
// degrade to an unbounded fresh snapshot.
const accountCleanupLogoutAllPrefix = "logout-all:"

// Committed account-cleanup events are replay markers, not work items: their
// device payload is dropped at commit and they are retained only so a
// same-event replay dedups instead of re-snapshotting. The bounds below keep
// that retention finite; prepared but uncommitted events are never evicted.
const (
	accountCleanupEventTTL           = 24 * time.Hour
	maxAccountCleanupEventsPerOwner  = 64
	maxAccountCleanupAliasesPerOwner = 256
	maxAccountCleanupEvents          = 1024
	maxAccountCleanupAliases         = 4096
)

type accountCleanupKey struct {
	ownerID string
	eventID string
}

type accountCleanupEvent struct {
	deviceSessions []session.SessionInfo
	// runbookCommit/runbookRollback are the runbook reservation taken once
	// when the event is first prepared; replays reuse it instead of
	// re-snapshotting (which would cancel a new generation of runs).
	runbookCommit   func(...string)
	runbookRollback func()
	committed       bool
	committedAt     time.Time
	aborted         bool
}

type accountCleanupState struct {
	mu      sync.Mutex
	events  map[accountCleanupKey]*accountCleanupEvent
	aliases map[accountCleanupKey]accountCleanupKey

	// runbook is the narrow owner-cleanup contract of the runbook service,
	// wired by the composition root (SetRunbookCleanup). Nil until wired.
	runbook runbookCleanup

	// now stamps commit times for the retention bounds; tests replace it.
	now func() time.Time

	// Fault seams for deterministic tests: when non-nil, these replace the
	// generation-bound read and the owner device listing.
	cutoffBound       func(ctx context.Context, ownerID, cleanupID string) (*time.Time, error)
	listOwnerSessions func(ctx context.Context, ownerID string) ([]session.SessionInfo, error)
}

func (s *accountCleanupState) nowTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// runbookCleanup is the narrow slice of the runbook service's owner-cleanup
// contract the account cleanup lifecycle depends on; *runbookIPCService
// satisfies it.
type runbookCleanup interface {
	PrepareOwnerCleanup(ctx context.Context, ownerID, eventID string, cutoff *time.Time) (commit func(...string), rollback func())
}

// SetRunbookCleanup wires the runbook service into the account cleanup
// lifecycle so an owner cleanup also cancels that owner's live runbook runs.
// NewProductionWithServices calls it once the runbook service is built.
func (p *Production) SetRunbookCleanup(r runbookCleanup) {
	p.accountCleanup.runbook = r
}

// CleanupAccount runs the cleanup for an already-committed revocation (the
// natural-expiry deferred path). A snapshot read failure is returned explicitly:
// nothing is committed, the event stays absent, and a later trigger with the
// same event id retries the snapshot. The deferred path resolves the durable
// generation bound (normalized logout-all key, indexed session-row read) so a
// late cleanup of an evicted event stays on the terminal generation instead
// of sweeping a new one.
func (p *Production) CleanupAccount(ctx context.Context, ownerID, cleanupID string, sessionIDs ...string) error {
	if p == nil || ownerID == "" || cleanupID == "" {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	cutoff, err := p.accountCleanupGenerationBound(ctx, ownerID, cleanupID)
	if err != nil {
		return err
	}
	commit, _, err := p.prepareAccountCleanup(ctx, ownerID, cleanupID, cutoff)
	if err != nil {
		return err
	}
	commit(sessionIDs...)
	return nil
}

// PrepareAccountCleanup reserves the owner snapshot before revocation. The
// owner-listing read failure is returned explicitly so the caller can abort:
// nothing is committed (no empty event, never an unbounded sweep), and a
// same-event retry takes a fresh snapshot. The paired path runs inside (or
// immediately before) the revocation transaction, so its prepare-time snapshot
// already bounds the generation consistently; it never reads the durable
// bound, which a single-writer backend would deadlock on inside the open
// transaction.
func (p *Production) PrepareAccountCleanup(ctx context.Context, ownerID, cleanupID string) (func(...string), func(), error) {
	return p.prepareAccountCleanup(ctx, ownerID, cleanupID, nil)
}

func (p *Production) prepareAccountCleanup(ctx context.Context, ownerID, cleanupID string, cutoff *time.Time) (func(...string), func(), error) {
	if p == nil || ownerID == "" || cleanupID == "" {
		return func(...string) {}, func() {}, nil
	}
	ctx = context.WithoutCancel(ctx)
	commitBatches := func(...string) {}
	rollbackBatches := func() {}
	if p.Services.ExecBatches != nil {
		commitBatches, rollbackBatches = p.Services.ExecBatches.PrepareOwnerCleanup(ctx, ownerID, cleanupID, cutoff)
	}
	key, event, err := p.prepareAccountDeviceSnapshot(ctx, ownerID, cleanupID, cutoff)
	if err != nil {
		rollbackBatches()
		return func(...string) {}, func() {}, err
	}
	return func(sessionIDs ...string) {
			devices, ok := p.commitAccountDeviceSnapshot(key, event, sessionIDs)
			if !ok {
				return
			}
			commitBatches(sessionIDs...)
			event.runbookCommit(sessionIDs...)
			p.cleanupAccountDeviceSnapshot(ctx, ownerID, devices)
			if p.Services.inventory != nil {
				_ = p.Services.inventory.stopWatch(ctx, ownerID)
			}
		}, func() {
			if p.rollbackAccountDeviceSnapshot(key, event) {
				rollbackBatches()
				event.runbookRollback()
			}
		}, nil
}

// accountCleanupGenerationBound resolves the cleanup event's durable
// generation bound, honoring the test fault seam.
func (p *Production) accountCleanupGenerationBound(ctx context.Context, ownerID, cleanupID string) (*time.Time, error) {
	state := &p.accountCleanup
	cutoffFn := state.cutoffBound
	if cutoffFn == nil {
		cutoffFn = p.accountCleanupCutoff
	}
	cutoff, err := cutoffFn(ctx, ownerID, cleanupID)
	if err != nil {
		return nil, fmt.Errorf("resolve account device cleanup generation: %w", err)
	}
	return cutoff, nil
}

func (p *Production) prepareAccountDeviceSnapshot(ctx context.Context, ownerID, cleanupID string, cutoff *time.Time) (accountCleanupKey, *accountCleanupEvent, error) {
	key := accountCleanupKey{ownerID: ownerID, eventID: cleanupID}
	state := &p.accountCleanup
	state.mu.Lock()
	if state.events == nil {
		state.events = make(map[accountCleanupKey]*accountCleanupEvent)
		state.aliases = make(map[accountCleanupKey]accountCleanupKey)
	}
	// Retention applies on every prepare path, including same-event replays:
	// a canonical that outlived the TTL or the caps is evicted here, so the
	// replay below re-derives a bounded snapshot instead of deduping against
	// a stale marker forever.
	state.purgeCommittedLocked(state.nowTime())
	if canonical, ok := state.aliases[key]; ok {
		key = canonical
	}
	if event, ok := state.events[key]; ok {
		state.mu.Unlock()
		return key, event, nil
	}
	state.mu.Unlock()

	commitRunbook, rollbackRunbook := func(...string) {}, func() {}
	if state.runbook != nil {
		commitRunbook, rollbackRunbook = state.runbook.PrepareOwnerCleanup(ctx, ownerID, key.eventID, cutoff)
	}
	snapshot, err := p.accountDeviceSnapshot(ctx, ownerID, cutoff)
	if err != nil {
		rollbackRunbook()
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
	event := &accountCleanupEvent{deviceSessions: snapshot, runbookCommit: commitRunbook, runbookRollback: rollbackRunbook}
	state.events[key] = event
	state.purgeCommittedLocked(state.nowTime())
	return key, event, nil
}

// commitAccountDeviceSnapshot validates the event, registers the revoked
// session aliases, and marks the event committed in one critical section,
// detaching the device payload with the mark: a concurrent retention purge
// can evict the committed event immediately afterwards, but it can never
// reclaim the payload of a commit that already ran. Replays get a nil
// payload and stay exactly-once no-ops.
func (p *Production) commitAccountDeviceSnapshot(key accountCleanupKey, event *accountCleanupEvent, sessionIDs []string) ([]session.SessionInfo, bool) {
	state := &p.accountCleanup
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.events[key] != event || event.aborted {
		return nil, false
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
	if !event.committed {
		event.committed = true
		event.committedAt = state.nowTime()
	}
	devices := event.deviceSessions
	event.deviceSessions = nil
	return devices, true
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

// purgeCommittedLocked bounds committed cleanup events and their aliases:
// everything past the TTL, then the oldest committed entries beyond the
// per-owner and global caps. Prepared but uncommitted events are never
// evicted, and eviction order is deterministic (oldest commit first, ties
// broken by key). A replay of an evicted event re-derives its (bounded)
// snapshot from the durable generation bound.
func (s *accountCleanupState) purgeCommittedLocked(now time.Time) {
	if len(s.events) == 0 {
		return
	}
	type committedEntry struct {
		key   accountCleanupKey
		event *accountCleanupEvent
	}
	committed := make([]committedEntry, 0, len(s.events))
	for key, event := range s.events {
		if event.committed {
			committed = append(committed, committedEntry{key: key, event: event})
		}
	}
	sort.Slice(committed, func(i, j int) bool {
		left, right := committed[i], committed[j]
		if !left.event.committedAt.Equal(right.event.committedAt) {
			return left.event.committedAt.Before(right.event.committedAt)
		}
		if left.key.ownerID != right.key.ownerID {
			return left.key.ownerID < right.key.ownerID
		}
		return left.key.eventID < right.key.eventID
	})
	evicted := make(map[accountCleanupKey]struct{})
	for _, entry := range committed {
		if !entry.event.committedAt.Add(accountCleanupEventTTL).After(now) {
			evicted[entry.key] = struct{}{}
		}
	}
	// Cap pressure keeps the newest entries: an entry is evicted when the
	// newer entries at or after its position already fill the allowance.
	ownerTotals := make(map[string]int)
	total := 0
	for _, entry := range committed {
		if _, gone := evicted[entry.key]; !gone {
			ownerTotals[entry.key.ownerID]++
			total++
		}
	}
	ownerSeen := make(map[string]int)
	seen := 0
	for _, entry := range committed {
		if _, gone := evicted[entry.key]; gone {
			continue
		}
		ownerSeen[entry.key.ownerID]++
		seen++
		if ownerTotals[entry.key.ownerID]-ownerSeen[entry.key.ownerID] >= maxAccountCleanupEventsPerOwner ||
			total-seen >= maxAccountCleanupEvents {
			evicted[entry.key] = struct{}{}
		}
	}
	for key := range evicted {
		delete(s.events, key)
		for alias, canonical := range s.aliases {
			if canonical == key {
				delete(s.aliases, alias)
			}
		}
	}
	s.purgeAliasesLocked()
}

// purgeAliasesLocked trims aliases past the per-owner and global caps, oldest
// canonical commit first; aliases whose canonical event is gone are dropped
// regardless.
func (s *accountCleanupState) purgeAliasesLocked() {
	type aliasEntry struct {
		alias     accountCleanupKey
		committed time.Time
	}
	aliases := make([]aliasEntry, 0, len(s.aliases))
	for alias, canonical := range s.aliases {
		event := s.events[canonical]
		if event == nil {
			delete(s.aliases, alias)
			continue
		}
		aliases = append(aliases, aliasEntry{alias: alias, committed: event.committedAt})
	}
	sort.Slice(aliases, func(i, j int) bool {
		left, right := aliases[i], aliases[j]
		if !left.committed.Equal(right.committed) {
			return left.committed.Before(right.committed)
		}
		if left.alias.ownerID != right.alias.ownerID {
			return left.alias.ownerID < right.alias.ownerID
		}
		return left.alias.eventID < right.alias.eventID
	})
	ownerTotals := make(map[string]int)
	total := 0
	for _, entry := range aliases {
		ownerTotals[entry.alias.ownerID]++
		total++
	}
	ownerSeen := make(map[string]int)
	seen := 0
	for _, entry := range aliases {
		ownerSeen[entry.alias.ownerID]++
		seen++
		if ownerTotals[entry.alias.ownerID]-ownerSeen[entry.alias.ownerID] >= maxAccountCleanupAliasesPerOwner ||
			total-seen >= maxAccountCleanupAliases {
			delete(s.aliases, entry.alias)
		}
	}
}

// accountCleanupCutoff reads the durable session row (indexed by primary key)
// for the cleanup event's generation bound: the earlier of the stored expiry
// and revocation time. Natural expiry has no pre-revocation snapshot, so this
// bound is what keeps a late cleanup on the terminal generation.
func (p *Production) accountCleanupCutoff(ctx context.Context, ownerID, cleanupID string) (*time.Time, error) {
	if p.Services.Store == nil {
		return nil, nil
	}
	sessionID := cleanupID
	if !ids.Valid(sessionID) {
		// Logout-all events key the canonical session behind a fixed prefix;
		// normalize it so the cutoff read cannot silently degrade to an
		// unbounded fresh snapshot.
		normalized, found := strings.CutPrefix(cleanupID, accountCleanupLogoutAllPrefix)
		if !found || !ids.Valid(normalized) {
			return nil, nil
		}
		sessionID = normalized
	}
	var row store.UserSession
	if err := p.Services.Store.GORM().WithContext(ctx).
		Select("expires_at", "revoked_at").
		Where("id = ? AND user_id = ?", sessionID, ownerID).
		Take(&row).Error; err != nil {
		return nil, err
	}
	cutoff := time.UnixMilli(row.ExpiresAt)
	if row.RevokedAt != nil && *row.RevokedAt > 0 && (row.ExpiresAt <= 0 || *row.RevokedAt < row.ExpiresAt) {
		cutoff = time.UnixMilli(*row.RevokedAt)
	}
	return &cutoff, nil
}

// accountCleanupAtOrBeforeGeneration reports whether t belongs to the
// cleanup generation at or before the cutoff. The durable cutoff carries
// millisecond resolution while session, run, and batch clocks may carry
// sub-millisecond fractions, so the comparison is normalized to
// milliseconds (same-millisecond inclusive, fail-closed) without mutating
// the stored timestamps.
func accountCleanupAtOrBeforeGeneration(t, cutoff time.Time) bool {
	return !t.Truncate(time.Millisecond).After(cutoff.Truncate(time.Millisecond))
}

// accountDeviceSnapshot lists the owner's device sessions created at or before
// the cleanup event's generation bound; the same-millisecond boundary is
// inclusive (fail-closed). A failed listing read is returned explicitly: the
// caller must never substitute an unbounded snapshot.
func (p *Production) accountDeviceSnapshot(ctx context.Context, ownerID string, cutoff *time.Time) ([]session.SessionInfo, error) {
	if p.Services.Sessions == nil {
		return nil, nil
	}
	listFn := p.accountCleanup.listOwnerSessions
	if listFn == nil {
		listFn = func(ctx context.Context, ownerID string) ([]session.SessionInfo, error) {
			return p.Services.Sessions.ListSessionsFor(ipc.WithUserID(ctx, ownerID))
		}
	}
	infos, err := listFn(ctx, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list account device sessions for cleanup: %w", err)
	}
	devices := make([]session.SessionInfo, 0, len(infos))
	for _, info := range infos {
		if info.Kind != session.KindDevice || cutoff != nil && !accountCleanupAtOrBeforeGeneration(info.CreatedAt, *cutoff) {
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
