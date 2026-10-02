// Package ipc owns the transport-neutral command, event, and stream contracts.
// Business modules register commands once; Wails and HTTP/WS adapters supply
// Environment implementations without changing business handlers.
package ipc
