package supervisor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// HelperConsentToken names exactly the helper and session count a user saw:
// "protocol:pid:sessions:count", where count is 1 when every session was
// counted, 2 when only PTY sessions were listed and 0 when the count is
// unknown.
func HelperConsentToken(running RunningHelper) string {
	count := 0
	switch {
	case running.ActivityKnown:
		count = 1
	case running.PTYListed:
		count = 2
	}
	return fmt.Sprintf("%d:%d:%d:%d", running.Protocol, running.PID, running.Activity.Total(), count)
}

// ParseHelperConsent reads a token of HelperConsentToken.
func ParseHelperConsent(token string) (RunningHelper, bool) {
	parts := strings.Split(token, ":")
	if len(parts) != 4 {
		return RunningHelper{}, false
	}
	values := make([]int, len(parts))
	for index, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return RunningHelper{}, false
		}
		values[index] = value
	}
	if values[0] <= 0 || values[1] <= 0 || values[3] > 2 {
		return RunningHelper{}, false
	}
	return RunningHelper{
		Protocol: values[0], PID: values[1],
		Activity:      ActivitySnapshot{PTY: values[2]},
		ActivityKnown: values[3] == 1, PTYListed: values[3] == 2,
	}, true
}

// ConsentCovers reports whether consent given for the helper described by
// consent still covers current: the same process, protocol and kind of
// count, and no more sessions than the user agreed to end.
func ConsentCovers(consent, current RunningHelper) bool {
	return consent.PID == current.PID && consent.Protocol == current.Protocol &&
		consent.ActivityKnown == current.ActivityKnown && consent.PTYListed == current.PTYListed &&
		current.Activity.Total() <= consent.Activity.Total()
}

// ReplaceStage is a stage of replacing an older helper.
type ReplaceStage string

const (
	// ReplaceVerify inspects the helper again and matches it to the consent.
	ReplaceVerify ReplaceStage = "verify"
	// ReplaceTerminate stops it: TERM, bounded wait, KILL, then the state
	// lock is released.
	ReplaceTerminate ReplaceStage = "terminate"
	// ReplaceStart starts this build's helper for the state directory.
	ReplaceStart ReplaceStage = "start"
	// ReplaceReady: this build's helper serves the state directory.
	ReplaceReady ReplaceStage = "ready"
	// ReplaceRollback restarts the stopped helper's executable after this
	// build's helper failed to start. Its sessions are not revived.
	ReplaceRollback ReplaceStage = "rollback"
	// ReplaceFailed ends a replacement that did not reach ready. Nothing is
	// retried: another attempt needs new consent.
	ReplaceFailed ReplaceStage = "failed"
)

// replaceTransitions is the complete transition table. Every path ends in
// ready or failed, and no stage is entered twice, so a replacement never
// loops.
var replaceTransitions = map[ReplaceStage][]ReplaceStage{
	ReplaceVerify:    {ReplaceTerminate, ReplaceStart, ReplaceFailed},
	ReplaceTerminate: {ReplaceStart, ReplaceFailed},
	ReplaceStart:     {ReplaceReady, ReplaceRollback},
	ReplaceRollback:  {ReplaceFailed},
	ReplaceReady:     nil,
	ReplaceFailed:    nil,
}

// ErrHelperReplaceFailed reports a consented replacement that did not leave
// this build's helper serving the state directory.
var ErrHelperReplaceFailed = fmt.Errorf("%w: replacing the older supervisor helper failed", ErrUnavailable)

// HelperReplaceError reports where a replacement failed and whether the
// older helper's executable was started again.
type HelperReplaceError struct {
	Stage      ReplaceStage
	RolledBack bool
	Cause      error
}

func (e *HelperReplaceError) Error() string {
	text := fmt.Sprintf("replacing the older supervisor helper failed at %s", e.Stage)
	if e.RolledBack {
		text += "; the older helper was started again without its ended sessions"
	}
	if e.Cause != nil {
		text += ": " + e.Cause.Error()
	}
	return text
}

func (e *HelperReplaceError) Unwrap() []error { return []error{ErrHelperReplaceFailed, e.Cause} }

