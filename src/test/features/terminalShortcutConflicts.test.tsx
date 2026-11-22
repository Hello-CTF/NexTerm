/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { Terminal } from "@xterm/xterm";
import { mount, type MountedView } from "./reactTestUtils";

const harness = vi.hoisted(() => ({ mac: false }));

vi.mock("../../app/platform", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../app/platform")>()),
  isMac: () => harness.mac,
}));
vi.mock("../../ipc/env", () => ({ WEB: false }));

import { getKeybindings, loadKeybindings, resetAllKeybindings, setKeybinding } from "../../app/keybindings";
import {
  terminalShortcutConflicts,
  type TerminalChord,
  type TerminalShortcutConflict,
} from "../../features/terminal/terminalInput/shortcutConflicts";
import { ShortcutsCard } from "../../features/settings/ShortcutsCard";

let mounted: MountedView | null = null;

beforeEach(() => {
  harness.mac = false;
  localStorage.clear();
  resetAllKeybindings();
  loadKeybindings();
});

afterEach(() => {
  mounted?.unmount();
  mounted = null;
  resetAllKeybindings();
});

function byChord(conflicts: TerminalShortcutConflict[], chord: string): TerminalShortcutConflict | undefined {
  return conflicts.find((conflict) => conflict.chord === chord);
}

const KEY_CODES: Record<string, number> = {
  "\\": 220,
  "[": 219,
  "]": 221,
  "=": 187,
  "-": 189,
  "/": 191,
  Tab: 9,
  Enter: 13,
  ArrowLeft: 37,
  ArrowUp: 38,
  ArrowRight: 39,
  ArrowDown: 40,
};

function domKey(chord: TerminalChord): KeyboardEventInit & { keyCode: number } {
  const { key } = chord;
  if (/^[a-z]$/.test(key)) {
    return { key: chord.shift ? key.toUpperCase() : key, code: `Key${key.toUpperCase()}`, keyCode: key.toUpperCase().charCodeAt(0) };
  }
  if (/^[0-9]$/.test(key)) return { key, code: `Digit${key}`, keyCode: key.charCodeAt(0) };
  const named = { "\\": "Backslash", "[": "BracketLeft", "]": "BracketRight", "=": "Equal", "-": "Minus", "/": "Slash" }[key];
  return { key, code: named ?? key, keyCode: KEY_CODES[key] ?? key.charCodeAt(0) };
}

