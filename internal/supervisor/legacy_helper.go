package supervisor

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"time"
)

const legacyHelperProbeTimeout = time.Second

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

// legacyHelperError explains a failed dial of this client's own helper
// endpoint when an older helper owns the state directory, so the caller gets
// an upgrade error instead of a bare connection failure.
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
	return legacyHelperError(version, legacyHelperMessage(stateDir, version))
}

// legacyRejectionConn stands in for the helper connection of a bridge whose
// state directory is owned by an older helper. It answers the client's hello
// with a version rejection, which the client reports as a helper upgrade,
// instead of letting the bridge end with a bare EOF.
func legacyRejectionConn(stateDir string, version int) net.Conn {
	client, server := net.Pipe()
	go func() {
		defer func() { _ = server.Close() }()
		if _, _, err := readFrame(server); err != nil {
			return
		}
		payload, err := marshalFrame(frameError, errorMsg{Code: codeLegacyHelper, Message: legacyHelperMessage(stateDir, version), Version: version})
		if err != nil {
			return
		}
		_ = writeFrame(server, frameError, payload)
	}()
	return client
}
