/** @vitest-environment jsdom */

import { afterEach, describe, expect, it } from "vitest";
import { Terminal, type ILink } from "@xterm/xterm";
import {
  MAX_CONTINUATION_ROWS,
  createTerminalLinkProvider,
  extractTerminalLinks,
  findLinksInRows,
  findTargetsInRows,
  type DetectedTarget,
  type TextRow,
} from "../../features/terminal/terminalLinkProvider";
import { LINK_FIXTURES } from "../fixtures/links";

const terms: Terminal[] = [];

afterEach(() => {
  for (const t of terms.splice(0)) t.dispose();
});

async function makeTerm(cols: number, data: string, rows = 40, scrollback = 100): Promise<Terminal> {
  const host = document.createElement("div");
  document.body.appendChild(host);
  const term = new Terminal({ allowProposedApi: true, cols, rows, scrollback });
  terms.push(term);
  term.open(host);
  await new Promise<void>((resolve) => term.write(data, resolve));
  return term;
}

function provide(term: Terminal, y: number, onTargetHover?: (t: DetectedTarget | null) => void): ILink[] {
  const calls: string[] = [];
  const provider = createTerminalLinkProvider(term, {
    activate: (_e, u) => calls.push(`activate ${u}`),
    hover: (_e, u) => calls.push(`hover ${u}`),
    leave: (_e, u) => calls.push(`leave ${u}`),
    onTargetHover,
  });
  let result: ILink[] = [];
  provider.provideLinks(y, (links) => {
    result = links ?? [];
  });
  return result;
}

function bufferRows(term: Terminal): string[] {
  const buf = term.buffer.active;
  const out: string[] = [];
  for (let i = 0; i < buf.length; i++) out.push(buf.getLine(i)!.translateToString(true));
  return out;
}

const hard = (...lines: string[]): TextRow[] => lines.map((text) => ({ text, isWrapped: false }));
const urls = (rows: TextRow[], cols: number) => findLinksInRows(rows, cols).map((l) => l.url);

describe("link fixtures", () => {
  for (const fx of LINK_FIXTURES) {
    it(`${fx.name}: extracts full URLs and links every row`, async () => {
      const term = await makeTerm(fx.cols, fx.data);
      const extracted = extractTerminalLinks(term).map((l) => l.url);
      for (const { url } of fx.urls) expect(extracted).toContain(url);
      expect(extracted.length).toBe(fx.urls.length);

      const rows = bufferRows(term);
      for (const { url, parts } of fx.urls) {
        let from = 0;
        for (const part of parts) {
          const y = rows.findIndex((r, i) => i >= from && r.includes(part)) + 1;
          expect(y, part).toBeGreaterThan(0);
          from = y;
          const link = provide(term, y).find((l) => l.text === url);
          expect(link, `row ${y}`).toBeDefined();
          const col = rows[y - 1].indexOf(part);
          expect(link!.range.start).toEqual({ x: col + 1, y });
          expect(link!.range.end).toEqual({ x: col + part.length, y });
          expect(link!.decorations).toEqual({ underline: true, pointerCursor: true });
        }
      }
    });
  }
});

describe("link stitching rules", () => {
  it("does not join two independent URLs", () => {
    const a = "https://a.example/" + "a".repeat(22);
    const b = "https://b.example/" + "b".repeat(22);
    expect(urls(hard(a, b), 40)).toEqual([a, b]);
  });

  it("finds the boundary before closing punctuation", () => {
    for (const [text, url] of [
      ["(see https://a.example/x)", "https://a.example/x"],
      ["go to https://a.example/x.", "https://a.example/x"],
      ["https://a.example/x, then", "https://a.example/x"],
      ['"https://a.example/x"', "https://a.example/x"],
      ["https://en.wikipedia.org/wiki/Foo_(bar).", "https://en.wikipedia.org/wiki/Foo_(bar)"],
    ]) {
      expect(urls(hard(text), 80), text).toEqual([url]);
    }
  });

  it("does not append prose to a URL that fills the row", () => {
    const line = "See https://example.com/docs";
    expect(urls(hard(line, "for details"), line.length)).toEqual(["https://example.com/docs"]);
    expect(urls(hard(line, "Continue"), line.length)).toEqual(["https://example.com/docs"]);
  });

  it("does not join a boxed URL with a following boxed sentence", () => {
    const rows = hard("│ https://a.example/x      │", "│ Paste code here          │");
    expect(urls(rows, 40)).toEqual(["https://a.example/x"]);
  });

  it("does not join a full URL row with a boxed short word", () => {
    const full = "https://a.example/" + "a".repeat(22);
    expect(urls(hard(full, "│ Done │"), 40)).toEqual([full]);
  });

  it("truncates a continuation at a second URL and stops", () => {
    const first = "https://a.example/" + "a".repeat(22);
    const rows = hard(first, "abc,https://b.example/zz", "tail");
    expect(urls(rows, 40)).toEqual([first + "abc", "https://b.example/zz"]);
  });

  it("joins at most MAX_CONTINUATION_ROWS extra rows", () => {
    const rows = hard("https://a.example/" + "a".repeat(22), ...Array.from({ length: 6 }, () => "1".repeat(40)));
    const [link] = findLinksInRows(rows, 40);
    expect(link.segments.length).toBe(MAX_CONTINUATION_ROWS + 1);
    expect(link.url.length).toBe(40 * (MAX_CONTINUATION_ROWS + 1));
  });

  it("stops before exceeding the length limit", () => {
    const head = "https://a.example/" + "a".repeat(1000 - 18);
    const rows = hard(head, "b".repeat(1000), "c".repeat(1000));
    expect(urls(rows, 1000)).toEqual([head + "b".repeat(1000)]);
  });

  it("only recognises http and https, and drops blocked URLs", () => {
    expect(urls(hard("javascript:alert(1)", "ftp://a.example/x", "mailto:a@b.c"), 80)).toEqual([]);
    expect(urls(hard("https://a.example/x​evil"), 80)).toEqual([]);
  });

  it("joins soft-wrapped rows without conditions", () => {
    const rows: TextRow[] = [
      { text: "x https://a.example/aa", isWrapped: false },
      { text: "bb cc", isWrapped: true },
    ];
    expect(urls(rows, 22)).toEqual(["https://a.example/aabb"]);
  });
});

