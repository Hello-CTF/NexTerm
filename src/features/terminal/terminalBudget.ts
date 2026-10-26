// Terminal output budgets shared by the stream writer and the app-wide output
// scheduler. They are starting points to be tuned against NexTerm's own
// benchmarks and the backend's backpressure parameters; keep them together so
// that tuning happens in one place.

// Stream writer watermarks (see terminalStream.ts). Bytes handed to the
// scheduler count as in flight until xterm's write callback fires, so a
// hidden pane's scheduler backlog is bounded by these as well.
export const STREAM_HIGH_WATER_BYTES = 4 << 20;
export const STREAM_LOW_WATER_BYTES = 1 << 20;
export const STREAM_MAX_PENDING_BYTES = 16 << 20;

// Background (hidden) panes drain in slices of at most this many bytes.
export const BACKGROUND_CHUNK_BYTES = 16 * 1024;
// Delay between background drains. Driven by timers, not requestAnimationFrame:
// a hidden window throttles rAF to a stop, which would strand the backlog.
export const BACKGROUND_DRAIN_INTERVAL_MS = 16;
// Slices handed to xterm per drain, across all background panes.
export const BACKGROUND_MAX_WRITES_PER_DRAIN = 2;
// Wall-clock budget for one drain, across all background panes.
export const BACKGROUND_DRAIN_BUDGET_MS = 8;
// Slices a background pane may have handed to xterm whose write callbacks
// have not fired yet. Gating on the callback keeps xterm's own write buffer
// from accumulating hidden output faster than it parses.
export const BACKGROUND_MAX_IN_FLIGHT_SLICES = 1;
