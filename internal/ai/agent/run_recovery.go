package agent

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// RunRecoveryState classifies an interrupted run's runtime after an
// absence. It mirrors the roster resolution semantics: disconnected may
// return, gone requires authoritative absence, and unverifiable never
// triggers an automatic write and never masquerades as gone.
type RunRecoveryState string

const (
	RunRecoveryConnected    RunRecoveryState = "connected"
	RunRecoveryDisconnected RunRecoveryState = "disconnected"
	RunRecoveryGone         RunRecoveryState = "gone"
	RunRecoveryExited       RunRecoveryState = "exited"
	RunRecoveryReplaced     RunRecoveryState = "replaced"
	RunRecoveryUnverifiable RunRecoveryState = "unverifiable"
)

// RunRuntimeProbe authoritatively classifies the runtime behind a run.
type RunRuntimeProbe interface {
	ProbeRunRuntime(ctx context.Context, runID string) (RosterRuntimeStatus, error)
}

// RunRecoveryStore is the persistence contract of the recovery scan,
// satisfied by *store.Store.
type RunRecoveryStore interface {
	RunPage(ctx context.Context, filter store.RunPageFilter) (store.RunPage, error)
	RunGet(ctx context.Context, id string) (store.RunRow, error)
	RunFinishUsageIfStatus(ctx context.Context, runID, expectedStatus, status, answer, errMsg string, turns int, tokensIn, tokensOut, cacheCreationTokens, latencyMS int64) (bool, error)
}

// RunRecoveryEntry accounts for one run examined by the recovery scan.
type RunRecoveryEntry struct {
	RunID  string           `json:"runId"`
	State  RunRecoveryState `json:"state"`
	Wrote  bool             `json:"wrote"`
	Reason string           `json:"reason"`
}

// RunRecoverySummary is the auditable recovery result: every active run is
// accounted for with its state and whether a write happened.
type RunRecoverySummary struct {
	Entries      []RunRecoveryEntry `json:"entries"`
	Connected    int                `json:"connected"`
	Disconnected int                `json:"disconnected"`
	Gone         int                `json:"gone"`
	Exited       int                `json:"exited"`
	Replaced     int                `json:"replaced"`
	Unverifiable int                `json:"unverifiable"`
}

// RunRecovery scans the owner's active runs and reconciles them against
// the runtime probe, paging through the full active set so more than fifty
// runs recover exactly like a handful. Only authoritative outcomes write.
type RunRecovery struct {
	store RunRecoveryStore
}

func NewRunRecovery(store RunRecoveryStore) *RunRecovery {
	return &RunRecovery{store: store}
}

// Recover examines every running or interrupted run of the owner. Canceled
// and otherwise finished runs are outside the active set and never touched.
// The scan pages by the stable (created_at, id) keyset, so writes landed
// during recovery never move a row across the cursor.
func (r *RunRecovery) Recover(ctx context.Context, probe RunRuntimeProbe) (RunRecoverySummary, error) {
	summary := RunRecoverySummary{Entries: []RunRecoveryEntry{}}
	var cursor *store.RunCursor
	for {
		page, err := r.store.RunPage(ctx, store.RunPageFilter{
			Statuses:       []string{store.RunStatusRunning, store.RunStatusInterrupted},
			UnfinishedOnly: true,
			After:          cursor,
			Limit:          runRecoveryPageSize,
		})
		if err != nil {
			return RunRecoverySummary{}, err
		}
		for _, run := range page.Runs {
			if run.FinishedAt != nil {
				continue
			}
			entry, write := r.examine(ctx, probe, run)
			if write {
				applied, current, err := r.applyWrite(ctx, run, entry.State)
				switch {
				case err != nil:
					entry.State = RunRecoveryUnverifiable
					entry.Wrote = false
					entry.Reason = "authoritative write failed: " + err.Error()
				case !applied:
					// The row moved past the scanned state: the stored
					// state stands, and the summary accounts for it instead
					// of claiming the write. Only a finished row gets the
					// terminal wording; a resumed, still-active row is
					// reported as a changed-after-scan no-op.
					entry.Wrote = false
					if current.FinishedAt != nil {
						entry.Reason = "no-op: a concurrent writer already finished the run as " + current.Status
					} else {
						entry.Reason = "no-op: the run changed after the scan and is still active as " + current.Status
					}
				}
			}
			summary.add(entry)
		}
		if page.NextCursor == nil {
			return summary, nil
		}
		cursor = page.NextCursor
	}
}

