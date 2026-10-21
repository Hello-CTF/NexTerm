package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type fakeRosterStore struct {
	mu       sync.Mutex
	rows     map[string]store.AgentRosterRow
	byHandle map[string]string
	clock    int64
}

func newFakeRosterStore() *fakeRosterStore {
	return &fakeRosterStore{rows: map[string]store.AgentRosterRow{}, byHandle: map[string]string{}}
}

func rosterHandleKey(owner, handle string) string { return owner + "\x00" + handle }

func (f *fakeRosterStore) AgentRosterProvision(ctx context.Context, row store.AgentRosterRow) (store.AgentRosterRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	owner := ipc.OwnerID(ctx)
	if _, exists := f.byHandle[rosterHandleKey(owner, row.Handle)]; exists {
		return store.AgentRosterRow{}, ipc.NewError(ipc.Code("conflict"), "conflict")
	}
	f.clock++
	if row.ID == "" {
		row.ID = fmt.Sprintf("roster-%d", f.clock)
	}
	if row.CreatedAt == 0 {
		row.CreatedAt = f.clock
	}
	row.UpdatedAt = f.clock
	row.OwnerID = owner
	if row.MetadataJSON == "" {
		row.MetadataJSON = "{}"
	}
	f.rows[row.ID] = row
	f.byHandle[rosterHandleKey(owner, row.Handle)] = row.ID
	return row, nil
}

func (f *fakeRosterStore) AgentRosterGet(ctx context.Context, id string) (store.AgentRosterRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok || row.OwnerID != ipc.OwnerID(ctx) {
		return store.AgentRosterRow{}, ipc.NewError(ipc.CodeNotFound, "not found")
	}
	return row, nil
}

func (f *fakeRosterStore) AgentRosterGetByHandle(ctx context.Context, handle string) (store.AgentRosterRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byHandle[rosterHandleKey(ipc.OwnerID(ctx), handle)]
	if !ok {
		return store.AgentRosterRow{}, ipc.NewError(ipc.CodeNotFound, "not found")
	}
	return f.rows[id], nil
}

