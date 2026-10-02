// Package terminalgrid coordinates terminal viewport intentions with the grid
// known to be active on a real transport.
//
// A Model keeps viewport pixels, the local desired grid, the committed grid and
// the last known-good grid as distinct state. A zero Grid means unknown; the
// package never substitutes a default terminal size. Committed state changes
// only after a ResizeFunc reports success or an observer accepts an
// authoritative remote result. Reattach clears committed state while retaining
// the last known-good value.
//
// Controller mode schedules local intent. Observer mode accepts authoritative
// grids but never invokes the transport. Hidden mode also suppresses transport
// work and preserves the last valid desired grid across zero-sized and sub-cell
// viewport measurements. Claim and transitions back to controller mode force a
// final synchronization; Flush provides the pointer-up synchronization boundary.
//
// Each model has at most one in-flight request and one pending request. A newer
// intent replaces the pending request. Local revision numbers increase for
// every new intent. Observer revisions form a separate authoritative sequence
// and are filtered independently. All methods are safe for concurrent use.
//
// ResizeFunc is the transport boundary for later adapters. It must return nil
// only after the peer has applied the requested grid, and it must honor context
// cancellation. The package deliberately does not update a DOM, terminal
// emulator, session or transport implementation itself.
package terminalgrid
