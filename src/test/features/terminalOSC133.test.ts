import { describe, expect, it } from "vitest";
import {
  createOscStreamFilter,
  type OscStreamEvent,
} from "../../features/terminal/oscStream";

const enc = new TextEncoder();
const dec = new TextDecoder();

function harness() {
  const events: OscStreamEvent[] = [];
  const filter = createOscStreamFilter((e) => events.push(e));
  const fed: Uint8Array[] = [];
  const feed = (chunk: Uint8Array | string) => {
    const bytes = typeof chunk === "string" ? enc.encode(chunk) : chunk;
    const out = filter.push(bytes);
    if (out.length > 0) fed.push(out);
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

describe("OSC 133 command lifecycle", () => {
  it("strips 133;A/B/C and emits command events without exit codes", () => {
    const h = harness();
    h.feed("prompt\x1b]133;A\x07\x1b]133;B\x1b\\$ ls\r\n\x1b]133;C\x07");
    expect(h.output()).toBe("prompt$ ls\r\n");
    expect(h.events).toEqual([
      { kind: "command", phase: "A", exitCode: null },
      { kind: "command", phase: "B", exitCode: null },
      { kind: "command", phase: "C", exitCode: null },
    ]);
  });

  it("parses 133;D;<exit> into an exit code", () => {
    const h = harness();
    h.feed("done\x1b]133;D;1\x07next");
    expect(h.output()).toBe("donenext");
    expect(h.events).toEqual([{ kind: "command", phase: "D", exitCode: 1 }]);
  });

  it("accepts 133;D without an exit code", () => {
    const h = harness();
    h.feed("a\x1b]133;D\x07b\x1b]133;D;\x07c");
    expect(h.output()).toBe("abc");
    expect(h.events).toEqual([
      { kind: "command", phase: "D", exitCode: null },
      { kind: "command", phase: "D", exitCode: null },
    ]);
  });

  it("accepts large exit codes", () => {
    const h = harness();
    h.feed("\x1b]133;D;130\x07");
    expect(h.events).toEqual([{ kind: "command", phase: "D", exitCode: 130 }]);
  });

  it("keeps malformed 133 payloads verbatim", () => {
    const h = harness();
    const stream = "a\x1b]133\x07b\x1b]133;\x07c\x1b]133;X\x07d\x1b]133;D;12a\x07e\x1b]133;DD\x07f";
    h.feed(stream);
    expect(h.output()).toBe(stream);
    expect(h.events).toEqual([]);
  });

  it("handles sequences split at every possible chunk boundary", () => {
    const stream = "one \x1b]133;D;2\x07 two \x1b]133;C\x1b\\ three";
    const bytes = enc.encode(stream);
    for (let split = 0; split <= bytes.length; split++) {
      const h = harness();
      h.feed(bytes.subarray(0, split));
      h.feed(bytes.subarray(split));
      expect(h.output()).toBe("one  two  three");
      expect(h.events).toEqual([
        { kind: "command", phase: "D", exitCode: 2 },
        { kind: "command", phase: "C", exitCode: null },
      ]);
    }
  });

  it("mixes 133 with OSC 9/52 handling", () => {
    const h = harness();
    h.feed("\x1b]133;C\x07\x1b]9;note\x07\x1b]133;D;0\x07\x1b]52;c;aGk=\x07");
    expect(h.output()).toBe("");
    expect(h.events).toEqual([
      { kind: "command", phase: "C", exitCode: null },
      { kind: "notification", body: "note" },
      { kind: "command", phase: "D", exitCode: 0 },
      { kind: "clipboard", payload: "c;aGk=" },
    ]);
  });
});
