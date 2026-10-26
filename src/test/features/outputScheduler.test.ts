import { describe, expect, it, vi } from "vitest";
import {
  createOutputScheduler,
  type OutputData,
  type OutputSchedulerOptions,
} from "../../features/terminal/outputScheduler";
import {
  BACKGROUND_CHUNK_BYTES,
  BACKGROUND_DRAIN_INTERVAL_MS,
  BACKGROUND_MAX_IN_FLIGHT_SLICES,
  BACKGROUND_MAX_WRITES_PER_DRAIN,
  BUSY_FOREGROUND_CHUNK_BYTES,
  BUSY_FOREGROUND_MAX_WRITES_PER_DRAIN,
  FOREGROUND_ACTIVE_WINDOW_MS,
  STREAM_HIGH_WATER_BYTES,
  STREAM_LOW_WATER_BYTES,
} from "../../features/terminal/terminalBudget";
import { createTerminalStreamWriter } from "../../features/terminal/terminalStream";
import type { OscStreamEvent } from "../../features/terminal/oscStream";

const INTERVAL = BACKGROUND_DRAIN_INTERVAL_MS;

// Manual clock: timers fire only when advance() passes their deadline.
function fakeClock() {
  let time = 0;
  let seq = 0;
  let timers: { at: number; id: number; fn: () => void }[] = [];
  return {
    now: () => time,
    tick: (ms: number) => {
      time += ms;
    },
    setTimer: (fn: () => void, delayMs: number) => {
      seq += 1;
      timers.push({ at: time + delayMs, id: seq, fn });
      return seq;
    },
    clearTimer: (handle: unknown) => {
      timers = timers.filter((timer) => timer.id !== handle);
    },
    pending: () => timers.length,
    advance: (ms: number) => {
      const end = time + ms;
      for (;;) {
        const due = timers.filter((timer) => timer.at <= end).sort((a, b) => a.at - b.at || a.id - b.id)[0];
        if (!due) break;
        timers = timers.filter((timer) => timer !== due);
        time = Math.max(time, due.at);
        due.fn();
      }
      time = Math.max(time, end);
    },
  };
}

type Clock = ReturnType<typeof fakeClock>;

// Document visibility the scheduler observes.
function fakeDocument() {
  let hidden = false;
  const listeners: (() => void)[] = [];
  return {
    isDocumentHidden: () => hidden,
    onDocumentVisibilityChange: (listener: () => void) => {
      listeners.push(listener);
    },
    setHidden: (next: boolean) => {
      hidden = next;
      listeners.forEach((listener) => listener());
    },
  };
}

// Fake xterm: write() only queues; parse() runs the FIFO and fires callbacks
// in order, like xterm's write buffer.
function fakeTerm() {
  const pending: { data: OutputData; callback?: () => void }[] = [];
  const parsed: number[] = [];
  const writes: number[] = [];
  const toBytes = (data: OutputData) =>
    typeof data === "string" ? Array.from(data, (ch) => ch.charCodeAt(0)) : Array.from(data);
  return {
    writes,
    parsed,
    pendingWrites: () => pending.length,
    written: () => writes.reduce((total, size) => total + size, 0),
    write(data: OutputData, callback?: () => void) {
      writes.push(data.length);
      pending.push({ data, callback });
    },
    parseOne() {
      const next = pending.shift();
      if (!next) return false;
      for (const byte of toBytes(next.data)) parsed.push(byte);
      next.callback?.();
      return true;
    },
    parseAll() {
      while (this.parseOne()) {
        // drain the FIFO
      }
    },
  };
}

function bytes(length: number, seed = 0): Uint8Array {
  const out = new Uint8Array(length);
  for (let i = 0; i < length; i += 1) out[i] = (i * 31 + seed) & 0xff;
  return out;
}

// Printable filler for stream-writer tests (no escape bytes for the OSC filter).
const text = (length: number, fill = 0x61) => new Uint8Array(length).fill(fill);

