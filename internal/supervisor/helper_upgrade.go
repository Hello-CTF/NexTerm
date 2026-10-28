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

// consentDigestLength is how much of the state digest a consent token keeps:
// enough to tell state directories apart, short enough to read.
const consentDigestLength = 16

// HelperConsentToken names exactly the helper and session count a user saw:
// "protocol:pid:sessions:count:start:digest". count is 1 when every session
// was counted, 2 when only PTY sessions were listed and 0 when the count is
// unknown; start is the process start time (empty where the platform cannot
// read it) and digest the start of the state digest.
func HelperConsentToken(running RunningHelper) string {
	count := 0
	switch {
	case running.ActivityKnown:
		count = 1
	case running.PTYListed:
		count = 2
	}
	start := ""
	if running.StartTime != 0 {
		start = strconv.FormatUint(running.StartTime, 10)
	}
	digest := running.StateDigest
	if len(digest) > consentDigestLength {
		digest = digest[:consentDigestLength]
	}
	return fmt.Sprintf("%d:%d:%d:%d:%s:%s", running.Protocol, running.PID, running.Activity.Total(), count, start, digest)
}

// ParseHelperConsent reads a token of HelperConsentToken. The start time may
// be empty (platforms that cannot read it); the state digest may not.
func ParseHelperConsent(token string) (RunningHelper, bool) {
	parts := strings.Split(token, ":")
	if len(parts) != 6 {
		return RunningHelper{}, false
	}
	values := make([]int, 4)
	for index, part := range parts[:4] {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return RunningHelper{}, false
		}
		values[index] = value
	}
	if values[0] <= 0 || values[1] <= 0 || values[3] > 2 {
		return RunningHelper{}, false
	}
	running := RunningHelper{
		Protocol: values[0], PID: values[1],
		Activity:      ActivitySnapshot{PTY: values[2]},
		ActivityKnown: values[3] == 1, PTYListed: values[3] == 2,
	}
	if parts[4] != "" {
		start, err := strconv.ParseUint(parts[4], 10, 64)
		if err != nil || start == 0 {
			return RunningHelper{}, false
		}
		running.StartTime = start
	}
	if parts[5] == "" {
		return RunningHelper{}, false
	}
	for _, char := range parts[5] {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return RunningHelper{}, false
		}
	}
	running.StateDigest = parts[5]
	return running, true
}

// sameProcessStart reports whether current is the process consent named. A
// consent without a start time only covers a helper whose start time cannot
// be read either.
func sameProcessStart(consent, current RunningHelper) bool {
	return consent.StartTime == current.StartTime
}