// replaceSteps are the operations a replacement runs; tests substitute them.
type replaceSteps struct {
	// verify inspects the helper serving the state directory on the consent's
	// protocol. ErrHelperNotRunning means it already exited.
	verify func(ctx context.Context, protocol int) (RunningHelper, error)
	// executable reports the executable of the verified helper process, so a
	// failed start can be rolled back to it.
	executable func(pid int) (string, error)
	stop       func(ctx context.Context, expected RunningHelper) error
	start      func(ctx context.Context) (*Helper, error)
	// restore starts executable again as the helper of the old protocol and
	// waits until it serves.
	restore func(ctx context.Context, executable string, protocol int) error
}

// replaceRun walks the transition table once.
type replaceRun struct {
	stage ReplaceStage
	trace []ReplaceStage
}

func (r *replaceRun) enter(next ReplaceStage) {
	if !slices.Contains(replaceTransitions[r.stage], next) {
		panic(fmt.Sprintf("supervisor: illegal helper replacement transition %s -> %s", r.stage, next))
	}
	r.stage = next
	r.trace = append(r.trace, next)
}

// run replaces the helper the consent names. It returns this build's helper,
// or a *LegacyHelperError when the consent no longer covers the running
// helper, or a *HelperReplaceError.
func (s replaceSteps) run(ctx context.Context, consent RunningHelper) (*Helper, []ReplaceStage, error) {
	run := &replaceRun{stage: ReplaceVerify, trace: []ReplaceStage{ReplaceVerify}}
	fail := func(stage ReplaceStage, rolledBack bool, cause error) (*Helper, []ReplaceStage, error) {
		run.enter(ReplaceFailed)
		return nil, run.trace, &HelperReplaceError{Stage: stage, RolledBack: rolledBack, Cause: cause}
	}
	current, err := s.verify(ctx, consent.Protocol)
	gone := errors.Is(err, ErrHelperNotRunning)
	switch {
	case gone:
		// The helper the user agreed to end already exited; nothing is
		// stopped and this build's helper starts.
	case err != nil:
		run.enter(ReplaceFailed)
		return nil, run.trace, &HelperReplaceError{Stage: ReplaceVerify, Cause: err}
	case current.Protocol >= ProtocolVersion || !ConsentCovers(consent, current):
		run.enter(ReplaceFailed)
		return nil, run.trace, &LegacyHelperError{
			Helper: current, ConsentToken: HelperConsentToken(current), Stale: true,
			message: "the older helper changed since the user agreed to end it; confirm again",
		}
	}
	executable := ""
	if !gone {
		if path, err := s.executable(current.PID); err == nil {
			executable = path
		}
		run.enter(ReplaceTerminate)
		if err := s.stop(ctx, consent); err != nil {
			// StopHelper refuses before signalling when the helper changed, and
			// reports a helper that would not exit; either way nothing new is
			// started beside it.
			return fail(ReplaceTerminate, false, err)
		}
	}
	run.enter(ReplaceStart)
	helper, err := s.start(ctx)
	if err == nil {
		run.enter(ReplaceReady)
		return helper, run.trace, nil
	}
	startErr := err
	run.enter(ReplaceRollback)
	if errors.Is(startErr, ErrLegacyHelperRunning) {
		// Another client started an older helper again between the stop and
		// this start. It serves its own sessions; it is not stopped without
		// new consent.
		return fail(ReplaceStart, false, startErr)
	}
	if executable == "" {
		return fail(ReplaceStart, false, fmt.Errorf("%w; the older helper's executable is unknown, so it was not started again", startErr))
	}
	if err := s.restore(ctx, executable, consent.Protocol); err != nil {
		return fail(ReplaceStart, false, errors.Join(startErr, fmt.Errorf("restore the older helper: %w", err)))
	}
	return fail(ReplaceStart, true, startErr)
}

