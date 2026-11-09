/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { Terminal } from "@xterm/xterm";
import { mount, type MountedView } from "./reactTestUtils";

const harness = vi.hoisted(() => ({ mac: false }));

vi.mock("../../app/platform", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../app/platform")>()),
  isMac: () => harness.mac,
}));
vi.mock("../../ipc/env", () => ({ WEB: false }));

import { getKeybindings, matchKeybinding, resetAllKeybindings, setKeybinding } from "../../app/keybindings";
import {
  ShortcutsOverlayHost,
  closeShortcutsOverlay,
  shortcutGroups,
  toggleShortcutsOverlay,
} from "../../app/shell/ShortcutsOverlay";

let mounted: MountedView | null = null;

beforeEach(() => {
  harness.mac = false;
  localStorage.clear();
  resetAllKeybindings();
});

afterEach(() => {
  act(() => closeShortcutsOverlay());
  mounted?.unmount();
  mounted = null;
  resetAllKeybindings();
});

function dialog(): HTMLElement | null {
  return document.querySelector<HTMLElement>('[role="dialog"]');
}

describe("shortcut cheat sheet", () => {
  it("groups every action by area with its current chord", () => {
    const groups = shortcutGroups(getKeybindings(), "");
    const panes = groups.find((group) => group.area === "panes");
    expect(panes?.rows.find((row) => row.id === "toggleZoomPane")?.chords).toEqual(["Ctrl+Shift+Enter"]);
    const terminal = groups.find((group) => group.area === "terminal");
    expect(terminal?.rows.map((row) => row.id)).toEqual(
      expect.arrayContaining(["terminalFontIncrease", "terminalFontDecrease", "terminalFontReset"]),
    );
    const total = groups.reduce((sum, group) => sum + group.rows.length, 0);
    expect(total).toBe(Object.keys(getKeybindings()).length);
  });

  it("follows rebinding and shows an unbound action without a chord", () => {
    setKeybinding("toggleZoomPane", "Mod+Alt+z");
    setKeybinding("lockVault", null);
    const rows = shortcutGroups(getKeybindings(), "").flatMap((group) => group.rows);
    expect(rows.find((row) => row.id === "toggleZoomPane")?.chords).toEqual(["Ctrl+Alt+Z"]);
    expect(rows.find((row) => row.id === "lockVault")?.chords).toEqual([]);
  });

  it("lists the extra macOS tab chord on macOS desktop", () => {
    harness.mac = true;
    resetAllKeybindings();
    const rows = shortcutGroups(getKeybindings(), "", true).flatMap((group) => group.rows);
    expect(rows.find((row) => row.id === "nextTab")?.chords).toHaveLength(2);
  });

  it("filters by name and by chord", () => {
    const byName = shortcutGroups(getKeybindings(), "字号").flatMap((group) => group.rows.map((row) => row.id));
    expect(byName).toEqual(["terminalFontIncrease", "terminalFontDecrease", "terminalFontReset"]);
    const byChord = shortcutGroups(getKeybindings(), "ctrl+shift+enter").flatMap((group) => group.rows.map((row) => row.id));
    expect(byChord).toEqual(["toggleZoomPane"]);
    expect(shortcutGroups(getKeybindings(), "no such shortcut")).toEqual([]);
  });

  it("opens from the host, filters live and closes on Escape", () => {
    mounted = mount(createElement(ShortcutsOverlayHost));
    expect(dialog()).toBeNull();
    act(() => toggleShortcutsOverlay());
    expect(dialog()).not.toBeNull();
    const input = dialog()!.querySelector<HTMLInputElement>('input[type="search"]')!;
    act(() => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(input, "最大化");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect([...dialog()!.querySelectorAll("[data-shortcut]")].map((row) => row.getAttribute("data-shortcut"))).toEqual(["toggleZoomPane"]);
    act(() => {
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    });
    expect(dialog()).toBeNull();
  });

  it("does not open from a focused terminal that keeps Ctrl+/ as readline undo", () => {
    const host = document.createElement("div");
    document.body.append(host);
    const term = new Terminal();
    term.open(host);
    const opened = vi.fn();
    // The app handler runs on window keydown; a chord xterm encodes never gets there.
    const onKey = (event: KeyboardEvent) => {
      if (!event.defaultPrevented && matchKeybinding(event, "showShortcuts")) opened();
    };
    window.addEventListener("keydown", onKey);
    const sent: string[] = [];
    const listener = term.onData((data) => sent.push(data));
    try {
      const init = { key: "/", code: "Slash", keyCode: 191, ctrlKey: true, bubbles: true, cancelable: true };
      term.textarea!.dispatchEvent(new KeyboardEvent("keydown", init));
      expect(sent.join("")).toBe("\x1f");
      expect(opened).not.toHaveBeenCalled();
      // Outside the terminal the same chord reaches the app.
      document.body.dispatchEvent(new KeyboardEvent("keydown", init));
      expect(opened).toHaveBeenCalledTimes(1);
    } finally {
      listener.dispose();
      window.removeEventListener("keydown", onKey);
      term.dispose();
      host.remove();
    }
  });
});
