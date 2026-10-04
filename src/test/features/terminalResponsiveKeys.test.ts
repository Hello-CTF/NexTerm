import { describe, expect, it } from "vitest";
import { TERMINAL_KEYS, terminalKeySequence } from "../../features/terminal/terminalKeys";

describe("mobile terminal key sequences", () => {
  it("emits navigation and editing keys without depending on a hardware keyboard", () => {
    const byId = new Map(TERMINAL_KEYS.map((key) => [key.id, key]));
    expect(terminalKeySequence(byId.get("escape")!, false)).toBe("\x1b");
    expect(terminalKeySequence(byId.get("up")!, false)).toBe("\x1b[A");
    expect(terminalKeySequence(byId.get("home")!, false)).toBe("\x1b[H");
    expect(terminalKeySequence(byId.get("c")!, false)).toBe("c");
  });

  it("applies one-shot Ctrl sequences without bypassing terminal input handling", () => {
    const byId = new Map(TERMINAL_KEYS.map((key) => [key.id, key]));
    expect(terminalKeySequence(byId.get("c")!, true)).toBe("\x03");
    expect(terminalKeySequence(byId.get("d")!, true)).toBe("\x04");
    expect(terminalKeySequence(byId.get("z")!, true)).toBe("\x1a");
    expect(terminalKeySequence(byId.get("up")!, true)).toBe("\x1b[A");
  });
});
