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
package agent

import "errors"

const (
	// ProtocolVersion is the device control channel protocol spoken by this
	// agent. The server rejects mismatches with a version_mismatch error.
	ProtocolVersion = 1

	configVersion = 1

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
