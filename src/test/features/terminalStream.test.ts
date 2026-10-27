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
import type { StreamMode } from "../../features/terminal/streamClasses";

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

function setup(options?: {
  highWaterBytes?: number;
  lowWaterBytes?: number;
  maxPendingBytes?: number;
  now?: () => number;
}) {
  const h = fakeTerm();
  const events: OscStreamEvent[] = [];
  const pauses: boolean[] = [];
  const refusals: number[] = [];
  const writer = createTerminalStreamWriter(
    h.term,
    (event) => events.push(event),
    {
      highWaterBytes: options?.highWaterBytes ?? 1024,
      lowWaterBytes: options?.lowWaterBytes ?? 512,
      maxPendingBytes: options?.maxPendingBytes,
      now: options?.now,
      onPausedChange: (paused) => pauses.push(paused),
      onRefused: () => refusals.push(1),
    },
  );
  return { ...h, events, pauses, refusals, writer };
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

describe("terminal stream writer bounded FIFO (T-03 hard backpressure)", () => {
  it("rejects a high watermark that is not below maxPendingBytes", () => {
    const h = fakeTerm();
    expect(() =>
      createTerminalStreamWriter(h.term, () => undefined, {
        highWaterBytes: 8,
        lowWaterBytes: 4,
        maxPendingBytes: 8,
      }),
    ).toThrow(RangeError);
  });

  it("keeps queuedBytes bounded under a sustained producer with a stalled consumer", () => {
    const h = setup({ highWaterBytes: 4, lowWaterBytes: 2, maxPendingBytes: 12 });
    // Producer keeps feeding 4-byte chunks; the consumer never drains.
    expect(h.writer.write([text("aaaa")])).toBe(true);
    expect(h.writer.write([text("bbbb")])).toBe(true);
    expect(h.writer.write([text("cccc")])).toBe(true);
    let maxPending = 0;
    for (let i = 0; i < 100; i += 1) {
      h.writer.write([text("dddd")]);
      maxPending = Math.max(maxPending, h.writer.stats().pendingBytes);
    }
    // 100 sustained writes never pushed pending past the cap, and the whole
    // overflow collapsed into a single refusal episode.
    expect(maxPending).toBe(12);
    expect(h.writer.stats().pendingBytes).toBe(12);
    expect(h.writer.stats().refusedBytes).toBe(400);
    expect(h.refusals.length).toBe(1);
    expect(h.writes.length).toBe(1);
  });

  it("refuses a batch atomically: nothing enqueued, no event dispatched", () => {
    const h = setup({ highWaterBytes: 4, lowWaterBytes: 2, maxPendingBytes: 8 });
    h.writer.write([text("aaaa")]);
    h.writer.write([text("bbbb")]);
    expect(h.writer.write([text("cccc"), notify("lost?")])).toBe(false);
    expect(h.writer.write([notify("also-lost?"), text("dddd")])).toBe(false);
    expect(h.events).toEqual([]);
    expect(h.writer.stats().pendingBytes).toBe(8);
    h.drain();
    expect(h.writtenText()).toBe("aaaabbbb");
    expect(h.events).toEqual([]);
    expect(h.writer.stats().suppressedEvents).toBe(0);
    expect(h.writer.stats().refusedBytes).toBe(8);
  });

  it("re-feeding the refused suffix in original order restores the contiguous stream", () => {
    const h = setup({ highWaterBytes: 4, lowWaterBytes: 2, maxPendingBytes: 10 });
    h.writer.write([text("aaaa")]);
    h.writer.write([text("bbbb")]);
    // The cap is full: every later chunk is refused, and refusal is sticky,
    // so nothing accepted can slip in behind the refused suffix.
    expect(h.writer.write([text("cccc")])).toBe(false);
    expect(h.writer.write([text("ddd")])).toBe(false);
    expect(h.writer.write([text("ee")])).toBe(false);
    h.drain();
    expect(h.writtenText()).toBe("aaaabbbb");
    // The attachment boundary ends the episode; the replay redelivers the
    // refused suffix in its original order and the stream is contiguous.
    h.writer.beginAttachment();
    for (const chunk of ["cccc", "ddd", "ee"]) {
      expect(h.writer.write([text(chunk)])).toBe(true);
    }
    h.drain();
    expect(h.writtenText()).toBe("aaaabbbbccccdddee");
    expect(h.writer.stats().refusedBytes).toBe(9);
  });

  it("pushBytes is transactional: a refused chunk never reaches the OSC filter", () => {
    const h = setup({ highWaterBytes: 8, lowWaterBytes: 4, maxPendingBytes: 24 });
    // Text first, then a partial OSC sequence the filter holds back; only
    // the text counts toward the writer's pending bytes.
    expect(h.writer.pushBytes(enc.encode("0123456789ab"))).toBe(true);
    expect(h.writer.pushBytes(enc.encode("\x1b]9;part"))).toBe(true);
    expect(h.writer.stats().pendingBytes).toBe(20);
    // This chunk would complete the sequence, but it does not fit the cap:
    // it is refused whole and the filter's partial state stays intact.
    const completion = " done\x07" + "t".repeat(10);
    expect(h.writer.pushBytes(enc.encode(completion))).toBe(false);
    expect(h.events).toEqual([]);
    h.writer.beginAttachment();
    h.drain();
    // The replay redelivers the same completion: the sequence completes
    // exactly once and the stream stays in order.
    expect(h.writer.pushBytes(enc.encode(completion))).toBe(true);
    h.drain();
    expect(h.events).toEqual([{ kind: "notification", body: "part done" }]);
    expect(h.writtenText()).toBe("0123456789ab" + "t".repeat(10));
  });

  it("the refusal episode is sticky until beginAttachment", () => {
    const h = setup({ highWaterBytes: 4, lowWaterBytes: 2, maxPendingBytes: 6 });
    h.writer.write([text("aaaa")]);
    h.writer.write([text("bb")]);
    expect(h.writer.write([text("cc")])).toBe(false);
    expect(h.refusals.length).toBe(1);
    h.drain();
    // Still refused even with room: the episode ends only at the attachment
    // boundary, so the accepted prefix stays contiguous.
    expect(h.writer.write([text("cc")])).toBe(false);
    expect(h.refusals.length).toBe(1);
    h.writer.beginAttachment();
    expect(h.writer.write([text("cc")])).toBe(true);
    h.drain();
    expect(h.writtenText()).toBe("aaaabbcc");
    expect(h.refusals.length).toBe(1);
  });

  it("a split OSC refused at the cap keeps the filter intact and replays byte-identically", () => {
    const h = setup({ highWaterBytes: 4, lowWaterBytes: 2, maxPendingBytes: 8 });
    h.writer.pushBytes(enc.encode("aaaa"));
    // The partial sequence fits the cap and is held by the filter; it counts
    // toward pending from now on.
    expect(h.writer.pushBytes(enc.encode("\x1b]0;"))).toBe(true);
    expect(h.writer.stats().pendingBytes).toBe(8);
    // The completion would fit the raw precheck, but the assembled batch
    // (held prefix + new bytes) exceeds the cap: refused whole, filter intact.
    expect(h.writer.pushBytes(enc.encode("x\x07"))).toBe(false);
    expect(h.events).toEqual([]);
    h.writer.beginAttachment();
    h.drain();
    expect(h.writer.pushBytes(enc.encode("x\x07"))).toBe(true);
    h.drain();
    // Byte-identical replay: the full title sequence reaches xterm once.
    expect(h.writtenText()).toBe("aaaa\x1b]0;x\x07");
    expect(h.events).toEqual([]);
  });

  it("a split intercepted OSC refused at the cap dispatches its event exactly once on replay", () => {
    const h = setup({ highWaterBytes: 4, lowWaterBytes: 2, maxPendingBytes: 8 });
    h.writer.pushBytes(enc.encode("aaaa"));
    expect(h.writer.pushBytes(enc.encode("\x1b]9;"))).toBe(true);
    expect(h.writer.pushBytes(enc.encode("x\x07"))).toBe(false);
    expect(h.events).toEqual([]);
    h.writer.beginAttachment();
    h.drain();
    expect(h.writer.pushBytes(enc.encode("x\x07"))).toBe(true);
    h.drain();
    expect(h.events).toEqual([{ kind: "notification", body: "x" }]);
    expect(h.writtenText()).toBe("aaaa");
  });

  it("event-only batches are always accepted even when the FIFO is full", () => {
    const h = setup({ highWaterBytes: 4, lowWaterBytes: 2, maxPendingBytes: 6 });
    h.writer.write([text("aaaa")]);
    h.writer.write([text("bb")]);
    expect(h.writer.stats().pendingBytes).toBe(6);
    expect(h.writer.write([notify("marker")])).toBe(true);
    expect(h.writer.write([text("cc")])).toBe(false);
    h.drain();
    expect(h.events).toEqual([{ kind: "notification", body: "marker" }]);
    expect(h.writtenText()).toBe("aaaabb");
  });

  it("writeLocalText bypasses the cap so notices are never lost", () => {
    const h = setup({ highWaterBytes: 4, lowWaterBytes: 2, maxPendingBytes: 6 });
    h.writer.write([text("aaaa")]);
    h.writer.write([text("bb")]);
    expect(h.writer.write([text("cc")])).toBe(false);
    h.writer.writeLocalText("NOTICE\n");
    h.drain();
    expect(h.writtenText()).toBe("aaaabbNOTICE\n");
  });

  it("bounds a real-xterm flood at the hard cap with zero loss after replay", async () => {
    const term = new Terminal({ cols: 80, rows: 24, scrollback: 20000, allowProposedApi: true });
    const writer = createTerminalStreamWriter(term, () => undefined, {
      highWaterBytes: 4096,
      lowWaterBytes: 1024,
      maxPendingBytes: 16384,
    });
    const line = enc.encode("line-\r\n");
    const refused: Uint8Array[] = [];
    let accepted = 0;
    let maxPending = 0;
    // 10000 chunks = 70000 bytes against a 16 KiB cap, consumer stalled.
    for (let i = 0; i < 10000; i += 1) {
      if (writer.write([{ kind: "text", data: line }])) accepted += 1;
      else refused.push(line);
      maxPending = Math.max(maxPending, writer.stats().pendingBytes);
    }
    expect(accepted + refused.length).toBe(10000);
    expect(accepted).toBeGreaterThan(0);
    expect(refused.length).toBeGreaterThan(0);
    expect(maxPending).toBeLessThanOrEqual(16384);
    expect(writer.stats().refusedBytes).toBe(refused.length * line.length);
    await drainWriter(writer, term);
    // The attachment boundary ends the episode; the replay redelivers the
    // refused suffix in bounded slices: every byte arrives, nothing is lost.
    writer.beginAttachment();
    let replayed = 0;
    while (replayed < refused.length) {
      const slice = refused.slice(replayed, replayed + 1000);
      for (const chunk of slice) {
        expect(writer.write([{ kind: "text", data: chunk }])).toBe(true);
      }
      replayed += slice.length;
      await drainWriter(writer, term);
    }
    const buffer = term.buffer.active;
    expect(buffer.length).toBe(10001);
    for (let i = 0; i < 10000; i += 1) {
      expect(buffer.getLine(i)?.translateToString(true)).toBe("line-");
    }
    term.dispose();
  });
});

describe("terminal stream writer replayed bytes", () => {
  it("reports the parse mode exactly around replayed and answered bytes in a real xterm", async () => {
    const term = new Terminal({ cols: 40, rows: 10, scrollback: 1000, allowProposedApi: true });
    let mode: StreamMode = "live";
    const replies: { data: string; mode: StreamMode }[] = [];
    term.onData((data) => replies.push({ data, mode }));
    const events: { event: OscStreamEvent; mode: StreamMode }[] = [];
    const writer = createTerminalStreamWriter(term, (event, eventMode) => events.push({ event, mode: eventMode }), {
      highWaterBytes: 1 << 20,
      lowWaterBytes: 1 << 19,
      onReplayChange: (next) => {
        mode = next;
      },
    });
    writer.pushBytes(enc.encode("old\r\n\x1b[c\x1b[6n\x1b]9;old note\x07\x1b]52;c;b2xk\x07"), "replay");
    writer.pushBytes(enc.encode("mid\r\n\x1b[6n\x1b]9;mid note\x07"), "answered");
    writer.pushBytes(enc.encode("new\r\n\x1b[c\x1b]9;new note\x07"));
    await drainWriter(writer, term);
    expect(mode).toBe("live");
    // Every reply xterm produced is attributed to the mode of the bytes that
    // asked for it.
    expect(replies.map((reply) => reply.mode)).toEqual(["replay", "replay", "answered", "live"]);
    expect(replies[3].data).toMatch(/^\x1b\[\?[\d;]*c$/);
    expect(events).toEqual([
      { event: { kind: "notification", body: "old note" }, mode: "replay" },
      { event: { kind: "clipboard", payload: "c;b2xk" }, mode: "replay" },
      { event: { kind: "notification", body: "mid note" }, mode: "answered" },
      { event: { kind: "notification", body: "new note" }, mode: "live" },
    ]);
    expect(term.buffer.active.getLine(0)?.translateToString(true)).toBe("old");
    expect(term.buffer.active.getLine(2)?.translateToString(true)).toBe("new");
    term.dispose();
  });
});
