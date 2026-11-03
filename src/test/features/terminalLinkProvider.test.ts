/** @vitest-environment jsdom */

import { afterEach, describe, expect, it } from "vitest";
import { Terminal, type ILink } from "@xterm/xterm";
import {
  MAX_LINK_LENGTH,
  maxContinuationRows,
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

const MAX_CONTINUATION_ROWS_FLOOR = 4;
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
      for (const { url, parts, soft } of fx.urls) {
        let from = 0;
        const ys: number[] = [];
        for (const part of parts) {
          const y = rows.findIndex((r, i) => i >= from && r.includes(part)) + 1;
          expect(y, part).toBeGreaterThan(0);
          from = y;
          ys.push(y);
        }
        parts.forEach((part, i) => {
          const y = ys[i];
          const link = provide(term, y).find((l) => l.text === url);
          expect(link, `row ${y}`).toBeDefined();
          expect(link!.decorations).toEqual({ underline: true, pointerCursor: true });
          const col = rows[y - 1].indexOf(part);
          if (soft) {
            // One range from the first to the last character, whichever row is hovered.
            const lastPart = parts[parts.length - 1];
            expect(link!.range.start).toEqual({ x: rows[ys[0] - 1].indexOf(parts[0]) + 1, y: ys[0] });
            expect(link!.range.end).toEqual({ x: rows[ys[ys.length - 1] - 1].indexOf(lastPart) + lastPart.length, y: ys[ys.length - 1] });
            if (parts.length > 1) expect(link!.range.start.y).not.toBe(link!.range.end.y);
          } else {
            // Hard-wrapped pieces are underlined per row, keeping borders and prefixes out.
            expect(link!.range.start).toEqual({ x: col + 1, y });
            expect(link!.range.end).toEqual({ x: col + part.length, y });
          }
        });
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

  it("stops a URL at full-width punctuation", () => {
    expect(urls(hard("访问 https://example.com/docs。然后"), 80)).toEqual(["https://example.com/docs"]);
    expect(urls(hard("链接：https://a.example/x，谢谢"), 80)).toEqual(["https://a.example/x"]);
    expect(urls(hard("https://a.example/x；y https://b.example/z！"), 80)).toEqual(["https://a.example/x", "https://b.example/z"]);
    expect(urls(hard("（https://a.example/x）"), 80)).toEqual(["https://a.example/x"]);
  });

  it("does not continue a full URL row with non-ASCII text", () => {
    const full = "https://a.example/" + "a".repeat(22);
    expect(urls(hard(full, "中文文字"), 40)).toEqual([full]);
    expect(urls(hard(full, "caf\u00e9-menu"), 40)).toEqual([full]);
  });

  it("does not join a lone token that follows a URL filling the row", () => {
    const full = "https://a.example/" + "a".repeat(22);
    // A bracketed tag is never a continuation, so the first row stays a link.
    expect(urls(hard(full, "[OK]"), 40)).toEqual([full]);
    expect(urls(hard(`Docs: ${full.slice(0, 34)}`, "[OK]"), 40)).toEqual([full.slice(0, 34)]);
    // A number after letters is no tail either, but the filled row may be cut:
    // an address that could be a prefix is not offered.
    for (const token of ["200", "42"]) {
      expect(urls(hard(full, token), 40), token).toEqual([]);
      expect(urls(hard(`Docs: ${full.slice(0, 34)}`, token), 40), token).toEqual([]);
    }
    // A path tail with a slash is evidence enough even after a row that also holds prose.
    const head = "https://a.example/" + "a".repeat(10);
    const prose = `Docs: ${head}`;
    expect(urls(hard(prose, "/api/v2/next"), prose.length)).toEqual([head + "/api/v2/next"]);
    expect(urls(hard(prose, "/api/v2/next?x=1"), prose.length)).toEqual([head + "/api/v2/next?x=1"]);
  });

  it("never cuts a hard-wrapped URL down to a valid prefix", () => {
    const a = "Created PR: https://github.com/acme/wi";
    expect(urls(hard(a, "dget/pull/1234"), a.length)).toEqual(["https://github.com/acme/widget/pull/1234"]);
    const b = "https://github.com/acme/widget/pull/12";
    expect(urls(hard(b, "34"), b.length)).toEqual(["https://github.com/acme/widget/pull/1234"]);
    // A number after a letter is not a tail; the filled row is dropped, not truncated.
    const c = "See https://a.example/docs";
    expect(urls(hard(c, "200"), c.length)).toEqual([]);
    // Letters-only tokens keep the first-row link.
    expect(urls(hard(c, "Done"), c.length)).toEqual(["https://a.example/docs"]);
  });

  it("joins a path tail after a row that holds only the URL (indistinguishable from a cut URL)", () => {
    const full = "https://a.example/" + "a".repeat(22);
    expect(urls(hard(full, "/api/v2/next"), 40)).toEqual([full + "/api/v2/next"]);
    expect(urls(hard(`│ ${full} │`, "│ /api/v2/next                           │"), 44)).toEqual([full + "/api/v2/next"]);
  });

  it("accepts a letters-only tail when the URL has a query and the context ends cleanly", () => {
    const head = "https://a.example/x?k=" + "a".repeat(18);
    expect(urls(hard(head, "abcdef"), 40)).toEqual([head + "abcdef"]);
    expect(urls(hard(head, "abcdef", ""), 40)).toEqual([head + "abcdef"]);
    expect(urls(hard(head, "abcdef", "$ ls"), 40)).toEqual([head + "abcdef"]);
    expect(urls(hard(head, "abcdef", "> "), 40)).toEqual([head + "abcdef"]);
    expect(urls(hard(head, "abcdef", "╰────────╯"), 40)).toEqual([head + "abcdef"]);
    expect(urls(hard(head, "abcdef", "Next sentence follows"), 40)).toEqual([head]);
    const noQuery = "https://a.example/" + "a".repeat(22);
    expect(urls(hard(noQuery, "abcdef", ""), 40)).toEqual([noQuery]);
    expect(urls(hard(`Docs: ${head.slice(0, 34)}`, "abcdef", ""), 40)).toEqual([head.slice(0, 34)]);
  });

  it("excludes pipe, backslash and caret from URLs", () => {
    expect(urls(hard("https://a.example/x|grep"), 80)).toEqual(["https://a.example/x"]);
    expect(urls(hard("https://a.example/x\\n"), 80)).toEqual(["https://a.example/x"]);
    expect(urls(hard("https://a.example/x^M"), 80)).toEqual(["https://a.example/x"]);
    const full = "https://a.example/" + "a".repeat(22);
    expect(urls(hard(full, "bb|grep"), 40)).toEqual([full]);
    expect(urls(hard(full, "bb\\cc"), 40)).toEqual([full]);
    expect(urls(hard(full, "bb^cc"), 40)).toEqual([full]);
  });

  it("does not join a boxed URL with a following boxed sentence", () => {
    const rows = hard("│ https://a.example/x      │", "│ Paste code here          │");
    expect(urls(rows, 40)).toEqual(["https://a.example/x"]);
  });

  it("does not stitch a short boxed URL to the next boxed row", () => {
    const url = "https://a.com/docs";
    for (const next of ["│ v1.2.3                      │", "│ /next                       │"]) {
      expect(urls(hard("│ https://a.com/docs          │", next), 40), next).toEqual([url]);
    }
  });

  it("stitches a boxed URL that reaches the border, with at most one blank cell", () => {
    const a = "https://a.example/" + "a".repeat(10);
    expect(urls(hard(`│ ${a} │`, "│ b1/c2                │"), 40)).toEqual([a + "b1/c2"]);
    expect(urls(hard(`│ ${a}│`, "│ b1/c2                │"), 40)).toEqual([a + "b1/c2"]);
    expect(urls(hard(`│ ${a}  │`, "│ b1/c2                │"), 40)).toEqual([a]);
  });

  it("allows right padding that mirrors the left padding of the box", () => {
    const a = "https://a.example/" + "a".repeat(10);
    const box = (text: string, width: number) => `│  ${text.padEnd(width)}  │`;
    const w = a.length;
    expect(urls(hard(box(a, w), box("b1/c2", w)), 40)).toEqual([a + "b1/c2"]);
    expect(urls(hard(box(a, w), box("b1/c2d3e4f5g6h7i8j9k0l1m2n3o4p5q6r7".slice(0, w), w), box("t1/u2", w)), 60)).toEqual([
      a + "b1/c2d3e4f5g6h7i8j9k0l1m2n3o4p5q6r7".slice(0, w) + "t1/u2",
    ]);
    // One cell more than the left side has is still not a border.
    expect(urls(hard(`│  ${a}   │`, box("b1/c2", w)), 40)).toEqual([a]);
    // Short boxed URLs are not stitched to the next boxed row.
    const url = "https://a.com/docs";
    for (const next of ["v1.2.3", "/next"]) {
      expect(urls(hard(`│  ${url}      │`, `│  ${next.padEnd(url.length + 4)}  │`), 40), next).toEqual([url]);
    }
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

  it("sizes the continuation limit from the terminal width", () => {
    expect(maxContinuationRows(1000)).toBe(4);
    expect(maxContinuationRows(45)).toBeGreaterThanOrEqual(Math.ceil(MAX_LINK_LENGTH / 45));
  });

  it("stitches a URL over many rows when the width needs them", () => {
    const rows = hard("https://a.example/" + "a".repeat(22), ...Array.from({ length: 9 }, () => "1".repeat(40)));
    const [link] = findLinksInRows(rows, 40);
    expect(link.segments.length).toBe(10);
    expect(link.url.length).toBe(400);
  });

  it("gives no link when the length limit cuts a URL that would continue", () => {
    const head = "https://a.example/" + "a".repeat(1000 - 18);
    const rows = hard(head, "b".repeat(1000), "c".repeat(1000));
    expect(urls(rows, 1000)).toEqual([]);
  });

  it("gives no link when the row limit cuts a URL that would continue", () => {
    const rows = hard("https://a.example/" + "a".repeat(982), ...Array.from({ length: 6 }, () => "1".repeat(1000)));
    expect(urls(rows, 1000)).toEqual([]);
  });

  it("stops at the length limit when nothing continues past it", () => {
    const head = "https://a.example/" + "a".repeat(1000 - 18);
    expect(urls(hard(head, "b".repeat(1000)), 1000)).toEqual([head + "b".repeat(1000)]);
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

describe("long URLs on narrow terminals", () => {
  const fx = LINK_FIXTURES.find((f) => f.name === "claude-code-login-45-cols")!;

  it("recognises a 379 character URL hard-wrapped at 45 columns", () => {
    expect(fx.urls[0].url.length).toBeGreaterThan(370);
    expect(fx.urls[0].parts.length).toBeGreaterThan(MAX_CONTINUATION_ROWS_FLOOR);
  });

  it("provides the whole URL from its first, middle and last row", async () => {
    const term = await makeTerm(fx.cols, fx.data);
    const rows = bufferRows(term);
    const { url, parts } = fx.urls[0];
    for (const idx of [0, Math.floor(parts.length / 2), parts.length - 1]) {
      const y = rows.findIndex((r) => r === parts[idx]) + 1;
      expect(y).toBeGreaterThan(0);
      expect(provide(term, y).map((l) => l.text)).toContain(url);
    }
  });
});

describe("extractTerminalLinks context", () => {
  it("drops control and invisible characters from the source line summary", async () => {
    const term = await makeTerm(100, "see\u202e\u200b txt https://example.com/a now\r\n");
    const [link] = extractTerminalLinks(term);
    expect(link.url).toBe("https://example.com/a");
    expect(link.context).toContain("https://example.com/a");
    expect(link.context).not.toMatch(/[\u202e\u200b\u0007]/);
  });
});

describe("rows written for another width", () => {
  const fx = LINK_FIXTURES[0];

  it("stitches rows that are as wide as each other although the terminal is wider", () => {
    const parts = fx.urls[0].parts;
    expect(urls(hard(...parts), 120)).toEqual([fx.urls[0].url]);
    expect(urls(hard("Open:", ...parts, "", "Paste code here >"), 200)).toEqual([fx.urls[0].url]);
  });

  it("still recognises the URL after the terminal is resized wider", async () => {
    const term = await makeTerm(fx.cols, fx.data);
    term.resize(120, 40);
    expect(extractTerminalLinks(term).map((l) => l.url)).toEqual([fx.urls[0].url]);
    const rows = bufferRows(term);
    for (const part of fx.urls[0].parts) {
      const y = rows.findIndex((r) => r === part) + 1;
      expect(provide(term, y).map((l) => l.text)).toContain(fx.urls[0].url);
    }
  });

  it("does not let equal widths join unrelated short rows", () => {
    expect(urls(hard("https://a.com/docs", "v1.2.3-rc1-release"), 120)).toEqual(["https://a.com/docs"]);
    expect(urls(hard("│ https://a.com/docs          │", "│ v1.2.3                      │"), 120)).toEqual(["https://a.com/docs"]);
  });
});

describe("underline ranges", () => {
  it("covers a soft-wrapped URL as one range from any of its rows", async () => {
    const fx = LINK_FIXTURES.find((f) => f.name === "npm-login")!;
    const term = await makeTerm(fx.cols, fx.data);
    const rows = bufferRows(term);
    const first = rows.findIndex((r) => r === fx.urls[1].parts[0]) + 1;
    const [a, b] = [provide(term, first), provide(term, first + 1)].map((l) => l.find((x) => x.text === fx.urls[1].url)!);
    expect(a.range).toEqual(b.range);
    expect(a.range.start).toEqual({ x: 1, y: first });
    expect(a.range.end.y).toBe(first + 1);
  });

  it("keeps hard-wrapped pieces apart so borders stay out of the underline", async () => {
    const fx = LINK_FIXTURES.find((f) => f.name === "claude-code-login-boxed")!;
    const term = await makeTerm(fx.cols, fx.data);
    const y = bufferRows(term).findIndex((r) => r.includes(fx.urls[0].parts[0])) + 1;
    const link = provide(term, y).find((l) => l.text === fx.urls[0].url)!;
    expect(link.range.start.y).toBe(link.range.end.y);
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