describe("terminal shortcut conflict table", () => {
  it("lists the chords a focused terminal withholds under the default desktop keymap", () => {
    const conflicts = terminalShortcutConflicts(getKeybindings(), false, false);
    expect(byChord(conflicts, "Ctrl+Shift+D")).toMatchObject({ bytes: null, action: { id: "splitDown" } });
    expect(byChord(conflicts, "Ctrl+Shift+W")).toMatchObject({ bytes: null, action: { id: "closeTab" } });
    expect(byChord(conflicts, "Ctrl+Shift+Alt+W")).toMatchObject({ action: { id: "closePane" } });
    expect(byChord(conflicts, "Ctrl+Shift+T")).toMatchObject({ bytes: null, action: { id: "newTerminal" } });
    expect(byChord(conflicts, "Ctrl+Shift+Alt+T")).toMatchObject({ action: { id: "restoreClosedTab" } });
    expect(byChord(conflicts, "Ctrl+Shift+B")).toMatchObject({ bytes: null, action: { id: "toggleSidebar" } });
    expect(byChord(conflicts, "Ctrl+Shift+F")).toMatchObject({ bytes: null, action: { id: "terminalSearch" } });
    expect(byChord(conflicts, "Ctrl+Tab")).toMatchObject({ bytes: "\t", action: { id: "nextTab" } });
    expect(byChord(conflicts, "Ctrl+Alt+1…9")).toMatchObject({ action: { id: "switchTab" } });
    for (const chord of ["Ctrl+\\", "Ctrl+W", "Ctrl+B", "Ctrl+T", "Ctrl+J", "Ctrl+F", "Ctrl+1…9", "Ctrl+-", "Ctrl+=", "Ctrl+0", "Esc"]) {
      expect(byChord(conflicts, chord), chord).toBeUndefined();
    }
  });

  // The default keymap off macOS desktop never takes a key the shell or tmux uses: no bare Ctrl+character chord and
  // none of the shifted chords xterm encodes as control characters (Ctrl+_, Ctrl+@, Ctrl+^).
  it.each([
    ["Linux/Windows desktop", false, false],
    ["web", false, true],
    ["macOS web", true, true],
  ])("takes no terminal control key under the default keymap (%s)", (_name, mac, web) => {
    const conflicts = terminalShortcutConflicts(getKeybindings(), mac, web);
    for (const conflict of conflicts) {
      const { ctrl, shift, alt, key } = conflict.keys;
      const bareCtrl = ctrl && !shift && !alt;
      // Ctrl+, sends nothing, so the settings chord takes nothing from the program.
      expect(bareCtrl && (key === "1-9" || (key.length === 1 && conflict.bytes !== null)), conflict.chord).toBe(false);
      expect(["\x1f", "\x00", "\x1e"], conflict.chord).not.toContain(conflict.bytes);
    }
  });

  it("matches what xterm itself sends for every listed chord", () => {
    const host = document.createElement("div");
    document.body.append(host);
    const term = new Terminal();
    term.open(host);
    const textarea = term.textarea!;
    try {
      for (const conflict of terminalShortcutConflicts(getKeybindings(), false, false)) {
        if (conflict.keys.key === "1-9") continue;
        const sent: string[] = [];
        const listener = term.onData((data) => sent.push(data));
        const init = { ...domKey(conflict.keys), ctrlKey: conflict.keys.ctrl, shiftKey: conflict.keys.shift, altKey: conflict.keys.alt };
        textarea.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init }));
        textarea.dispatchEvent(new KeyboardEvent("keyup", { bubbles: true, cancelable: true, ...init }));
        listener.dispose();
        expect([conflict.chord, sent.join("") || null]).toEqual([conflict.chord, conflict.bytes]);
      }
    } finally {
      term.dispose();
      host.remove();
    }
  });

  it("reports what xterm sends for the shifted control chords a user may bind", () => {
    setKeybinding("terminalFontDecrease", "Mod+Shift+-");
    setKeybinding("lockApp", "Ctrl+Shift+6");
    const conflicts = terminalShortcutConflicts(getKeybindings(), false, false);
    expect(byChord(conflicts, "Ctrl+Shift+-")).toMatchObject({ bytes: "\x1f" });
    expect(byChord(conflicts, "Ctrl+Shift+6")).toMatchObject({ bytes: "\x1e" });
  });

  it("follows rebinding and drops chords that no longer reach the app", () => {
    setKeybinding("splitDown", null);
    setKeybinding("lockVault", "Ctrl+Alt+l");
    const conflicts = terminalShortcutConflicts(getKeybindings(), false, false);
    expect(byChord(conflicts, "Ctrl+Shift+D")).toBeUndefined();
    expect(byChord(conflicts, "Ctrl+Alt+L")).toMatchObject({ bytes: "\x1b\x0c", action: { id: "lockVault" } });
  });

  it("leaves Mod chords to the terminal on macOS desktop, where Mod is Cmd only", () => {
    harness.mac = true;
    loadKeybindings();
    const conflicts = terminalShortcutConflicts(getKeybindings(), true, false);
    for (const chord of ["Ctrl+W", "Ctrl+B", "Ctrl+T", "Ctrl+J", "Ctrl+\\", "Ctrl+1…9", "Ctrl+Shift+]", "Ctrl+Alt+→", "Ctrl+="]) {
      expect(byChord(conflicts, chord)).toBeUndefined();
    }
    // terminalSearch is Primary+F, which is Cmd+F on macOS and never reaches the terminal as input.
    expect(byChord(conflicts, "Ctrl+F")).toBeUndefined();
    // Literal Ctrl bindings still withhold their chord.
    expect(byChord(conflicts, "Ctrl+Tab")).toMatchObject({ action: { id: "nextTab" } });
  });

  it("lists Mod chords as Ctrl on the macOS web build, which keeps the Ctrl fallback", () => {
    const conflicts = terminalShortcutConflicts(getKeybindings(), true, true);
    expect(byChord(conflicts, "Ctrl+Shift+W")).toMatchObject({ action: { id: "closeTab" } });
    expect(byChord(conflicts, "Ctrl+Shift+B")).toMatchObject({ action: { id: "toggleSidebar" } });
  });

  it("lists the terminal font size chords and leaves the cheat sheet chord to the terminal", () => {
    const conflicts = terminalShortcutConflicts(getKeybindings(), false, false);
    expect(byChord(conflicts, "Ctrl+Alt+=")).toMatchObject({ action: { id: "terminalFontIncrease" } });
    expect(byChord(conflicts, "Ctrl+Shift+=")).toMatchObject({ bytes: null, action: { id: "terminalFontIncrease" } });
    expect(byChord(conflicts, "Ctrl+Alt+-")).toMatchObject({ action: { id: "terminalFontDecrease" } });
    expect(byChord(conflicts, "Ctrl+Alt+0")).toMatchObject({ action: { id: "terminalFontReset" } });
    expect(byChord(conflicts, "Ctrl+Shift+Enter")).toMatchObject({ action: { id: "toggleZoomPane" } });
    // Ctrl+/ is readline's undo: the cheat sheet does not take it from a focused terminal.
    expect(byChord(conflicts, "Ctrl+/")).toBeUndefined();
  });

  it("gives a shared chord to the binding the user chose, as the runtime match does", () => {
    setKeybinding("lockVault", "Mod+Shift+p");
    const owners = terminalShortcutConflicts(getKeybindings(), false, false).filter((conflict) => conflict.chord === "Ctrl+Shift+P");
    expect(owners.map((conflict) => conflict.action.id)).toEqual(["lockVault"]);
  });

  it("shows the table in the shortcuts settings card", () => {
    mounted = mount(createElement(ShortcutsCard));
    const table = mounted.container.querySelector<HTMLTableElement>('table[aria-label="与终端按键的冲突"]');
    expect(table).not.toBeNull();
    const row = table!.querySelector<HTMLTableRowElement>('tr[data-terminal-conflict="splitDown"]');
    expect([...row!.cells].map((cell) => cell.textContent)).toEqual(["Ctrl+Shift+D", "向下分屏", "无（终端默认不发送）"]);
    const nextTab = table!.querySelector<HTMLTableRowElement>('tr[data-terminal-conflict="nextTab"]');
    expect(nextTab!.cells[0].textContent).toBe("Ctrl+Tab");
  });
});
