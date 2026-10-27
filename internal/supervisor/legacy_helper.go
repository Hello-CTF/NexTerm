package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"time"
)

const legacyHelperProbeTimeout = time.Second

// legacyBridgeReplaceTimeout bounds a consented replacement run by a bridge:
// stopping the old helper (TERM, grace, KILL, lock release) and starting this
// build's helper.
const legacyBridgeReplaceTimeout = 3 * time.Minute

// legacyHelperVersion reports the protocol of an older helper that still
// serves stateDir on its own versioned endpoint. Such a helper holds the state
// directory lock, so a helper of this protocol cannot start until it exits,
// and its sessions are invisible to this client. Nothing here stops it.
func legacyHelperVersion(ctx context.Context, stateDir string) (int, bool) {
	for version := ProtocolVersion - 1; version >= 1; version-- {
		endpoint, err := helperEndpointVersion(stateDir, version)
		if err != nil {
			continue
		}
		dialCtx, cancel := context.WithTimeout(ctx, legacyHelperProbeTimeout)
		conn, err := dialSocket(dialCtx, endpoint)
		cancel()
		if err != nil {
			continue
		}
		_ = conn.Close()
		return version, true
	}
	return 0, false
}

// legacyHelperError reports an older helper of protocol version that still
// owns the state directory while this side speaks the current protocol.
func legacyHelperError(version int, message string) error {
	return fmt.Errorf("%w: a protocol %d helper is still running and this client requires protocol %d (%s)", ErrLegacyHelperRunning, version, ProtocolVersion, message)
}

func legacyHelperMessage(stateDir string, version int) string {
	return fmt.Sprintf("a protocol %d helper still serves %s; its sessions keep running there, and a protocol %d helper can start only after it exits", version, stateDir, ProtocolVersion)
}

// LegacyHelperError is ErrLegacyHelperRunning with what the user must see
// before agreeing to end the older helper: the helper inspected on the host
// that runs it (owner and pid verified) and its session count, read with a
// protocol 2 compatible probe, plus the consent token naming exactly that
// helper and count. ConsentToken is empty when the helper answers its
// endpoint but could not be inspected; then nothing can be approved.
type LegacyHelperError struct {
	Helper       RunningHelper
	ConsentToken string
	// Stale is set when a replacement was requested with a token that no
	// longer names the running helper; Helper and ConsentToken are fresh.
	Stale   bool
	message string
}

func (e *LegacyHelperError) Error() string {
	return legacyHelperError(e.Helper.Protocol, e.message).Error()
}

func (e *LegacyHelperError) Unwrap() error { return ErrLegacyHelperRunning }

// legacyHelperDetail inspects the older helper serving stateDir on version.
func legacyHelperDetail(ctx context.Context, stateDir string, version int) *LegacyHelperError {
	detail := &LegacyHelperError{Helper: RunningHelper{Protocol: version}, message: legacyHelperMessage(stateDir, version)}
	running, err := inspectHelperVersion(ctx, stateDir, version)
	if err != nil {
		detail.message += "; it cannot be inspected: " + err.Error()
		return detail
	}
	detail.Helper = running
	detail.ConsentToken = HelperConsentToken(running)
	return detail
}

// legacyErrorWire is the legacy helper detail carried by a legacy_helper_running
// error frame.
type legacyErrorWire struct {
	PID           int    `json:"pid,omitempty"`
	PTY           int    `json:"pty"`
	Exec          int    `json:"exec"`
	ActivityKnown bool   `json:"activity_known,omitempty"`
	PTYListed     bool   `json:"pty_listed,omitempty"`
	Release       string `json:"release,omitempty"`
	ConsentToken  string `json:"consent_token,omitempty"`
	Stale         bool   `json:"stale,omitempty"`
}

func legacyErrorMessage(detail *LegacyHelperError) errorMsg {
	msg := errorMsg{Code: codeLegacyHelper, Message: detail.message, Version: detail.Helper.Protocol}
	wire := &legacyErrorWire{
		PID: detail.Helper.PID, PTY: detail.Helper.Activity.PTY, Exec: detail.Helper.Activity.Exec,
		ActivityKnown: detail.Helper.ActivityKnown, PTYListed: detail.Helper.PTYListed,
		ConsentToken: detail.ConsentToken, Stale: detail.Stale,
	}
	if detail.Helper.Runtime != nil {
		wire.Release = detail.Helper.Runtime.Version
	}
	msg.Legacy = wire
	return msg
}

