/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const harness = vi.hoisted(() => ({ mac: false }));

vi.mock("../../app/platform", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../app/platform")>()),
  isMac: () => harness.mac,
}));
vi.mock("../../ipc/env", () => ({ WEB: true }));

import { getKeybindings, matchTerminalWithheldKeybinding, resetAllKeybindings } from "../../app/keybindings";
import { resolveKey, type TerminalKeyEvent } from "../../features/terminal/terminalInput/resolveKey";
import { terminalShortcutConflicts } from "../../features/terminal/terminalInput/shortcutConflicts";

function ev(key: string, code: string, mods: Partial<TerminalKeyEvent> = {}): TerminalKeyEvent {
  return {
    type: "keydown",
    key,
    code,
    keyCode: 0,
    shiftKey: false,
    ctrlKey: false,
    altKey: false,
    metaKey: false,
    repeat: false,
    isComposing: false,
    ...mods,
  };
}

const resolve = (event: TerminalKeyEvent) =>
  resolveKey(event, { mac: harness.mac, matchAppShortcut: matchTerminalWithheldKeybinding, sinceCompositionEnd: null, kittyKeyboard: false });

beforeEach(() => {
  harness.mac = false;
  localStorage.clear();
  resetAllKeybindings();
});

describe("resolveKey with the web keymap", () => {
  it.each([false, true])("leaves browser-reserved chords to the terminal and uses the web replacements (mac=%s)", (mac) => {
    harness.mac = mac;
    // Ctrl+Shift+W is the browser's close-window chord, so the web build binds closePane to Ctrl+Alt+W instead.
    expect(resolve(ev("W", "KeyW", { ctrlKey: true, shiftKey: true }))).toEqual({ kind: "pass" });
    expect(resolve(ev("w", "KeyW", { ctrlKey: true, altKey: true }))).toEqual({
      kind: "app-shortcut",
      hit: { action: "closePane", digit: null },
      repeat: false,
    });
    expect(resolve(ev("]", "BracketRight", { ctrlKey: true, altKey: true }))).toEqual({
      kind: "app-shortcut",
      hit: { action: "nextTab", digit: null },
      repeat: false,
    });
    // The desktop macOS tab chords are not active on the web.
    expect(resolve(ev("}", "BracketRight", { metaKey: true, shiftKey: true }))).toEqual({ kind: "pass" });
  });

  it("lists the web chords in the terminal conflict table", () => {
    const chords = terminalShortcutConflicts(getKeybindings(), false, true).map((conflict) => conflict.chord);
    expect(chords).toContain("Ctrl+Alt+W");
    expect(chords).not.toContain("Ctrl+Shift+W");
  });
});
