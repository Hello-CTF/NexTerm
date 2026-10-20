import { createOscStreamFilter, type OscStreamEvent, type OscStreamFilter, type OscStreamSegment } from "./oscStream";

// Parsed watermarks for the terminal write pipeline. Bytes handed to xterm
// whose write callbacks have not fired yet are the parsed backlog: past the
// high watermark the writer stops feeding xterm (pausing the upstream pump),
// and it resumes once the backlog drains to the low watermark — or whenever
// xterm has fully caught up, so queued bytes always keep flowing. Accepted
// bytes wait in the writer's FIFO instead of growing xterm's own write
// buffer, and no accepted byte is ever dropped or reordered.
//
// The FIFO itself is hard-bounded by maxPendingBytes. Acceptance is
// transactional at the stream-byte level: pushBytes refuses a chunk whole
// before the OSC filter or any queue state advances, so a refused chunk
// leaves no trace — the caller keeps its byte offset (streamBytes must not
// advance) and the server's resume replay redelivers the chunk in order.
// Refusal is sticky for the whole episode: everything is refused until
// beginAttachment() marks the next attachment's stream boundary, keeping the
// accepted prefix contiguous so the replayed suffix joins it in original
// stream order. Local notices bypass the cap: they are a few dozen bytes of
// UI chrome that must always render.
export const STREAM_HIGH_WATER_BYTES = 4 << 20;
export const STREAM_LOW_WATER_BYTES = 1 << 20;
export const STREAM_MAX_PENDING_BYTES = 16 << 20;

export interface TerminalStreamBacklog {
  paused: boolean;
  pendingBytes: number;
  oldestPendingAgeMs: number;
}

export interface TerminalStreamStats extends TerminalStreamBacklog {
  generation: number;
  inFlightWrites: number;
  suppressedEvents: number;
  refusedBytes: number;
  maxPendingBytes: number;
}

export interface TerminalStreamWriterOptions {
  highWaterBytes: number;
  lowWaterBytes: number;
  maxPendingBytes?: number;
  now?: () => number;
  onPausedChange?: (paused: boolean, stats: TerminalStreamStats) => void;
  onRefused?: (stats: TerminalStreamStats) => void;
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
  pushBytes: (bytes: Uint8Array) => boolean;
  write: (segments: OscStreamSegment[]) => boolean;
  canAccept: (bytes: number) => boolean;
  writeLocalText: (data: string) => void;
  beginAttachment: () => void;
  resetStream: () => void;
  nextGeneration: () => number;
  stats: () => TerminalStreamStats;
}

export function createTerminalStreamWriter(
  term: { write: (data: string | Uint8Array, callback?: () => void) => void },
  dispatch: (event: OscStreamEvent) => void,
  options: TerminalStreamWriterOptions,
): TerminalStreamWriter {
  const { highWaterBytes, lowWaterBytes } = options;
  const maxPendingBytes = options.maxPendingBytes ?? STREAM_MAX_PENDING_BYTES;
  if (
    !Number.isFinite(highWaterBytes) ||
    !Number.isFinite(lowWaterBytes) ||
    !Number.isFinite(maxPendingBytes) ||
    lowWaterBytes >= highWaterBytes ||
    highWaterBytes >= maxPendingBytes
  ) {
    throw new RangeError(
      "stream writer requires lowWaterBytes < highWaterBytes < maxPendingBytes",
    );
  }
  const now = options.now ?? Date.now;
  const encoder = new TextEncoder();
  let filter: OscStreamFilter = createOscStreamFilter();
  let episode = false;
  const queue: WriteUnit[] = [];
  const inFlight: WriteUnit[] = [];
  let queuedBytes = 0;
  let inFlightBytes = 0;
  let generation = 0;
  let paused = false;
  let pumping = false;
  let suppressedEvents = 0;
  let refusedBytes = 0;
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
      refusedBytes,
      maxPendingBytes,
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

  const canAccept = (bytes: number): boolean =>
    bytes <= 0 || pendingBytes() + bytes <= maxPendingBytes;

  const refuse = (incoming: number): boolean => {
    refusedBytes += incoming;
    if (!episode) {
      episode = true;
      options.onRefused?.(stats());
    }
    return false;
  };

  const write = (segments: OscStreamSegment[]): boolean => {
    let incoming = 0;
    for (const segment of segments) {
      if (segment.kind === "text") incoming += segment.data.length;
    }
    // Atomic refusal: a batch that would push pending bytes past the hard cap
    // is rejected whole — nothing is enqueued and no event is dispatched, so
    // the caller can recover the exact byte range later (resume replay).
    if (episode || (incoming > 0 && !canAccept(incoming))) {
      return refuse(incoming);
    }
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
    return true;
  };

  // pushBytes is the transactional stream entry: the chunk is accepted or
  // refused whole, and only acceptance lets the OSC filter see it. A refused
  // chunk leaves the filter, the queue, and the caller's byte offset exactly
  // as they were, so the resume replay can redeliver it in order.
  const pushBytes = (bytes: Uint8Array): boolean => {
    if (bytes.length === 0) return true;
    if (episode || !canAccept(bytes.length)) {
      return refuse(bytes.length);
    }
    return write(filter.push(bytes));
  };

  const writeLocalText = (data: string) => {
    // Local notices bypass the cap: they are a few dozen bytes of UI chrome
    // that must always render, and they carry no stream-sequence bytes.
    enqueue(encoder.encode(data), []);
    pump();
  };

  // beginAttachment marks the next attachment's stream boundary: the refusal
  // episode ends here, not on any RPC response, so replay bytes that arrive
  // before the attach response is processed are still accepted.
  const beginAttachment = () => {
    episode = false;
  };

  // resetStream drops partial OSC state (an expired anchor means the screen
  // was rebuilt; the replay starts a fresh byte stream).
  const resetStream = () => {
    filter = createOscStreamFilter();
  };

  const nextGeneration = () => {
    generation += 1;
    return generation;
  };

  return {
    pushBytes,
    write,
    canAccept,
    writeLocalText,
    beginAttachment,
    resetStream,
    nextGeneration,
    stats,
  };
}
