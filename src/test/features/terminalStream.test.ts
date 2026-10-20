import { describe, expect, it } from "vitest";
import { Terminal } from "@xterm/xterm";
import {
  createTerminalStreamWriter,
  type TerminalStreamWriter,
} from "../../features/terminal/terminalStream";
import {
  createOscStreamFilter,
  type OscStreamEvent,
  type OscStreamSegment,
} from "../../features/terminal/oscStream";

const enc = new TextEncoder();
const dec = new TextDecoder();

function fakeTerm() {
  const writes: Uint8Array[] = [];
  const callbacks: (() => void)[] = [];
  const term = {
    write(data: string | Uint8Array, callback?: () => void) {
      writes.push(typeof data === "string" ? enc.encode(data) : data.slice());
      if (callback) callbacks.push(callback);
    },
  };
  const writtenText = () => {
    const total = writes.reduce((n, part) => n + part.length, 0);
    const merged = new Uint8Array(total);
    let offset = 0;
    for (const part of writes) {
      merged.set(part, offset);
      offset += part.length;
    }
    return dec.decode(merged);
  };
  const drain = (count?: number) => {
    if (count === undefined) {
      let guard = 0;
      while (callbacks.length > 0 && guard++ < 10_000) callbacks.shift()!();
      return;
    }
    for (let i = 0; i < count && callbacks.length > 0; i += 1) callbacks.shift()!();
  };
  return { term, writes, callbacks, writtenText, drain };
}

const text = (data: string): OscStreamSegment => ({ kind: "text", data: enc.encode(data) });
const notify = (body: string): OscStreamSegment => ({
  kind: "event",
  event: { kind: "notification", body },
});

function setup(options?: { highWaterBytes?: number; lowWaterBytes?: number; now?: () => number }) {
  const h = fakeTerm();
  const events: OscStreamEvent[] = [];
  const pauses: boolean[] = [];
  const writer = createTerminalStreamWriter(
    h.term,
    (event) => events.push(event),
    {
      highWaterBytes: options?.highWaterBytes ?? 1024,
      lowWaterBytes: options?.lowWaterBytes ?? 512,
      now: options?.now,
      onPausedChange: (paused) => pauses.push(paused),
    },
  );
  return { ...h, events, pauses, writer };
}

async function drainWriter(writer: TerminalStreamWriter, term: Terminal) {
  for (let i = 0; i < 500; i += 1) {
    if (writer.stats().pendingBytes === 0) return;
    await new Promise<void>((resolve) => term.write("", resolve));
  }
  throw new Error("stream writer did not drain");
}

