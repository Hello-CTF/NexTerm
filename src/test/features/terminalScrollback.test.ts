/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import { Terminal } from "@xterm/xterm";

describe("terminal scrollback", () => {
  it("retains 100k lines of scrollback and serves deep buffer lines", async () => {
    const term = new Terminal({ scrollback: 100_000, cols: 80, rows: 24 });
    const total = 120_000;
    const data = `${Array.from({ length: total }, (_, i) => `line-${i}`).join("\r\n")}\r\n`;
    await new Promise<void>((resolve) => term.write(data, resolve));

    const buffer = term.buffer.active;
    expect(buffer.length).toBe(100_024);
    expect(buffer.baseY).toBe(100_000);

    const firstRetained = total + 1 - buffer.length;
    expect(buffer.getLine(0)?.translateToString(true)).toBe(`line-${firstRetained}`);
    expect(buffer.getLine(500)?.translateToString(true)).toBe(`line-${firstRetained + 500}`);
    expect(buffer.getLine(buffer.length - 1)?.translateToString(true)).toBe("");
    term.dispose();
  });
});