function scheduler(clock: Clock, budget?: OutputSchedulerOptions["budget"], doc = fakeDocument()) {
  return createOutputScheduler({
    now: clock.now,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
    isDocumentHidden: doc.isDocumentHidden,
    onDocumentVisibilityChange: doc.onDocumentVisibilityChange,
    budget,
  });
}

// Deterministic PRNG (mulberry32) for the property-style test.
function prng(seed: number) {
  let state = seed >>> 0;
  return () => {
    state = (state + 0x6d2b79f5) >>> 0;
    let t = state;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

describe("terminal output scheduler", () => {
  it("writes foreground output immediately while ten busy background panes yield to it", () => {
    const clock = fakeClock();
    const s = scheduler(clock);
    const foregroundTerm = fakeTerm();
    const foreground = s.register(foregroundTerm, { visible: true });
    const backgroundTerms = Array.from({ length: 10 }, () => fakeTerm());
    const backgrounds = backgroundTerms.map((term) => s.register(term, { visible: false }));
    backgrounds.forEach((pane, index) => pane.write(bytes(1 << 20, index)));
    // Run the immediate first drain so each advance below covers one drain.
    foreground.write(bytes(1));
    clock.advance(0);
    foregroundTerm.parseAll();
    foregroundTerm.writes.length = 0;
    expect(backgroundTerms.reduce((n, term) => n + term.writes.length, 0)).toBe(BUSY_FOREGROUND_MAX_WRITES_PER_DRAIN);
    backgroundTerms.forEach((term) => term.parseAll());

    for (let frame = 0; frame < 20; frame += 1) {
      let callbackFired = false;
      foreground.write(bytes(8, frame), () => {
        callbackFired = true;
      });
      // The echo reaches xterm synchronously, not behind the background backlog.
      expect(foregroundTerm.pendingWrites()).toBe(1);
      foregroundTerm.parseAll();
      expect(callbackFired).toBe(true);
      expect(s.stats().foregroundActive).toBe(true);

      const before = backgroundTerms.map((term) => term.writes.length);
      clock.advance(INTERVAL);
      const sliceSizes = backgroundTerms.flatMap((term, index) => term.writes.slice(before[index]));
      expect(sliceSizes.length).toBeLessThanOrEqual(BUSY_FOREGROUND_MAX_WRITES_PER_DRAIN);
      for (const size of sliceSizes) expect(size).toBeLessThanOrEqual(BUSY_FOREGROUND_CHUNK_BYTES);
      backgroundTerms.forEach((term) => term.parseAll());
    }
    expect(foregroundTerm.writes).toHaveLength(20);
  });

  it("drains hidden panes much faster while the foreground is idle", () => {
    const drainedIn = (busy: boolean) => {
      const clock = fakeClock();
      const s = scheduler(clock);
      const term = fakeTerm();
      const pane = s.register(term, { visible: false });
      pane.write(bytes(8 << 20));
      for (let cycle = 0; cycle < 10; cycle += 1) {
        if (busy) s.noteForegroundActivity();
        clock.advance(INTERVAL);
        term.parseAll();
      }
      return term.written();
    };
    const idle = drainedIn(false);
    const busy = drainedIn(true);
    // Idle: the pane fills every in-flight slot with a 64 KiB slice per drain.
    expect(idle).toBeGreaterThanOrEqual(10 * BACKGROUND_MAX_IN_FLIGHT_SLICES * BACKGROUND_CHUNK_BYTES);
    // Busy: one 16 KiB slice per drain.
    expect(busy).toBeLessThanOrEqual(11 * BUSY_FOREGROUND_CHUNK_BYTES);
    expect(idle).toBeGreaterThanOrEqual(busy * 7);
  });

  it("returns to the fast tier once the foreground has been quiet for the activity window", () => {
    const clock = fakeClock();
    const s = scheduler(clock);
    s.noteForegroundActivity();
    expect(s.stats().foregroundActive).toBe(true);
    clock.advance(FOREGROUND_ACTIVE_WINDOW_MS);
    expect(s.stats().foregroundActive).toBe(false);
  });

  it("caps idle drains across many hidden panes by the per-drain write count", () => {
    const clock = fakeClock();
    const s = scheduler(clock);
    const terms = Array.from({ length: 6 }, () => fakeTerm());
    const panes = terms.map((term) => s.register(term, { visible: false }));
    panes.forEach((pane, index) => pane.write(bytes(1 << 20, index)));
    // Run the immediate first drain so each advance below covers one drain.
    clock.advance(0);
    expect(terms.reduce((n, term) => n + term.writes.length, 0)).toBe(BACKGROUND_MAX_WRITES_PER_DRAIN);
    terms.forEach((term) => term.parseAll());
    for (let cycle = 0; cycle < 6; cycle += 1) {
      const before = terms.map((term) => term.writes.length);
      clock.advance(INTERVAL);
      const sizes = terms.flatMap((term, index) => term.writes.slice(before[index]));
      expect(sizes.length).toBe(BACKGROUND_MAX_WRITES_PER_DRAIN);
      for (const size of sizes) expect(size).toBeLessThanOrEqual(BACKGROUND_CHUNK_BYTES);
      terms.forEach((term) => term.parseAll());
    }
  });

  it("lets a hidden 8 MiB/s producer run without pausing upstream while the foreground is idle", () => {
    const run = (busyForeground: boolean) => {
      const clock = fakeClock();
      const s = scheduler(clock);
      const term = fakeTerm();
      const pane = s.register(term, { visible: false });
      const pauses: boolean[] = [];
      const writer = createTerminalStreamWriter(pane, () => undefined, {
        highWaterBytes: STREAM_HIGH_WATER_BYTES,
        lowWaterBytes: STREAM_LOW_WATER_BYTES,
        onPausedChange: (paused) => pauses.push(paused),
      });
      // 8 MiB/s in 16 ms ticks.
      const perTick = Math.ceil((8 << 20) / (1000 / INTERVAL));
      expect(perTick * (1000 / INTERVAL)).toBeGreaterThanOrEqual(8 << 20);
      let accepted = 0;
      // Two seconds of output at 8 MiB/s.
      for (let tick = 0; tick < 2000 / INTERVAL; tick += 1) {
        if (busyForeground) s.noteForegroundActivity();
        if (writer.pushBytes(text(perTick))) accepted += perTick;
        clock.advance(INTERVAL);
        term.parseAll();
      }
      return { pauses, accepted, pending: writer.stats().pendingBytes };
    };
    const idle = run(false);
    expect(idle.pauses).toEqual([]);
    expect(idle.pending).toBeLessThanOrEqual(2 * BACKGROUND_MAX_IN_FLIGHT_SLICES * BACKGROUND_CHUNK_BYTES);
    // The same producer behind a busy foreground is throttled by backpressure.
    expect(run(true).pauses).toContain(true);
  });

  it("drains on a fixed cadence measured from the previous drain", () => {
    const clock = fakeClock();
    const s = scheduler(clock);
    const term = fakeTerm();
    const pane = s.register(term, { visible: false });
    pane.write(bytes(BACKGROUND_CHUNK_BYTES * 8));
    // The first drain after a quiet period runs right away.
    clock.advance(0);
    expect(term.writes).toHaveLength(BACKGROUND_MAX_IN_FLIGHT_SLICES);
    // xterm reports the parse 10 ms later; the next drain still lands at 16 ms.
    clock.advance(10);
    term.parseAll();
    clock.advance(INTERVAL - 10 - 1);
    expect(term.writes).toHaveLength(BACKGROUND_MAX_IN_FLIGHT_SLICES);
    clock.advance(1);
    expect(term.writes).toHaveLength(2 * BACKGROUND_MAX_IN_FLIGHT_SLICES);
  });

  it("feeds parse progress back through the in-flight gate", () => {
    const clock = fakeClock();
    const s = scheduler(clock);
    const term = fakeTerm();
    const pane = s.register(term, { visible: false });
    pane.write(bytes(BACKGROUND_CHUNK_BYTES * 8));
    // xterm never reports progress: no further slices pile into its buffer.
    clock.advance(INTERVAL * 10);
    expect(term.writes).toHaveLength(BACKGROUND_MAX_IN_FLIGHT_SLICES);
    expect(s.stats().drainScheduled).toBe(false);
    term.parseOne();
    expect(s.stats().drainScheduled).toBe(true);
    clock.advance(INTERVAL);
    expect(term.writes).toHaveLength(BACKGROUND_MAX_IN_FLIGHT_SLICES + 1);
  });

  it("writes straight through every pane while the document is hidden and flushes backlogs when it hides", () => {
    const clock = fakeClock();
    const doc = fakeDocument();
    const s = scheduler(clock, undefined, doc);
    const a = fakeTerm();
    const b = fakeTerm();
    const paneA = s.register(a, { visible: false });
    const paneB = s.register(b, { visible: false });
    const first = bytes(BACKGROUND_CHUNK_BYTES * 8, 1);
    paneA.write(first);
    paneB.write(bytes(100, 2));
    clock.advance(0);
    a.parseAll();
    b.parseAll();
    const queuedA = paneA.queuedBytes();
    expect(queuedA).toBeGreaterThan(0);

    doc.setHidden(true);
    // visibilitychange to hidden releases every backlog in order.
    expect(paneA.queuedBytes()).toBe(0);
    expect(s.stats()).toMatchObject({ backgroundBacklogPanes: 0, drainScheduled: false });
    const late = bytes(10, 3);
    paneA.write(late);
    expect(a.pendingWrites()).toBe(2);
    a.parseAll();
    expect(a.parsed).toEqual([...first, ...late]);

    // Becoming visible again does not flush; hidden panes queue once more.
    doc.setHidden(false);
    paneB.write(bytes(5));
    expect(paneB.queuedBytes()).toBe(5);
  });

  it("flushes a pane's backlog before passing through when the document hid without an event", () => {
    const clock = fakeClock();
    let hidden = false;
    const s = createOutputScheduler({
      now: clock.now,
      setTimer: clock.setTimer,
      clearTimer: clock.clearTimer,
      isDocumentHidden: () => hidden,
      onDocumentVisibilityChange: () => undefined,
    });
    const term = fakeTerm();
    const pane = s.register(term, { visible: false });
    pane.write(bytes(BACKGROUND_CHUNK_BYTES * 4, 1));
    hidden = true;
    pane.write(bytes(3, 2));
    expect(pane.queuedBytes()).toBe(0);
    term.parseAll();
    expect(term.parsed).toEqual([...bytes(BACKGROUND_CHUNK_BYTES * 4, 1), ...bytes(3, 2)]);
  });

  it("serves background panes round-robin", () => {
    const clock = fakeClock();
    const s = scheduler(clock, { idle: { maxWritesPerDrain: 1, chunkBytes: 1024 } });
    const order: string[] = [];
    const terms = ["a", "b", "c"].map((name) => {
      const term = fakeTerm();
      const write = term.write.bind(term);
      term.write = (data, callback) => {
        order.push(name);
        write(data, callback);
      };
      return term;
    });
    const panes = terms.map((term) => s.register(term, { visible: false }));
    // Pane "a" floods; "b" and "c" join with smaller backlogs.
    panes[0].write(bytes(1024 * 6));
    panes[1].write(bytes(1024 * 2));
    panes[2].write(bytes(1024 * 2));
    for (let i = 0; i < 12; i += 1) {
      clock.advance(INTERVAL);
      terms.forEach((term) => term.parseAll());
    }
    expect(order.slice(0, 6)).toEqual(["a", "b", "c", "a", "b", "c"]);
    expect(order.slice(6)).toEqual(["a", "a", "a", "a"]);
  });

  it("drains the backlog in order before new data on reveal", () => {
    const clock = fakeClock();
    const s = scheduler(clock, { idle: { maxInFlightSlices: 1 } });
    const term = fakeTerm();
    const pane = s.register(term, { visible: false });
    const first = bytes(BACKGROUND_CHUNK_BYTES * 2 + 100, 1);
    const second = bytes(500, 2);
    const done: string[] = [];
    pane.write(first, () => done.push("first"));
    pane.write(second, () => done.push("second"));
    clock.advance(0);
    expect(term.writes).toEqual([BACKGROUND_CHUNK_BYTES]);

    pane.setVisible(true);
    const fresh = bytes(10, 3);
    pane.write(fresh, () => done.push("fresh"));
    // The remainder of the partly drained unit, the queued unit, then new data.
    expect(term.writes).toEqual([BACKGROUND_CHUNK_BYTES, first.length - BACKGROUND_CHUNK_BYTES, second.length, 10]);
    term.parseAll();
    expect(term.parsed).toEqual([...first, ...second, ...fresh]);
    expect(done).toEqual(["first", "second", "fresh"]);
    expect(s.stats()).toMatchObject({ drainScheduled: false, backgroundBacklogPanes: 0 });

    // Hiding again queues new output.
    pane.setVisible(false);
    pane.write(bytes(4));
    expect(term.writes).toHaveLength(4);
    expect(pane.queuedBytes()).toBe(4);
  });

  it("returns a unit's credit only after xterm's callback for its last byte", () => {
    const clock = fakeClock();
    const s = scheduler(clock, { idle: { maxInFlightSlices: 1 } });
    const term = fakeTerm();
    const pane = s.register(term, { visible: false });
    let credited = 0;
    pane.write(bytes(BACKGROUND_CHUNK_BYTES * 2), () => {
      credited += 1;
    });
    clock.advance(0);
    term.parseAll();
    expect(credited).toBe(0);
    clock.advance(INTERVAL);
    expect(term.writes).toHaveLength(2);
    expect(credited).toBe(0);
    term.parseAll();
    expect(credited).toBe(1);
  });

  it("keeps the stream writer's backpressure while hidden output waits in the scheduler", () => {
    const clock = fakeClock();
    const s = scheduler(clock);
    const term = fakeTerm();
    const pane = s.register(term, { visible: false });
    const pauses: boolean[] = [];
    let written = 0;
    const writer = createTerminalStreamWriter(pane, () => undefined, {
      highWaterBytes: 256 * 1024,
      lowWaterBytes: 64 * 1024,
      maxPendingBytes: 512 * 1024,
      onPausedChange: (paused) => pauses.push(paused),
      onWritten: () => {
        written += 1;
      },
    });
    expect(writer.pushBytes(text(320 * 1024, 0x61))).toBe(true);
    // Nothing reached xterm yet, but the writer already counts the bytes in flight.
    expect(term.writes).toHaveLength(0);
    expect(pauses).toEqual([true]);
    expect(writer.stats().pendingBytes).toBe(320 * 1024);
    expect(writer.pushBytes(text(160 * 1024, 0x62))).toBe(true);
    // Past the hard cap the chunk is refused whole for the offset replay.
    expect(writer.pushBytes(text(160 * 1024, 0x63))).toBe(false);

    clock.advance(0);
    term.parseAll();
    expect(written).toBe(0);
    pane.setVisible(true);
    term.parseAll();
    expect(written).toBeGreaterThan(0);
    expect(pauses).toEqual([true, false]);
    expect(writer.stats().pendingBytes).toBe(0);
    expect(term.parsed).toEqual([...text(320 * 1024, 0x61), ...text(160 * 1024, 0x62)]);
  });

  it("dispatches a hidden pane's notifications on acceptance while other events wait for the parse", () => {
    const clock = fakeClock();
    const s = scheduler(clock);
    const term = fakeTerm();
    const pane = s.register(term, { visible: false });
    const events: OscStreamEvent["kind"][] = [];
    const writer = createTerminalStreamWriter(pane, (event) => events.push(event.kind), {
      highWaterBytes: STREAM_HIGH_WATER_BYTES,
      lowWaterBytes: STREAM_LOW_WATER_BYTES,
      dispatchEarly: (event) => event.kind === "notification",
    });
    const enc = new TextEncoder();
    writer.pushBytes(enc.encode("build\x1b]133;D;0\x07done\x1b]9;finished\x07tail"));
    expect(events).toEqual(["notification"]);
    expect(term.writes).toHaveLength(0);
    for (let cycle = 0; cycle < 3; cycle += 1) {
      clock.advance(INTERVAL);
      term.parseAll();
    }
    expect(events).toEqual(["notification", "command"]);
    // The bytes themselves keep their order.
    expect(new TextDecoder().decode(Uint8Array.from(term.parsed))).toBe("builddonetail");
  });

  it("never loses, duplicates, or reorders bytes across random scheduling", () => {
    for (const seed of [1, 7, 42, 1337, 9001]) {
      const random = prng(seed);
      const clock = fakeClock();
      const doc = fakeDocument();
      const tier = () => ({
        chunkBytes: 1 + Math.floor(random() * 64),
        maxWritesPerDrain: 1 + Math.floor(random() * 4),
        maxInFlightSlices: 1 + Math.floor(random() * 3),
      });
      const s = scheduler(clock, { idle: tier(), busy: tier() }, doc);
      const panes = Array.from({ length: 4 }, () => {
        const term = fakeTerm();
        const handle = s.register(term, { visible: random() < 0.5 });
        return { term, handle, expected: [] as number[], callbacks: [] as number[], next: 0 };
      });
      for (let step = 0; step < 500; step += 1) {
        const pane = panes[Math.floor(random() * panes.length)];
        const roll = random();
        if (roll < 0.45) {
          const unit = pane.next;
          pane.next += 1;
          const chunk = random() < 0.1 ? "" : String.fromCharCode(...bytes(1 + Math.floor(random() * 200), unit));
          const data: OutputData = random() < 0.5 ? chunk : Uint8Array.from(chunk, (ch) => ch.charCodeAt(0));
          pane.expected.push(...Array.from(chunk, (ch) => ch.charCodeAt(0)));
          const end = pane.expected.length;
          pane.handle.write(data, () => {
            // Credit only after every byte of the unit has been parsed.
            expect(pane.term.parsed.length).toBeGreaterThanOrEqual(end);
            pane.callbacks.push(unit);
          });
        } else if (roll < 0.57) {
          pane.handle.setVisible(random() < 0.5);
        } else if (roll < 0.6) {
          doc.setHidden(random() < 0.3);
        } else if (roll < 0.65) {
          s.noteForegroundActivity();
        } else if (roll < 0.82) {
          pane.term.parseOne();
        } else {
          clock.advance(Math.floor(random() * 40));
        }
      }
      for (const pane of panes) pane.handle.setVisible(true);
      for (const pane of panes) {
        pane.term.parseAll();
        expect(pane.term.parsed).toEqual(pane.expected);
        expect(pane.callbacks).toEqual(Array.from({ length: pane.next }, (_, i) => i));
        expect(pane.handle.queuedBytes()).toBe(0);
      }
      expect(s.stats()).toMatchObject({ queuedBytes: 0, drainScheduled: false });
    }
  });

  it("cleans up a disposed pane with queued and in-flight output", () => {
    const clock = fakeClock();
    const s = scheduler(clock);
    const term = fakeTerm();
    const pane = s.register(term, { visible: false });
    const other = s.register(fakeTerm(), { visible: true });
    let credited = 0;
    pane.write(bytes(BACKGROUND_CHUNK_BYTES * 8), () => {
      credited += 1;
    });
    clock.advance(0);
    expect(term.writes).toHaveLength(BACKGROUND_MAX_IN_FLIGHT_SLICES);
    expect(s.stats()).toMatchObject({ panes: 2, backgroundBacklogPanes: 1 });

    pane.dispose();
    pane.dispose();
    expect(s.stats()).toMatchObject({ panes: 1, backgroundBacklogPanes: 0, queuedBytes: 0, drainScheduled: false });
    expect(clock.pending()).toBe(0);
    // The late callbacks of slices already in xterm are inert.
    term.parseAll();
    clock.advance(INTERVAL * 4);
    expect(term.writes).toHaveLength(BACKGROUND_MAX_IN_FLIGHT_SLICES);
    expect(credited).toBe(0);
    // Writes and visibility changes after dispose are ignored.
    pane.write(bytes(10));
    pane.setVisible(true);
    expect(term.writes).toHaveLength(BACKGROUND_MAX_IN_FLIGHT_SLICES);
    other.dispose();
    expect(s.stats().panes).toBe(0);
  });

  it("keeps the drain timer running only while a pane can drain", () => {
    const clock = fakeClock();
    const s = scheduler(clock, { idle: { maxInFlightSlices: 1 } });
    const a = fakeTerm();
    const b = fakeTerm();
    const paneA = s.register(a, { visible: false });
    const paneB = s.register(b, { visible: false });
    paneA.write(bytes(BACKGROUND_CHUNK_BYTES * 2));
    paneB.write(bytes(BACKGROUND_CHUNK_BYTES * 2));
    clock.advance(0);
    // Both panes wait on their in-flight slice: no timer spins meanwhile.
    expect(s.stats().drainScheduled).toBe(false);
    a.parseAll();
    expect(s.stats().drainScheduled).toBe(true);
    // Revealing one pane leaves the other's drain scheduled.
    b.parseAll();
    paneA.setVisible(true);
    expect(s.stats().drainScheduled).toBe(true);
    paneB.setVisible(true);
    expect(s.stats().drainScheduled).toBe(false);
  });

  it("rejects a budget that cannot make progress", () => {
    const clock = fakeClock();
    expect(() => scheduler(clock, { idle: { chunkBytes: 0 } })).toThrow(RangeError);
    expect(() => scheduler(clock, { busy: { maxWritesPerDrain: 0 } })).toThrow(RangeError);
    expect(() => scheduler(clock, { idle: { maxInFlightSlices: Number.NaN } })).toThrow(RangeError);
    expect(() => scheduler(clock, { drainIntervalMs: -1 })).toThrow(RangeError);
    expect(() => scheduler(clock, { foregroundActiveMs: -1 })).toThrow(RangeError);
  });

  it("drives drains with real timers by default", async () => {
    const s = createOutputScheduler({ budget: { drainIntervalMs: 0 } });
    const term = fakeTerm();
    const pane = s.register(term, { visible: false });
    pane.write("hello");
    await new Promise((resolve) => setTimeout(resolve, 5));
    term.parseAll();
    expect(String.fromCharCode(...term.parsed)).toBe("hello");
    pane.dispose();
  });

  it("follows the document's visibility by default", () => {
    const doc = Object.assign(new EventTarget(), { visibilityState: "visible" });
    vi.stubGlobal("document", doc);
    try {
      const clock = fakeClock();
      const s = createOutputScheduler({ now: clock.now, setTimer: clock.setTimer, clearTimer: clock.clearTimer });
      const term = fakeTerm();
      const pane = s.register(term, { visible: false });
      pane.write("queued");
      expect(pane.queuedBytes()).toBe(6);
      doc.visibilityState = "hidden";
      doc.dispatchEvent(new Event("visibilitychange"));
      expect(pane.queuedBytes()).toBe(0);
      pane.write("!");
      term.parseAll();
      expect(String.fromCharCode(...term.parsed)).toBe("queued!");
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