describe("terminal stream writer generation isolation (T-01)", () => {
  it("dispatches each event after the text preceding it has been parsed", () => {
    const h = setup();
    h.writer.write([text("one\r\n"), notify("a"), text("two\r\n"), notify("b")]);
    expect(h.writes.length).toBe(2);
    expect(h.events).toEqual([]);
    h.drain();
    expect(h.events).toEqual([
      { kind: "notification", body: "a" },
      { kind: "notification", body: "b" },
    ]);
  });

  it("dispatches an event immediately when no write is in flight", () => {
    const h = setup();
    h.writer.write([notify("now")]);
    expect(h.events).toEqual([{ kind: "notification", body: "now" }]);
  });

  it("suppresses events whose deferred write callback fires after a generation bump", () => {
    const h = setup();
    h.writer.write([text("before\r\n"), notify("stale")]);
    expect(h.events).toEqual([]);
    h.writer.nextGeneration();
    h.drain();
    expect(h.events).toEqual([]);
    expect(h.writer.stats().suppressedEvents).toBe(1);
    expect(h.writtenText()).toBe("before\r\n");
  });

  it("dispatches fresh events queued after a generation bump in stream order", () => {
    const h = setup();
    h.writer.write([text("a"), notify("old")]);
    h.writer.nextGeneration();
    h.writer.write([text("b"), notify("fresh")]);
    h.drain();
    expect(h.events).toEqual([{ kind: "notification", body: "fresh" }]);
    expect(h.writer.stats().suppressedEvents).toBe(1);
    expect(h.writtenText()).toBe("ab");
  });

  it("cancels queued events on a generation bump but keeps queued text in order", () => {
    const h = setup({ highWaterBytes: 4, lowWaterBytes: 2 });
    h.writer.write([text("aa"), notify("queued-event")]);
    h.writer.write([text("bb")]);
    expect(h.writer.stats().paused).toBe(true);
    h.writer.nextGeneration();
    h.drain();
    expect(h.events).toEqual([]);
    expect(h.writtenText()).toBe("aabb");
    expect(h.writer.stats().suppressedEvents).toBe(1);
  });

  it("queues an event that arrives while an old-generation write is in flight under the new generation", () => {
    const h = setup();
    h.writer.write([text("a")]);
    h.writer.nextGeneration();
    h.writer.write([notify("fresh")]);
    expect(h.events).toEqual([]);
    h.drain();
    expect(h.events).toEqual([{ kind: "notification", body: "fresh" }]);
    expect(h.writer.stats().suppressedEvents).toBe(0);
    expect(h.writtenText()).toBe("a");
  });

  it("increments and reports the generation", () => {
    const h = setup();
    expect(h.writer.stats().generation).toBe(0);
    expect(h.writer.nextGeneration()).toBe(1);
    expect(h.writer.nextGeneration()).toBe(2);
    expect(h.writer.stats().generation).toBe(2);
  });

  it("suppresses stale events across a real xterm parse backlog while preserving text order", async () => {
    const term = new Terminal({ cols: 40, rows: 10, scrollback: 1000, allowProposedApi: true });
    const events: OscStreamEvent[] = [];
    const filter = createOscStreamFilter();
    const writer = createTerminalStreamWriter(term, (event) => events.push(event), {
      highWaterBytes: 1 << 20,
      lowWaterBytes: 1 << 19,
    });
    writer.write(filter.push(enc.encode("line zero\r\n".repeat(200))));
    writer.write(filter.push(enc.encode("\x1b]9;stale notification\x07")));
    // Bump while xterm still holds the backlog: the deferred callback must not
    // dispatch the pre-expiry event, but the text stays in order.
    writer.nextGeneration();
    writer.write(filter.push(enc.encode("fresh line\r\n\x1b]9;fresh notification\x07")));
    await drainWriter(writer, term);
    expect(events).toEqual([{ kind: "notification", body: "fresh notification" }]);
    expect(writer.stats().suppressedEvents).toBe(1);
    const buffer = term.buffer.active;
    expect(buffer.getLine(0)?.translateToString(true)).toBe("line zero");
    expect(buffer.getLine(200)?.translateToString(true)).toBe("fresh line");
    term.dispose();
  });
});

