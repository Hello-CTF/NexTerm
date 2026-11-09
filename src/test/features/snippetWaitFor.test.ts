import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createAnsiStripper,
  createOutputTail,
  createStepMatcher,
  stepWaitTimeoutMs,
  waitForOutput,
  waitPatternError,
  DEFAULT_WAIT_TIMEOUT_MS,
} from "../../features/explorer/snippetWaitFor";

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
  it("matches plain text literally", () => {
    const match = createStepMatcher("a.b (x)", false);
    expect(match("got a.b (x) here")).toBe(true);
    expect(match("got aXb (x) here")).toBe(false);
  });

  it("matches a regex in multiline mode", () => {
    const match = createStepMatcher("[$#] $", true);
    expect(match("root@h:~# \nmore")).toBe(true);
    expect(match("no prompt")).toBe(false);
  });

  it("reports an invalid regex but accepts the same text as plain text", () => {
    expect(waitPatternError("(", true)).not.toBeNull();
    expect(waitPatternError("(", false)).toBeNull();
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
