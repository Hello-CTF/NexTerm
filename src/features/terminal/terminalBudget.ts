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

// Background (hidden) pane drains. A drain runs every interval on timers, not
// requestAnimationFrame: a hidden window throttles rAF to a stop, which would
// strand the backlog. The drain rate must stay well above what busy hidden
// programs produce: a hidden pane that falls behind crosses the high
// watermark, pauses upstream, and ends up blocking the program's PTY reads.
export const BACKGROUND_DRAIN_INTERVAL_MS = 16;

// Default tier, used while the foreground is idle: up to 4 x 64 KiB per drain
// across all hidden panes (about 15.6 MiB/s). A single pane may use all four
// slots: with only two in flight its ceiling would be 2 x 64 KiB per 16 ms,
// about 7.8 MiB/s, below the 8 MiB/s a busy build or `cat` easily sustains.
export const BACKGROUND_CHUNK_BYTES = 64 * 1024;
export const BACKGROUND_MAX_WRITES_PER_DRAIN = 4;
export const BACKGROUND_MAX_IN_FLIGHT_SLICES = 4;

// Conservative tier, used only while a visible pane had output or user input
// within FOREGROUND_ACTIVE_WINDOW_MS: hidden panes then yield the main thread
// to the pane the user is looking at.
export const BUSY_FOREGROUND_CHUNK_BYTES = 16 * 1024;
export const BUSY_FOREGROUND_MAX_WRITES_PER_DRAIN = 2;
export const BUSY_FOREGROUND_MAX_IN_FLIGHT_SLICES = 1;
export const FOREGROUND_ACTIVE_WINDOW_MS = 150;