// ReplaceLegacyHelper ends the older helper a consent token names, with all
// of its sessions, and starts this build's helper for config.StateDir. The
// token must come from a LegacyHelperError the user approved; a helper that
// changed since (another pid, more sessions, a count it no longer reports) is
// refused with a fresh *LegacyHelperError (Stale) before anything is
// signalled. Stopping is StopHelper: the owner and pid of the endpoint are
// verified, then TERM, a bounded wait, KILL, and the wait for the state lock.
// When this build's helper does not start, the older helper's executable is
// started again when it is known, and a *HelperReplaceError reports the
// failure. Nothing is retried.
func ReplaceLegacyHelper(ctx context.Context, config HelperConfig, token string) (*Helper, error) {
	consent, ok := ParseHelperConsent(token)
	if !ok || consent.Protocol >= ProtocolVersion {
		return nil, fmt.Errorf("%w: malformed helper replacement consent", ErrInvalidInput)
	}
	stateDir, err := filepath.Abs(config.StateDir)
	if err != nil || config.StateDir == "" {
		return nil, fmt.Errorf("%w: helper state directory is required", ErrInvalidInput)
	}
	config.StateDir = stateDir
	config.NoSpawn = false
	steps := replaceSteps{
		verify: func(ctx context.Context, protocol int) (RunningHelper, error) {
			running, err := inspectHelperVersion(ctx, stateDir, protocol)
			if errors.Is(err, errNoListener) {
				// It exited, or was already replaced: starting attaches to a
				// helper of this build that serves already.
				return RunningHelper{}, fmt.Errorf("%w: %v", ErrHelperNotRunning, err)
			}
			return running, err
		},
		executable: helperProcessExecutable,
		stop: func(ctx context.Context, expected RunningHelper) error {
			return StopHelper(ctx, stateDir, expected)
		},
		start: func(ctx context.Context) (*Helper, error) {
			return ConnectHelper(ctx, config)
		},
		restore: func(ctx context.Context, executable string, protocol int) error {
			return restoreHelper(ctx, stateDir, executable, protocol)
		},
	}
	helper, _, err := steps.run(ctx, consent)
	return helper, err
}

// helperRestoreWait bounds how long a restored older helper may take to serve.
var helperRestoreWait = 15 * time.Second

func restoreHelper(ctx context.Context, stateDir, executable string, protocol int) error {
	process, err := spawnDetached(executable, []string{HelperCommand, "--state-dir", stateDir}, filepath.Join(stateDir, "helper.log"))
	if err != nil {
		return err
	}
	deadline := time.Now().Add(helperRestoreWait)
	for {
		if _, err := inspectHelperVersion(ctx, stateDir, protocol); err == nil {
			return nil
		} else if time.Now().After(deadline) {
			process.kill()
			return fmt.Errorf("the restored helper did not serve within %s: %w", helperRestoreWait, err)
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-process.done:
			return fmt.Errorf("the restored helper exited: %v", process.exitErr())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// ReplaceLegacyHelper asks the side that runs the helpers to end the older
// helper named by token and continue on this build's helper. Over a bridge
// (SSH, a device bridge) the bridge on that host verifies and stops the
// helper; for a local client this process does. It returns nil once this
// build's helper answered, a *LegacyHelperError when the consent is stale or
// an older helper still serves, or an error matching ErrHelperReplaceFailed.
func (c *Client) ReplaceLegacyHelper(ctx context.Context, token string) error {
	if c.dial == nil {
		_, err := ReplaceLegacyHelper(ctx, HelperConfig{StateDir: c.stateDir}, token)
		return err
	}
	netConn, err := c.dial(ctx)
	if err != nil {
		return fmt.Errorf("%w: dial supervisor: %v", ErrUnavailable, err)
	}
	conn := &clientConn{conn: netConn}
	defer func() { _ = conn.Close() }()
	kind, payload, err := conn.request(ctx, frameHello, helloMsg{Version: ProtocolVersion, StateDigest: c.stateDigest, ReplaceLegacy: token})
	if err != nil {
		return err
	}
	if kind != frameHelloAck {
		return protocolMismatch(kind)
	}
	var ack helloMsg
	if err := unmarshalFrame(payload, &ack); err != nil {
		return err
	}
	if ack.Version != ProtocolVersion {
		return versionMismatchError(ack.Version, "hello acknowledged with another protocol version")
	}
	return nil
}
