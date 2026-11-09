import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import {
  createAnsiStripper,
  createOutputTail,
  createRegexMatcher,
  createTextMatcher,
  loadRegexEngine,
  regexPatternError,
  stepWaitTimeoutMs,
  waitForOutput,
  waitPatternBytes,
  DEFAULT_WAIT_TIMEOUT_MS,
  MATCH_CHECK_INTERVAL_MS,
  REGEX_LOOKBACK_CHARS,
  type RegexEngine,
} from "../../features/explorer/snippetWaitFor";

let engine: RegexEngine;
beforeAll(async () => {
  engine = await loadRegexEngine();
});
const createStepMatcher = (pattern: string, regex: boolean) =>
  regex ? createRegexMatcher(engine, pattern) : createTextMatcher(pattern);

afterEach(() => {
  vi.useRealTimers();
});

describe("ANSI stripping", () => {
  it("removes colors, OSC titles and carriage returns but keeps text, newlines and tabs", () => {
    const strip = createAnsiStripper();
    expect(strip("\x1b[1;32muser@host\x1b[0m:\x1b]0;title\x07~\t$ \r\nnext")).toBe("user@host:~\t$ \nnext");
  });

  it("joins a sequence split across chunks", () => {
    const strip = createAnsiStripper();
    expect(strip("pass\x1b[")).toBe("pass");
    expect(strip("31")).toBe("");
    expect(strip("mword:\x1b]8;;http://x")).toBe("word:");
    expect(strip("\x1b\\ ok")).toBe(" ok");
  });

  it("drops DCS payloads and two-character escapes", () => {
    const strip = createAnsiStripper();
    expect(strip("a\x1bPq#0;2;0;0;0\x1b\\b\x1b(Bc\x1b=d")).toBe("abcd");
  });
});

describe("step matchers", () => {
  it("matches plain text literally, including across the checked boundary", () => {
    const match = createStepMatcher("a.b (x)", false);
    expect(match("got a.b (x) here", 0)).toBe(true);
    expect(match("got aXb (x) here", 0)).toBe(false);
    expect(match("got a.b (x)", 6)).toBe(true);
  });

  it("matches a regex in multiline mode", () => {
    const match = createStepMatcher("[$#] $", true);
    expect(match("root@h:~# \nmore", 0)).toBe(true);
    expect(match("no prompt", 0)).toBe(false);
  });

  it("uses RE2 syntax, the same the store validates", () => {
    expect(regexPatternError(engine, "(")).not.toBeNull();
    expect(regexPatternError(engine, "(?=a)")).not.toBeNull();
    expect(regexPatternError(engine, String.raw`(a)\1`)).not.toBeNull();
    expect(regexPatternError(engine, "(?i)ok")).toBeNull();
    expect(createStepMatcher("(?i)ok", true)("OK", 0)).toBe(true);
    expect(createStepMatcher(String.raw`^\pL+\z`, true)("été", 0)).toBe(true);
  });

  it("runs a pattern that backtracks catastrophically in JavaScript in linear time", () => {
    const match = createStepMatcher("(a+)+$", true);
    const started = performance.now();
    expect(match(`${"a".repeat(64 * 1024 - 1)}b`, 0)).toBe(false);
    expect(performance.now() - started).toBeLessThan(2000);
  });

  it("only re-checks new output plus a bounded lookback from a line start", () => {
    const match = createStepMatcher("^old$", true);
    const text = `old\ny${"x".repeat(REGEX_LOOKBACK_CHARS * 2)}\nnew`;
    expect(match(text, 0)).toBe(true);
    expect(match(text, text.length - 3)).toBe(false);
    // The window starts inside the long line, which is not a line start.
    expect(createStepMatcher("^x+\nnew$", true)(text, text.length - 3)).toBe(false);
    expect(createStepMatcher("x\nnew$", true)(text, text.length - 3)).toBe(true);
    // Within the lookback the window starts at a line start, so ^ anchors there and a match may span lines.
    const lines = `${"q".repeat(REGEX_LOOKBACK_CHARS)}\nprompt$ ls\nfile\n${"z".repeat(REGEX_LOOKBACK_CHARS - 13)}`;
    expect(createStepMatcher("^prompt\\$ ls\nfile$", true)(lines, lines.length - 3)).toBe(true);
    expect(createStepMatcher("q\nprompt", true)(lines, lines.length - 3)).toBe(false);
  });

  it("measures the pattern limit in UTF-8 bytes", () => {
    expect(waitPatternBytes("密码")).toBe(6);
  });

  it("uses the default timeout unless the step sets one", () => {
    expect(stepWaitTimeoutMs({})).toBe(DEFAULT_WAIT_TIMEOUT_MS);
    expect(stepWaitTimeoutMs({ timeoutMs: 2500 })).toBe(2500);
  });
});