func (f *fakeRosterStore) AgentRosterList(ctx context.Context, conversationID string) ([]store.AgentRosterRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	owner := ipc.OwnerID(ctx)
	rows := []store.AgentRosterRow{}
	for _, row := range f.rows {
		if row.OwnerID != owner {
			continue
		}
		if conversationID != "" && row.ConversationID != conversationID {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (f *fakeRosterStore) AgentRosterUpdate(ctx context.Context, id string, identity store.AgentRosterIdentity, updates map[string]any) (store.AgentRosterRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok || row.OwnerID != ipc.OwnerID(ctx) {
		return store.AgentRosterRow{}, ipc.NewError(ipc.CodeNotFound, "not found")
	}
	if row.CreatedAt != identity.CreatedAt || row.Incarnation != identity.Incarnation {
		return store.AgentRosterRow{}, store.ErrAgentRosterIdentityConflict
	}
	f.applyUpdates(&row, updates)
	f.rows[id] = row
	return row, nil
}

func (f *fakeRosterStore) AgentRosterCompareAndSwapState(ctx context.Context, id string, identity store.AgentRosterIdentity, expectedState string, updates map[string]any) (store.AgentRosterRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok || row.OwnerID != ipc.OwnerID(ctx) {
		return store.AgentRosterRow{}, ipc.NewError(ipc.CodeNotFound, "not found")
	}
	if row.CreatedAt != identity.CreatedAt || row.Incarnation != identity.Incarnation {
		return store.AgentRosterRow{}, store.ErrAgentRosterIdentityConflict
	}
	if row.State != expectedState {
		return store.AgentRosterRow{}, store.ErrAgentRosterStateConflict
	}
	f.applyUpdates(&row, updates)
	f.rows[id] = row
	return row, nil
}

func (f *fakeRosterStore) applyUpdates(row *store.AgentRosterRow, updates map[string]any) {
	for key, value := range updates {
		switch key {
		case "state":
			row.State = value.(string)
		case "job_id":
			row.JobID = value.(string)
		case "released_at":
			if value == nil {
				row.ReleasedAt = nil
			} else {
				stamp := value.(int64)
				row.ReleasedAt = &stamp
			}
		case "metadata_json":
			row.MetadataJSON = value.(string)
		}
	}
	f.clock++
	row.UpdatedAt = f.clock
}

// mutate rewrites a stored row directly (simulating an out-of-band replacement).
func (f *fakeRosterStore) mutate(id string, update func(*store.AgentRosterRow)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.rows[id]
	update(&row)
	f.rows[id] = row
}

type fakeRosterProbe struct {
	mu       sync.Mutex
	statuses map[string]RosterRuntimeStatus
	errors   map[string]error
	calls    []string
}

func newFakeRosterProbe() *fakeRosterProbe {
	return &fakeRosterProbe{statuses: map[string]RosterRuntimeStatus{}, errors: map[string]error{}}
}

func (p *fakeRosterProbe) set(handle string, kind RosterRuntimeKind, currentIncarnation string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statuses[handle] = RosterRuntimeStatus{Kind: kind, CurrentIncarnation: currentIncarnation}
}

func (p *fakeRosterProbe) setConnected(handle, episode string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statuses[handle] = RosterRuntimeStatus{Kind: RosterRuntimeConnected, ObservedIncarnation: episode}
}

func (p *fakeRosterProbe) setError(handle string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.errors[handle] = err
}

func (p *fakeRosterProbe) ProbeRosterRuntime(_ context.Context, handle, incarnation string) (RosterRuntimeStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, handle+":"+incarnation)
	if err := p.errors[handle]; err != nil {
		return RosterRuntimeStatus{}, err
	}
	if status, ok := p.statuses[handle]; ok {
		return status, nil
	}
	return RosterRuntimeStatus{Kind: RosterRuntimeUnverifiable}, nil
}

func (p *fakeRosterProbe) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func newTestRoster(store RosterStore, probe RosterRuntimeProbe) *Roster {
	roster := NewRoster(store, probe)
	var mu sync.Mutex
	sequence := 0
	roster.now = func() int64 { return 9999 }
	roster.newID = func() string {
		mu.Lock()
		defer mu.Unlock()
		sequence++
		return fmt.Sprintf("inc-%d", sequence)
	}
	return roster
}

func rosterOwnerContext(owner string) context.Context {
	return ipc.WithUserID(context.Background(), owner)
}

func mustProvision(t *testing.T, roster *Roster, ctx context.Context, conversationID, handle string) RosterEntry {
	t.Helper()
	entry, err := roster.Provision(ctx, conversationID, handle, handle+"-alias", "tab-"+handle, nil)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func rosterIdentityOf(entry RosterEntry) RosterIdentity {
	return RosterIdentity{CreatedAt: entry.CreatedAt, Incarnation: entry.Incarnation}
}

func TestRosterProvisionActivateAttachLifecycle(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	fake := newFakeRosterStore()
	probe := newFakeRosterProbe()
	probe.set("handle-a", RosterRuntimeConnected, "")
	roster := newTestRoster(fake, probe)

	entry := mustProvision(t, roster, ctx, "conv-1", "handle-a")
	if entry.State != RosterStateProvisioning || entry.TabID != "tab-handle-a" || entry.Incarnation == "" {
		t.Fatalf("provisioning-first entry = %+v", entry)
	}
	if entry.Alias != "handle-a-alias" {
		t.Fatalf("alias = %q", entry.Alias)
	}
	// Handles are permanent: a second provision of the same handle conflicts.
	if _, err := roster.Provision(ctx, "conv-1", "handle-a", "", "tab-x", nil); !errors.Is(err, ErrRosterHandleConflict) {
		t.Fatalf("duplicate handle err=%v", err)
	}
	// The preassigned TabID is mandatory (provisioning-first contract).
	if _, err := roster.Provision(ctx, "conv-1", "handle-b", "", "", nil); err == nil {
		t.Fatal("provision without preassigned TabID succeeded")
	}

	// Attach with a connected probe activates and binds the job.
	attachment, err := roster.Attach(ctx, "handle-a", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Resolution != RosterResolutionConnected || attachment.Entry.State != RosterStateActive || attachment.Entry.JobID != "job-1" {
		t.Fatalf("attach = %+v", attachment)
	}
	// Cross-job resolution: a later job rebinds the same handle.
	attachment, err = roster.Attach(ctx, "handle-a", "job-2")
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Entry.JobID != "job-2" {
		t.Fatalf("cross-job bind = %+v", attachment.Entry)
	}

	// Release is two-phase: release-pending first, confirmed later.
	released, err := roster.RequestRelease(ctx, "handle-a", rosterIdentityOf(entry))
	if err != nil || released.State != RosterStateReleasePending {
		t.Fatalf("request release = %+v err=%v", released, err)
	}
	// Idempotent while pending.
	if again, err := roster.RequestRelease(ctx, "handle-a", rosterIdentityOf(entry)); err != nil || again.State != RosterStateReleasePending {
		t.Fatalf("second request release = %+v err=%v", again, err)
	}
	confirmed, err := roster.ConfirmRelease(ctx, "handle-a", rosterIdentityOf(entry))
	if err != nil || confirmed.State != RosterStateReleased || confirmed.ReleasedAt == nil || *confirmed.ReleasedAt != 9999 {
		t.Fatalf("confirm release = %+v err=%v", confirmed, err)
	}
	// Confirming twice is an invalid transition.
	if _, err := roster.ConfirmRelease(ctx, "handle-a", rosterIdentityOf(entry)); !errors.Is(err, ErrRosterInvalidTransition) {
		t.Fatalf("second confirm err=%v", err)
	}
	// A released handle resolves as authoritatively gone without a probe call.
	callsBefore := probe.callCount()
	attachment, err = roster.Attach(ctx, "handle-a", "job-3")
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Resolution != RosterResolutionGone || probe.callCount() != callsBefore {
		t.Fatalf("attach after release = %+v probeCalls=%d", attachment, probe.callCount())
	}
}

func TestRosterAttachClassifications(t *testing.T) {
	tests := []struct {
		name           string
		kind           RosterRuntimeKind
		current        string
		wantResolution RosterResolution
		wantState      RosterState
		wantWrote      bool
	}{
		{"disconnected never writes", RosterRuntimeDisconnected, "", RosterResolutionDisconnected, RosterStateActive, false},
		{"gone requires authority and writes", RosterRuntimeGone, "", RosterResolutionGone, RosterStateGone, true},
		{"replaced records the new incarnation", RosterRuntimeReplaced, "inc-new", RosterResolutionReplaced, RosterStateReplaced, true},
		{"exited writes", RosterRuntimeExited, "", RosterResolutionExited, RosterStateExited, true},
		{"unverifiable never writes and is not gone", RosterRuntimeUnverifiable, "", RosterResolutionUnverifiable, RosterStateActive, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := rosterOwnerContext("owner-a")
			fake := newFakeRosterStore()
			probe := newFakeRosterProbe()
			probe.set("handle-a", test.kind, test.current)
			roster := newTestRoster(fake, probe)
			entry := mustProvision(t, roster, ctx, "conv-1", "handle-a")
			if _, err := roster.Activate(ctx, entry.ID, rosterIdentityOf(entry), "job-0"); err != nil {
				t.Fatal(err)
			}
			attachment, err := roster.Attach(ctx, "handle-a", "job-1")
			if err != nil {
				t.Fatal(err)
			}
			if attachment.Resolution != test.wantResolution {
				t.Fatalf("resolution=%s want %s", attachment.Resolution, test.wantResolution)
			}
			if attachment.Entry.State != test.wantState {
				t.Fatalf("state=%s want %s", attachment.Entry.State, test.wantState)
			}
			if test.wantResolution == RosterResolutionReplaced && attachment.CurrentIncarnation != test.current {
				t.Fatalf("current incarnation=%q want %q", attachment.CurrentIncarnation, test.current)
			}
			if !test.wantWrote {
				stored, err := fake.AgentRosterGetByHandle(ctx, "handle-a")
				if err != nil {
					t.Fatal(err)
				}
				if stored.State != string(RosterStateActive) {
					t.Fatalf("non-authoritative outcome wrote state %s", stored.State)
				}
			}
		})
	}
}

func TestRosterAttachProbeErrorIsUnverifiable(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	fake := newFakeRosterStore()
	probe := newFakeRosterProbe()
	probe.setError("handle-a", errors.New("probe transport failed"))
	roster := newTestRoster(fake, probe)
	entry := mustProvision(t, roster, ctx, "conv-1", "handle-a")
	if _, err := roster.Activate(ctx, entry.ID, rosterIdentityOf(entry), ""); err != nil {
		t.Fatal(err)
	}
	attachment, err := roster.Attach(ctx, "handle-a", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Resolution != RosterResolutionUnverifiable {
		t.Fatalf("probe error resolution=%s", attachment.Resolution)
	}
	stored, err := fake.AgentRosterGetByHandle(ctx, "handle-a")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != string(RosterStateActive) {
		t.Fatalf("probe error wrote state %s", stored.State)
	}
}

func TestRosterAttachNonpersistentFallback(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	probe := newFakeRosterProbe()
	roster := newTestRoster(newFakeRosterStore(), probe)

	// Only a connected runtime without a roster row attaches, as nonpersistent.
	probe.set("np-connected", RosterRuntimeConnected, "")
	attachment, err := roster.Attach(ctx, "np-connected", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Resolution != RosterResolutionNonpersistent {
		t.Fatalf("fallback resolution=%s", attachment.Resolution)
	}

	// Every other probe answer keeps its typed resolution: none of them is
	// collapsed into not-found, and gone is only reported on authority.
	tests := []struct {
		handle   string
		kind     RosterRuntimeKind
		current  string
		expected RosterResolution
	}{
		{"np-gone", RosterRuntimeGone, "", RosterResolutionGone},
		{"np-exited", RosterRuntimeExited, "", RosterResolutionExited},
		{"np-replaced", RosterRuntimeReplaced, "inc-x", RosterResolutionReplaced},
		{"np-disconnected", RosterRuntimeDisconnected, "", RosterResolutionDisconnected},
		{"np-unverifiable", RosterRuntimeUnverifiable, "", RosterResolutionUnverifiable},
	}
	for _, test := range tests {
		probe.set(test.handle, test.kind, test.current)
		attachment, err := roster.Attach(ctx, test.handle, "job-1")
		if err != nil {
			t.Fatalf("%s attach err=%v", test.handle, err)
		}
		if attachment.Resolution != test.expected {
			t.Fatalf("%s resolution=%s want %s", test.handle, attachment.Resolution, test.expected)
		}
		if test.expected == RosterResolutionReplaced && attachment.CurrentIncarnation != "inc-x" {
			t.Fatalf("replaced incarnation=%q", attachment.CurrentIncarnation)
		}
	}
	// A probe transport error is unverifiable, never not-found authority.
	probe.setError("np-error", errors.New("probe transport failed"))
	attachment, err = roster.Attach(ctx, "np-error", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Resolution != RosterResolutionUnverifiable {
		t.Fatalf("probe error resolution=%s", attachment.Resolution)
	}
	// Without a probe the handle is honestly unknown.
	roster = newTestRoster(newFakeRosterStore(), nil)
	if _, err := roster.Attach(ctx, "np-unknown", "job-1"); !errors.Is(err, ErrRosterNotFound) {
		t.Fatalf("unknown handle err=%v", err)
	}
}

func TestRosterReconcileWritesOnlyAuthoritativeOutcomes(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	fake := newFakeRosterStore()
	probe := newFakeRosterProbe()
	roster := newTestRoster(fake, probe)

	cases := []struct {
		handle    string
		kind      RosterRuntimeKind
		current   string
		wantOut   RosterResolution
		wantWrote bool
	}{
		{"h-connected", RosterRuntimeConnected, "", RosterResolutionConnected, false},
		{"h-disconnected", RosterRuntimeDisconnected, "", RosterResolutionDisconnected, false},
		{"h-gone", RosterRuntimeGone, "", RosterResolutionGone, true},
		{"h-exited", RosterRuntimeExited, "", RosterResolutionExited, true},
		{"h-replaced", RosterRuntimeReplaced, "inc-new", RosterResolutionReplaced, true},
		{"h-unverifiable", RosterRuntimeUnverifiable, "", RosterResolutionUnverifiable, false},
	}
	for _, caseInfo := range cases {
		probe.set(caseInfo.handle, caseInfo.kind, caseInfo.current)
		entry := mustProvision(t, roster, ctx, "conv-1", caseInfo.handle)
		if _, err := roster.Activate(ctx, entry.ID, rosterIdentityOf(entry), "job-0"); err != nil {
			t.Fatal(err)
		}
	}
	// A terminal row is not examined again.
	released := mustProvision(t, roster, ctx, "conv-1", "h-released")
	if _, err := roster.Activate(ctx, released.ID, rosterIdentityOf(released), "job-r"); err != nil {
		t.Fatal(err)
	}
	if _, err := roster.RequestRelease(ctx, "h-released", rosterIdentityOf(released)); err != nil {
		t.Fatal(err)
	}
	if _, err := roster.ConfirmRelease(ctx, "h-released", rosterIdentityOf(released)); err != nil {
		t.Fatal(err)
	}
	_ = released

	report, err := roster.Reconcile(ctx, "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != len(cases) {
		t.Fatalf("reconciled %d entries, want %d", len(report.Entries), len(cases))
	}
	byHandle := map[string]RosterReconcileEntry{}
	for _, entry := range report.Entries {
		byHandle[entry.Handle] = entry
		if entry.Reason == "" {
			t.Fatalf("entry %s has no auditable reason", entry.Handle)
		}
	}
	for _, caseInfo := range cases {
		entry := byHandle[caseInfo.handle]
		if entry.Outcome != caseInfo.wantOut || entry.Wrote != caseInfo.wantWrote {
			t.Fatalf("%s outcome=%s wrote=%t, want %s/%t", caseInfo.handle, entry.Outcome, entry.Wrote, caseInfo.wantOut, caseInfo.wantWrote)
		}
		stored, err := fake.AgentRosterGetByHandle(ctx, caseInfo.handle)
		if err != nil {
			t.Fatal(err)
		}
		if caseInfo.wantWrote {
			if string(caseInfo.wantOut) != stored.State {
				t.Fatalf("%s stored state=%s want %s", caseInfo.handle, stored.State, caseInfo.wantOut)
			}
		} else if stored.State != string(RosterStateActive) {
			t.Fatalf("%s was written to %s without authority", caseInfo.handle, stored.State)
		}
	}
	// Replaced keeps the authoritative new incarnation for audit.
	if byHandle["h-replaced"].Outcome != RosterResolutionReplaced {
		t.Fatal("replaced outcome missing")
	}
}

func TestRosterInjectContext(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	roster := newTestRoster(newFakeRosterStore(), newFakeRosterProbe())
	mustProvision(t, roster, ctx, "conv-1", "handle-a")
	second := mustProvision(t, roster, ctx, "conv-1", "handle-b")
	if _, err := roster.Activate(ctx, second.ID, rosterIdentityOf(second), "job-b"); err != nil {
		t.Fatal(err)
	}
	injection, err := roster.InjectContext(ctx, "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(injection.Entries) != 2 {
		t.Fatalf("injection entries = %d", len(injection.Entries))
	}
	if !strings.HasPrefix(injection.Prompt, "Persistent agent roster") {
		t.Fatalf("prompt header missing: %q", injection.Prompt)
	}
	for _, entry := range injection.Entries {
		if !strings.Contains(injection.Prompt, `"handle":"`+entry.Handle+`"`) {
			t.Fatalf("prompt missing entry %s: %q", entry.Handle, injection.Prompt)
		}
		if entry.TabID == "" || entry.State == "" || entry.Incarnation == "" {
			t.Fatalf("injection entry incomplete: %+v", entry)
		}
	}
	// Injection is owner-scoped.
	other, err := roster.InjectContext(rosterOwnerContext("owner-b"), "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Entries) != 0 {
		t.Fatalf("cross-owner injection leaked %d entries", len(other.Entries))
	}
}

func TestRosterExactIdentityConflictBecomesReplaced(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	fake := newFakeRosterStore()
	roster := newTestRoster(fake, newFakeRosterProbe())
	entry := mustProvision(t, roster, ctx, "conv-1", "handle-a")
	if _, err := roster.Activate(ctx, entry.ID, rosterIdentityOf(entry), "job-0"); err != nil {
		t.Fatal(err)
	}
	// A writer holding the stale pre-replacement row attempts an authoritative
	// transition after an out-of-band re-provision changed the incarnation.
	stale, err := fake.AgentRosterGetByHandle(ctx, "handle-a")
	if err != nil {
		t.Fatal(err)
	}
	fake.mutate(entry.ID, func(row *store.AgentRosterRow) {
		row.Incarnation = "inc-out-of-band"
		row.State = string(RosterStateActive)
	})
	attachment, wrote, err := roster.transition(ctx, stale, RosterStateActive, RosterStateGone, RosterResolutionGone, nil)
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Fatal("stale identity wrote to the replacement row")
	}
	if attachment.Resolution != RosterResolutionReplaced {
		t.Fatalf("identity conflict resolution=%s", attachment.Resolution)
	}
	stored, err := fake.AgentRosterGetByHandle(ctx, "handle-a")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Incarnation != "inc-out-of-band" || stored.State != string(RosterStateActive) {
		t.Fatalf("stale writer mutated the replacement row: %+v", stored)
	}
}

func TestRosterOwnerIsolation(t *testing.T) {
	fake := newFakeRosterStore()
	// The probe answers unverifiable (its default) for handles it does not
	// know, mirroring an owner-scoped runtime registry: owner B must not
	// resolve owner A's roster row or a runtime behind it.
	roster := newTestRoster(fake, newFakeRosterProbe())
	entry := mustProvision(t, roster, rosterOwnerContext("owner-a"), "conv-1", "handle-a")

	ctxB := rosterOwnerContext("owner-b")
	// Owner B holds no roster row and its probe cannot verify a runtime, so
	// the typed fallback reports unverifiable, never owner A's row and never
	// a connected/nonpersistent attachment to another owner's runtime.
	attachment, err := roster.Attach(ctxB, "handle-a", "job-b")
	if err != nil {
		t.Fatalf("cross-owner attach err=%v", err)
	}
	if attachment.Resolution != RosterResolutionUnverifiable || attachment.Entry.ID != "" {
		t.Fatalf("cross-owner attach = %+v", attachment)
	}
	if _, err := roster.RequestRelease(ctxB, "handle-a", rosterIdentityOf(entry)); !errors.Is(err, ErrRosterNotFound) {
		t.Fatalf("cross-owner release err=%v", err)
	}
	report, err := roster.Reconcile(ctxB, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 0 {
		t.Fatalf("cross-owner reconcile leaked %d entries", len(report.Entries))
	}
	injection, err := roster.InjectContext(ctxB, "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(injection.Entries) != 0 {
		t.Fatalf("cross-owner injection leaked %d entries", len(injection.Entries))
	}
}

// sequencingProbe blocks its first probe call until released and answers
// different statuses to later calls, forcing the exact attach-vs-authority
// interleavings the concurrent stress tests cannot pin down.
type sequencingProbe struct {
	mu      sync.Mutex
	calls   int
	first   RosterRuntimeStatus
	later   RosterRuntimeStatus
	entered chan struct{}
	release chan struct{}
}

func newSequencingProbe(first, later RosterRuntimeStatus) *sequencingProbe {
	return &sequencingProbe{
		first: first, later: later,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
}

func (p *sequencingProbe) ProbeRosterRuntime(_ context.Context, _, _ string) (RosterRuntimeStatus, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	status := p.first
	if call > 1 {
		status = p.later
	}
	p.mu.Unlock()
	if call == 1 {
		close(p.entered)
		<-p.release
	}
	return status, nil
}

func TestRosterAttachNeverResurrectsAuthoritativeGone(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	fake := newFakeRosterStore()
	probe := newSequencingProbe(RosterRuntimeStatus{Kind: RosterRuntimeConnected}, RosterRuntimeStatus{Kind: RosterRuntimeGone})
	roster := newTestRoster(fake, probe)
	entry := mustProvision(t, roster, ctx, "conv-1", "handle-a")
	if _, err := roster.Activate(ctx, entry.ID, rosterIdentityOf(entry), "job-0"); err != nil {
		t.Fatal(err)
	}

	attachDone := make(chan RosterAttachment, 1)
	attachErr := make(chan error, 1)
	go func() {
		attachment, err := roster.Attach(ctx, "handle-a", "job-1")
		if err != nil {
			attachErr <- err
			return
		}
		attachDone <- attachment
	}()
	// Attach is now blocked inside the probe after reading the active row.
	<-probe.entered
	// Reconciliation authoritatively marks the row gone before attach resumes.
	report, err := roster.Reconcile(ctx, "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 1 || !report.Entries[0].Wrote || report.Entries[0].Outcome != RosterResolutionGone {
		t.Fatalf("reconcile = %+v", report.Entries)
	}
	close(probe.release)
	var attachment RosterAttachment
	select {
	case attachment = <-attachDone:
	case err := <-attachErr:
		t.Fatal(err)
	}
	// The connected probe must not resurrect the gone row: the expected-state
	// CAS fails, the re-read resolves authoritatively, and nothing is written.
	if attachment.Resolution != RosterResolutionGone {
		t.Fatalf("attach resolution=%s", attachment.Resolution)
	}
	stored, err := fake.AgentRosterGetByHandle(ctx, "handle-a")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != string(RosterStateGone) || stored.JobID != "job-0" {
		t.Fatalf("authoritative gone was overwritten: %+v", stored)
	}
}

func TestRosterAttachNeverBindsReleasedRow(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	fake := newFakeRosterStore()
	probe := newSequencingProbe(RosterRuntimeStatus{Kind: RosterRuntimeConnected}, RosterRuntimeStatus{Kind: RosterRuntimeConnected})
	roster := newTestRoster(fake, probe)
	entry := mustProvision(t, roster, ctx, "conv-1", "handle-a")
	if _, err := roster.Activate(ctx, entry.ID, rosterIdentityOf(entry), "job-0"); err != nil {
		t.Fatal(err)
	}

	attachDone := make(chan RosterAttachment, 1)
	go func() {
		attachment, err := roster.Attach(ctx, "handle-a", "job-1")
		if err != nil {
			attachDone <- RosterAttachment{}
			return
		}
		attachDone <- attachment
	}()
	<-probe.entered
	// The release completes while attach is blocked inside the probe.
	if _, err := roster.RequestRelease(ctx, "handle-a", rosterIdentityOf(entry)); err != nil {
		t.Fatal(err)
	}
	if _, err := roster.ConfirmRelease(ctx, "handle-a", rosterIdentityOf(entry)); err != nil {
		t.Fatal(err)
	}
	close(probe.release)
	attachment := <-attachDone
	// The completed release resolves as authoritative absence; the attach
	// never binds the released row.
	if attachment.Resolution != RosterResolutionGone {
		t.Fatalf("attach resolution=%s", attachment.Resolution)
	}
	stored, err := fake.AgentRosterGetByHandle(ctx, "handle-a")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != string(RosterStateReleased) || stored.JobID != "job-0" {
		t.Fatalf("released row was rebound: %+v", stored)
	}
}

func TestRosterStaleSavedIdentityRejected(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	fake := newFakeRosterStore()
	roster := newTestRoster(fake, newFakeRosterProbe())
	entry := mustProvision(t, roster, ctx, "conv-1", "handle-a")

	// An out-of-band replacement changes the incarnation underneath the caller.
	fake.mutate(entry.ID, func(row *store.AgentRosterRow) {
		row.Incarnation = "inc-replacement"
	})
	// A delayed caller holding the stale identity cannot activate...
	if _, err := roster.Activate(ctx, entry.ID, rosterIdentityOf(entry), "job-1"); !errors.Is(err, ErrRosterIdentityConflict) {
		t.Fatalf("stale activate err=%v", err)
	}
	// ...and cannot mark release-pending once the row is live.
	current := RosterIdentity{CreatedAt: entry.CreatedAt, Incarnation: "inc-replacement"}
	if _, err := roster.Activate(ctx, entry.ID, current, "job-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := roster.RequestRelease(ctx, "handle-a", rosterIdentityOf(entry)); !errors.Is(err, ErrRosterIdentityConflict) {
		t.Fatalf("stale request release err=%v", err)
	}
	if _, err := roster.RequestRelease(ctx, "handle-a", current); err != nil {
		t.Fatal(err)
	}
	// ...and cannot confirm the release either.
	if _, err := roster.ConfirmRelease(ctx, "handle-a", rosterIdentityOf(entry)); !errors.Is(err, ErrRosterIdentityConflict) {
		t.Fatalf("stale confirm release err=%v", err)
	}
	// The row is now release-pending: the stale caller must not be
	// acknowledged on the idempotent pending path either.
	if _, err := roster.RequestRelease(ctx, "handle-a", rosterIdentityOf(entry)); !errors.Is(err, ErrRosterIdentityConflict) {
		t.Fatalf("stale request release on pending row err=%v", err)
	}
	stored, err := fake.AgentRosterGetByHandle(ctx, "handle-a")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Incarnation != "inc-replacement" || stored.State != string(RosterStateReleasePending) {
		t.Fatalf("stale caller disturbed the row: %+v", stored)
	}
	// The holder of the current identity completes the release.
	confirmed, err := roster.ConfirmRelease(ctx, "handle-a", current)
	if err != nil || confirmed.State != RosterStateReleased {
		t.Fatalf("current identity confirm = %+v err=%v", confirmed, err)
	}
}

func TestRosterReplacePreservesMetadataAndRequiresIncarnation(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	fake := newFakeRosterStore()
	probe := newFakeRosterProbe()
	roster := newTestRoster(fake, probe)
	entry, err := roster.Provision(ctx, "conv-1", "handle-a", "", "tab-a", map[string]any{"origin": "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := roster.Activate(ctx, entry.ID, rosterIdentityOf(entry), "job-0"); err != nil {
		t.Fatal(err)
	}

	// A replacement without the authoritative current incarnation is
	// unverifiable and writes nothing.
	probe.set("handle-a", RosterRuntimeReplaced, "")
	attachment, err := roster.Attach(ctx, "handle-a", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Resolution != RosterResolutionUnverifiable {
		t.Fatalf("empty replacement resolution=%s", attachment.Resolution)
	}
	stored, err := fake.AgentRosterGetByHandle(ctx, "handle-a")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != string(RosterStateActive) {
		t.Fatalf("empty replacement wrote state %s", stored.State)
	}

	// A real replacement records the new incarnation and keeps the
	// provisioning-time metadata for audit.
	probe.set("handle-a", RosterRuntimeReplaced, "inc-new")
	attachment, err = roster.Attach(ctx, "handle-a", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Resolution != RosterResolutionReplaced || attachment.CurrentIncarnation != "inc-new" {
		t.Fatalf("replacement = %+v", attachment)
	}
	stored, err = fake.AgentRosterGetByHandle(ctx, "handle-a")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != string(RosterStateReplaced) {
		t.Fatalf("state=%s", stored.State)
	}
	if !strings.Contains(stored.MetadataJSON, `"origin":"synthetic"`) || !strings.Contains(stored.MetadataJSON, `"replacedByIncarnation":"inc-new"`) {
		t.Fatalf("metadata not merged: %s", stored.MetadataJSON)
	}
	// Re-attaching to the replaced row resolves replaced without a probe call.
	callsBefore := probe.callCount()
	attachment, err = roster.Attach(ctx, "handle-a", "job-2")
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Resolution != RosterResolutionReplaced || attachment.CurrentIncarnation != "inc-new" {
		t.Fatalf("re-attach to replaced = %+v", attachment)
	}
	if probe.callCount() != callsBefore {
		t.Fatal("replaced row was probed again")
	}
}

// staleFallbackStore forces the concurrent-state-conflict fallback path in
// RequestRelease: the first read shows the caller's row, the CAS reports a
// state conflict, and the fallback re-read returns a different row.
type staleFallbackStore struct {
	first  store.AgentRosterRow
	second store.AgentRosterRow
	reads  int
}

func (s *staleFallbackStore) AgentRosterProvision(context.Context, store.AgentRosterRow) (store.AgentRosterRow, error) {
	panic("unexpected provision")
}

func (s *staleFallbackStore) AgentRosterGet(context.Context, string) (store.AgentRosterRow, error) {
	panic("unexpected get")
}

func (s *staleFallbackStore) AgentRosterGetByHandle(context.Context, string) (store.AgentRosterRow, error) {
	s.reads++
	if s.reads == 1 {
		return s.first, nil
	}
	return s.second, nil
}

func (s *staleFallbackStore) AgentRosterList(context.Context, string) ([]store.AgentRosterRow, error) {
	panic("unexpected list")
}

func (s *staleFallbackStore) AgentRosterUpdate(context.Context, string, store.AgentRosterIdentity, map[string]any) (store.AgentRosterRow, error) {
	panic("unexpected update")
}

func (s *staleFallbackStore) AgentRosterCompareAndSwapState(context.Context, string, store.AgentRosterIdentity, string, map[string]any) (store.AgentRosterRow, error) {
	return store.AgentRosterRow{}, store.ErrAgentRosterStateConflict
}

func TestRosterRequestReleaseFallbackValidatesIdentity(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	active := store.AgentRosterRow{
		ID: "row-1", OwnerID: "owner-a", Handle: "handle-a", TabID: "tab-a",
		State: string(RosterStateActive), Incarnation: "inc-a", CreatedAt: 100,
	}
	pendingSameIdentity := active
	pendingSameIdentity.State = string(RosterStateReleasePending)
	pendingReplaced := pendingSameIdentity
	pendingReplaced.Incarnation = "inc-b"

	// The fallback re-read re-validates the caller's identity: a replaced
	// pending row is not acknowledged to the stale caller.
	roster := newTestRoster(&staleFallbackStore{first: active, second: pendingReplaced}, nil)
	if _, err := roster.RequestRelease(ctx, "handle-a", RosterIdentity{CreatedAt: 100, Incarnation: "inc-a"}); !errors.Is(err, ErrRosterIdentityConflict) {
		t.Fatalf("stale fallback err=%v", err)
	}
	// The fallback succeeds only when the pending row still carries the
	// caller's exact identity.
	roster = newTestRoster(&staleFallbackStore{first: active, second: pendingSameIdentity}, nil)
	entry, err := roster.RequestRelease(ctx, "handle-a", RosterIdentity{CreatedAt: 100, Incarnation: "inc-a"})
	if err != nil || entry.State != RosterStateReleasePending {
		t.Fatalf("legitimate fallback = %+v err=%v", entry, err)
	}
}

func TestRosterProvisionRejectsNonObjectMetadata(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	roster := newTestRoster(newFakeRosterStore(), newFakeRosterProbe())
	for name, metadata := range map[string]any{
		"null":    json.RawMessage("null"),
		"array":   json.RawMessage(`[1,2]`),
		"string":  json.RawMessage(`"text"`),
		"number":  json.RawMessage(`42`),
		"nil ptr": (*string)(nil),
	} {
		if _, err := roster.Provision(ctx, "conv-1", "handle-"+name, "", "tab-x", metadata); err == nil {
			t.Fatalf("%s metadata was accepted", name)
		}
	}
	// A JSON object is accepted.
	if _, err := roster.Provision(ctx, "conv-1", "handle-ok", "", "tab-x", json.RawMessage(`{"origin":"synthetic"}`)); err != nil {
		t.Fatalf("object metadata rejected: %v", err)
	}
}

func TestRosterCorruptMetadataNeverPanicsNorWrites(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	fake := newFakeRosterStore()
	probe := newFakeRosterProbe()
	roster := newTestRoster(fake, probe)
	entry := mustProvision(t, roster, ctx, "conv-1", "handle-a")
	if _, err := roster.Activate(ctx, entry.ID, rosterIdentityOf(entry), "job-0"); err != nil {
		t.Fatal(err)
	}

	for _, corrupt := range []string{"null", `"text"`, `[1,2]`, `42`} {
		fake.mutate(entry.ID, func(row *store.AgentRosterRow) {
			row.MetadataJSON = corrupt
		})
		probe.set("handle-a", RosterRuntimeReplaced, "inc-new")

		// Attach with a corrupt persisted value: unverifiable, no write,
		// never a panic.
		attachment, err := roster.Attach(ctx, "handle-a", "job-1")
		if err != nil {
			t.Fatalf("attach with %s metadata err=%v", corrupt, err)
		}
		if attachment.Resolution != RosterResolutionUnverifiable {
			t.Fatalf("attach with %s metadata resolution=%s", corrupt, attachment.Resolution)
		}
		stored, err := fake.AgentRosterGetByHandle(ctx, "handle-a")
		if err != nil {
			t.Fatal(err)
		}
		if stored.State != string(RosterStateActive) || stored.MetadataJSON != corrupt {
			t.Fatalf("attach wrote over %s metadata: %+v", corrupt, stored)
		}

		// Reconciliation reaches the same helper: same honest outcome.
		report, err := roster.Reconcile(ctx, "conv-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Entries) != 1 || report.Entries[0].Outcome != RosterResolutionUnverifiable || report.Entries[0].Wrote {
			t.Fatalf("reconcile with %s metadata = %+v", corrupt, report.Entries)
		}
		if !strings.Contains(report.Entries[0].Reason, "no write") {
			t.Fatalf("reconcile reason hides the outcome: %q", report.Entries[0].Reason)
		}
	}
}

// A connected first attach binds the runtime episode through the same
// metadata merge, so a corrupt persisted value must fail closed exactly like
// the transition path: unverifiable, no write, never a reset of the corrupt
// value. A legal object still merges, keeping unrelated keys.
func TestRosterCorruptMetadataFailsClosedOnConnectedFirstAttach(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	fake := newFakeRosterStore()
	probe := newFakeRosterProbe()
	roster := newTestRoster(fake, probe)
	entry := mustProvision(t, roster, ctx, "conv-1", "handle-a")

	for _, corrupt := range []string{"null", `"text"`, `[1,2]`, `42`, `{malformed`} {
		fake.mutate(entry.ID, func(row *store.AgentRosterRow) {
			row.MetadataJSON = corrupt
		})
		probe.setConnected("handle-a", "episode-1")

		attachment, err := roster.Attach(ctx, "handle-a", "job-1")
		if err != nil {
			t.Fatalf("attach with %s metadata err=%v", corrupt, err)
		}
		if attachment.Resolution != RosterResolutionUnverifiable {
			t.Fatalf("attach with %s metadata resolution=%s", corrupt, attachment.Resolution)
		}
		stored, err := fake.AgentRosterGetByHandle(ctx, "handle-a")
		if err != nil {
			t.Fatal(err)
		}
		if stored.MetadataJSON != corrupt || stored.JobID != "" {
			t.Fatalf("attach wrote over %s metadata: %+v", corrupt, stored)
		}
	}

	// A legal object still merges: the episode binding lands and unrelated
	// keys survive.
	fake.mutate(entry.ID, func(row *store.AgentRosterRow) {
		row.MetadataJSON = `{"origin":"synthetic"}`
	})
	attachment, err := roster.Attach(ctx, "handle-a", "job-2")
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Resolution != RosterResolutionConnected {
		t.Fatalf("legal merge attach = %+v", attachment)
	}
	stored, err := fake.AgentRosterGetByHandle(ctx, "handle-a")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.MetadataJSON, `"runtimeEpisode":"episode-1"`) || !strings.Contains(stored.MetadataJSON, `"origin":"synthetic"`) {
		t.Fatalf("legal merge lost keys: %s", stored.MetadataJSON)
	}
}
