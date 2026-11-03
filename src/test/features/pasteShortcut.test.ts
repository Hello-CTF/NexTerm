/** @vitest-environment jsdom */
import { afterEach, describe, expect, it } from "vitest";
import { setMacPlatform, setWindowsPlatform } from "../../app/platform";
import { getCtrlVPaste, setCtrlVPaste } from "../../app/preferences";
import { pasteShortcutLabel } from "../../features/terminal/pasteShortcut";

afterEach(() => {
  setMacPlatform(false);
  setWindowsPlatform(false);
  localStorage.clear();
});

describe("pasteShortcutLabel", () => {
  it("names Cmd+V on macOS", () => {
    setMacPlatform(true);
    expect(pasteShortcutLabel(true)).toBe("Cmd+V");
  });

  it("names Ctrl+V on Windows only while the setting is on", () => {
    setWindowsPlatform(true);
    expect(pasteShortcutLabel(true)).toBe("Ctrl+V");
    expect(pasteShortcutLabel(false)).toBe("Ctrl+Shift+V");
  });

  it("names Ctrl+Shift+V on Linux", () => {
    expect(pasteShortcutLabel(true)).toBe("Ctrl+Shift+V");
  });
});

describe("Ctrl+V paste preference", () => {
  it("defaults to on and persists the choice locally", () => {
    expect(getCtrlVPaste()).toBe(true);
    setCtrlVPaste(false);
    expect(getCtrlVPaste()).toBe(false);
    expect(localStorage.getItem("nexterm.terminal.ctrlVPaste.v1")).toBe("0");
    setCtrlVPaste(true);
    expect(getCtrlVPaste()).toBe(true);
  });
});