describe("output tail", () => {
  it("decodes bytes split inside a UTF-8 character and strips escapes", () => {
    const tail = createOutputTail();
    const bytes = new TextEncoder().encode("\x1b[33m密码：\x1b[0m");
    tail.appendBytes(bytes.subarray(0, 7));
    tail.appendBytes(bytes.subarray(7));
    expect(tail.text()).toBe("密码：");
  });

  it("keeps only the most recent output", () => {
    const tail = createOutputTail(8);
    tail.append("0123456789abc");
    expect(tail.text()).toBe("56789abc");
    tail.clear();
    expect(tail.text()).toBe("");
  });
});

describe("waiting for output", () => {
  it("resolves at once when the tail already matches", async () => {
    const tail = createOutputTail();
    tail.append("user@host:~$ ");
    await expect(waitForOutput(tail, createStepMatcher("$ ", false), 1000, new AbortController().signal)).resolves.toBe("matched");
  });

  it("resolves when matching output arrives, even split across chunks", async () => {
    vi.useFakeTimers();
    const tail = createOutputTail();
    const result = waitForOutput(tail, createStepMatcher("password:", false), 5000, new AbortController().signal);
    tail.append("[sudo] pass");
    tail.append("\x1b[1mword:\x1b[0m");
    await vi.advanceTimersByTimeAsync(MATCH_CHECK_INTERVAL_MS);
    await expect(result).resolves.toBe("matched");
  });

  it("times out when the output never matches", async () => {
    vi.useFakeTimers();
    const tail = createOutputTail();
    const result = waitForOutput(tail, createStepMatcher("never", false), 3000, new AbortController().signal);
    tail.append("something else");
    vi.advanceTimersByTime(2999);
    tail.append("still not");
    vi.advanceTimersByTime(1);
    await expect(result).resolves.toBe("timeout");
  });

  it("counts output that arrived just before the deadline", async () => {
    vi.useFakeTimers();
    const tail = createOutputTail();
    const result = waitForOutput(tail, createStepMatcher("ready", false), 1000, new AbortController().signal);
    vi.advanceTimersByTime(999);
    tail.append("ready");
    vi.advanceTimersByTime(1);
    await expect(result).resolves.toBe("matched");
  });

  it("checks a burst of output once", async () => {
    vi.useFakeTimers();
    const tail = createOutputTail();
    const matcher = vi.fn(() => false);
    void waitForOutput(tail, matcher, 1000, new AbortController().signal);
    for (let i = 0; i < 50; i += 1) tail.append(`line ${i}\n`);
    vi.advanceTimersByTime(MATCH_CHECK_INTERVAL_MS);
    // Once for what the tail held at the start, once for the burst.
    expect(matcher).toHaveBeenCalledTimes(2);
    vi.advanceTimersByTime(1000);
  });

  it("resolves as cancelled when aborted, before or during the wait", async () => {
    vi.useFakeTimers();
    const tail = createOutputTail();
    const before = new AbortController();
    before.abort();
    await expect(waitForOutput(tail, createStepMatcher("x", false), 1000, before.signal)).resolves.toBe("cancelled");
    const during = new AbortController();
    const result = waitForOutput(tail, createStepMatcher("x", false), 1000, during.signal);
    during.abort();
    await expect(result).resolves.toBe("cancelled");
    // A match after the cancel does not change the outcome.
    tail.append("x");
  });
});