// ConsentCovers reports whether consent given for the helper described by
// consent still covers current: the same process (pid and start time), state directory, protocol and kind of count, and no more
// sessions than the user agreed to end.
func ConsentCovers(consent, current RunningHelper) bool {
	return consent.PID == current.PID && consent.Protocol == current.Protocol && sameProcessStart(consent, current) &&
		(consent.StateDigest != "" && strings.HasPrefix(current.StateDigest, consent.StateDigest)) &&
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

// ReplaceResult is what a replacement did to the older helper.
type ReplaceResult struct {
	// Stopped reports that this replacement ended the older helper and its
	// sessions; false when it had already exited or another replacement
	// ended it.
	Stopped bool
	// Forced reports that it did not exit after TERM and was killed.
	Forced bool
}

// HelperReplaceError reports where a replacement failed, whether the older
// helper had already been ended (Stopped, Forced) and whether the process
// this replacement started from its executable now serves (RolledBack).
type HelperReplaceError struct {
	Stage      ReplaceStage
	Stopped    bool
	Forced     bool
	RolledBack bool
	Cause      error
}

func (e *HelperReplaceError) Error() string {
	text := fmt.Sprintf("replacing the older supervisor helper failed at %s", e.Stage)
	if e.Stopped {
		text += "; the older helper and its sessions were ended"
	}
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
	// stop ends the verified helper and reports how, with the executable it
	// ran so a failed start can be rolled back to it.
	stop  func(ctx context.Context, expected RunningHelper) (StopResult, error)
	start func(ctx context.Context) (*Helper, error)
	// restore starts executable again as the helper of the old protocol and
	// reports whether the process it started is the one that now serves.
	restore func(ctx context.Context, executable string, protocol int) (bool, error)
	// sameProtocol is set when the helper to replace speaks this build's protocol and differs only in its
	// build: the verified helper must still be that older build for the consent to apply.
	sameProtocol bool
	stillOlder   func(RunningHelper) bool
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
func (s replaceSteps) run(ctx context.Context, consent RunningHelper) (*Helper, ReplaceResult, []ReplaceStage, error) {
	run := &replaceRun{stage: ReplaceVerify, trace: []ReplaceStage{ReplaceVerify}}
	var result ReplaceResult
	fail := func(stage ReplaceStage, rolledBack bool, cause error) (*Helper, ReplaceResult, []ReplaceStage, error) {
		run.enter(ReplaceFailed)
		return nil, result, run.trace, &HelperReplaceError{Stage: stage, Stopped: result.Stopped, Forced: result.Forced, RolledBack: rolledBack, Cause: cause}
	}
	current, err := s.verify(ctx, consent.Protocol)
	gone := errors.Is(err, ErrHelperNotRunning)
	switch {
	case gone:
		// The helper the user agreed to end already exited, or another
		// replacement ended it; nothing is stopped and this build's helper
		// starts or is attached.
	case err != nil:
		return fail(ReplaceVerify, false, err)
	case s.staleMismatch(current) || !ConsentCovers(consent, current):
		run.enter(ReplaceFailed)
		return nil, result, run.trace, &LegacyHelperError{
			Helper: current, ConsentToken: HelperConsentToken(current), Stale: true,
			message: "the older helper changed since the user agreed to end it; confirm again",
		}
	}
	executable := ""
	if !gone {
		run.enter(ReplaceTerminate)
		stopped, err := s.stop(ctx, consent)
		result.Forced = stopped.Forced
		if err != nil {
			// StopHelper refuses before signalling when the helper changed, and
			// reports a helper that would not exit; either way nothing new is
			// started beside it.
			return fail(ReplaceTerminate, false, err)
		}
		result.Stopped = true
		executable = stopped.Executable
	}
	run.enter(ReplaceStart)
	helper, err := s.start(ctx)
	if err == nil {
		run.enter(ReplaceReady)
		return helper, result, run.trace, nil
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
	ours, err := s.restore(ctx, executable, consent.Protocol)
	if err != nil {
		return fail(ReplaceStart, false, errors.Join(startErr, fmt.Errorf("restore the older helper: %w", err)))
	}
	if !ours {
		return fail(ReplaceStart, false, fmt.Errorf("%w; an older helper started by another client serves the state directory", startErr))
	}
	return fail(ReplaceStart, true, startErr)
}

// staleMismatch reports a verified helper that the replacement in progress does not apply to: a
// same-protocol replacement needs a helper still running an older build, any other needs an older protocol.
func (s replaceSteps) staleMismatch(current RunningHelper) bool {
	if s.sameProtocol {
		return current.Protocol != ProtocolVersion || s.stillOlder == nil || !s.stillOlder(current)
	}
	return current.Protocol >= ProtocolVersion
}

// helperReplaceWait bounds how long a replacement waits for another
// replacement of the same state directory to finish.
var helperReplaceWait = 2 * time.Minute

// waitReplaceLock takes the replacement lock of stateDir, waiting for a
// replacement that holds it.
func waitReplaceLock(ctx context.Context, stateDir string) (func(), error) {
	deadline := time.Now().Add(helperReplaceWait)
	for {
		unlock, err := lockHelperReplace(stateDir)
		if err == nil {
			return unlock, nil
		}
		if !errors.Is(err, ErrAlreadyExists) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: another replacement of the helper in %s did not finish", ErrUnavailable, stateDir)
		}
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// ReplaceLegacyHelper ends the older helper a consent token names, with all
// of its sessions, and starts this build's helper for config.StateDir. The
// token must come from a LegacyHelperError the user approved; a helper that
// changed since (another process, more sessions, a count it no longer
// reports) is refused with a fresh *LegacyHelperError (Stale) before anything
// is signalled. Stopping is StopHelper: the owner and pid of the endpoint are
// verified, then TERM, a bounded wait, KILL, and the wait for the state lock.
// Replacements of one state directory are serialized across processes by
// helper.replace.lock; one that waited finds the older helper gone and
// attaches to the helper the first one started. When this build's helper
// does not start, the older helper's executable is started again
// (HelperCommand --state-dir only) when it is known, and a
// *HelperReplaceError reports the failure. Nothing is retried.
func ReplaceLegacyHelper(ctx context.Context, config HelperConfig, token string) (*Helper, ReplaceResult, error) {
	consent, ok := ParseHelperConsent(token)
	if !ok || consent.Protocol >= ProtocolVersion {
		return nil, ReplaceResult{}, fmt.Errorf("%w: malformed helper replacement consent", ErrInvalidInput)
	}
	stateDir, err := filepath.Abs(config.StateDir)
	if err != nil || config.StateDir == "" {
		return nil, ReplaceResult{}, fmt.Errorf("%w: helper state directory is required", ErrInvalidInput)
	}
	config.StateDir = stateDir
	config.NoSpawn = false
	unlock, err := waitReplaceLock(ctx, stateDir)
	if err != nil {
		return nil, ReplaceResult{}, &HelperReplaceError{Stage: ReplaceVerify, Cause: err}
	}
	defer unlock()
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
		stop: func(ctx context.Context, expected RunningHelper) (StopResult, error) {
			return stopHelper(ctx, stateDir, expected)
		},
		start: func(ctx context.Context) (*Helper, error) {
			return ConnectHelper(ctx, config)
		},
		restore: func(ctx context.Context, executable string, protocol int) (bool, error) {
			return restoreHelper(ctx, stateDir, executable, protocol)
		},
	}
	helper, result, _, err := steps.run(ctx, consent)
	return helper, result, err
}

// helperRestoreWait bounds how long a restored older helper may take to serve.
var helperRestoreWait = 15 * time.Second

// restoreHelper starts executable as the helper of stateDir with only
// HelperCommand --state-dir, which every protocol accepts, and waits until a
// helper of protocol serves. It reports whether that helper is the process
// it started rather than one another client started meanwhile.
func restoreHelper(ctx context.Context, stateDir, executable string, protocol int) (bool, error) {
	process, err := spawnDetached(executable, []string{HelperCommand, "--state-dir", stateDir}, filepath.Join(stateDir, "helper.log"))
	if err != nil {
		return false, err
	}
	deadline := time.Now().Add(helperRestoreWait)
	for {
		running, err := inspectHelperVersion(ctx, stateDir, protocol)
		if err == nil {
			return running.PID == process.pid, nil
		}
		if time.Now().After(deadline) {
			process.kill()
			return false, fmt.Errorf("the restored helper did not serve within %s: %w", helperRestoreWait, err)
		}
		select {
		case <-ctx.Done():
			return false, context.Cause(ctx)
		case <-process.done:
			// It exited; another helper may still have taken the state.
			if running, err := inspectHelperVersion(ctx, stateDir, protocol); err == nil {
				return running.PID == process.pid, nil
			}
			return false, fmt.Errorf("the restored helper exited: %v", process.exitErr())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// ReplaceLegacyHelper asks the side that runs the helpers to end the older
// helper named by token and continue on this build's helper. Over a bridge
// (SSH, a device bridge) the bridge on that host verifies and stops the
// helper; for a local client this process does. It returns once this build's
// helper serves, a *LegacyHelperError when the consent is stale or an older
// helper still serves, or an error matching ErrHelperReplaceFailed.
func (c *Client) ReplaceLegacyHelper(ctx context.Context, token string) (ReplaceResult, error) {
	if c.dial == nil {
		_, result, err := ReplaceLegacyHelper(ctx, HelperConfig{StateDir: c.stateDir}, token)
		return result, err
	}
	netConn, err := c.dial(ctx)
	if err != nil {
		return ReplaceResult{}, fmt.Errorf("%w: dial supervisor: %v", ErrUnavailable, err)
	}
	conn := &clientConn{conn: netConn}
	defer func() { _ = conn.Close() }()
	kind, payload, err := conn.request(ctx, frameHello, helloMsg{Version: ProtocolVersion, StateDigest: c.stateDigest, ReplaceLegacy: token})
	if err != nil {
		return ReplaceResult{}, err
	}
	switch kind {
	case frameOK:
		// The bridge replaced the older helper and reports how.
		var replaced replacedMsg
		if err := unmarshalFrame(payload, &replaced); err != nil {
			return ReplaceResult{}, err
		}
		return ReplaceResult{Stopped: replaced.Stopped, Forced: replaced.Forced}, nil
	case frameHelloAck:
		// This build's helper already serves: nothing was left to replace.
		var ack helloMsg
		if err := unmarshalFrame(payload, &ack); err != nil {
			return ReplaceResult{}, err
		}
		if ack.Version != ProtocolVersion {
			return ReplaceResult{}, versionMismatchError(ack.Version, "hello acknowledged with another protocol version")
		}
		return ReplaceResult{}, nil
	default:
		return ReplaceResult{}, protocolMismatch(kind)
	}
}

// replacedMsg answers a replace_legacy hello once this build's helper serves.
type replacedMsg struct {
	Stopped bool `json:"stopped,omitempty"`
	Forced  bool `json:"forced,omitempty"`
}
