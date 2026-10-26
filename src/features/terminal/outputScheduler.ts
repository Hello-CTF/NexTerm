import {
  BACKGROUND_CHUNK_BYTES,
  BACKGROUND_DRAIN_INTERVAL_MS,
  BACKGROUND_MAX_IN_FLIGHT_SLICES,
  BACKGROUND_MAX_WRITES_PER_DRAIN,
  BUSY_FOREGROUND_CHUNK_BYTES,
  BUSY_FOREGROUND_MAX_IN_FLIGHT_SLICES,
  BUSY_FOREGROUND_MAX_WRITES_PER_DRAIN,
  FOREGROUND_ACTIVE_WINDOW_MS,
} from "./terminalBudget";

// App-wide terminal output scheduler. Visible (foreground) panes write straight
// into xterm. Hidden (background) panes queue their output here and drain in
// slices on a fixed timer cadence, round-robin across panes, so a flood in
// hidden tabs cannot starve the visible pane's parsing, rendering, and input
// echo. The drain is adaptive: hidden panes get the conservative tier only
// while a visible pane is active (recent output or user input); otherwise they
// drain fast enough not to throttle the programs producing the output. When
// the whole document is hidden there is no foreground to protect, so every
// pane writes straight through.
//
// Work per drain is bounded by bytes, not by a wall-clock budget: term.write
// only appends to xterm's write buffer, which parses later in its own ~12 ms
// slices, so timing the write call would measure nothing. The parse cost
// feeds back through the in-flight gate instead: a pane gets its next slice
// only once xterm's write callback reports an earlier one parsed.
//
// The scheduler never drops, reorders, or duplicates bytes: a pane's units are
// written in FIFO order, and a unit's callback fires exactly once, from
// xterm's write callback for the unit's last byte (xterm parses writes in
// order, so that callback also covers every earlier slice). Bounding a hidden
// pane's backlog is the stream writer's job: queued bytes stay in flight from
// its point of view, so it pauses upstream at the high watermark and refuses
// whole chunks past the hard cap, which the server replays from the accepted
// offset.
//
// While hidden, frontend-side consumers of the output (bell, title, OSC 52
// and 133 events, and xterm's automatic DA/DSR/CPR replies) trail the
// backlog; they cannot run ahead of it without reordering the byte stream.

export type OutputData = string | Uint8Array;

export interface OutputTarget {
  write: (data: OutputData, callback?: () => void) => void;
}

export interface DrainTier {
  chunkBytes: number;
  maxWritesPerDrain: number;
  maxInFlightSlices: number;
}

export interface OutputSchedulerBudget {
  drainIntervalMs: number;
  foregroundActiveMs: number;
  idle: DrainTier;
  busy: DrainTier;
}

export interface OutputSchedulerOptions {
  budget?: Partial<Omit<OutputSchedulerBudget, "idle" | "busy">> & {
    idle?: Partial<DrainTier>;
    busy?: Partial<DrainTier>;
  };
  now?: () => number;
  setTimer?: (callback: () => void, delayMs: number) => unknown;
  clearTimer?: (handle: unknown) => void;
  // Whether the whole document is hidden (window minimised or occluded).
  isDocumentHidden?: () => boolean;
  // Subscribes to document visibility changes; returns nothing (the
  // scheduler lives as long as the app).
  onDocumentVisibilityChange?: (listener: () => void) => void;
}

export interface ScheduledTerminalOutput extends OutputTarget {
  // Reveal drains this pane's backlog into xterm in order before any new data.
  setVisible: (visible: boolean) => void;
  // Bytes queued in the scheduler and not yet handed to xterm.
  queuedBytes: () => number;
  // Drops the queue; callbacks for bytes never handed to xterm do not fire.
  dispose: () => void;
}

export interface OutputSchedulerStats {
  panes: number;
  backgroundBacklogPanes: number;
  queuedBytes: number;
  drainScheduled: boolean;
  foregroundActive: boolean;
}

export interface OutputScheduler {
  register: (target: OutputTarget, options?: { visible?: boolean }) => ScheduledTerminalOutput;
  // Marks the foreground busy (user input in a visible pane).
  noteForegroundActivity: () => void;
  stats: () => OutputSchedulerStats;
}

