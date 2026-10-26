/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const harness = vi.hoisted(() => ({ mac: false }));

vi.mock("../../app/platform", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../app/platform")>()),
  isMac: () => harness.mac,
}));
vi.mock("../../ipc/env", () => ({ WEB: false }));

import { loadKeybindings, matchTerminalWithheldKeybinding, resetAllKeybindings, setKeybinding } from "../../app/keybindings";
import {
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
    commitEnterPending: false,
    kittyKeyboard: false,
    ...overrides,
  };
}

const bytes = (value: string): KeyResolution => ({ kind: "terminal-bytes", bytes: value });
const PASS: KeyResolution = { kind: "pass" };
const app = (action: string, dropRepeat = false, digit: number | null = null): KeyResolution =>
  ({ kind: "app-shortcut", hit: { action, digit }, dropRepeat }) as KeyResolution;

// Platform defaults (terminal search) are resolved when the keymap snapshot is built.
function setMac(mac: boolean): void {
  harness.mac = mac;
  loadKeybindings();
}

beforeEach(() => {
  setMac(false);
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
    ["a held palette shortcut is dropped", ev("P", "KeyP", { ctrlKey: true, shiftKey: true, repeat: true }), app("commandPalette", true)],
    ["a held split shortcut is dropped", ev("\\", "Backslash", { ctrlKey: true, repeat: true }), app("splitDown", true)],
    ["a held close-pane shortcut is dropped", ev("W", "KeyW", { ctrlKey: true, shiftKey: true, repeat: true }), app("closePane", true)],
    ["a held tab switch is dropped", ev("2", "Digit2", { ctrlKey: true, repeat: true }), app("switchTab", true, 2)],
    ["a held lock shortcut is dropped", ev("L", "KeyL", { ctrlKey: true, shiftKey: true, repeat: true }), app("lockVault", true)],
    ["a held sidebar toggle is dropped", ev("b", "KeyB", { ctrlKey: true, repeat: true }), app("toggleSidebar", true)],
    ["workspace navigation repeats", ev("ArrowRight", "ArrowRight", { ctrlKey: true, altKey: true, repeat: true }), app("nextWorkspace")],
    ["pane focus repeats", ev("ArrowUp", "ArrowUp", { ctrlKey: true, altKey: true, repeat: true }), app("focusPaneUp")],
    ["Mod+Shift+T restores a closed tab", ev("T", "KeyT", { ctrlKey: true, shiftKey: true }), app("restoreClosedTab")],
    ["Ctrl+F stays with readline", ev("f", "KeyF", { ctrlKey: true }), PASS],
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
        setMac(mac);
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
    ["Ctrl+Shift+F is terminal search off macOS only", ev("F", "KeyF", { ctrlKey: true, shiftKey: true }), PASS, app("terminalSearch")],
    ["Cmd+F is terminal search on macOS only", ev("f", "KeyF", { metaKey: true }), app("terminalSearch"), PASS],
    ["held Ctrl+Tab keeps cycling", ev("Tab", "Tab", { ctrlKey: true, repeat: true }), app("nextTab"), app("nextTab")],
  ])("%s differs by platform", (_name, event, onMac, offMac) => {
    setMac(true);
    expect(resolveKey(event, ctx())).toEqual(onMac);
    setMac(false);
    expect(resolveKey(event, ctx())).toEqual(offMac);
  });

  it("keeps a custom terminal search binding across platforms", () => {
    setKeybinding("terminalSearch", "Ctrl+f");
    setMac(true);
    expect(resolveKey(ev("f", "KeyF", { ctrlKey: true }), ctx())).toEqual(app("terminalSearch"));
    expect(resolveKey(ev("f", "KeyF", { metaKey: true }), ctx())).toEqual(PASS);
  });

  it("never treats an AltGr chord as an app shortcut", () => {
    const altGr = { ...ev("t", "KeyT", { ctrlKey: true, altKey: true }), getModifierState: (key: string) => key === "AltGraph" };
    setKeybinding("restoreClosedTab", "Ctrl+Alt+t");
    expect(resolveKey(altGr, ctx())).toEqual(PASS);
    expect(resolveKey({ ...altGr, getModifierState: () => false }, ctx())).toEqual(app("restoreClosedTab"));
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

  it("withholds only an Enter while a WebKit commit Enter is pending", () => {
    const enter = ev("Enter", "Enter", {}, { keyCode: 13 });
    expect(resolveKey(enter, ctx({ commitEnterPending: true }))).toEqual({ kind: "ime", toXterm: false });
    expect(resolveKey({ ...enter, type: "keypress" }, ctx({ commitEnterPending: true }))).toEqual({ kind: "ime", toXterm: false });
    expect(resolveKey(enter, ctx())).toEqual(PASS);
    expect(resolveKey(ev("a", "KeyA"), ctx({ commitEnterPending: true }))).toEqual(PASS);
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
