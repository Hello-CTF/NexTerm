/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Terminal } from "@xterm/xterm";
import {
  RESPONSE_BURST_BYTES,
  RESPONSE_REFILL_BYTES_PER_SECOND,
  classifyTerminalResponse,
  createTerminalResponseLimiter,
  isAutomaticTerminalReply,
  terminalResponseKey,
  type TerminalResponseStats,
} from "../../features/terminal/terminalResponses";

describe("terminal automatic response classification", () => {
  it("recognizes xterm automatic report shapes", () => {
    const cases: [string, string][] = [
      ["\x1b[?1;2c", "da"],
      ["\x1b[?62;22c", "da"],
      ["\x1b[>0;276;0c", "da"],
      ["\x1b[>41;373;0c", "da"],
      ["\x1b/Z", "da"],
      ["\x1b[0n", "dsr"],
      ["\x1b[3n", "dsr"],
      ["\x1b[12;1R", "cpr"],
      ["\x1b[?12;1R", "cpr"],
      ["\x1b[8;24;80t", "window"],
      ["\x1bP>|xterm.js (1.0)\x1b\\", "dcs"],
      ["\x1bP1$r64\x1b\\", "dcs"],
      ["\x1b]10;rgb:ffff/ffff/ffff\x1b\\", "osc-color"],
      ["\x1b]4;1;rgb:0000/0000/0000\x07", "osc-color"],
      ["\x1b[?2004;1$y", "mode"],
      ["\x1b[4;2$y", "mode"],
      ["\x1b[?0u", "kitty-keyboard"],
      ["\x1b[?31u", "kitty-keyboard"],
    ];
    for (const [payload, type] of cases) {
      expect(classifyTerminalResponse(payload), JSON.stringify(payload)).toBe(type);
    }
  });

  it("does not classify user input or partial sequences as responses", () => {
    const inputs = [
      "a",
      "ls -la\r",
      "\x1b[A",
      "\x1b[200~pasted\x1b[201~",
      "\x1b]0;tab title\x07",
      "\x1b[1;2Rextra",
      "x1b[0n",
      "\x1b[?1;2c\r",
      "\x1b]52;c;aGVsbG8=\x07",
    ];
    for (const input of inputs) {
      expect(classifyTerminalResponse(input), JSON.stringify(input)).toBeNull();
    }
  });

  it("keys replies so only state reports and identical answers collapse", () => {
    expect(terminalResponseKey("cpr", "\x1b[1;1R")).toBe(terminalResponseKey("cpr", "\x1b[9;3R"));
    expect(terminalResponseKey("cpr", "\x1b[?1;1R")).not.toBe(terminalResponseKey("cpr", "\x1b[1;1R"));
    expect(terminalResponseKey("window", "\x1b[8;24;80t")).toBe(terminalResponseKey("window", "\x1b[8;40;120t"));
    expect(terminalResponseKey("osc-color", "\x1b]4;1;rgb:0/0/0\x07")).not.toBe(
      terminalResponseKey("osc-color", "\x1b]4;2;rgb:0/0/0\x07"),
    );
    expect(terminalResponseKey("osc-color", "\x1b]10;rgb:0/0/0\x07")).not.toBe(
      terminalResponseKey("osc-color", "\x1b]11;rgb:0/0/0\x07"),
    );
    expect(terminalResponseKey("da", "\x1b[?1;2c")).not.toBe(terminalResponseKey("da", "\x1b[>0;276;0c"));
    expect(terminalResponseKey("dcs", "\x1bP1$r0m\x1b\\")).not.toBe(terminalResponseKey("dcs", "\x1bP1$r2 q\x1b\\"));
    expect(terminalResponseKey("mode", "\x1b[?2004;1$y")).toBe(terminalResponseKey("mode", "\x1b[?2004;2$y"));
    expect(terminalResponseKey("mode", "\x1b[?2004;1$y")).not.toBe(terminalResponseKey("mode", "\x1b[?1049;1$y"));
    expect(terminalResponseKey("mode", "\x1b[?4;1$y")).not.toBe(terminalResponseKey("mode", "\x1b[4;1$y"));
    expect(terminalResponseKey("kitty-keyboard", "\x1b[?0u")).toBe(terminalResponseKey("kitty-keyboard", "\x1b[?1u"));
  });
});

