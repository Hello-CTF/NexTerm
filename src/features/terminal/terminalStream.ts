import type { OscStreamEvent, OscStreamSegment } from "./oscStream";

// Parsed watermarks for the terminal write pipeline. Bytes handed to xterm
// whose write callbacks have not fired yet are the parsed backlog: past the
// high watermark the writer stops feeding xterm (pausing the upstream pump),
// and it resumes once the backlog drains to the low watermark — or whenever
// xterm has fully caught up, so queued bytes always keep flowing. Accepted
// bytes wait in the writer's FIFO instead of growing xterm's own write
// buffer, and no accepted byte is ever dropped or reordered.
export const STREAM_HIGH_WATER_BYTES = 4 << 20;
export const STREAM_LOW_WATER_BYTES = 1 << 20;

export interface TerminalStreamBacklog {
  paused: boolean;
  pendingBytes: number;
  oldestPendingAgeMs: number;
}

export interface TerminalStreamStats extends TerminalStreamBacklog {
  generation: number;
  inFlightWrites: number;
  suppressedEvents: number;
}

export interface TerminalStreamWriterOptions {
  highWaterBytes: number;
  lowWaterBytes: number;
  now?: () => number;
  onPausedChange?: (paused: boolean, stats: TerminalStreamStats) => void;
  onWritten?: () => void;
}

interface WriteUnit {
  data: Uint8Array;
  events: OscStreamEvent[];
  generation: number;
  enqueuedAt: number;
  done: boolean;
}

export interface TerminalStreamWriter {
  write: (segments: OscStreamSegment[]) => void;
  writeLocalText: (data: string) => void;
  nextGeneration: () => number;
  stats: () => TerminalStreamStats;
}

export function createTerminalStreamWriter(
  term: { write: (data: string | Uint8Array, callback?: () => void) => void },
  dispatch: (event: OscStreamEvent) => void,
  options: TerminalStreamWriterOptions,
): TerminalStreamWriter {
  const { highWaterBytes, lowWaterBytes } = options;
  if (
    !Number.isFinite(highWaterBytes) ||
    !Number.isFinite(lowWaterBytes) ||
    lowWaterBytes >= highWaterBytes
  ) {
    throw new RangeError("stream writer requires lowWaterBytes < highWaterBytes");
  }
  const now = options.now ?? Date.now;
  const encoder = new TextEncoder();
  const queue: WriteUnit[] = [];
  const inFlight: WriteUnit[] = [];
  let queuedBytes = 0;
  let inFlightBytes = 0;
  let generation = 0;
  let paused = false;
  let pumping = false;
  let suppressedEvents = 0;
  let lastUnit: WriteUnit | null = null;

  const pendingBytes = () => queuedBytes + inFlightBytes;

  const stats = (): TerminalStreamStats => {
    let oldest = Number.POSITIVE_INFINITY;
    for (const unit of inFlight) oldest = Math.min(oldest, unit.enqueuedAt);
    for (const unit of queue) oldest = Math.min(oldest, unit.enqueuedAt);
    return {
      generation,
      pendingBytes: pendingBytes(),
      oldestPendingAgeMs: oldest === Number.POSITIVE_INFINITY ? 0 : Math.max(0, now() - oldest),
      paused,
      inFlightWrites: inFlight.length,
      suppressedEvents,
    };
  };

  const setPaused = (next: boolean) => {
    if (paused === next) return;
    paused = next;
    options.onPausedChange?.(paused, stats());
  };

  const pump = () => {
    if (pumping) return;
    pumping = true;
    try {
      while (!paused && queue.length > 0) {
        const unit = queue.shift()!;
        queuedBytes -= unit.data.length;
        inFlight.push(unit);
        inFlightBytes += unit.data.length;
        term.write(unit.data, () => complete(unit));
        if (inFlightBytes >= highWaterBytes) setPaused(true);
      }
    } finally {
      pumping = false;
    }
  };

  const complete = (unit: WriteUnit) => {
    inFlightBytes -= unit.data.length;
    const index = inFlight.indexOf(unit);
    if (index >= 0) inFlight.splice(index, 1);
    unit.done = true;
    if (lastUnit === unit) lastUnit = null;
    if (unit.generation === generation) {
      for (const event of unit.events) dispatch(event);
    } else {
      suppressedEvents += unit.events.length;
    }
    options.onWritten?.();
    if (paused) {
      // Resume when the backlog drained to the low watermark, or whenever
      // xterm has fully caught up: an idle xterm with a non-empty queue must
      // always be refilled or the remaining bytes would stall forever.
      if (pendingBytes() <= lowWaterBytes || inFlightBytes === 0) {
        setPaused(false);
        pump();
      }
    } else if (queue.length > 0) {
      pump();
    }
  };

  const enqueue = (data: Uint8Array, events: OscStreamEvent[]): WriteUnit => {
    const unit: WriteUnit = { data, events, generation, enqueuedAt: now(), done: false };
    queue.push(unit);
    queuedBytes += data.length;
    lastUnit = unit;
    return unit;
  };

  const write = (segments: OscStreamSegment[]) => {
    for (const segment of segments) {
      if (segment.kind === "event") {
        const tail = lastUnit;
        if (tail && !tail.done && tail.generation === generation) {
          tail.events.push(segment.event);
        } else if (tail && !tail.done) {
          // An older-generation write is still parsing: queue the event behind
          // it as an empty unit so it keeps stream order under the current
          // generation instead of being suppressed with the stale unit.
          enqueue(new Uint8Array(0), [segment.event]);
        } else {
          dispatch(segment.event);
        }
        continue;
      }
      enqueue(segment.data, []);
    }
    pump();
  };

  const writeLocalText = (data: string) => {
    enqueue(encoder.encode(data), []);
    pump();
  };

  const nextGeneration = () => {
    generation += 1;
    return generation;
  };

  return { write, writeLocalText, nextGeneration, stats };
}
