/** @vitest-environment jsdom */

import { afterEach, describe, expect, it } from "vitest";
import { Terminal } from "@xterm/xterm";
import { attachKeywordHighlight, BURST_LINES, BURST_QUIET_MS, type KeywordHighlighter } from "../../features/terminal/keywordHighlight";
import {
  compileKeywordHighlights,
  hasNestedRepeat,
  highlightForeground,
  highlightPatternError,
  KEYWORD_HIGHLIGHT_PRESETS,
  matchKeywordHighlights,
  parseKeywordHighlights,
  type KeywordHighlightRule,
  type KeywordHighlightSettings,
} from "../../features/terminal/keywordHighlightRules";

function stubMatchMedia(): void {
  window.matchMedia = (() => ({
    matches: false,
    media: "",
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    onchange: null,
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
}

function rule(pattern: string, extra: Partial<KeywordHighlightRule> = {}): KeywordHighlightRule {
  return {
    id: `r-${pattern.length}`,
    pattern,
    regex: false,
    caseSensitive: false,
    wholeWord: false,
    foreground: "red",
    background: "",
    underline: false,
    enabled: true,
    ...extra,
  };
}

const settings = (...rules: KeywordHighlightRule[]): KeywordHighlightSettings => ({ presets: [], rules });

describe("keyword highlight matching", () => {
  it("matches plain text without regex meaning and honors case", () => {
    const rules = compileKeywordHighlights(settings(rule("a+b")));
    expect(matchKeywordHighlights("xA+B aab", rules).map((m) => [m.start, m.end])).toEqual([[1, 4]]);
    const sensitive = compileKeywordHighlights(settings(rule("Error", { caseSensitive: true })));
    expect(matchKeywordHighlights("error Error", sensitive).map((m) => m.start)).toEqual([6]);
  });

  it("checks word boundaries only at word characters, so CJK keywords match inside CJK text", () => {
    const rules = compileKeywordHighlights(settings(rule("ERROR", { wholeWord: true }), rule("失败", { wholeWord: true })));
    const text = "ERRORS ERROR: 连接失败了";
    expect(matchKeywordHighlights(text, rules).map((m) => text.slice(m.start, m.end))).toEqual(["ERROR", "失败"]);
  });

  it("lets an earlier rule win where matches overlap", () => {
    const rules = compileKeywordHighlights(
      settings(rule("connection failed", { foreground: "yellow" }), rule("failed", { foreground: "red" })),
    );
    const matches = matchKeywordHighlights("connection failed; retry failed", rules);
    expect(matches.map((m) => [m.start, m.style.foreground])).toEqual([
      [0, "yellow"],
      [25, "red"],
    ]);
  });

  it("skips disabled rules, rules of other hosts and rules that no longer validate", () => {
    const value = settings(
      rule("one", { enabled: false }),
      rule("two", { assetIds: ["asset-a"] }),
      rule("(a+)+", { regex: true }),
      rule("four"),
    );
    const text = "one two (a+)+ four";
    const forOther = compileKeywordHighlights(value, "asset-b");
    expect(matchKeywordHighlights(text, forOther).map((m) => text.slice(m.start, m.end))).toEqual(["four"]);
    const forA = compileKeywordHighlights(value, "asset-a");
    expect(matchKeywordHighlights(text, forA).map((m) => text.slice(m.start, m.end))).toEqual(["two", "four"]);
  });

  it("applies enabled presets after the user's rules", () => {
    const text = "WARN 10.0.0.1 failed 123e4567-e89b-12d3-a456-426614174000 OK ok";
    const rules = compileKeywordHighlights({ presets: KEYWORD_HIGHLIGHT_PRESETS.map((p) => p.id), rules: [] });
    expect(matchKeywordHighlights(text, rules).map((m) => text.slice(m.start, m.end))).toEqual([
      "WARN",
      "10.0.0.1",
      "failed",
      "123e4567-e89b-12d3-a456-426614174000",
      "OK",
    ]);
    expect(compileKeywordHighlights({ presets: [], rules: [] })).toEqual([]);
  });
});

describe("keyword highlight validation", () => {
  it("accepts every built-in preset", () => {
    for (const preset of KEYWORD_HIGHLIGHT_PRESETS) {
      for (const presetRule of preset.rules) expect(highlightPatternError(presetRule)).toBeNull();
    }
  });

  it("refuses patterns one of the two engines cannot run safely", () => {
    const regex = (pattern: string) => highlightPatternError({ pattern, regex: true, caseSensitive: false });
    expect(regex("\\d+\\.\\d+")).toBeNull();
    expect(regex("(?:\\d{1,3}\\.){3}\\d{1,3}")).toBeNull();
    for (const pattern of ["foo(?=bar)", "(?<!x)y", "(a)\\1", "(?i)foo", "(?P<n>x)", "(", "x*", "(a+)+", "(ab|a)*", "", "a\nb"]) {
      expect(regex(pattern), pattern).not.toBeNull();
    }
    expect(highlightPatternError({ pattern: "(a+)+", regex: false, caseSensitive: false })).toBeNull();
  });

  it("detects nested unbounded repeats", () => {
    expect(hasNestedRepeat("(a+)+")).toBe(true);
    expect(hasNestedRepeat("((a*))*")).toBe(true);
    expect(hasNestedRepeat("(?:x|y){2,}")).toBe(true);
    expect(hasNestedRepeat("[(+]+")).toBe(false);
    expect(hasNestedRepeat("\\(a+\\)+")).toBe(false);
    expect(hasNestedRepeat("(a+){3}")).toBe(false);
    expect(hasNestedRepeat("(?:ab)+c+")).toBe(false);
  });

  it("keeps rules with a known shape when reading a stored value", () => {
    const parsed = parseKeywordHighlights({
      presets: ["errors", "errors", 3],
      rules: [{ id: "a", pattern: "(a+)+", regex: true, foreground: "red", enabled: true }, { id: "b", pattern: 1 }, { id: "c", pattern: "x", foreground: "pink" }],
    });
    expect(parsed?.presets).toEqual(["errors"]);
    expect(parsed?.rules.map((r) => r.id)).toEqual(["a"]);
    expect(parseKeywordHighlights([])).toBeNull();
  });
});

describe("keyword highlight decorations", () => {
  let term: Terminal | null = null;
  let highlighter: KeywordHighlighter | null = null;
  let clock = 1_000;

  afterEach(() => {
    highlighter?.dispose();
    term?.dispose();
    highlighter = null;
    term = null;
  });

  function open(rules: KeywordHighlightRule[], options: { cols?: number; rows?: number; scrollback?: number } = {}) {
    stubMatchMedia();
    term = new Terminal({ cols: options.cols ?? 40, rows: options.rows ?? 5, scrollback: options.scrollback ?? 100, allowProposedApi: true });
    const host = document.createElement("div");
    document.body.append(host);
    term.open(host);
    const compiled = compileKeywordHighlights(settings(...rules));
    highlighter = attachKeywordHighlight(term, { rules: () => compiled, theme: () => "dark", now: () => clock });
    return { term, highlighter };
  }

  const write = (t: Terminal, data: string) => new Promise<void>((resolve) => t.write(data, resolve));
  const lineOf = (t: Terminal, text: string) => {
    for (let y = 0; y < t.buffer.active.length; y++) if (t.buffer.active.getLine(y)?.translateToString(true).includes(text)) return y;
    throw new Error(`missing ${text}`);
  };

  it("decorates matches in output that xterm already parsed, at their cell columns", async () => {
    const { term, highlighter } = open([rule("ERROR", { underline: true })]);
    await write(term, "ok \x1b[31mERROR\x1b[0m here\r\n宽字符 ERROR\r\n");
    highlighter.scanNow();
    const red = highlightForeground("red", "dark");
    expect(highlighter.rendered()).toEqual([
      { line: lineOf(term, "ok "), x: 3, width: 5, foreground: red, underline: true },
      { line: lineOf(term, "宽字符"), x: 7, width: 5, foreground: red, underline: true },
    ]);
  });

  it("splits a match that wraps across rows into one decoration per row", async () => {
    const { term, highlighter } = open([rule("CONNECTION_REFUSED")], { cols: 20 });
    await write(term, `${"x".repeat(15)}CONNECTION_REFUSED\r\n`);
    highlighter.scanNow();
    const first = lineOf(term, "xxxxx");
    expect(highlighter.rendered().map(({ line, x, width }) => ({ line, x, width }))).toEqual([
      { line: first, x: 15, width: 5 },
      { line: first + 1, x: 0, width: 13 },
    ]);
  });

  it("only holds decorations for the viewport and drops them with trimmed, cleared or reset rows", async () => {
    const { term, highlighter } = open([rule("FAIL")], { rows: 5, scrollback: 10 });
    await write(term, Array.from({ length: 30 }, (_, i) => `line ${i} FAIL`).join("\r\n"));
    highlighter.scanNow();
    const rendered = highlighter.rendered();
    expect(rendered).toHaveLength(5);
    const buffer = term.buffer.active;
    expect(rendered.every((d) => d.line >= buffer.viewportY && d.line < buffer.viewportY + term.rows)).toBe(true);

    await write(term, "\r\n".repeat(20));
    expect(highlighter.rendered()).toHaveLength(0);

    await write(term, "FAIL again\r\nFAIL more");
    highlighter.scanNow();
    expect(highlighter.rendered()).toHaveLength(2);
    term.clear();
    highlighter.scanNow();
    expect(highlighter.rendered().map((d) => d.line)).toEqual([0]);

    await write(term, "\r\nFAIL\r\n");
    highlighter.scanNow();
    expect(highlighter.rendered().length).toBeGreaterThan(0);
    term.reset();
    expect(highlighter.rendered()).toHaveLength(0);
    highlighter.scanNow();
    expect(highlighter.rendered()).toHaveLength(0);
  });

  it("does not highlight the alternate screen and rescans on return", async () => {
    const { term, highlighter } = open([rule("FAIL")]);
    await write(term, "FAIL\r\n");
    highlighter.scanNow();
    expect(highlighter.rendered()).toHaveLength(1);
    await write(term, "\x1b[?1049hFAIL in vim");
    highlighter.scanNow();
    expect(highlighter.rendered()).toHaveLength(0);
    await write(term, "\x1b[?1049l");
    highlighter.scanNow();
    expect(highlighter.rendered()).toHaveLength(1);
  });

  it("keeps decorations of unchanged lines and replaces those of rewritten lines", async () => {
    const { term, highlighter } = open([rule("FAIL")]);
    await write(term, "a FAIL\r\nprogress");
    highlighter.scanNow();
    const before = highlighter.rendered();
    await write(term, "\r\x1b[KFAIL now");
    highlighter.scanNow();
    const after = highlighter.rendered();
    expect(after[0]).toEqual(before[0]);
    expect(after.map((d) => d.x)).toEqual([2, 0]);
  });

  it("pauses during an output burst and resumes once it quiets down", async () => {
    const { term, highlighter } = open([rule("FAIL")]);
    await write(term, Array.from({ length: BURST_LINES + 50 }, () => "y FAIL").join("\r\n"));
    expect(highlighter.paused()).toBe(true);
    highlighter.scanNow();
    expect(highlighter.rendered()).toHaveLength(0);
    clock += BURST_QUIET_MS + 1;
    expect(highlighter.paused()).toBe(false);
    highlighter.scanNow();
    expect(highlighter.rendered()).toHaveLength(term.rows);
  });
});
