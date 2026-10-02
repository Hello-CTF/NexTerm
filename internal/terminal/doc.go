// Package terminal implements the terminal engine core: raw-byte scrollback,
// a VT screen state machine, incremental transcoding, key encoding, recording,
// and ordered fan-out with cancellation-aware backpressure.
//
// Data flow (strict order): transport read -> Output.Chunk ->
//  1. Tab.Feed: raw bytes into the Ring (untouched), decoded bytes into the
//     VT screen and the mode tracker.
//  2. Recording of the same raw bytes, when enabled.
//  3. Fanout to subscribed clients (immediately when visible, batched when
//     hidden).
//
// The raw Ring is the source of truth for replay/dump/export; the VT screen
// is the source of truth for screen reads (AI takeover, idle detection).
// Transcoding never alters the stored raw bytes.
//
// The VT screen is implemented in-package on top of the
// charmbracelet/x/ansi parser (see screen.go for why the full x/vt
// emulator was rejected: throughput below the acceptance budget and
// Write-boundary grapheme flushing). Runes are processed incrementally
// with vt100-style cells, so the screen, cursor and history are identical
// for any Feed/chunk packetization, including split combining marks, ZWJ
// emoji and regional-indicator flags. Terminal query replies (DSR cursor
// reports, device attributes) are generated synchronously during Feed and
// delivered to the response handler in Feed order; ConPTY requires prompt
// cursor reports.
//
// Tabs and Outputs are safe for concurrent use. Output.Close and Tab.Close
// are idempotent and release their goroutines (the hidden-batch flusher).
package terminal
