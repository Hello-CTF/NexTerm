import { describe, expect, it } from "vitest";
import { terminalKeyOverride } from "../../features/terminal/terminalKeys";

const key = (k: string, mods: Partial<Record<"shiftKey" | "ctrlKey" | "altKey" | "metaKey", boolean>> = {}) => ({
  key: k,
  shiftKey: false,
  ctrlKey: false,
  altKey: false,
  metaKey: false,
  ...mods,
});

describe("terminalKeyOverride", () => {
  it("sends ESC+CR for plain Shift+Enter on every platform", () => {
    expect(terminalKeyOverride(key("Enter", { shiftKey: true }), false)).toBe("\x1b\r");
    expect(terminalKeyOverride(key("Enter", { shiftKey: true }), true)).toBe("\x1b\r");
    expect(terminalKeyOverride(key("Enter"), false)).toBeNull();
    expect(terminalKeyOverride(key("Enter", { shiftKey: true, ctrlKey: true }), false)).toBeNull();
  });

  it("maps Ctrl+Backspace to delete-word", () => {
    expect(terminalKeyOverride(key("Backspace", { ctrlKey: true }), false)).toBe("\x17");
    expect(terminalKeyOverride(key("Backspace"), false)).toBeNull();
  });

  it("maps macOS Option and Cmd navigation only on macOS", () => {
    expect(terminalKeyOverride(key("ArrowLeft", { altKey: true }), true)).toBe("\x1bb");
    expect(terminalKeyOverride(key("ArrowRight", { altKey: true }), true)).toBe("\x1bf");
    expect(terminalKeyOverride(key("ArrowLeft", { metaKey: true }), true)).toBe("\x01");
    expect(terminalKeyOverride(key("ArrowRight", { metaKey: true }), true)).toBe("\x05");
    expect(terminalKeyOverride(key("Backspace", { metaKey: true }), true)).toBe("\x15");
    expect(terminalKeyOverride(key("ArrowLeft", { altKey: true }), false)).toBeNull();
    expect(terminalKeyOverride(key("ArrowLeft", { metaKey: true }), false)).toBeNull();
  });
});
