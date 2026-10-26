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
// process is signalled. ErrUnavailable means no helper answers.
func InspectHelper(ctx context.Context, stateDir string) (RunningHelper, error) {
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return RunningHelper{}, fmt.Errorf("%w: helper state directory: %v", ErrInvalidInput, err)
	}
	var errs []error
	for version := ProtocolVersion; version >= ProtocolVersion-1 && version >= 1; version-- {
		helper, err := inspectHelperVersion(ctx, absolute, version)
		if err == nil {
			return helper, nil
		}
		if ctx.Err() != nil {
			return RunningHelper{}, context.Cause(ctx)
		}
		errs = append(errs, err)
	}
	return RunningHelper{}, fmt.Errorf("%w: no supervisor helper serves %s: %w", ErrUnavailable, absolute, errors.Join(errs...))
}

func inspectHelperVersion(ctx context.Context, stateDir string, version int) (RunningHelper, error) {
	endpoint, err := helperEndpointVersion(stateDir, version)
	if err != nil {
		return RunningHelper{}, err
	}
	digest, err := stateDigestFor(stateDir)
	if err != nil {
		return RunningHelper{}, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, helperInspectWait)
	defer cancel()
	conn, err := dialSocket(probeCtx, endpoint)
	if err != nil {
		return RunningHelper{}, err
	}
	defer func() { _ = conn.Close() }()
	pid, err := verifiedHelperPeer(conn)
	if err != nil {
		return RunningHelper{}, err
	}
	if recorded, err := recordedHelperPID(stateDir); err == nil && recorded != pid {
		return RunningHelper{}, fmt.Errorf("%w: endpoint %s is served by pid %d but the helper pid file names %d", ErrStateMismatch, endpoint, pid, recorded)
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
		return RunningHelper{}, err
	}
	if err := writeFrame(conn, frameHello, payload); err != nil {
		return RunningHelper{}, err
	}
	kind, reply, err := readFrame(conn)
	if err != nil {
		return RunningHelper{}, err
	}
	if kind == frameError {
		var msg errorMsg
		if err := unmarshalFrame(reply, &msg); err != nil {
			return RunningHelper{}, err
		}
		return RunningHelper{}, decodedError(msg)
	}
	if kind != frameHelloAck {
		return RunningHelper{}, protocolMismatch(kind)
	}
	var ack probeHelloMsg
	if err := unmarshalFrame(reply, &ack); err != nil {
		return RunningHelper{}, err
	}
	if ack.Version != version {
		return RunningHelper{}, versionMismatchError(ack.Version, "inspection hello acknowledged with another protocol version")
	}
	helper := RunningHelper{Protocol: version, PID: pid}
	if ack.Activity != nil {
		if ack.Activity.PTY < 0 || ack.Activity.Exec < 0 {
			return RunningHelper{}, fmt.Errorf("%w: negative daemon activity count", ErrProtocol)
		}
		helper.Activity = *ack.Activity
	} else if version >= 3 {
		return RunningHelper{}, fmt.Errorf("%w: hello acknowledgement lacks daemon activity", ErrProtocol)
	}
	if ack.Runtime != nil && validateRuntimeMetadata(*ack.Runtime, version) == nil {
		runtime := *ack.Runtime
		helper.Runtime = &runtime
	}
	if version < 3 {
		// Protocol 2 offered activity as a feature; a helper without it cannot
		// report its sessions, which is not the same as having none.
		_ = writeDetach(conn)
	}
	return helper, nil
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
// different process or protocol now serving the state directory is refused
// with ErrHelperChanged. The helper gets a graceful stop first, then a forced
// one, and StopHelper returns once its state lock is released, so a new
// helper can start.
func StopHelper(ctx context.Context, stateDir string, expected RunningHelper) error {
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return fmt.Errorf("%w: helper state directory: %v", ErrInvalidInput, err)
	}
	current, err := inspectHelperVersion(ctx, absolute, expected.Protocol)
	if err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return fmt.Errorf("%w: %v", ErrHelperChanged, err)
	}
	if current.PID != expected.PID || expected.PID <= 0 {
		return fmt.Errorf("%w: pid %d now serves the state directory, consent covered pid %d", ErrHelperChanged, current.PID, expected.PID)
	}
	if current.Activity.Total() > expected.Activity.Total() {
		return fmt.Errorf("%w: the helper now runs %d sessions, consent covered %d", ErrHelperChanged, current.Activity.Total(), expected.Activity.Total())
	}
	if err := stopHelperProcess(current.PID); err != nil {
		return fmt.Errorf("stop supervisor helper %d: %w", current.PID, err)
	}
	if !waitHelperExit(ctx, current.PID, helperStopGrace) {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if err := killHelperProcess(current.PID); err != nil {
			return fmt.Errorf("kill supervisor helper %d: %w", current.PID, err)
		}
		if !waitHelperExit(ctx, current.PID, helperKillWait) {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			return fmt.Errorf("%w: supervisor helper %d did not exit", ErrUnavailable, current.PID)
		}
	}
	return waitHelperUnlocked(ctx, absolute, helperUnlockWait)
}

func waitHelperExit(ctx context.Context, pid int, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for {
		if !processAlive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

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
