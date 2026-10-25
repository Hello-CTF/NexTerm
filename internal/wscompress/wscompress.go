// Package wscompress centralises the permessage-deflate policy of every NexTerm WebSocket.
//
// Context takeover keeps a ~0.8 MB flate writer alive for the lifetime of any connection that ever sends a frame
// above the threshold, while saving almost nothing on top of per-message compression because the busy channels
// already send 32-64 KiB frames. Every purpose therefore uses no-context-takeover (the writer is pooled and only
// held while a message is being compressed) with a purpose-specific threshold, and opaque tunnelled traffic is not
// compressed at all.
package wscompress

import (
	"os"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

// Purpose describes what a WebSocket carries.
type Purpose int

const (
	// Events carries small JSON events.
	Events Purpose = iota
	// Terminal carries terminal output and input.
	Terminal
	// File carries file transfer frames.
	File
	// Control is the mostly idle agent control channel (tiny JSON).
	Control
	// Device is the shared device endpoint that serves both control channels and bridges.
	Device
	// Opaque is tunnelled traffic that is usually already encrypted or compressed.
	Opaque
)

// EnvMode overrides the policy: "takeover", "nocontext" or "off".
const EnvMode = "NEXTERM_WS_COMPRESSION"

type override int

const (
	overrideNone override = iota
	overrideTakeover
	overrideNoContext
	overrideOff
)

var envOverride = sync.OnceValue(func() override { return parseOverride(os.Getenv(EnvMode)) })

func parseOverride(value string) override {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "takeover":
		return overrideTakeover
	case "nocontext", "no-context", "no_context":
		return overrideNoContext
	case "off", "disabled", "none":
		return overrideOff
	}
	return overrideNone
}

// Setting is the compression configuration for one connection.
type Setting struct {
	Mode      websocket.CompressionMode
	Threshold int
}

func defaultSetting(p Purpose) Setting {
	switch p {
	case Events:
		return Setting{websocket.CompressionNoContextTakeover, 1024}
	case Terminal, File:
		return Setting{websocket.CompressionNoContextTakeover, 512}
	case Control:
		return Setting{websocket.CompressionNoContextTakeover, 4096}
	case Device:
		return Setting{websocket.CompressionNoContextTakeover, 1024}
	default:
		return Setting{Mode: websocket.CompressionDisabled}
	}
}

func apply(p Purpose, o override) Setting {
	setting := defaultSetting(p)
	if setting.Mode == websocket.CompressionDisabled {
		return setting
	}
	switch o {
	case overrideTakeover:
		setting.Mode = websocket.CompressionContextTakeover
	case overrideNoContext:
		setting.Mode = websocket.CompressionNoContextTakeover
	case overrideOff:
		setting.Mode = websocket.CompressionDisabled
	}
	return setting
}

// For returns the setting of a purpose, honouring NEXTERM_WS_COMPRESSION (read once).
func For(p Purpose) Setting { return apply(p, envOverride()) }

// Accept fills the compression fields of a server-side AcceptOptions.
// Browsers and older peers that offer context takeover are still accepted: the server answers with the
// conservative no-context-takeover parameters and the peer falls back accordingly.
func Accept(p Purpose, options *websocket.AcceptOptions) *websocket.AcceptOptions {
	if options == nil {
		options = &websocket.AcceptOptions{}
	}
	setting := For(p)
	options.CompressionMode = setting.Mode
	options.CompressionThreshold = setting.Threshold
	return options
}

// Dial fills the compression fields of a client-side DialOptions.
func Dial(p Purpose, options *websocket.DialOptions) *websocket.DialOptions {
	if options == nil {
		options = &websocket.DialOptions{}
	}
	setting := For(p)
	options.CompressionMode = setting.Mode
	options.CompressionThreshold = setting.Threshold
	return options
}

// ForBridge maps a device bridge purpose ("" and "terminal" are terminal streams) to a policy: terminal and file
// streams are text-like, while forward, listen and accept tunnel opaque traffic that is usually already encrypted
// or compressed, where deflate only burns CPU.
func ForBridge(purpose string) Purpose {
	switch purpose {
	case "", "terminal":
		return Terminal
	case "file":
		return File
	default:
		return Opaque
	}
}