func decodedLegacyError(msg errorMsg) error {
	detail := &LegacyHelperError{Helper: RunningHelper{Protocol: msg.Version}, message: msg.Message}
	if wire := msg.Legacy; wire != nil && wire.PTY >= 0 && wire.Exec >= 0 {
		detail.Helper.PID = wire.PID
		detail.Helper.Activity = ActivitySnapshot{PTY: wire.PTY, Exec: wire.Exec}
		detail.Helper.ActivityKnown = wire.ActivityKnown
		detail.Helper.PTYListed = wire.PTYListed && !wire.ActivityKnown
		if wire.Release != "" {
			detail.Helper.Runtime = &RuntimeMetadata{Version: wire.Release, Protocol: msg.Version}
		}
		detail.ConsentToken = wire.ConsentToken
		detail.Stale = wire.Stale
		// The token carries the process start time and state digest.
		if parsed, ok := ParseHelperConsent(wire.ConsentToken); ok {
			detail.Helper.StartTime = parsed.StartTime
			detail.Helper.StateDigest = parsed.StateDigest
		}
	}
	return detail
}

// legacyHelperError explains a failed dial of this client's own helper
// endpoint when an older helper owns the state directory, so the caller gets
// an upgrade error with the older helper's sessions instead of a bare
// connection failure.
func (c *Client) legacyHelperError(ctx context.Context) error {
	if c.dial != nil || c.stateDir == "" {
		return nil
	}
	stateDir, err := filepath.Abs(c.stateDir)
	if err != nil {
		return nil
	}
	endpoint, err := helperEndpoint(stateDir)
	if err != nil || endpoint != c.socketPath {
		return nil
	}
	version, ok := legacyHelperVersion(ctx, stateDir)
	if !ok {
		return nil
	}
	return legacyHelperDetail(ctx, stateDir, version)
}

// legacyBridgeConn stands in for the helper connection of a bridge whose
// state directory is owned by an older helper. A plain hello is answered with
// the legacy rejection and the older helper's sessions, so the client reports
// a helper upgrade at once instead of a bare EOF or a timeout. A hello that
// carries the consent token of that rejection replaces the older helper (see
// ReplaceLegacyHelper) and, once this build's helper serves, is answered
// with frameOK and how the older helper ended; the client then connects as
// usual. The bridge never stops
// a helper on its own.
func legacyBridgeConn(stateDir string, version int) net.Conn {
	client, server := net.Pipe()
	go serveLegacyBridge(server, stateDir, version)
	return client
}

func serveLegacyBridge(server net.Conn, stateDir string, version int) {
	kind, payload, err := readFrame(server)
	if err != nil {
		_ = server.Close()
		return
	}
	reject := func(msg errorMsg) {
		if encoded, err := marshalFrame(frameError, msg); err == nil {
			_ = writeFrame(server, frameError, encoded)
		}
		_ = server.Close()
	}
	var hello helloMsg
	if kind != frameHello || unmarshalFrame(payload, &hello) != nil || hello.ReplaceLegacy == "" {
		ctx, cancel := context.WithTimeout(context.Background(), helperInspectWait)
		detail := legacyHelperDetail(ctx, stateDir, version)
		cancel()
		reject(legacyErrorMessage(detail))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), legacyBridgeReplaceTimeout)
	defer cancel()
	_, result, err := ReplaceLegacyHelper(ctx, HelperConfig{StateDir: stateDir}, hello.ReplaceLegacy)
	if err != nil {
		reject(replaceErrorMessage(err))
		return
	}
	if encoded, err := marshalFrame(frameOK, replacedMsg{Stopped: result.Stopped, Forced: result.Forced}); err == nil {
		_ = writeFrame(server, frameOK, encoded)
	}
	_ = server.Close()
}

func replaceErrorMessage(err error) errorMsg {
	var legacy *LegacyHelperError
	if errors.As(err, &legacy) {
		return legacyErrorMessage(legacy)
	}
	var failed *HelperReplaceError
	if errors.As(err, &failed) {
		message := err.Error()
		if failed.Cause != nil {
			message = failed.Cause.Error()
		}
		return errorMsg{Code: codeReplaceFailed, Message: message, Stage: string(failed.Stage), Stopped: failed.Stopped, Forced: failed.Forced, RolledBack: failed.RolledBack}
	}
	return errorMessage(err)
}