describe("terminal link provider", () => {
  it("maps columns of a URL after wide characters", async () => {
    const term = await makeTerm(60, "中文前缀 https://example.com/path end");
    const [link] = provide(term, 1);
    expect(link.text).toBe("https://example.com/path");
    // 4 wide characters + one space = 9 cells before the URL.
    expect(link.range.start).toEqual({ x: 10, y: 1 });
    expect(link.range.end).toEqual({ x: 9 + "https://example.com/path".length, y: 1 });
  });

  it("passes the full URL to every handler from any row", async () => {
    const fx = LINK_FIXTURES[0];
    const term = await makeTerm(fx.cols, fx.data);
    const calls: string[] = [];
    const provider = createTerminalLinkProvider(term, {
      activate: (_e, u) => calls.push(u),
      hover: (_e, u) => calls.push(u),
      leave: (_e, u) => calls.push(u),
    });
    provider.provideLinks(4, (links) => {
      const link = links![0];
      const ev = new MouseEvent("click");
      link.activate(ev, link.text);
      link.hover?.(ev, link.text);
      link.leave?.(ev, link.text);
    });
    expect(calls).toEqual(Array(3).fill(fx.urls[0].url));
  });
});

describe("IP and host:port targets", () => {
  const targets = (text: string) => findTargetsInRows(hard(text), 120).map((t) => [t.kind, t.text, t.host, t.port]);

  it("recognises addresses", () => {
    expect(targets("ping 10.0.0.12 now")).toEqual([["ip", "10.0.0.12", "10.0.0.12", undefined]]);
    expect(targets("ssh 192.168.1.10:22")).toEqual([["hostport", "192.168.1.10:22", "192.168.1.10", 22]]);
    expect(targets("on localhost:3000.")).toEqual([["hostport", "localhost:3000", "localhost", 3000]]);
    expect(targets("api.example.com:8443")).toEqual([["hostport", "api.example.com:8443", "api.example.com", 8443]]);
    expect(targets("listen [::1]:8080")).toEqual([["hostport", "[::1]:8080", "::1", 8080]]);
  });

  it("ignores version numbers, compiler output and times", () => {
    for (const text of ["v1.2.3.4", "1.2.3.4.5", "main.go:42", "app.tsx:12", "12:30", "999.1.1.1", "src/lib.rs:7"]) {
      expect(targets(text), text).toEqual([]);
    }
  });

  it("skips targets inside URLs", () => {
    expect(targets("https://10.0.0.1:8080/x")).toEqual([]);
  });

  it("offers targets without link affordances and reports hover", async () => {
    const term = await makeTerm(80, "listen on 10.0.0.12:8080 ok");
    expect(provide(term, 1)).toEqual([]);
    const events: Array<DetectedTarget | null> = [];
    const [link] = provide(term, 1, (t) => events.push(t));
    expect(link.decorations).toEqual({ underline: false, pointerCursor: false });
    expect(link.range.start).toEqual({ x: 11, y: 1 });
    const ev = new MouseEvent("mousemove");
    link.hover?.(ev, link.text);
    link.leave?.(ev, link.text);
    expect(events[0]).toMatchObject({ kind: "hostport", host: "10.0.0.12", port: 8080 });
    expect(events[1]).toBeNull();
  });
});

describe("extractTerminalLinks performance", () => {
  it("scans 2000 scrollback rows in under 50ms", async () => {
    const url = "https://claude.ai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&state=" + "x".repeat(60);
    const lines: string[] = [];
    for (let i = 0; i < 2300; i++) {
      switch (i % 10) {
        case 3:
          lines.push(`see ${url}`);
          break;
        case 5:
          lines.push(...[url.slice(0, 120), url.slice(120)]);
          break;
        case 7:
          lines.push(`│ ${url.slice(0, 100).padEnd(116)} │`, `│ ${url.slice(100).padEnd(116)} │`);
          break;
        case 8:
          lines.push(`connect 10.0.0.${i % 250}:${3000 + (i % 50)} established`);
          break;
        default:
          lines.push(`2026-01-01 12:00:${String(i % 60).padStart(2, "0")} INFO worker ${i} processed request id=${i * 7919}`);
      }
    }
    const term = await makeTerm(120, lines.join("\r\n"), 40, 5000);
    expect(term.buffer.active.length).toBeGreaterThan(2200);
    let best = Infinity;
    let count = 0;
    for (let i = 0; i < 5; i++) {
      const started = performance.now();
      count = extractTerminalLinks(term, 2000).length;
      best = Math.min(best, performance.now() - started);
    }
    expect(count).toBeGreaterThan(0);
    expect(best, `best of 5: ${best.toFixed(2)}ms`).toBeLessThan(50);
  });
});
