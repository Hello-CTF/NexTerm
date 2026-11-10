/** @vitest-environment jsdom */

import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { Terminal, type IUnicodeVersionProvider } from "@xterm/xterm";
import { Unicode11Addon } from "@xterm/addon-unicode11";
import { activateUnicode11 } from "../../features/terminal/xtermAddons";
import { snapshotEscape } from "../../features/sharing/previewApi";

// The fixture is shared with internal/terminal/width_test.go: the server screen model and the frontend renderer must
// give every code point the same number of cells, or cursor columns from snapshots and replays drift from the live view.
interface UnicodeWidthFixture {
  samples: { name: string; text: string; cells: number }[];
  screen: { cols: number; rows: number; input: string; lines: string[]; cursorRow: number; cursorCol: number };
  ranges: [number, number, number][];
}

const fixture = JSON.parse(readFileSync("internal/terminal/testdata/unicode_widths.json", "utf8")) as UnicodeWidthFixture;

function unicodeTerminal(cols: number, rows: number): Terminal {
  const terminal = new Terminal({ cols, rows, allowProposedApi: true });
  activateUnicode11(terminal);
  return terminal;
}

function write(terminal: Terminal, data: string): Promise<void> {
  return new Promise((resolve) => terminal.write(data, resolve));
}

function visibleLines(terminal: Terminal): string[] {
  const buffer = terminal.buffer.active;
  const lines: string[] = [];
  for (let row = 0; row < terminal.rows; row += 1) {
    lines.push(buffer.getLine(buffer.viewportY + row)?.translateToString(true).trimEnd() ?? "");
  }
  while (lines.length > 0 && lines[lines.length - 1] === "") lines.pop();
  return lines;
}

describe("terminal unicode widths", () => {
  it("activates the unicode 11 table", () => {
    const terminal = unicodeTerminal(80, 4);
    try {
      expect(terminal.unicode.activeVersion).toBe("11");
    } finally {
      terminal.dispose();
    }
  });

  it("gives every printable code point the width the server screen model uses", () => {
    let provider: IUnicodeVersionProvider | undefined;
    new Unicode11Addon().activate({ unicode: { register: (p: IUnicodeVersionProvider) => (provider = p) } } as unknown as Terminal);
    expect(provider?.version).toBe("11");
    const expected = new Map<number, number>();
    for (const [first, last, width] of fixture.ranges) {
      for (let cp = first; cp <= last; cp += 1) expected.set(cp, width);
    }
    const mismatches: string[] = [];
    for (let cp = 0x20; cp <= 0x10ffff; cp += 1) {
      if ((cp >= 0x7f && cp < 0xa0) || (cp >= 0xd800 && cp <= 0xdfff)) continue;
      const got = provider!.wcwidth(cp);
      const want = expected.get(cp) ?? 1;
      if (got !== want && mismatches.length < 20) mismatches.push(`U+${cp.toString(16).toUpperCase()}: ${got} != ${want}`);
    }
    expect(mismatches).toEqual([]);
  });

  it.each(fixture.samples)("advances the cursor by the shared width: $name", async ({ text, cells }) => {
    const terminal = unicodeTerminal(80, 4);
    try {
      await write(terminal, text);
      expect([terminal.buffer.active.cursorY, terminal.buffer.active.cursorX]).toEqual([0, cells]);
    } finally {
      terminal.dispose();
    }
  });

  it("renders a server snapshot at the same cells as the live output", async () => {
    const { cols, rows, input, lines, cursorRow, cursorCol } = fixture.screen;
    const live = unicodeTerminal(cols, rows);
    const restored = unicodeTerminal(cols, rows);
    try {
      await write(live, input);
      // The live view lays the output out exactly as the server model recorded it.
      expect(visibleLines(live)).toEqual(lines);
      expect([live.buffer.active.cursorY, live.buffer.active.cursorX]).toEqual([cursorRow, cursorCol]);

      // A view restored from the server snapshot puts the text and cursor on the same cells.
      await write(restored, snapshotEscape({ lines, cursorRow, cursorCol, cols, rows }));
      expect(visibleLines(restored)).toEqual(lines);
      expect([restored.buffer.active.cursorY, restored.buffer.active.cursorX]).toEqual([cursorRow, cursorCol]);
      await write(live, "X");
      await write(restored, "X");
      expect(visibleLines(restored)).toEqual(visibleLines(live));
    } finally {
      live.dispose();
      restored.dispose();
    }
  });
});
