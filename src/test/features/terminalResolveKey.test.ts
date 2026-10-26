/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const harness = vi.hoisted(() => ({ mac: false }));

vi.mock("../../app/platform", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../app/platform")>()),
  isMac: () => harness.mac,
}));
vi.mock("../../ipc/env", () => ({ WEB: false }));

import { matchTerminalWithheldKeybinding, resetAllKeybindings, setKeybinding } from "../../app/keybindings";
import {
  COMPOSITION_COMMIT_WINDOW_MS,
  resolveKey,
  type KeyResolution,
  type ResolveKeyContext,
  type TerminalKeyEvent,
} from "../../features/terminal/terminalInput/resolveKey";

type Mods = Partial<Pick<TerminalKeyEvent, "shiftKey" | "ctrlKey" | "altKey" | "metaKey" | "repeat" | "isComposing">>;

function ev(key: string, code: string, mods: Mods = {}, extra: Partial<TerminalKeyEvent> = {}): TerminalKeyEvent {
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
    ...extra,
  };
}

function ctx(overrides: Partial<ResolveKeyContext> = {}): ResolveKeyContext {
  return {
    mac: harness.mac,
    matchAppShortcut: matchTerminalWithheldKeybinding,
    sinceCompositionEnd: null,
    kittyKeyboard: false,
    ...overrides,
  };
}

const bytes = (value: string): KeyResolution => ({ kind: "terminal-bytes", bytes: value });
const PASS: KeyResolution = { kind: "pass" };
const app = (action: string, repeat = false, digit: number | null = null): KeyResolution =>
  ({ kind: "app-shortcut", hit: { action, digit }, repeat }) as KeyResolution;

beforeEach(() => {
  harness.mac = false;
  localStorage.clear();
  resetAllKeybindings();
});

afterEach(() => {
  resetAllKeybindings();
});

