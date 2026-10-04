import { describe, expect, it } from "vitest";
import {
  BRACKETED_PASTE_END,
  BRACKETED_PASTE_START,
  wrapBracketedPaste,
} from "../../features/terminal/terminalPaste";

describe("bracketed paste wrapping", () => {
  it("wraps multi-line text only when the application enabled the mode", () => {
    const multiLine = "echo one\necho two\n";
    expect(wrapBracketedPaste(multiLine, true)).toBe(
      `${BRACKETED_PASTE_START}echo one\necho two\n${BRACKETED_PASTE_END}`,
    );
    expect(wrapBracketedPaste(multiLine, false)).toBe(multiLine);
  });

  it("preserves raw bytes otherwise, including carriage returns and escapes", () => {
    const raw = "a\rb\x1b[1;5C\n";
    expect(wrapBracketedPaste(raw, false)).toBe(raw);
    expect(wrapBracketedPaste("", true)).toBe(`${BRACKETED_PASTE_START}${BRACKETED_PASTE_END}`);
  });
});
