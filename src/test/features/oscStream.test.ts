import { describe, expect, it } from "vitest";
import {
  createOscStreamFilter,
  type OscStreamEvent,
} from "../../features/terminal/oscStream";

const enc = new TextEncoder();
const dec = new TextDecoder();

function harness() {
  const events: OscStreamEvent[] = [];
  const filter = createOscStreamFilter();
  const fed: Uint8Array[] = [];
  const feed = (chunk: Uint8Array | string) => {
    const bytes = typeof chunk === "string" ? enc.encode(chunk) : chunk;
    for (const segment of filter.push(bytes)) {
      if (segment.kind === "event") events.push(segment.event);
      else if (segment.data.length > 0) fed.push(segment.data);
    }
  };
  const output = () => {
    const total = fed.reduce((n, part) => n + part.length, 0);
    const merged = new Uint8Array(total);
    let offset = 0;
    for (const part of fed) {
      merged.set(part, offset);
      offset += part.length;
    }
    return dec.decode(merged);
  };
  return { events, filter, feed, output };
}

describe("OSC stream filter", () => {
  it("passes plain text through unchanged with no events", () => {
    const h = harness();
    h.feed("hello \x1b[31mred\x1b[0m world\r\n");
    expect(h.output()).toBe("hello \x1b[31mred\x1b[0m world\r\n");
    expect(h.events).toEqual([]);
  });

  it("strips OSC 9 with BEL terminator and emits a notification", () => {
    const h = harness();
    h.feed("before\x1b]9;build finished\x07after");
    expect(h.output()).toBe("beforeafter");
    expect(h.events).toEqual([{ kind: "notification", body: "build finished" }]);
  });

  it("strips OSC 9 with ST terminator", () => {
    const h = harness();
    h.feed("a\x1b]9;deploy done\x1b\\b");
    expect(h.output()).toBe("ab");
    expect(h.events).toEqual([{ kind: "notification", body: "deploy done" }]);
  });

  it("strips OSC 52 and emits the raw payload", () => {
    const h = harness();
    h.feed("x\x1b]52;c;aGVsbG8=\x07y");
    expect(h.output()).toBe("xy");
    expect(h.events).toEqual([{ kind: "clipboard", payload: "c;aGVsbG8=" }]);
  });

  it("keeps OSC 0/2 byte-identical for xterm to handle titles itself", () => {
    const h = harness();
    const stream = "a\x1b]0;title one\x07b\x1b]2;title two\x1b\\c";
    h.feed(stream);
    expect(h.output()).toBe(stream);
    expect(h.events).toEqual([]);
  });

  it("keeps unknown and malformed OSC sequences verbatim", () => {
    const h = harness();
    const stream = "a\x1b]4;c;#ff0000\x07b\x1b]1337;file=xyz\x1b\\c\x1b]9\x07d\x1b];nosemi\x07e";
    h.feed(stream);
    expect(h.output()).toBe(stream);
    expect(h.events).toEqual([]);
  });

  it("treats an ESC not followed by backslash as payload content", () => {
    const h = harness();
    h.feed("\x1b]9;a\x1bxb\x07");
    expect(h.events).toEqual([{ kind: "notification", body: "a\x1bxb" }]);
  });

  it("handles sequences split at every possible chunk boundary", () => {
    const stream =
      "one \x1b]9;first note\x07 two \x1b]52;c;aGVsbG8=\x07 three \x1b]9;second\x1b\\ four";
    const bytes = enc.encode(stream);
    for (let split = 0; split <= bytes.length; split++) {
      const h = harness();
      h.feed(bytes.subarray(0, split));
      h.feed(bytes.subarray(split));
      expect(h.output()).toBe("one  two  three  four");
      expect(h.events).toEqual([
        { kind: "notification", body: "first note" },
        { kind: "clipboard", payload: "c;aGVsbG8=" },
        { kind: "notification", body: "second" },
      ]);
    }
  });

  it("handles byte-by-byte feeding", () => {
    const h = harness();
    const stream = "ab\x1b]9;ping\x07cd\x1b]52;p;eA==\x07ef";
    for (const byte of enc.encode(stream)) {
      h.feed(new Uint8Array([byte]));
    }
    expect(h.output()).toBe("abcdef");
    expect(h.events).toEqual([
      { kind: "notification", body: "ping" },
      { kind: "clipboard", payload: "p;eA==" },
    ]);
  });

  it("holds back a lone trailing ESC and completes it on the next chunk", () => {
    const h = harness();
    h.feed("abc\x1b");
    expect(h.output()).toBe("abc");
    h.feed("]9;late\x07");
    expect(h.output()).toBe("abc");
    expect(h.events).toEqual([{ kind: "notification", body: "late" }]);
  });

  it("flush releases held-back bytes", () => {
    const h = harness();
    h.feed("abc\x1b");
    expect(h.output()).toBe("abc");
    const rest = h.filter.flush();
    expect(dec.decode(rest)).toBe("\x1b");
    expect(h.filter.flush().length).toBe(0);
  });

  it("degrades to passthrough for an oversized unterminated sequence", () => {
    const h = harness();
    h.feed("\x1b]9;" + "a".repeat((1 << 20) + 16));
    expect(h.output().length).toBe(4 + (1 << 20) + 16);
    expect(h.events).toEqual([]);
    h.feed("\x1b]9;ok\x07");
    expect(h.events).toEqual([{ kind: "notification", body: "ok" }]);
  });

  it("an unterminated OSC consumes bytes up to the next terminator, like a real terminal", () => {
    const h = harness();
    h.feed("a\x1b]52;c;aGVsbG8= three \x1b]9;second\x1b\\b");
    expect(h.output()).toBe("ab");
    expect(h.events).toEqual([
      { kind: "clipboard", payload: "c;aGVsbG8= three \x1b]9;second" },
    ]);
  });

  it("decodes payload bytes as UTF-8 without throwing on invalid sequences", () => {
    const h = harness();
    const payload = enc.encode("构建完成 🎉");
    const prefix = enc.encode("\x1b]9;");
    const whole = new Uint8Array(prefix.length + payload.length + 1);
    whole.set(prefix, 0);
    whole.set(payload, prefix.length);
    whole[whole.length - 1] = 0x07;
    h.feed(whole);
    expect(h.events).toEqual([{ kind: "notification", body: "构建完成 🎉" }]);

    const broken = new Uint8Array([0x1b, 0x5d, 0x39, 0x3b, 0xff, 0xfe, 0x07]);
    h.feed(broken);
    expect(h.events[1]?.kind).toBe("notification");
  });

  it("preserves output and events across deterministic randomized chunking", () => {
    const bytes = enc.encode(
      "start\x1b]9;构建完成 🎉\x07middle\x1b]52;c;aGVsbG8=\x1b\\middle2\x1b]0;title\x07end1\x1b]133;D;23\x07end2",
    );
    for (let seed = 1; seed <= 32; seed++) {
      let state = seed;
      const random = () => {
        state = (state * 1664525 + 1013904223) >>> 0;
        return state;
      };
      const h = harness();
      for (let offset = 0; offset < bytes.length; ) {
        const size = 1 + (random() % 23);
        h.feed(bytes.subarray(offset, offset + size));
        offset += size;
      }
      expect(h.output(), `seed ${seed}`).toBe("startmiddlemiddle2\x1b]0;title\x07end1end2");
      expect(h.events, `seed ${seed}`).toEqual([
        { kind: "notification", body: "构建完成 🎉" },
        { kind: "clipboard", payload: "c;aGVsbG8=" },
        { kind: "command", phase: "D", exitCode: 23 },
      ]);
    }
  });

  it("recognizes ST and BEL terminators arriving in later chunks", () => {
    const h = harness();
    h.feed("a\x1b]9;one\x1b");
    expect(h.output()).toBe("a");
    expect(h.events).toEqual([]);
    h.feed("\\b\x1b]52;c;eA==");
    expect(h.output()).toBe("ab");
    expect(h.events).toEqual([{ kind: "notification", body: "one" }]);
    h.feed("\x07c\x1b]133;D;");
    expect(h.output()).toBe("abc");
    expect(h.events).toEqual([
      { kind: "notification", body: "one" },
      { kind: "clipboard", payload: "c;eA==" },
    ]);
    h.feed("7");
    h.feed("\x07d");
    expect(h.output()).toBe("abcd");
    expect(h.events).toEqual([
      { kind: "notification", body: "one" },
      { kind: "clipboard", payload: "c;eA==" },
      { kind: "command", phase: "D", exitCode: 7 },
    ]);
  });

  it("keeps text and events in stream order when pending sequences complete", () => {
    const filter = createOscStreamFilter();
    const ordered: string[] = [];
    const feed = (chunk: string) => {
      for (const segment of filter.push(enc.encode(chunk))) {
        if (segment.kind === "text") ordered.push(`text:${dec.decode(segment.data)}`);
        else ordered.push(`event:${segment.event.kind}`);
      }
    };
    feed("before\x1b]9;one");
    feed("\x07middle\x1b]52;c;eA==\x1b");
    feed("\\after\x1b]133;D;4\x07tail");
    expect(ordered).toEqual([
      "text:before",
      "event:notification",
      "text:middle",
      "event:clipboard",
      "text:after",
      "event:command",
      "text:tail",
    ]);
  });

  it("holds exactly 1 MiB and passes through one byte over the limit across chunks", () => {
    for (const length of [1 << 20, (1 << 20) + 1]) {
      const bytes = new Uint8Array(length);
      bytes.fill(0x61);
      bytes.set([0x1b, 0x5d, 0x39, 0x3b]);
      const h = harness();
      for (let offset = 0; offset < bytes.length; offset += 64) {
        h.feed(bytes.subarray(offset, offset + 64));
      }
      expect(h.events).toEqual([]);
      if (length === 1 << 20) {
        expect(h.output()).toBe("");
        const rest = h.filter.flush();
        expect(rest.length).toBe(bytes.length);
        expect(rest.every((byte, index) => byte === bytes[index])).toBe(true);
      } else {
        expect(h.output()).toBe(dec.decode(bytes));
        expect(h.filter.flush().length).toBe(0);
      }
    }
  });

  it("processes a 1 MiB unterminated OSC in 64-byte chunks in under 100ms", () => {
    const bytes = new Uint8Array(1 << 20);
    bytes.fill(0x61);
    bytes.set([0x1b, 0x5d, 0x39, 0x3b]);
    const h = harness();
    const started = performance.now();
    for (let offset = 0; offset < bytes.length; offset += 64) {
      h.feed(bytes.subarray(offset, offset + 64));
    }
    const rest = h.filter.flush();
    const elapsedMs = performance.now() - started;
    expect(h.output()).toBe("");
    expect(h.events).toEqual([]);
    expect(rest.length).toBe(bytes.length);
    expect(rest.every((byte, index) => byte === bytes[index])).toBe(true);
    expect(elapsedMs).toBeLessThan(100);
  });
});
