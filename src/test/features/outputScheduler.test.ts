import { describe, expect, it } from "vitest";
import {
  createOutputScheduler,
  type OutputData,
  type OutputSchedulerOptions,
} from "../../features/terminal/outputScheduler";
import {
  BACKGROUND_CHUNK_BYTES,
  BACKGROUND_DRAIN_INTERVAL_MS,
  BACKGROUND_MAX_WRITES_PER_DRAIN,
} from "../../features/terminal/terminalBudget";
import { createTerminalStreamWriter } from "../../features/terminal/terminalStream";

// Manual clock: timers fire only when advance() passes their deadline.
function fakeClock() {
  let time = 0;
  let seq = 0;
  let timers: { at: number; id: number; fn: () => void }[] = [];
  const clock = {
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
  return clock;
}

type Clock = ReturnType<typeof fakeClock>;

// Fake xterm: write() only queues; parse() runs the FIFO and fires callbacks
// in order, like xterm's write buffer. writeCostMs models synchronous work.
function fakeTerm(clock?: Clock, writeCostMs = 0) {
  const pending: { data: OutputData; callback?: () => void }[] = [];
  const parsed: number[] = [];
  const writes: number[] = [];
  const toBytes = (data: OutputData) =>
    typeof data === "string" ? Array.from(data, (ch) => ch.charCodeAt(0)) : Array.from(data);
  return {
    writes,
    parsed,
    pendingWrites: () => pending.length,
    write(data: OutputData, callback?: () => void) {
      clock?.tick(writeCostMs);
      writes.push(data.length);
      pending.push({ data, callback });
    },
    parseOne() {
      const next = pending.shift();
      if (!next) return false;
      parsed.push(...toBytes(next.data));
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

function scheduler(clock: Clock, budget?: OutputSchedulerOptions["budget"]) {
  return createOutputScheduler({
    now: clock.now,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
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
  it("writes foreground output immediately while ten busy background panes drain within the budget", () => {
    const clock = fakeClock();
    const s = scheduler(clock);
    const foregroundTerm = fakeTerm();
    const foreground = s.register(foregroundTerm, { visible: true });
    const backgroundTerms = Array.from({ length: 10 }, () => fakeTerm());
    const backgrounds = backgroundTerms.map((term) => s.register(term, { visible: false }));
    backgrounds.forEach((pane, index) => pane.write(bytes(1 << 20, index)));

    const echoed: number[] = [];
    for (let frame = 0; frame < 20; frame += 1) {
      let callbackFired = false;
      foreground.write(bytes(8, frame), () => {
        callbackFired = true;
      });
      // The echo reaches xterm synchronously, not behind the background backlog.
      expect(foregroundTerm.pendingWrites()).toBe(1);
      foregroundTerm.parseAll();
      expect(callbackFired).toBe(true);
      echoed.push(frame);

      const before = backgroundTerms.map((term) => term.writes.length);
      clock.advance(BACKGROUND_DRAIN_INTERVAL_MS);
      const sliceSizes = backgroundTerms.flatMap((term, index) => term.writes.slice(before[index]));
      expect(sliceSizes.length).toBeLessThanOrEqual(BACKGROUND_MAX_WRITES_PER_DRAIN);
      for (const size of sliceSizes) expect(size).toBeLessThanOrEqual(BACKGROUND_CHUNK_BYTES);
      backgroundTerms.forEach((term) => term.parseAll());
    }
    expect(echoed).toHaveLength(20);
    expect(foregroundTerm.writes).toHaveLength(20);
  });

  it("queues hidden output and drains it in slices on the drain interval", () => {
    const clock = fakeClock();
    const s = scheduler(clock);
    const term = fakeTerm();
    const pane = s.register(term, { visible: false });
    pane.write(bytes(40 * 1024));
    expect(term.writes).toHaveLength(0);
    expect(pane.queuedBytes()).toBe(40 * 1024);

    clock.advance(BACKGROUND_DRAIN_INTERVAL_MS - 1);
    expect(term.writes).toHaveLength(0);
    clock.advance(1);
    expect(term.writes).toEqual([BACKGROUND_CHUNK_BYTES]);
    // The next slice waits for xterm to parse the previous one.
    clock.advance(BACKGROUND_DRAIN_INTERVAL_MS * 3);
    expect(term.writes).toHaveLength(1);
    term.parseAll();
    clock.advance(BACKGROUND_DRAIN_INTERVAL_MS);
    term.parseAll();
    clock.advance(BACKGROUND_DRAIN_INTERVAL_MS);
    term.parseAll();
    expect(term.writes).toEqual([BACKGROUND_CHUNK_BYTES, BACKGROUND_CHUNK_BYTES, 8 * 1024]);
    expect(term.parsed).toEqual(Array.from(bytes(40 * 1024)));
    expect(pane.queuedBytes()).toBe(0);
    expect(s.stats().drainScheduled).toBe(false);
  });

  it("stops a drain once its time budget is spent", () => {
    const clock = fakeClock();
    const s = scheduler(clock, { maxWritesPerDrain: 10, drainBudgetMs: 8 });
    const terms = Array.from({ length: 6 }, () => fakeTerm(clock, 3));
    const panes = terms.map((term) => s.register(term, { visible: false }));
    panes.forEach((pane) => pane.write(bytes(1024)));
    clock.advance(BACKGROUND_DRAIN_INTERVAL_MS);
    // 3 ms per write: the third write crosses the 8 ms budget and ends the drain.
    expect(terms.filter((term) => term.writes.length > 0)).toHaveLength(3);
    expect(s.stats().drainScheduled).toBe(true);
    clock.advance(BACKGROUND_DRAIN_INTERVAL_MS);
    expect(terms.filter((term) => term.writes.length > 0)).toHaveLength(6);
  });

  it("serves background panes round-robin", () => {
    const clock = fakeClock();
    const s = scheduler(clock, { maxWritesPerDrain: 1 });
    const order: string[] = [];
    const names = ["a", "b", "c"];
    const terms = names.map((name) => {
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
    panes[0].write(bytes(BACKGROUND_CHUNK_BYTES * 6));
    panes[1].write(bytes(BACKGROUND_CHUNK_BYTES * 2));
    panes[2].write(bytes(BACKGROUND_CHUNK_BYTES * 2));
    for (let i = 0; i < 8; i += 1) {
      clock.advance(BACKGROUND_DRAIN_INTERVAL_MS);
      terms.forEach((term) => term.parseAll());
    }
    expect(order.slice(0, 6)).toEqual(["a", "b", "c", "a", "b", "c"]);
    expect(order.slice(6)).toEqual(["a", "a"]);
  });

  it("drains the backlog in order before new data on reveal", () => {
    const clock = fakeClock();
    const s = scheduler(clock);
    const term = fakeTerm();
    const pane = s.register(term, { visible: false });
    const first = bytes(BACKGROUND_CHUNK_BYTES * 2 + 100, 1);
    const second = bytes(500, 2);
    const done: string[] = [];
    pane.write(first, () => done.push("first"));
    pane.write(second, () => done.push("second"));
    clock.advance(BACKGROUND_DRAIN_INTERVAL_MS);
    expect(term.writes).toEqual([BACKGROUND_CHUNK_BYTES]);

    pane.setVisible(true);
    const fresh = bytes(10, 3);
    pane.write(fresh, () => done.push("fresh"));
    // The remainder of the partly drained unit, the queued unit, then new data.
    expect(term.writes).toEqual([BACKGROUND_CHUNK_BYTES, first.length - BACKGROUND_CHUNK_BYTES, second.length, 10]);
    term.parseAll();
    expect(term.parsed).toEqual([...first, ...second, ...fresh]);
    expect(done).toEqual(["first", "second", "fresh"]);
    expect(s.stats().drainScheduled).toBe(false);
    expect(s.stats().backgroundBacklogPanes).toBe(0);

    // Hiding again queues new output.
    pane.setVisible(false);
    pane.write(bytes(4));
    expect(term.writes).toHaveLength(4);
    expect(pane.queuedBytes()).toBe(4);
  });

  it("returns a unit's credit only after xterm's callback for its last byte", () => {
    const clock = fakeClock();
    const s = scheduler(clock);
    const term = fakeTerm();
    const pane = s.register(term, { visible: false });
    let credited = 0;
    pane.write(bytes(BACKGROUND_CHUNK_BYTES * 2), () => {
      credited += 1;
    });
    clock.advance(BACKGROUND_DRAIN_INTERVAL_MS);
    term.parseAll();
    expect(credited).toBe(0);
    clock.advance(BACKGROUND_DRAIN_INTERVAL_MS);
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
      highWaterBytes: 64 * 1024,
      lowWaterBytes: 16 * 1024,
      maxPendingBytes: 128 * 1024,
      onPausedChange: (paused) => pauses.push(paused),
      onWritten: () => {
        written += 1;
      },
    });
    expect(writer.pushBytes(bytes(80 * 1024))).toBe(true);
    // Nothing reached xterm yet, but the writer already counts the bytes in flight.
    expect(term.writes).toHaveLength(0);
    expect(pauses).toEqual([true]);
    expect(writer.stats().pendingBytes).toBe(80 * 1024);
    expect(writer.pushBytes(bytes(40 * 1024))).toBe(true);
    // Past the hard cap the chunk is refused whole for the offset replay.
    expect(writer.pushBytes(bytes(40 * 1024))).toBe(false);

    clock.advance(BACKGROUND_DRAIN_INTERVAL_MS);
    term.parseAll();
    expect(written).toBe(0);
    pane.setVisible(true);
    term.parseAll();
    expect(written).toBeGreaterThan(0);
    expect(pauses).toEqual([true, false]);
    expect(writer.stats().pendingBytes).toBe(0);
    expect(term.parsed).toEqual([...bytes(80 * 1024), ...bytes(40 * 1024)]);
  });

  it("never loses, duplicates, or reorders bytes across random scheduling", () => {
    for (const seed of [1, 7, 42, 1337, 9001]) {
      const random = prng(seed);
      const clock = fakeClock();
      const s = scheduler(clock, { chunkBytes: 1 + Math.floor(random() * 64), maxWritesPerDrain: 1 + Math.floor(random() * 3) });
      const panes = Array.from({ length: 4 }, (_, index) => {
        const term = fakeTerm();
        const visible = random() < 0.5;
        const handle = s.register(term, { visible });
        return { term, handle, index, expected: [] as number[], callbacks: [] as number[], unitEnds: [] as number[], next: 0 };
      });
      for (let step = 0; step < 400; step += 1) {
        const pane = panes[Math.floor(random() * panes.length)];
        const roll = random();
        if (roll < 0.45) {
          const unit = pane.next;
          pane.next += 1;
          const chunk = random() < 0.1 ? "" : String.fromCharCode(...bytes(1 + Math.floor(random() * 200), unit));
          const data: OutputData = random() < 0.5 ? chunk : Uint8Array.from(chunk, (ch) => ch.charCodeAt(0));
          pane.expected.push(...Array.from(chunk, (ch) => ch.charCodeAt(0)));
          const end = pane.expected.length;
          pane.unitEnds.push(end);
          pane.handle.write(data, () => {
            // Credit only after every byte of the unit has been parsed.
            expect(pane.term.parsed.length).toBeGreaterThanOrEqual(end);
            pane.callbacks.push(unit);
          });
        } else if (roll < 0.6) {
          pane.handle.setVisible(random() < 0.5);
        } else if (roll < 0.8) {
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
      expect(s.stats().queuedBytes).toBe(0);
      expect(s.stats().drainScheduled).toBe(false);
    }
  });

  it("cleans up a disposed pane with queued and in-flight output", () => {
    const clock = fakeClock();
    const s = scheduler(clock);
    const term = fakeTerm();
    const pane = s.register(term, { visible: false });
    const other = s.register(fakeTerm(), { visible: true });
    let credited = 0;
    pane.write(bytes(BACKGROUND_CHUNK_BYTES * 3), () => {
      credited += 1;
    });
    clock.advance(BACKGROUND_DRAIN_INTERVAL_MS);
    expect(term.writes).toHaveLength(1);
    expect(s.stats()).toMatchObject({ panes: 2, backgroundBacklogPanes: 1 });

    pane.dispose();
    pane.dispose();
    expect(s.stats()).toEqual({ panes: 1, backgroundBacklogPanes: 0, queuedBytes: 0, drainScheduled: false });
    expect(clock.pending()).toBe(0);
    // The late callback of the slice already in xterm is inert.
    term.parseAll();
    clock.advance(BACKGROUND_DRAIN_INTERVAL_MS * 4);
    expect(term.writes).toHaveLength(1);
    expect(credited).toBe(0);
    // Writes and visibility changes after dispose are ignored.
    pane.write(bytes(10));
    pane.setVisible(true);
    expect(term.writes).toHaveLength(1);
    other.dispose();
    expect(s.stats().panes).toBe(0);
  });

  it("keeps the drain timer running only while a pane can drain", () => {
    const clock = fakeClock();
    const s = scheduler(clock);
    const a = fakeTerm();
    const b = fakeTerm();
    const paneA = s.register(a, { visible: false });
    const paneB = s.register(b, { visible: false });
    paneA.write(bytes(BACKGROUND_CHUNK_BYTES * 2));
    paneB.write(bytes(BACKGROUND_CHUNK_BYTES * 2));
    clock.advance(BACKGROUND_DRAIN_INTERVAL_MS);
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
    expect(() => scheduler(clock, { chunkBytes: 0 })).toThrow(RangeError);
    expect(() => scheduler(clock, { maxWritesPerDrain: 0 })).toThrow(RangeError);
    expect(() => scheduler(clock, { maxInFlightSlices: Number.NaN })).toThrow(RangeError);
    expect(() => scheduler(clock, { drainIntervalMs: -1 })).toThrow(RangeError);
    expect(() => scheduler(clock, { drainBudgetMs: -1 })).toThrow(RangeError);
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
});
