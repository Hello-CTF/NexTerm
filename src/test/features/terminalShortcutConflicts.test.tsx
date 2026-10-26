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

import { getKeybindings, resetAllKeybindings, setKeybinding } from "../../app/keybindings";
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
});

afterEach(() => {
  mounted?.unmount();
  mounted = null;
  resetAllKeybindings();
});

function byChord(conflicts: TerminalShortcutConflict[], chord: string): TerminalShortcutConflict | undefined {
  return conflicts.find((conflict) => conflict.chord === chord);
}

const KEY_CODES: Record<string, number> = { "\\": 220, "[": 219, "]": 221, Tab: 9, ArrowLeft: 37, ArrowUp: 38, ArrowRight: 39, ArrowDown: 40 };

function domKey(chord: TerminalChord): KeyboardEventInit & { keyCode: number } {
  const { key } = chord;
  if (/^[a-z]$/.test(key)) {
    return { key: chord.shift ? key.toUpperCase() : key, code: `Key${key.toUpperCase()}`, keyCode: key.toUpperCase().charCodeAt(0) };
  }
  const named = { "\\": "Backslash", "[": "BracketLeft", "]": "BracketRight" }[key];
  return { key, code: named ?? key, keyCode: KEY_CODES[key] ?? key.charCodeAt(0) };
}

describe("terminal shortcut conflict table", () => {
  it("lists the chords a focused terminal withholds under the default desktop keymap", () => {
    const conflicts = terminalShortcutConflicts(getKeybindings(), false, false);
    expect(byChord(conflicts, "Ctrl+\\")).toMatchObject({ bytes: "\x1c", meaning: "退出并转储 SIGQUIT", action: { id: "splitDown" } });
    expect(byChord(conflicts, "Ctrl+Shift+W")).toMatchObject({ bytes: null, action: { id: "closePane" } });
    expect(byChord(conflicts, "Ctrl+W")).toMatchObject({ bytes: "\x17", meaning: "删除前一个词", action: { id: "closeTab" } });
    expect(byChord(conflicts, "Ctrl+B")).toMatchObject({ bytes: "\x02", action: { id: "toggleSidebar" } });
    expect(byChord(conflicts, "Ctrl+F")).toMatchObject({ bytes: "\x06", action: { id: "terminalSearch" } });
    expect(byChord(conflicts, "Ctrl+Tab")).toMatchObject({ bytes: "\t", action: { id: "nextTab" } });
    expect(byChord(conflicts, "Ctrl+1…9")?.meaning).toContain("SIGQUIT");
    expect(byChord(conflicts, "Esc")).toBeUndefined();
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

  it("follows rebinding and drops chords that no longer reach the app", () => {
    setKeybinding("splitDown", null);
    setKeybinding("lockVault", "Ctrl+Alt+l");
    const conflicts = terminalShortcutConflicts(getKeybindings(), false, false);
    expect(byChord(conflicts, "Ctrl+\\")).toBeUndefined();
    expect(byChord(conflicts, "Ctrl+Alt+L")).toMatchObject({ bytes: "\x1b\x0c", action: { id: "lockVault" } });
  });

  it("lists Mod chords as Ctrl on macOS and leaves Cmd-only chords out", () => {
    const conflicts = terminalShortcutConflicts(getKeybindings(), true, false);
    expect(byChord(conflicts, "Ctrl+W")).toMatchObject({ action: { id: "closeTab" } });
    expect(byChord(conflicts, "Ctrl+Shift+]")).toMatchObject({ action: { id: "nextTab" } });
    // terminalSearch is Primary+F, which is Cmd+F on macOS and never reaches the terminal as input.
    expect(byChord(conflicts, "Ctrl+F")).toBeUndefined();
    expect(byChord(conflicts, "Ctrl+Alt+→")).toMatchObject({ bytes: "\x1b[1;7C", action: { id: "nextWorkspace" } });
  });

  it("keeps the first action when two share a chord, as the runtime match does", () => {
    setKeybinding("lockVault", "Mod+Shift+p");
    const owners = terminalShortcutConflicts(getKeybindings(), false, false).filter((conflict) => conflict.chord === "Ctrl+Shift+P");
    expect(owners.map((conflict) => conflict.action.id)).toEqual(["commandPalette"]);
  });

  it("shows the table in the shortcuts settings card", () => {
    mounted = mount(createElement(ShortcutsCard));
    const table = mounted.container.querySelector<HTMLTableElement>('table[aria-label="与终端按键的冲突"]');
    expect(table).not.toBeNull();
    const row = table!.querySelector<HTMLTableRowElement>('tr[data-terminal-conflict="splitDown"]');
    expect([...row!.cells].map((cell) => cell.textContent)).toEqual(["Ctrl+\\", "向下分屏", "^\\（退出并转储 SIGQUIT）"]);
    const closePane = table!.querySelector<HTMLTableRowElement>('tr[data-terminal-conflict="closePane"]');
    expect(closePane!.cells[2].textContent).toBe("无（终端默认不发送）");
  });
});
