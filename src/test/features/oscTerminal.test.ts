import { describe, expect, it } from "vitest";
import { Terminal } from "@xterm/xterm";
import {
  createOscStreamFilter,
  type OscStreamEvent,
} from "../../features/terminal/oscStream";
import { decodeOsc52Payload } from "../../features/terminal/oscHandlers";

const enc = new TextEncoder();

function wire() {
  const term = new Terminal({ allowProposedApi: true, scrollback: 1000, cols: 80, rows: 24 });
  const events: OscStreamEvent[] = [];
  const titles: string[] = [];
  term.onTitleChange((t) => titles.push(t));
  const filter = createOscStreamFilter((e) => events.push(e));
  const feed = (bytes: Uint8Array) => {
    const out = filter.push(bytes);
    if (out.length > 0) term.write(out);
  };
  const settle = () => new Promise<void>((r) => setTimeout(r, 40));
  const line = (n: number) => term.buffer.active.getLine(n)?.translateToString(true) ?? "";
  return { term, events, titles, feed, settle, line, filter };
}

describe("OSC sequences through a real xterm terminal", () => {
  it("titles update xterm, notifications/clipboard are intercepted, screen stays clean", async () => {
    const h = wire();
    h.feed(
      enc.encode(
        "before \x1b]0;tab title\x07 mid \x1b]9;build done\x07 \x1b]52;c;aGVsbG8=\x07 after",
      ),
    );
    await h.settle();
    expect(h.titles).toEqual(["tab title"]);
    expect(h.events).toEqual([
      { kind: "notification", body: "build done" },
      { kind: "clipboard", payload: "c;aGVsbG8=" },
    ]);
    expect(h.line(0)).toBe("before  mid   after");
    h.term.dispose();
  });

  it("byte-by-byte feeding produces the same results", async () => {
    const h = wire();
    const stream = "ab\x1b]2;remote\x07cd\x1b]9;ping\x1b\\ef";
    for (const byte of enc.encode(stream)) {
      h.feed(new Uint8Array([byte]));
    }
    await h.settle();
    expect(h.titles).toEqual(["remote"]);
    expect(h.events).toEqual([{ kind: "notification", body: "ping" }]);
    expect(h.line(0)).toBe("abcdef");
    h.term.dispose();
  });

  it("OSC 52 payload decodes to the original unicode text end to end", async () => {
    const h = wire();
    const text = "hello 剪贴板 🎉";
    const b64 = btoa(String.fromCharCode(...enc.encode(text)));
    h.feed(enc.encode(`\x1b]52;c;${b64}\x07`));
    await h.settle();
    expect(h.events).toEqual([{ kind: "clipboard", payload: `c;${b64}` }]);
    const clip = h.events[0];
    expect(clip?.kind === "clipboard" && decodeOsc52Payload(clip.payload)).toBe(text);
    expect(h.line(0)).toBe("");
    h.term.dispose();
  });

  it("OSC 0 and OSC 2 both reach xterm as title changes", async () => {
    const h = wire();
    h.feed(enc.encode("\x1b]0;first\x07\x1b]2;second\x1b\\"));
    await h.settle();
    expect(h.titles).toEqual(["first", "second"]);
    h.term.dispose();
  });
});
