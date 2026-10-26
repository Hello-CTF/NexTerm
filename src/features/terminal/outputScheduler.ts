import {
  BACKGROUND_CHUNK_BYTES,
  BACKGROUND_DRAIN_BUDGET_MS,
  BACKGROUND_DRAIN_INTERVAL_MS,
  BACKGROUND_MAX_IN_FLIGHT_SLICES,
  BACKGROUND_MAX_WRITES_PER_DRAIN,
} from "./terminalBudget";

// App-wide terminal output scheduler. Visible (foreground) panes write straight
// into xterm. Hidden (background) panes queue their output here and drain in
// small slices on a timer, round-robin across panes, within a per-drain write
// count and time budget, so a flood in hidden tabs cannot starve the visible
// pane's parsing, rendering, and input echo.
//
// The scheduler never drops, reorders, or duplicates bytes: a pane's units are
// written in FIFO order, and a unit's callback fires exactly once, from
// xterm's write callback for the unit's last byte (xterm parses writes in
// order, so that callback also covers every earlier slice). Bounding a hidden
// pane's backlog is the stream writer's job: queued bytes stay in flight from
// its point of view, so it pauses upstream at the high watermark and refuses
// whole chunks past the hard cap, which the server replays from the accepted
// offset.

export type OutputData = string | Uint8Array;

export interface OutputTarget {
  write: (data: OutputData, callback?: () => void) => void;
}

export interface OutputSchedulerBudget {
  chunkBytes: number;
  drainIntervalMs: number;
  maxWritesPerDrain: number;
  drainBudgetMs: number;
  maxInFlightSlices: number;
}

export interface OutputSchedulerOptions {
  budget?: Partial<OutputSchedulerBudget>;
  now?: () => number;
  setTimer?: (callback: () => void, delayMs: number) => unknown;
  clearTimer?: (handle: unknown) => void;
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
}

export interface OutputScheduler {
  register: (target: OutputTarget, options?: { visible?: boolean }) => ScheduledTerminalOutput;
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

const DEFAULT_BUDGET: OutputSchedulerBudget = {
  chunkBytes: BACKGROUND_CHUNK_BYTES,
  drainIntervalMs: BACKGROUND_DRAIN_INTERVAL_MS,
  maxWritesPerDrain: BACKGROUND_MAX_WRITES_PER_DRAIN,
  drainBudgetMs: BACKGROUND_DRAIN_BUDGET_MS,
  maxInFlightSlices: BACKGROUND_MAX_IN_FLIGHT_SLICES,
};

const sliceOf = (data: OutputData, start: number, end: number): OutputData =>
  typeof data === "string" ? data.slice(start, end) : data.subarray(start, end);

const defaultNow = () => (typeof performance !== "undefined" ? performance.now() : Date.now());

export function createOutputScheduler(options: OutputSchedulerOptions = {}): OutputScheduler {
  const budget: OutputSchedulerBudget = { ...DEFAULT_BUDGET, ...options.budget };
  if (
    !(budget.chunkBytes >= 1) ||
    !(budget.maxWritesPerDrain >= 1) ||
    !(budget.maxInFlightSlices >= 1) ||
    !(budget.drainIntervalMs >= 0) ||
    !(budget.drainBudgetMs >= 0)
  ) {
    throw new RangeError("output scheduler budget must allow at least one write per drain");
  }
  const now = options.now ?? defaultNow;
  const setTimer = options.setTimer ?? ((callback, delayMs) => setTimeout(callback, delayMs));
  const clearTimer = options.clearTimer ?? ((handle) => clearTimeout(handle as ReturnType<typeof setTimeout>));

  const panes = new Set<Pane>();
  // Background panes with queued bytes, in round-robin order: a pane that
  // was just served moves to the end.
  const backlog = new Set<Pane>();
  let timer: unknown = null;

  const scheduleDrain = () => {
    if (timer !== null || backlog.size === 0) return;
    timer = setTimer(drain, budget.drainIntervalMs);
  };

  const cancelDrain = () => {
    if (timer === null) return;
    clearTimer(timer);
    timer = null;
  };

  const eligible = (pane: Pane) => pane.inFlightSlices < budget.maxInFlightSlices;

  // Hands the next slice of a background pane's head unit to xterm.
  const writeSlice = (pane: Pane) => {
    const unit = pane.queue[0];
    const end = Math.min(unit.data.length, unit.offset + budget.chunkBytes);
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
    const startedAt = now();
    let writes = 0;
    while (writes < budget.maxWritesPerDrain) {
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
      writeSlice(next);
      writes += 1;
      if (now() - startedAt >= budget.drainBudgetMs) break;
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

  const stats = (): OutputSchedulerStats => {
    let queuedBytes = 0;
    for (const pane of panes) queuedBytes += pane.queuedBytes;
    return {
      panes: panes.size,
      backgroundBacklogPanes: backlog.size,
      queuedBytes,
      drainScheduled: timer !== null,
    };
  };

  return { register, stats };
}

export const terminalOutputScheduler = createOutputScheduler();
