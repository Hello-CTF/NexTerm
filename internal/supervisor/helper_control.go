package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// RunningHelper describes the helper process that serves a state directory,
// whichever protocol it speaks. It is read through the helper's own endpoint,
// so the counts include sessions other clients created.
type RunningHelper struct {
	// Protocol is the protocol version the helper speaks.
	Protocol int
	// PID is the process serving the endpoint, taken from the connection's
	// peer credentials and checked against the helper's pid file.
	PID      int
	Activity ActivitySnapshot
	// ActivityKnown is false for a protocol 2 helper that did not report its
	// sessions: an unknown count is not zero sessions.
	ActivityKnown bool
	// Runtime is nil when the helper did not report the executable it runs.
	Runtime *RuntimeMetadata
}

// Current reports whether the helper speaks this build's protocol.
func (h RunningHelper) Current() bool {
	return h.Protocol == ProtocolVersion
}

// ErrHelperChanged reports that the helper serving a state directory is no
// longer the one a caller inspected, so an action approved for it is refused.
var ErrHelperChanged = fmt.Errorf("%w: the supervisor helper changed since it was inspected", ErrStateMismatch)

const (
	helperStopGrace   = helperShutdownTimeout + 5*time.Second
	helperKillWait    = 5 * time.Second
	helperUnlockWait  = 10 * time.Second
	helperInspectWait = 5 * time.Second
)

// probeHelloMsg is a hello understood by protocol 2 and 3 helpers: protocol 2
// returns runtime metadata and activity only for the features it was offered.
type probeHelloMsg struct {
	Version     int               `json:"version"`
	StateDigest string            `json:"state_digest,omitempty"`
	Features    []string          `json:"features,omitempty"`
	Generation  string            `json:"generation,omitempty"`
	Runtime     *RuntimeMetadata  `json:"runtime,omitempty"`
	Activity    *ActivitySnapshot `json:"activity,omitempty"`
}

// InspectHelper reports the helper that serves stateDir on this build's
// protocol or an older one. It changes nothing: no session is attached and no
// process is signalled. ErrHelperNotRunning means no helper listens; any
// other error means one may run but could not be inspected.
func InspectHelper(ctx context.Context, stateDir string) (RunningHelper, error) {
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return RunningHelper{}, fmt.Errorf("%w: helper state directory: %v", ErrInvalidInput, err)
	}
	var errs []error
	listening := false
	for version := ProtocolVersion; version >= ProtocolVersion-1 && version >= 1; version-- {
		helper, err := inspectHelperVersion(ctx, absolute, version)
		if err == nil {
			return helper, nil
		}
		if ctx.Err() != nil {
			return RunningHelper{}, context.Cause(ctx)
		}
		if !errors.Is(err, errNoListener) {
			listening = true
		}
		errs = append(errs, err)
	}
	if !listening {
		return RunningHelper{}, fmt.Errorf("%w: %s: %w", ErrHelperNotRunning, absolute, errors.Join(errs...))
	}
	// Something answers but cannot be inspected (a hello that times out, a
	// pid that does not match helper.pid, a protocol error): that is not the
	// same as no helper, so the caller must not treat it as one.
	return RunningHelper{}, fmt.Errorf("%w: the supervisor helper serving %s cannot be inspected: %w", ErrUnavailable, absolute, errors.Join(errs...))
}

// ErrHelperNotRunning reports that no helper listens on any endpoint of a
// state directory: no endpoint exists or the connection is refused.
var ErrHelperNotRunning = fmt.Errorf("%w: no supervisor helper is running", ErrUnavailable)

// errNoListener marks a dial that found no helper listening.
var errNoListener = errors.New("no helper listens on the endpoint")

func noListener(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)
}

func inspectHelperVersion(ctx context.Context, stateDir string, version int) (RunningHelper, error) {
	helper, conn, err := inspectHelperConn(ctx, stateDir, version)
	if err != nil {
		return RunningHelper{}, err
	}
	if version < 3 {
		_ = writeDetach(conn)
	}
	_ = conn.Close()
	return helper, nil
}