describe("terminal automatic response limiter", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  function harness(overrides: Partial<Parameters<typeof createTerminalResponseLimiter>[0]> = {}) {
    const sent: string[] = [];
    const logs: TerminalResponseStats[] = [];
    const limiter = createTerminalResponseLimiter({
      send: (data) => sent.push(data),
      log: (_message, stats) => logs.push(stats),
      ...overrides,
    });
    return { sent, logs, limiter };
  }

  it("passes user input through synchronously, even with an empty bucket", () => {
    const { sent, limiter } = harness({ burstBytes: 0 });
    expect(limiter.submit("ls -la\r")).toBe("input");
    expect(limiter.submit("\x1b[A")).toBe("input");
    expect(sent).toEqual(["ls -la\r", "\x1b[A"]);
    expect(limiter.stats().sent).toBe(0);
  });

  it("answers 1000 queries spaced over hours exactly once, in order and immediately", () => {
    const { sent, logs, limiter } = harness();
    const expected: string[] = [];
    for (let i = 0; i < 1000; i += 1) {
      // fish/prompt_toolkit CPR per prompt plus TUIs re-querying DA and colours.
      const batch = [`\x1b[${(i % 50) + 1};1R`, "\x1b[?62;22c", `\x1b]11;rgb:${i.toString(16)}/0/0\x1b\\`];
      for (const reply of batch) {
        expect(limiter.submit(reply)).toBe("sent");
        expected.push(reply);
      }
      expect(sent).toEqual(expected);
      vi.advanceTimersByTime(15_000);
    }
    expect(sent).toEqual(expected);
    expect(limiter.stats()).toMatchObject({ sent: 3000, deferred: 0, coalesced: 0, dropped: 0, pending: 0 });
    expect(logs).toEqual([]);
  });

  it("answers a full palette probe burst without deferral", () => {
    const { sent, limiter } = harness();
    const replies = Array.from({ length: 256 }, (_, i) => `\x1b]4;${i};rgb:ffff/ffff/ffff\x1b\\`);
    for (const reply of replies) expect(limiter.submit(reply)).toBe("sent");
    expect(sent).toEqual(replies);
  });

  it("bounds the upstream byte rate of a 10k queries/s flood and keeps answering", () => {
    const { sent, logs, limiter } = harness();
    const reply = "\x1b[?62;22c";
    const sentBytes = () => sent.reduce((n, s) => n + s.length, 0);
    const seconds = 10;
    for (let ms = 0; ms < seconds * 1000; ms += 1) {
      for (let i = 0; i < 10; i += 1) limiter.submit(i % 2 ? reply : `\x1b[${ms % 24};${i}R`);
      vi.advanceTimersByTime(1);
    }
    vi.advanceTimersByTime(1000);
    expect(sentBytes()).toBeLessThanOrEqual(RESPONSE_BURST_BYTES + (seconds + 1) * RESPONSE_REFILL_BYTES_PER_SECOND);
    // The flood keeps getting answers rather than being cut off.
    expect(sent.at(-1)).toBeDefined();
    const stats = limiter.stats();
    expect(stats.pending).toBe(0);
    expect(stats.coalesced).toBeGreaterThan(90_000);
    expect(logs).toHaveLength(1);
    expect(logs[0].coalesced).toBeGreaterThan(0);
  });

  it("queues replies behind earlier queued ones and drains them in order as tokens refill", () => {
    const { sent, limiter } = harness({ burstBytes: 10, refillBytesPerSecond: 10 });
    expect(limiter.submit("\x1b[?1;2c")).toBe("sent"); // 7 bytes
    expect(limiter.submit("\x1b[0n")).toBe("queued");
    expect(limiter.submit("\x1b[8;24;80t")).toBe("queued");
    expect(limiter.submit("\x1b[3;1R")).toBe("queued");
    expect(limiter.submit("\x1b[4;1R")).toBe("coalesced");
    // User input is never delayed by queued replies.
    expect(limiter.submit("x")).toBe("input");
    expect(sent).toEqual(["\x1b[?1;2c", "x"]);
    vi.advanceTimersByTime(5_000);
    expect(sent).toEqual(["\x1b[?1;2c", "x", "\x1b[0n", "\x1b[8;24;80t", "\x1b[4;1R"]);
    expect(limiter.stats()).toMatchObject({ sent: 4, deferred: 3, coalesced: 1, dropped: 0, pending: 0 });
  });

  it("keeps a full palette probe and the DA1 fence after it when the bucket is empty", () => {
    const { sent, limiter } = harness({ burstBytes: 4, refillBytesPerSecond: 1024 });
    expect(limiter.submit("\x1b[0n")).toBe("sent");
    const palette = Array.from({ length: 256 }, (_, i) => `\x1b]4;${i};rgb:ffff/ffff/ffff\x1b\\`);
    for (const reply of palette) expect(limiter.submit(reply)).toBe("queued");
    expect(limiter.submit("\x1b]11;rgb:0000/0000/0000\x1b\\")).toBe("queued");
    expect(limiter.submit("\x1b[?62;22c")).toBe("queued");
    vi.advanceTimersByTime(60_000);
    expect(sent).toEqual(["\x1b[0n", ...palette, "\x1b]11;rgb:0000/0000/0000\x1b\\", "\x1b[?62;22c"]);
    expect(limiter.stats()).toMatchObject({ dropped: 0, pending: 0 });
  });

  it("rate-limits mode and kitty keyboard replies like other replies", () => {
    const { sent, limiter } = harness({ burstBytes: 12, refillBytesPerSecond: 12 });
    expect(limiter.submit("\x1b[?2004;1$y")).toBe("sent");
    expect(limiter.submit("\x1b[?2004;2$y")).toBe("queued");
    expect(limiter.submit("\x1b[?2004;1$y")).toBe("coalesced");
    expect(limiter.submit("\x1b[?1u")).toBe("queued");
    expect(limiter.submit("\x1b[?5u")).toBe("coalesced");
    vi.advanceTimersByTime(10_000);
    expect(sent).toEqual(["\x1b[?2004;1$y", "\x1b[?2004;1$y", "\x1b[?5u"]);
  });

  it("drops replies beyond the queued byte bound and counts them", () => {
    const { sent, logs, limiter } = harness({ burstBytes: 4, refillBytesPerSecond: 1000, maxQueuedBytes: 18 });
    expect(limiter.submit("\x1b[0n")).toBe("sent");
    sent.length = 0;
    expect(limiter.submit("\x1bP1$r0m\x1b\\")).toBe("queued");
    expect(limiter.submit("\x1bP1$r1m\x1b\\")).toBe("queued");
    expect(limiter.submit("\x1bP1$r2m\x1b\\")).toBe("dropped");
    vi.advanceTimersByTime(1000);
    expect(sent).toEqual(["\x1bP1$r0m\x1b\\", "\x1bP1$r1m\x1b\\"]);
    expect(limiter.stats().dropped).toBe(1);
    expect(logs).toEqual([expect.objectContaining({ dropped: 1, deferred: 2 })]);
  });

  it("never sends a previous generation's queued replies into a new generation", () => {
    const { sent, logs, limiter } = harness({ burstBytes: 4, refillBytesPerSecond: 100 });
    expect(limiter.submit("\x1b[0n")).toBe("sent");
    expect(limiter.submit("\x1b[?1;2c")).toBe("queued");
    expect(limiter.submit("\x1b[5;1R")).toBe("queued");
    limiter.setGeneration(1);
    vi.advanceTimersByTime(60_000);
    expect(sent).toEqual(["\x1b[0n"]);
    expect(logs).toEqual([expect.objectContaining({ generation: 0, dropped: 2 })]);
    expect(limiter.stats()).toMatchObject({ generation: 1, dropped: 0, pending: 0 });
    expect(limiter.submit("\x1b[3n")).toBe("sent");
    expect(sent).toEqual(["\x1b[0n", "\x1b[3n"]);
  });

  it("still sends a reply larger than the whole burst once the bucket is full", () => {
    const { sent, limiter } = harness({ burstBytes: 4, refillBytesPerSecond: 4 });
    const long = "\x1bP>|xterm.js(6.1.0)\x1b\\";
    expect(limiter.submit(long)).toBe("sent");
    expect(limiter.submit(long)).toBe("queued");
    vi.advanceTimersByTime(60_000);
    expect(sent).toEqual([long, long]);
  });

  it("logs at most once per generation", () => {
    const { logs, limiter } = harness({ burstBytes: 4, refillBytesPerSecond: 1000 });
    for (let round = 0; round < 3; round += 1) {
      limiter.submit("\x1b[0n");
      limiter.submit("\x1b[0n");
      vi.advanceTimersByTime(1000);
    }
    expect(logs).toHaveLength(1);
    limiter.setGeneration(2);
    limiter.submit("\x1b[0n");
    limiter.submit("\x1b[0n");
    vi.advanceTimersByTime(1000);
    expect(logs.map((s) => s.generation)).toEqual([0, 2]);
  });

  it("discards queued replies on dispose and writes nothing afterwards", () => {
    const { sent, logs, limiter } = harness({ burstBytes: 4, refillBytesPerSecond: 100 });
    limiter.submit("\x1b[0n");
    expect(limiter.submit("\x1b[3n")).toBe("queued");
    limiter.dispose();
    vi.advanceTimersByTime(60_000);
    expect(limiter.submit("\x1b[0n")).toBe("dropped");
    expect(limiter.submit("typed")).toBe("input");
    expect(sent).toEqual(["\x1b[0n", "typed"]);
    expect(logs).toEqual([expect.objectContaining({ dropped: 1 })]);
  });

  it("logs through console.warn by default", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const limiter = createTerminalResponseLimiter({ send: () => undefined, burstBytes: 4 });
    limiter.submit("\x1b[0n");
    limiter.submit("\x1b[3n");
    limiter.dispose();
    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn.mock.calls[0][1]).toMatchObject({ dropped: 1 });
    warn.mockRestore();
  });
});

