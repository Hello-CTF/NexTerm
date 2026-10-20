import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  classifyTerminalResponse,
  createTerminalResponseCoalescer,
  type TerminalResponseOverflow,
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
});

describe("terminal automatic response coalescer", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("passes user input through synchronously without touching response budgets", () => {
    const send = vi.fn();
    const c = createTerminalResponseCoalescer({ send, immediate: 0, budgets: { da: 0 } });
    expect(c.submit("ls -la\r")).toBe("input");
    expect(c.submit("\x1b[A")).toBe("input");
    expect(send.mock.calls.map(([data]) => data)).toEqual(["ls -la\r", "\x1b[A"]);
    expect(c.stats().dropped).toBe(0);
  });

  it("sends the first responses of a type immediately and coalesces the rest to the latest payload", () => {
    const send = vi.fn();
    const c = createTerminalResponseCoalescer({ send, immediate: 2, windowMs: 100 });
    expect(c.submit("\x1b[?1;2c")).toBe("sent");
    expect(c.submit("\x1b[>0;276;0c")).toBe("sent");
    expect(c.submit("\x1b[?62;22c")).toBe("coalesced");
    expect(c.submit("\x1b[?63;23c")).toBe("coalesced");
    expect(send).toHaveBeenCalledTimes(2);
    vi.advanceTimersByTime(99);
    expect(send).toHaveBeenCalledTimes(2);
    vi.advanceTimersByTime(1);
    expect(send).toHaveBeenCalledTimes(3);
    expect(send).toHaveBeenLastCalledWith("\x1b[?63;23c");
    expect(c.stats().coalesced).toBe(2);
  });

  it("tracks pending responses per type independently", () => {
    const send = vi.fn();
    const c = createTerminalResponseCoalescer({ send, immediate: 0, windowMs: 100 });
    c.submit("\x1b[?1;2c");
    c.submit("\x1b[0n");
    c.submit("\x1b[8;24;80t");
    vi.advanceTimersByTime(100);
    expect(send.mock.calls.map(([data]) => data)).toEqual([
      "\x1b[?1;2c",
      "\x1b[0n",
      "\x1b[8;24;80t",
    ]);
  });

  it("does not delay user input while responses are pending", () => {
    const send = vi.fn();
    const c = createTerminalResponseCoalescer({ send, immediate: 0, windowMs: 100 });
    c.submit("\x1b[?1;2c");
    expect(c.submit("user keystroke\r")).toBe("input");
    expect(send).toHaveBeenCalledTimes(1);
    expect(send).toHaveBeenCalledWith("user keystroke\r");
  });

  it("drops responses beyond the per-generation budget with visible overflow", () => {
    const send = vi.fn();
    const overflows: TerminalResponseOverflow[] = [];
    const c = createTerminalResponseCoalescer({
      send,
      immediate: 1,
      windowMs: 50,
      budgets: { da: 2 },
      onOverflow: (info) => overflows.push(info),
    });
    expect(c.submit("\x1b[?1;2c")).toBe("sent");
    expect(c.submit("\x1b[?62;22c")).toBe("coalesced");
    vi.advanceTimersByTime(50);
    expect(send).toHaveBeenCalledTimes(2);

    expect(c.submit("\x1b[?63;23c")).toBe("dropped");
    expect(c.submit("\x1b[?64;24c")).toBe("dropped");
    expect(send).toHaveBeenCalledTimes(2);
    expect(overflows).toEqual([
      { type: "da", generation: 0, dropped: 1, budget: 2 },
      { type: "da", generation: 0, dropped: 2, budget: 2 },
    ]);
    expect(c.stats().dropped).toBe(2);
  });

  it("flushes pending responses on a generation change and re-arms budgets", () => {
    const send = vi.fn();
    const c = createTerminalResponseCoalescer({
      send,
      immediate: 0,
      windowMs: 10_000,
      budgets: { da: 1 },
    });
    c.submit("\x1b[?1;2c");
    expect(send).not.toHaveBeenCalled();
    c.setGeneration(1);
    expect(send).toHaveBeenCalledTimes(1);
    expect(send).toHaveBeenCalledWith("\x1b[?1;2c");
    expect(c.stats().generation).toBe(1);
    expect(c.stats().pending).toBe(0);

    c.submit("\x1b[?62;22c");
    vi.advanceTimersByTime(10_000);
    expect(send).toHaveBeenCalledTimes(2);
    expect(c.submit("\x1b[?63;23c")).toBe("dropped");
    expect(c.stats().dropped).toBe(1);
  });

  it("keeps budgets per generation", () => {
    const send = vi.fn();
    const overflows: TerminalResponseOverflow[] = [];
    const c = createTerminalResponseCoalescer({
      send,
      immediate: 1,
      windowMs: 10,
      budgets: { dsr: 1 },
      onOverflow: (info) => overflows.push(info),
    });
    expect(c.submit("\x1b[0n")).toBe("sent");
    expect(c.submit("\x1b[3n")).toBe("dropped");
    expect(overflows).toEqual([{ type: "dsr", generation: 0, dropped: 1, budget: 1 }]);

    c.setGeneration(7);
    expect(c.submit("\x1b[0n")).toBe("sent");
    expect(send).toHaveBeenCalledTimes(2);
    expect(c.submit("\x1b[3n")).toBe("dropped");
    expect(overflows[1]).toEqual({ type: "dsr", generation: 7, dropped: 1, budget: 1 });
  });

  it("coalesces a flood of queries into one write per window per type", () => {
    const send = vi.fn();
    const c = createTerminalResponseCoalescer({ send, immediate: 0, windowMs: 100 });
    for (let i = 1; i <= 500; i += 1) c.submit(`\x1b[${i};1R`);
    expect(send).not.toHaveBeenCalled();
    vi.advanceTimersByTime(100);
    expect(send).toHaveBeenCalledTimes(1);
    expect(send).toHaveBeenCalledWith("\x1b[500;1R");
    vi.advanceTimersByTime(100);
    expect(send).toHaveBeenCalledTimes(1);
  });
});