describe("terminal stream writer parsed watermarks (T-03)", () => {
  it("rejects a low watermark that is not below the high watermark", () => {
    const h = fakeTerm();
    expect(() =>
      createTerminalStreamWriter(h.term, () => undefined, { highWaterBytes: 4, lowWaterBytes: 4 }),
    ).toThrow(RangeError);
  });

  it("pauses the pump at the high watermark and resumes at the low watermark without losing bytes", () => {
    const h = setup({ highWaterBytes: 4, lowWaterBytes: 2 });
    h.writer.write([text("aa")]);
    expect(h.writer.stats().paused).toBe(false);
    h.writer.write([text("bb")]);
    expect(h.writer.stats().paused).toBe(true);
    expect(h.pauses).toEqual([true]);
    h.writer.write([text("cc")]);
    expect(h.writes.length).toBe(2);
    expect(h.writer.stats().pendingBytes).toBe(6);

    h.drain(1);
    expect(h.writer.stats().paused).toBe(true);
    expect(h.writer.stats().pendingBytes).toBe(4);
    h.drain(1);
    expect(h.writer.stats().paused).toBe(false);
    expect(h.pauses).toEqual([true, false]);
    h.drain();
    expect(h.writtenText()).toBe("aabbcc");
    expect(h.writer.stats().pendingBytes).toBe(0);
  });

  it("pauses exactly at the high watermark even with an empty queue", () => {
    const h = setup({ highWaterBytes: 4, lowWaterBytes: 2 });
    h.writer.write([text("abcd")]);
    expect(h.writer.stats().paused).toBe(true);
    expect(h.pauses).toEqual([true]);
    h.writer.write([text("ef")]);
    expect(h.writes.length).toBe(1);
    h.drain(1);
    expect(h.writer.stats().paused).toBe(false);
    h.drain();
    expect(h.writtenText()).toBe("abcdef");
  });

  it("does not emit pause transitions while the backlog stays below the high watermark", () => {
    const h = setup({ highWaterBytes: 8, lowWaterBytes: 4 });
    for (let i = 0; i < 10; i += 1) {
      h.writer.write([text("x")]);
      h.drain();
    }
    expect(h.pauses).toEqual([]);
    expect(h.writer.stats().paused).toBe(false);
  });

  it("reports pendingBytes and the oldest pending age", () => {
    let clock = 10_000;
    const h = setup({ highWaterBytes: 4, lowWaterBytes: 1, now: () => clock });
    h.writer.write([text("aa")]);
    clock = 10_300;
    h.writer.write([text("bb")]);
    expect(h.writer.stats().paused).toBe(true);
    clock = 10_500;
    const stats = h.writer.stats();
    expect(stats.pendingBytes).toBe(4);
    expect(stats.oldestPendingAgeMs).toBe(500);
    expect(stats.inFlightWrites).toBe(2);
    h.drain(1);
    clock = 10_900;
    const after = h.writer.stats();
    expect(after.pendingBytes).toBe(2);
    expect(after.oldestPendingAgeMs).toBe(600);
    h.drain();
    expect(h.writer.stats().oldestPendingAgeMs).toBe(0);
  });

  it("keeps local text in stream order behind a paused backlog", () => {
    const h = setup({ highWaterBytes: 4, lowWaterBytes: 2 });
    h.writer.write([text("aa")]);
    h.writer.write([text("bb")]);
    h.writer.writeLocalText("NOTICE\n");
    h.writer.write([text("cc")]);
    h.drain();
    expect(h.writtenText()).toBe("aabbNOTICE\ncc");
  });

  it("dispatches events of resumed units in order after a pause", () => {
    const h = setup({ highWaterBytes: 4, lowWaterBytes: 2 });
    h.writer.write([text("aa"), notify("first")]);
    h.writer.write([text("bb"), notify("second")]);
    h.writer.write([text("cc")]);
    h.drain();
    expect(h.events).toEqual([
      { kind: "notification", body: "first" },
      { kind: "notification", body: "second" },
    ]);
    expect(h.writtenText()).toBe("aabbcc");
  });

  it("bounds the unparsed backlog handed to a real xterm and loses no bytes", async () => {
    const term = new Terminal({ cols: 80, rows: 24, scrollback: 5000, allowProposedApi: true });
    const pauses: boolean[] = [];
    let handed = 0;
    const original = term.write.bind(term);
    (term as unknown as { write: (data: string | Uint8Array, cb?: () => void) => void }).write = (
      data: string | Uint8Array,
      cb?: () => void,
    ) => {
      handed += typeof data === "string" ? data.length : data.length;
      original(data, cb);
    };
    const writer = createTerminalStreamWriter(term, () => undefined, {
      highWaterBytes: 4096,
      lowWaterBytes: 1024,
      onPausedChange: (paused) => pauses.push(paused),
    });
    const line = enc.encode("line-\r\n");
    for (let i = 0; i < 1000; i += 1) writer.write([{ kind: "text", data: line }]);
    expect(pauses).toContain(true);
    // The pump stopped at the high watermark: xterm's own buffer stayed small
    // instead of absorbing the whole flood.
    expect(handed).toBeLessThanOrEqual(4096 + line.length);
    await drainWriter(writer, term);
    expect(handed).toBe(7000);
    expect(pauses).toContain(false);
    const buffer = term.buffer.active;
    expect(buffer.length).toBe(1001);
    for (let i = 0; i < 1000; i += 1) {
      expect(buffer.getLine(i)?.translateToString(true)).toBe("line-");
    }
    term.dispose();
  });
});