// inspectHelperConn inspects the helper serving the endpoint of version and
// returns the verified connection still open: while it is open the peer
// process is alive, so its pid cannot name another process yet.
func inspectHelperConn(ctx context.Context, stateDir string, version int) (_ RunningHelper, _ net.Conn, returnErr error) {
	endpoint, err := helperEndpointVersion(stateDir, version)
	if err != nil {
		return RunningHelper{}, nil, err
	}
	digest, err := stateDigestFor(stateDir)
	if err != nil {
		return RunningHelper{}, nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, helperInspectWait)
	defer cancel()
	conn, err := dialSocket(probeCtx, endpoint)
	if err != nil {
		if noListener(err) {
			return RunningHelper{}, nil, fmt.Errorf("%w: %v", errNoListener, err)
		}
		return RunningHelper{}, nil, err
	}
	defer func() {
		if returnErr != nil {
			_ = conn.Close()
		}
	}()
	pid, err := verifiedHelperPeer(conn)
	if err != nil {
		return RunningHelper{}, nil, err
	}
	if recorded, err := recordedHelperPID(stateDir); err == nil && recorded != pid {
		return RunningHelper{}, nil, fmt.Errorf("%w: endpoint %s is served by pid %d but the helper pid file names %d", ErrStateMismatch, endpoint, pid, recorded)
	}
	if deadline, ok := probeCtx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(probeCtx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	hello := probeHelloMsg{Version: version, StateDigest: digest}
	if version < 3 {
		hello.Features = []string{"runtime_metadata", "activity"}
	}
	payload, err := marshalFrame(frameHello, hello)
	if err != nil {
		return RunningHelper{}, nil, err
	}
	if err := writeFrame(conn, frameHello, payload); err != nil {
		return RunningHelper{}, nil, err
	}
	kind, reply, err := readFrame(conn)
	if err != nil {
		return RunningHelper{}, nil, err
	}
	if kind == frameError {
		var msg errorMsg
		if err := unmarshalFrame(reply, &msg); err != nil {
			return RunningHelper{}, nil, err
		}
		return RunningHelper{}, nil, decodedError(msg)
	}
	if kind != frameHelloAck {
		return RunningHelper{}, nil, protocolMismatch(kind)
	}
	var ack probeHelloMsg
	if err := unmarshalFrame(reply, &ack); err != nil {
		return RunningHelper{}, nil, err
	}
	if ack.Version != version {
		return RunningHelper{}, nil, versionMismatchError(ack.Version, "inspection hello acknowledged with another protocol version")
	}
	helper := RunningHelper{Protocol: version, PID: pid}
	if ack.Activity != nil {
		helper.ActivityKnown = true
		if ack.Activity.PTY < 0 || ack.Activity.Exec < 0 {
			return RunningHelper{}, nil, fmt.Errorf("%w: negative daemon activity count", ErrProtocol)
		}
		helper.Activity = *ack.Activity
	} else if version >= 3 {
		return RunningHelper{}, nil, fmt.Errorf("%w: hello acknowledgement lacks daemon activity", ErrProtocol)
	}
	if ack.Runtime != nil && validateRuntimeMetadata(*ack.Runtime, version) == nil {
		runtime := *ack.Runtime
		helper.Runtime = &runtime
	}
	_ = conn.SetDeadline(time.Time{})
	return helper, conn, nil
}

func writeDetach(conn net.Conn) error {
	payload, err := marshalFrame(frameDetach, struct{}{})
	if err != nil {
		return err
	}
	return writeFrame(conn, frameDetach, payload)
}

func recordedHelperPID(stateDir string) (int, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, "helper.pid"))
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("%w: helper pid file is malformed", ErrStateMismatch)
	}
	return pid, nil
}

// StopHelper ends the helper inspected as expected, and with it every session
// it runs. Callers must have the user's consent for exactly that helper: a
// different process or protocol now serving the state directory, or more
// sessions than consent covered, is refused with ErrHelperChanged. The
// process is held by a handle taken while the verified connection is open
// (a pidfd on Linux, a process handle on Windows), so a reused pid is never
// signalled; where no such handle exists the helper is verified again before
// it is forcibly killed, and left alone when that is impossible. StopHelper
// returns once the state lock is released, so a new helper can start.
func StopHelper(ctx context.Context, stateDir string, expected RunningHelper) error {
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return fmt.Errorf("%w: helper state directory: %v", ErrInvalidInput, err)
	}
	current, conn, err := inspectHelperConn(ctx, absolute, expected.Protocol)
	if err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return fmt.Errorf("%w: %v", ErrHelperChanged, err)
	}
	connOpen := true
	closeConn := func() {
		if connOpen {
			_ = conn.Close()
			connOpen = false
		}
	}
	defer closeConn()
	if current.PID != expected.PID || expected.PID <= 0 {
		return fmt.Errorf("%w: pid %d now serves the state directory, consent covered pid %d", ErrHelperChanged, current.PID, expected.PID)
	}
	if expected.ActivityKnown && (!current.ActivityKnown || current.Activity.Total() > expected.Activity.Total()) {
		return fmt.Errorf("%w: the helper now runs %d sessions, consent covered %d", ErrHelperChanged, current.Activity.Total(), expected.Activity.Total())
	}
	reverify := func(ctx context.Context) (int, net.Conn, error) {
		helper, conn, err := inspectHelperConn(ctx, absolute, expected.Protocol)
		if err != nil {
			return 0, nil, err
		}
		return helper.PID, conn, nil
	}
	target, err := openHelperTarget(current.PID, reverify)
	if err != nil {
		return fmt.Errorf("hold supervisor helper %d: %w", current.PID, err)
	}
	defer target.close()
	if err := target.terminate(); err != nil {
		return fmt.Errorf("stop supervisor helper %d: %w", current.PID, err)
	}
	closeConn()
	if !target.wait(ctx, helperStopGrace) {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if err := target.kill(ctx); err != nil {
			return fmt.Errorf("kill supervisor helper %d: %w", current.PID, err)
		}
		if !target.wait(ctx, helperKillWait) {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			return fmt.Errorf("%w: supervisor helper %d did not exit", ErrUnavailable, current.PID)
		}
	}
	return waitHelperUnlocked(ctx, absolute, helperUnlockWait)
}

// helperTarget is a stoppable handle on one verified helper process.
type helperTarget interface {
	// terminate asks the helper to stop; it is called while the verified
	// connection is open.
	terminate() error
	// wait reports whether the helper exited within limit.
	wait(ctx context.Context, limit time.Duration) bool
	// kill forces the helper to exit, only when it is provably still the
	// inspected process.
	kill(ctx context.Context) error
	close()
}

// helperReverify dials the helper endpoint again and returns the pid serving
// it with the verified connection open.
type helperReverify func(ctx context.Context) (int, net.Conn, error)

func waitHelperUnlocked(ctx context.Context, stateDir string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		unlock, err := lockHelperState(stateDir)
		if err == nil {
			unlock()
			return nil
		}
		if !errors.Is(err, ErrAlreadyExists) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: the supervisor helper state lock was not released", ErrUnavailable)
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