interface QueuedUnit {
  data: OutputData;
  offset: number;
  callback?: () => void;
}

interface Pane {
  target: OutputTarget;
  visible: boolean;
  disposed: boolean;
  queue: QueuedUnit[];
  queuedBytes: number;
  inFlightSlices: number;
}

// A string slice can split a UTF-16 surrogate pair (only Uint8Array reaches
// the scheduler today); xterm carries a split pair or UTF-8 sequence over to
// the next write either way.
const sliceOf = (data: OutputData, start: number, end: number): OutputData =>
  typeof data === "string" ? data.slice(start, end) : data.subarray(start, end);

const defaultNow = () => (typeof performance !== "undefined" ? performance.now() : Date.now());

const defaultIsDocumentHidden = () => typeof document !== "undefined" && document.visibilityState === "hidden";

const defaultOnDocumentVisibilityChange = (listener: () => void) => {
  if (typeof document !== "undefined") document.addEventListener("visibilitychange", listener);
};

const validTier = (tier: DrainTier) =>
  tier.chunkBytes >= 1 && tier.maxWritesPerDrain >= 1 && tier.maxInFlightSlices >= 1;

export function createOutputScheduler(options: OutputSchedulerOptions = {}): OutputScheduler {
  const budget: OutputSchedulerBudget = {
    drainIntervalMs: options.budget?.drainIntervalMs ?? BACKGROUND_DRAIN_INTERVAL_MS,
    foregroundActiveMs: options.budget?.foregroundActiveMs ?? FOREGROUND_ACTIVE_WINDOW_MS,
    idle: {
      chunkBytes: BACKGROUND_CHUNK_BYTES,
      maxWritesPerDrain: BACKGROUND_MAX_WRITES_PER_DRAIN,
      maxInFlightSlices: BACKGROUND_MAX_IN_FLIGHT_SLICES,
      ...options.budget?.idle,
    },
    busy: {
      chunkBytes: BUSY_FOREGROUND_CHUNK_BYTES,
      maxWritesPerDrain: BUSY_FOREGROUND_MAX_WRITES_PER_DRAIN,
      maxInFlightSlices: BUSY_FOREGROUND_MAX_IN_FLIGHT_SLICES,
      ...options.budget?.busy,
    },
  };
  if (
    !validTier(budget.idle) ||
    !validTier(budget.busy) ||
    !(budget.drainIntervalMs >= 0) ||
    !(budget.foregroundActiveMs >= 0)
  ) {
    throw new RangeError("output scheduler budget must allow at least one write per drain");
  }
  const now = options.now ?? defaultNow;
  const setTimer = options.setTimer ?? ((callback, delayMs) => setTimeout(callback, delayMs));
  const clearTimer = options.clearTimer ?? ((handle) => clearTimeout(handle as ReturnType<typeof setTimeout>));
  const isDocumentHidden = options.isDocumentHidden ?? defaultIsDocumentHidden;

  const panes = new Set<Pane>();
  // Background panes with queued bytes, in round-robin order: a pane that
  // was just served moves to the end.
  const backlog = new Set<Pane>();
  let timer: unknown = null;
  let lastDrainAt = Number.NEGATIVE_INFINITY;
  let lastForegroundAt = Number.NEGATIVE_INFINITY;

  const foregroundActive = () => now() - lastForegroundAt < budget.foregroundActiveMs;
  const currentTier = () => (foregroundActive() ? budget.busy : budget.idle);

  // Drains keep a fixed cadence measured from the previous drain's start, so
  // xterm's parse latency before a write callback does not stretch the cycle.
  const scheduleDrain = () => {
    if (timer !== null || backlog.size === 0) return;
    timer = setTimer(drain, Math.max(0, lastDrainAt + budget.drainIntervalMs - now()));
  };

  const cancelDrain = () => {
    if (timer === null) return;
    clearTimer(timer);
    timer = null;
  };

  // Hands the next slice of a background pane's head unit to xterm.
  const writeSlice = (pane: Pane, tier: DrainTier) => {
    const unit = pane.queue[0];
    const end = Math.min(unit.data.length, unit.offset + tier.chunkBytes);
    const slice = sliceOf(unit.data, unit.offset, end);
    const last = end >= unit.data.length;
    pane.queuedBytes -= end - unit.offset;
    unit.offset = end;
    if (last) pane.queue.shift();
    if (pane.queue.length === 0) backlog.delete(pane);
    pane.inFlightSlices += 1;
    pane.target.write(slice, () => {
      if (pane.disposed) return;
      pane.inFlightSlices -= 1;
      if (last) unit.callback?.();
      // The pane may have room for another slice now.
      if (backlog.has(pane)) scheduleDrain();
    });
  };

  const drain = () => {
    timer = null;
    lastDrainAt = now();
    const tier = currentTier();
    const eligible = (pane: Pane) => pane.inFlightSlices < tier.maxInFlightSlices;
    let writes = 0;
    while (writes < tier.maxWritesPerDrain) {
      let next: Pane | null = null;
      for (const pane of backlog) {
        if (eligible(pane)) {
          next = pane;
          break;
        }
      }
      if (!next) break;
      // Rotate before writing so the pane rejoins the end of the round.
      backlog.delete(next);
      backlog.add(next);
      writeSlice(next, tier);
      writes += 1;
    }
    // Panes still waiting on an in-flight slice reschedule from its callback.
    for (const pane of backlog) {
      if (eligible(pane)) {
        scheduleDrain();
        break;
      }
    }
  };

  // Writes the pane's whole backlog into xterm, in order, ahead of new data.
  // The handoff is at most about the stream writer's high watermark (4 MiB);
  // xterm parses it in its own ~12 ms slices, so the main thread never blocks
  // on it. On reveal the refit happens on the next animation frame, so bytes
  // produced for the old size may parse after the resize and briefly garble a
  // full-screen program until its SIGWINCH redraw.
  const flush = (pane: Pane) => {
    backlog.delete(pane);
    const queue = pane.queue;
    pane.queue = [];
    pane.queuedBytes = 0;
    for (const unit of queue) {
      const data = unit.offset === 0 ? unit.data : sliceOf(unit.data, unit.offset, unit.data.length);
      pane.target.write(data, unit.callback);
    }
    if (backlog.size === 0) cancelDrain();
  };

  // A hidden document has no foreground to protect: release every backlog.
  (options.onDocumentVisibilityChange ?? defaultOnDocumentVisibilityChange)(() => {
    if (!isDocumentHidden()) return;
    for (const pane of [...backlog]) flush(pane);
  });

  const register = (target: OutputTarget, registerOptions: { visible?: boolean } = {}): ScheduledTerminalOutput => {
    const pane: Pane = {
      target,
      visible: registerOptions.visible !== false,
      disposed: false,
      queue: [],
      queuedBytes: 0,
      inFlightSlices: 0,
    };
    panes.add(pane);
    return {
      write: (data, callback) => {
        if (pane.disposed) return;
        if (pane.visible) {
          lastForegroundAt = now();
          pane.target.write(data, callback);
          return;
        }
        if (isDocumentHidden()) {
          if (pane.queue.length > 0) flush(pane);
          pane.target.write(data, callback);
          return;
        }
        pane.queue.push({ data, offset: 0, callback });
        pane.queuedBytes += data.length;
        backlog.add(pane);
        scheduleDrain();
      },
      setVisible: (visible) => {
        if (pane.disposed || pane.visible === visible) return;
        pane.visible = visible;
        if (visible) flush(pane);
      },
      queuedBytes: () => pane.queuedBytes,
      dispose: () => {
        if (pane.disposed) return;
        pane.disposed = true;
        pane.queue = [];
        pane.queuedBytes = 0;
        panes.delete(pane);
        backlog.delete(pane);
        if (backlog.size === 0) cancelDrain();
      },
    };
  };

  const noteForegroundActivity = () => {
    lastForegroundAt = now();
  };

  const stats = (): OutputSchedulerStats => {
    let queuedBytes = 0;
    for (const pane of panes) queuedBytes += pane.queuedBytes;
    return {
      panes: panes.size,
      backgroundBacklogPanes: backlog.size,
      queuedBytes,
      drainScheduled: timer !== null,
      foregroundActive: foregroundActive(),
    };
  };

  return { register, noteForegroundActivity, stats };
}

export const terminalOutputScheduler = createOutputScheduler();