describe("terminal automatic responses from a real xterm", () => {
  it("answers a realistic TUI start-up burst in full and in order", async () => {
    window.matchMedia = (() => ({
      matches: false,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
      addListener: () => undefined,
      removeListener: () => undefined,
    })) as unknown as typeof window.matchMedia;
    const term = new Terminal({ allowProposedApi: true, cols: 80, rows: 24 });
    const host = document.createElement("div");
    document.body.appendChild(host);
    term.open(host);
    const sent: string[] = [];
    const limiter = createTerminalResponseLimiter({ send: (data) => sent.push(data), log: () => undefined });
    term.onData((data) => limiter.submit(data));
    const queries = [
      "\x1b[c", // DA1
      "\x1b[>c", // DA2
      "\x1b[>0q", // XTVERSION
      "\x1b]10;?\x1b\\",
      "\x1b]11;?\x1b\\",
      "\x1b]4;1;?\x1b\\",
      "\x1b]4;2;?\x1b\\",
      "\x1b[5;7H\x1b[6n", // CPR
      "\x1b[5n", // DSR
    ];
    for (let round = 0; round < 50; round += 1) {
      await new Promise<void>((resolve) => term.write(queries.join(""), resolve));
    }
    term.dispose();
    host.remove();
    const perRound = sent.length / 50;
    expect(perRound).toBe(queries.length);
    const kinds = sent.slice(0, queries.length).map(classifyTerminalResponse);
    expect(kinds).toEqual(["da", "da", "dcs", "osc-color", "osc-color", "osc-color", "osc-color", "cpr", "dsr"]);
    expect(sent[7]).toBe("\x1b[5;7R");
    expect(limiter.stats()).toMatchObject({ deferred: 0, coalesced: 0, dropped: 0 });
  });
});

describe("automatic reply detection for replayed output", () => {
  it("covers limiter replies and unsolicited reports but not user input", () => {
    for (const reply of ["\x1b[?1;2c", "\x1b[3;7R", "\x1b[0n", "\x1b[I", "\x1b[O", "\x1b[?997;1n", "\x1bP>|xterm.js(6)\x1b\\"]) {
      expect(isAutomaticTerminalReply(reply)).toBe(true);
    }
    for (const input of ["ls\r", "\x1b", "\x1b[A", "\x1b[200~text\x1b[201~", "\x03"]) {
      expect(isAutomaticTerminalReply(input)).toBe(false);
    }
  });
});