describe("resolveKey precedence (desktop)", () => {
  const shared: Array<[string, TerminalKeyEvent, KeyResolution]> = [
    ["Shift+Enter inserts a newline", ev("Enter", "Enter", { shiftKey: true }), bytes("\x1b\r")],
    ["Enter is xterm's", ev("Enter", "Enter"), PASS],
    ["Ctrl+Backspace deletes a word", ev("Backspace", "Backspace", { ctrlKey: true }), bytes("\x17")],
    ["Backspace is xterm's", ev("Backspace", "Backspace"), PASS],
    ["Shift+Enter repeats keep the override", ev("Enter", "Enter", { shiftKey: true, repeat: true }), bytes("\x1b\r")],
    ["plain letters pass", ev("a", "KeyA"), PASS],
    ["Shift letters pass, also on repeat", ev("H", "KeyH", { shiftKey: true, repeat: true }), PASS],
    ["Ctrl+C passes to xterm", ev("c", "KeyC", { ctrlKey: true }), PASS],
    ["Mod+Shift+P is the command palette", ev("P", "KeyP", { ctrlKey: true, shiftKey: true }), app("commandPalette")],
    ["app shortcut repeats are flagged", ev("P", "KeyP", { ctrlKey: true, shiftKey: true, repeat: true }), app("commandPalette", true)],
    ["Mod+Shift+W closes the pane", ev("W", "KeyW", { ctrlKey: true, shiftKey: true }), app("closePane")],
    ["Mod+Shift+\\ splits right", ev("|", "Backslash", { ctrlKey: true, shiftKey: true }), app("splitRight")],
    ["Mod+\\ splits down (withholds SIGQUIT)", ev("\\", "Backslash", { ctrlKey: true }), app("splitDown")],
    ["Mod+3 switches tab", ev("3", "Digit3", { ctrlKey: true }), app("switchTab", false, 3)],
    ["composition keys belong to the IME", ev("Enter", "Enter", { isComposing: true }), { kind: "ime", toXterm: false }],
    ["keyCode 229 stays with xterm's composition helper", ev("Process", "KeyP", { ctrlKey: true, shiftKey: true }, { keyCode: 229 }), { kind: "ime", toXterm: true }],
    ["IME beats an app shortcut", ev("P", "KeyP", { ctrlKey: true, shiftKey: true, isComposing: true }), { kind: "ime", toXterm: false }],
  ];

  for (const mac of [false, true]) {
    for (const [name, event, expected] of shared) {
      it(`${mac ? "macOS" : "Linux/Windows"}: ${name}`, () => {
        harness.mac = mac;
        expect(resolveKey(event, ctx())).toEqual(expected);
      });
    }
  }

  it.each<[string, TerminalKeyEvent, KeyResolution, KeyResolution]>([
    ["Option/Alt+Left", ev("ArrowLeft", "ArrowLeft", { altKey: true }), bytes("\x1bb"), PASS],
    ["Option/Alt+Right", ev("ArrowRight", "ArrowRight", { altKey: true }), bytes("\x1bf"), PASS],
    ["Cmd/Meta+Left", ev("ArrowLeft", "ArrowLeft", { metaKey: true }), bytes("\x01"), PASS],
    ["Cmd/Meta+Right", ev("ArrowRight", "ArrowRight", { metaKey: true }), bytes("\x05"), PASS],
    ["Cmd/Meta+Backspace", ev("Backspace", "Backspace", { metaKey: true }), bytes("\x15"), PASS],
    ["Ctrl+Tab cycles tabs off macOS only", ev("Tab", "Tab", { ctrlKey: true }), app("nextTab"), app("nextTab")],
    ["Cmd+Shift+] cycles tabs on macOS only", ev("}", "BracketRight", { metaKey: true, shiftKey: true }), app("nextTab"), PASS],
    ["Ctrl+F is terminal search off macOS only", ev("f", "KeyF", { ctrlKey: true }), PASS, app("terminalSearch")],
    ["Cmd+F is terminal search on macOS only", ev("f", "KeyF", { metaKey: true }), app("terminalSearch"), PASS],
  ])("%s differs by platform", (_name, event, onMac, offMac) => {
    harness.mac = true;
    expect(resolveKey(event, ctx())).toEqual(onMac);
    harness.mac = false;
    expect(resolveKey(event, ctx())).toEqual(offMac);
  });

  it("follows user rebinding: a freed chord reaches the terminal, a newly bound one is withheld", () => {
    const closePane = ev("W", "KeyW", { ctrlKey: true, shiftKey: true });
    const shiftEnter = ev("Enter", "Enter", { shiftKey: true });
    setKeybinding("closePane", null);
    expect(resolveKey(closePane, ctx())).toEqual(PASS);
    setKeybinding("closePane", "Shift+Enter");
    expect(resolveKey(shiftEnter, ctx())).toEqual(app("closePane"));
    setKeybinding("splitDown", null);
    expect(resolveKey(ev("\\", "Backslash", { ctrlKey: true }), ctx())).toEqual(PASS);
  });

  it("treats only an Enter right behind compositionend as the committing keystroke", () => {
    const enter = ev("Enter", "Enter", {}, { keyCode: 13 });
    expect(resolveKey(enter, ctx({ sinceCompositionEnd: 0 }))).toEqual({ kind: "ime", toXterm: false });
    expect(resolveKey({ ...enter, type: "keypress" }, ctx({ sinceCompositionEnd: COMPOSITION_COMMIT_WINDOW_MS }))).toEqual({
      kind: "ime",
      toXterm: false,
    });
    expect(resolveKey(enter, ctx({ sinceCompositionEnd: COMPOSITION_COMMIT_WINDOW_MS + 1 }))).toEqual(PASS);
    expect(resolveKey(ev("a", "KeyA"), ctx({ sinceCompositionEnd: 0 }))).toEqual(PASS);
  });

  it("leaves encoding to xterm while the program uses the kitty keyboard protocol, but still withholds app shortcuts", () => {
    const kitty = ctx({ kittyKeyboard: true });
    expect(resolveKey(ev("Enter", "Enter", { shiftKey: true }), kitty)).toEqual(PASS);
    expect(resolveKey(ev("Backspace", "Backspace", { ctrlKey: true }), kitty)).toEqual(PASS);
    expect(resolveKey(ev("P", "KeyP", { ctrlKey: true, shiftKey: true }), kitty)).toEqual(app("commandPalette"));
  });

  it("resolves keyup and keypress of an app shortcut the same way, so they never reach the terminal either", () => {
    const up = ev("W", "KeyW", { ctrlKey: true, shiftKey: true }, { type: "keyup" });
    expect(resolveKey(up, ctx())).toEqual(app("closePane"));
  });
});
