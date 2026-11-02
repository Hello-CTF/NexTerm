package supervisor

import (
	"context"
	"errors"
	"path/filepath"
)

// StateDirSteps are the operations SettleStateDir runs; nil fields use
// InspectHelper and StopHelper. Tests substitute them.
type StateDirSteps struct {
	Inspect func(ctx context.Context, stateDir string) (RunningHelper, error)
	Stop    func(ctx context.Context, stateDir string, expected RunningHelper) error
}

// StateDirChoice is where a client reaches the helper of one OS user.
type StateDirChoice struct {
	// Dir is the state directory to use now.
	Dir string
	// Legacy is set while Dir is still the previous directory because the
	// helper there runs sessions (or cannot be told to run none). Live PTYs
	// cannot move between helpers, so the client keeps using it until they end.
	Legacy bool
	// LegacySessions and LegacyKnown describe that helper's sessions while
	// Legacy; an unknown count is not zero.
	LegacySessions int
	LegacyKnown    bool
	// Stopped is set when the previous directory's helper ran no session and
	// was stopped in favour of the shared one.
	Stopped bool
	// Err is why the previous helper could not be settled (it did not answer
	// an inspection, or would not stop); the client stays on it.
	Err error
}

// SettleStateDir chooses between the shared helper directory and the previous
// per-app one. The shared directory is used when no helper runs in the previous
// one or the helper there runs no session (it is then stopped; its directory is
// never deleted). A previous helper that runs sessions, cannot report them, or
// does not answer keeps the client on the previous directory, so no terminal
// is ended. An empty or identical legacy directory selects shared. Callers
// run it at startup and periodically until Dir is the shared directory.
func SettleStateDir(ctx context.Context, shared, legacy string, steps StateDirSteps) StateDirChoice {
	if legacy == "" || samePath(shared, legacy) {
		return StateDirChoice{Dir: shared}
	}
	if steps.Inspect == nil {
		steps.Inspect = InspectHelper
	}
	if steps.Stop == nil {
		steps.Stop = StopHelper
	}
	stay := StateDirChoice{Dir: legacy, Legacy: true}
	running, err := steps.Inspect(ctx, legacy)
	switch {
	case errors.Is(err, ErrHelperNotRunning):
		return StateDirChoice{Dir: shared}
	case err != nil:
		stay.Err = err
		return stay
	}
	stay.LegacyKnown = running.ActivityKnown
	stay.LegacySessions = running.Activity.Total()
	if !running.ActivityKnown || running.Activity.Total() > 0 {
		return stay
	}
	if err := steps.Stop(ctx, legacy, running); err != nil {
		stay.Err = err
		return stay
	}
	return StateDirChoice{Dir: shared, Stopped: true}
}

func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && absA == absB
}
