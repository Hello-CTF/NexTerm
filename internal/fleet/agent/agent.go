// Package agent implements the NexTerm device agent: a same-binary
// enrollment/run/install/status runtime that keeps an outbound-only control
// channel to NexTerm servers, reports host metrics and service state, and
// bridges the local supervisor helper for durable terminal sessions. It opens
// no inbound listener and uses no tmux.
//
// Wire contract consumed by this package (server-side mounting of the agent
// routes is a separate integration slice):
//
//	GET  {base}/healthz            public probe; must answer {"ok":true,"service":"nexterm-server"}
//	POST {base}/device/enroll      {code,name,platform,app_version} -> credentials + fleet config
//	POST {base}/agent/sync         {device_id,secret,sample?,service_state?} -> {desired_autostart,metrics_interval_ms,terminal_enabled}
//	POST {base}/agent/current-url  {device_id,secret,url,reason} -> failover audit
//	GET  {base}/ws/device          control channel; first text message is the hello
//
// Failures use the ipc.Response failure shape {"ok":false,"error":{"code","message"}}.
// Control channel messages are text JSON envelopes; bridge connections carry
// raw supervisor protocol bytes as binary frames after the hello.
// 403/forbidden means revocation and stops the agent.
// A version_mismatch error frame carries the server version in server_version;
// the standalone agent answers it with a self-upgrade from the fixed official
// release source (ReleaseBaseURL) instead of a plain fatal exit.
package agent

import "errors"

const (
	// ProtocolVersion is the device control channel protocol spoken by this
	// agent. The server rejects mismatches with a version_mismatch error.
	ProtocolVersion = 1

	// PurposeTerminal is the implicit bridge purpose: an empty purpose on the
	// wire means a terminal bridge, and agents pre-dating purposes serve only it.
	PurposeTerminal = "terminal"

	// DeviceKindLocalHelper is the enroll kind of a desktop that exposes its
	// local helper sessions to its own account. The server never shares such
	// a device and echoes the kind, so a server that does not know it is
	// detected instead of enrolling an ordinary, shareable device.
	DeviceKindLocalHelper = "local-helper"

	// ConfigVersion is the version of the device config an enrollment writes.
	ConfigVersion = 1
	configVersion = ConfigVersion

	PathHealth     = "/healthz"
	PathEnroll     = "/device/enroll"
	PathSync       = "/agent/sync"
	PathCurrentURL = "/agent/current-url"
	PathDeviceWS   = "/ws/device"

	healthServiceName = "nexterm-server"

	defaultMetricsIntervalMS = 60000
	minMetricsIntervalMS     = 5000
	maxMetricsIntervalMS     = 24 * 3600 * 1000
)

var (
	ErrNotEnrolled        = errors.New("device is not enrolled")
	ErrRevoked            = errors.New("device or credential revoked")
	ErrProtocolMismatch   = errors.New("control channel protocol version mismatch")
	ErrNoEndpoint         = errors.New("no healthy server endpoint")
	ErrMetricsUnsupported = errors.New("host metrics unsupported on this platform")
	ErrServiceUnsupported = errors.New("service installation unsupported on this platform")
	errLockBusy           = errors.New("another agent instance is already running")
)

const (
	ExitOK       = 0
	ExitError    = 1
	ExitUsage    = 2
	ExitRevoked  = 3
	ExitProtocol = 4
)