const runRecoveryPageSize = 50

// examine classifies one run against the probe and reports whether an
// authoritative write is due. Only gone, exited, and replaced write;
// disconnected and unverifiable never do.
func (r *RunRecovery) examine(ctx context.Context, probe RunRuntimeProbe, run store.RunRow) (RunRecoveryEntry, bool) {
	entry := RunRecoveryEntry{RunID: run.ID}
	status, err := probe.ProbeRunRuntime(ctx, run.ID)
	if err != nil || status.Kind == "" {
		entry.State = RunRecoveryUnverifiable
		entry.Reason = "runtime probe unavailable: no automatic write"
		return entry, false
	}
	switch status.Kind {
	case RosterRuntimeConnected:
		entry.State = RunRecoveryConnected
		entry.Reason = "runtime still connected; no recovery write"
	case RosterRuntimeDisconnected:
		entry.State = RunRecoveryDisconnected
		entry.Reason = "runtime unreachable; may return without a write"
	case RosterRuntimeGone:
		entry.State = RunRecoveryGone
		entry.Wrote = true
		entry.Reason = "authoritative absence confirmed"
	case RosterRuntimeExited:
		entry.State = RunRecoveryExited
		entry.Wrote = true
		entry.Reason = "runtime exited during the absence"
	case RosterRuntimeReplaced:
		if status.CurrentIncarnation == "" {
			// A replacement answer without the authoritative current
			// incarnation cannot write, mirroring the roster contract.
			entry.State = RunRecoveryUnverifiable
			entry.Reason = "replacement reported without a current incarnation: no automatic write"
			return entry, false
		}
		entry.State = RunRecoveryReplaced
		entry.Wrote = true
		entry.Reason = "a newer incarnation superseded this run"
	default:
		entry.State = RunRecoveryUnverifiable
		entry.Reason = "state not verifiable: no automatic write"
	}
	return entry, entry.Wrote
}

func (s *RunRecoverySummary) add(entry RunRecoveryEntry) {
	s.Entries = append(s.Entries, entry)
	switch entry.State {
	case RunRecoveryConnected:
		s.Connected++
	case RunRecoveryDisconnected:
		s.Disconnected++
	case RunRecoveryGone:
		s.Gone++
	case RunRecoveryExited:
		s.Exited++
	case RunRecoveryReplaced:
		s.Replaced++
	default:
		s.Unverifiable++
	}
}

// applyWrite lands the authoritative terminal transition for a run and
// reports whether it actually applied. The scanned status is the
// commit-time guard: a run that moved past the scanned state (resumed,
// completed, canceled) is never terminally written from a stale probe. A
// false result with a nil error returns the re-read row so the summary
// can tell a finished row from a still-active one instead of claiming
// the write.
func (r *RunRecovery) applyWrite(ctx context.Context, run store.RunRow, state RunRecoveryState) (bool, store.RunRow, error) {
	var status, message string
	switch state {
	case RunRecoveryGone:
		status, message = store.RunStatusFailed, "runtime gone"
	case RunRecoveryExited:
		status, message = store.RunStatusInterrupted, "runtime exited during the absence"
	case RunRecoveryReplaced:
		status, message = RunStatusSuperseded, "run superseded by a newer incarnation"
	default:
		return false, store.RunRow{}, ipc.NewError(ipc.CodeBadParam, "state does not write")
	}
	applied, err := r.store.RunFinishUsageIfStatus(ctx, run.ID, run.Status, status, run.Answer, message, run.Turns, run.TokensIn, run.TokensOut, run.CacheCreationTokens, run.LatencyMS)
	if err != nil {
		return false, store.RunRow{}, err
	}
	if !applied {
		current, err := r.store.RunGet(ctx, run.ID)
		if err != nil {
			return false, store.RunRow{}, err
		}
		return false, current, nil
	}
	return true, store.RunRow{}, nil
}
