/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const harness = vi.hoisted(() => ({ mac: false }));

vi.mock("../../app/platform", () => ({
  isMac: () => harness.mac,
  modHint: () => (harness.mac ? "⌘" : "Ctrl"),
  setMacPlatform: (v: boolean) => {
    harness.mac = v;
  },
  wailsDragRegionStyle: {},
  wailsNoDragRegionStyle: {},
  isWailsDragRegionTarget: () => false,
}));
vi.mock("../../ipc/env", () => ({ WEB: false }));

import {
  KEYBINDING_ACTIONS,
  bindingConflicts,
  defaultBindingFor,
  formatBinding,
  formatBindingAria,
  getKeybinding,
  matchAppKeybinding,
  matchTerminalWithheldKeybinding,
  matchBinding,
  resetAllKeybindings,
  setKeybinding,
  type KeyEventLike,
  type KeybindingSnapshot,
} from "../../app/keybindings";

function keyEvent(init: Partial<KeyEventLike> & { key: string }): KeyEventLike {
  return {
    code: "",
    ctrlKey: false,
    metaKey: false,
    shiftKey: false,
    altKey: false,
    ...init,
  };
}

beforeEach(() => {
  localStorage.clear();
  resetAllKeybindings();
  harness.mac = false;
});

describe("platform-specific keybinding behaviour", () => {
  it("formats Mod and Primary with the platform hint", () => {
    expect(formatBinding("Mod+j")).toBe("Ctrl+J");
    harness.mac = true;
    expect(formatBinding("Mod+j")).toBe("⌘+J");
    expect(formatBinding("Primary+f")).toBe("⌘+F");
  });

  it("maps Mod and Primary to ARIA Meta on mac and Control elsewhere", () => {
    expect(formatBindingAria("Mod+j")).toBe("Control+J");
    expect(formatBindingAria("Primary+f")).toBe("Control+F");
    harness.mac = true;
    expect(formatBindingAria("Mod+j")).toBe("Meta+J");
    expect(formatBindingAria("Primary+f")).toBe("Meta+F");
    expect(formatBindingAria("Alt+a")).toBe("Alt+A");
  });

  it("matches Primary against the platform primary modifier only", () => {
    harness.mac = true;
    expect(matchBinding(keyEvent({ key: "f", code: "KeyF", metaKey: true }), "Primary+f")).toBe(true);
    expect(matchBinding(keyEvent({ key: "f", code: "KeyF", ctrlKey: true }), "Primary+f")).toBe(false);
    harness.mac = false;
    expect(matchBinding(keyEvent({ key: "f", code: "KeyF", ctrlKey: true }), "Primary+f")).toBe(true);
    expect(matchBinding(keyEvent({ key: "f", code: "KeyF", metaKey: true }), "Primary+f")).toBe(false);
  });

  it("round-trips mac Option+letter capture into match", () => {
    harness.mac = true;
    const event = keyEvent({ key: "å", code: "KeyA", altKey: true });
    expect(matchBinding(event, "Alt+a")).toBe(true);
    expect(matchBinding(event, "Alt+å")).toBe(false);
  });

  it("uses the requested desktop navigation bindings with real key events", () => {
    const cases = [
      { action: "nextWorkspace", binding: "Mod+Alt+ArrowRight", key: "ArrowRight", code: "ArrowRight", ctrlKey: true, altKey: true },
      { action: "previousWorkspace", binding: "Mod+Alt+ArrowLeft", key: "ArrowLeft", code: "ArrowLeft", ctrlKey: true, altKey: true },
      { action: "nextTab", binding: "Ctrl+Tab", key: "Tab", code: "Tab", ctrlKey: true },
      { action: "previousTab", binding: "Ctrl+Shift+Tab", key: "Tab", code: "Tab", ctrlKey: true, shiftKey: true },
      { action: "focusPaneUp", binding: "Mod+Alt+ArrowUp", key: "ArrowUp", code: "ArrowUp", ctrlKey: true, altKey: true },
      { action: "focusPaneDown", binding: "Mod+Alt+ArrowDown", key: "ArrowDown", code: "ArrowDown", ctrlKey: true, altKey: true },
      { action: "focusPaneLeft", binding: "Mod+Shift+Alt+ArrowLeft", key: "ArrowLeft", code: "ArrowLeft", ctrlKey: true, shiftKey: true, altKey: true },
      { action: "focusPaneRight", binding: "Mod+Shift+Alt+ArrowRight", key: "ArrowRight", code: "ArrowRight", ctrlKey: true, shiftKey: true, altKey: true },
      { action: "splitDown", binding: "Mod+Shift+d", key: "D", code: "KeyD", ctrlKey: true, shiftKey: true },
      { action: "splitRight", binding: "Mod+Shift+\\", key: "|", code: "Backslash", ctrlKey: true, shiftKey: true },
      { action: "newTerminal", binding: "Mod+Shift+t", key: "T", code: "KeyT", ctrlKey: true, shiftKey: true },
      { action: "closeTab", binding: "Mod+Shift+w", key: "W", code: "KeyW", ctrlKey: true, shiftKey: true },
      { action: "closePane", binding: "Mod+Shift+Alt+w", key: "W", code: "KeyW", ctrlKey: true, shiftKey: true, altKey: true },
      { action: "restoreClosedTab", binding: "Mod+Shift+Alt+t", key: "T", code: "KeyT", ctrlKey: true, shiftKey: true, altKey: true },
      { action: "toggleSidebar", binding: "Mod+Shift+b", key: "B", code: "KeyB", ctrlKey: true, shiftKey: true },
      { action: "toggleAiSidebar", binding: "Mod+Shift+j", key: "J", code: "KeyJ", ctrlKey: true, shiftKey: true },
      { action: "terminalFontDecrease", binding: "Mod+Alt+-", key: "-", code: "Minus", ctrlKey: true, altKey: true },
    ] as const;
    for (const { action, binding, ...init } of cases) {
      expect(getKeybinding(action)).toBe(binding);
      expect(matchAppKeybinding(new KeyboardEvent("keydown", init))).toEqual({ action, digit: null });
    }
  });

  it("matches macOS bracket alternatives from shifted key values and physical codes", () => {
    harness.mac = true;
    expect(matchAppKeybinding(new KeyboardEvent("keydown", {
      key: "}",
      code: "BracketRight",
      metaKey: true,
      shiftKey: true,
    }))).toEqual({ action: "nextTab", digit: null });
    expect(matchAppKeybinding(new KeyboardEvent("keydown", {
      key: "{",
      code: "BracketLeft",
      metaKey: true,
      shiftKey: true,
    }))).toEqual({ action: "previousTab", digit: null });
  });

  it("flags Mod+letter against Primary+letter on both platforms", () => {
    setKeybinding("terminalSearch", "Primary+f");
    setKeybinding("commandPalette", "Mod+f");
    expect(bindingConflicts(undefined, false)).toHaveLength(1);
    expect(bindingConflicts(undefined, true)).toHaveLength(1);
  });
});

describe("desktop default keymap", () => {
  const defaults = (mac: boolean): KeybindingSnapshot =>
    Object.fromEntries(KEYBINDING_ACTIONS.map((action) => [action.id, defaultBindingFor(action, mac, false)])) as KeybindingSnapshot;

  it.each([false, true])("has no conflicting defaults (mac=%s)", (mac) => {
    expect(bindingConflicts(defaults(mac), mac)).toEqual([]);
  });

  it("keeps the macOS desktop defaults, where Mod is Cmd and never a terminal key", () => {
    expect(defaults(true)).toEqual({
      commandPalette: "Mod+Shift+p",
      quickConnect: "Mod+Shift+k",
      newTerminal: "Mod+t",
      nextWorkspace: "Mod+Alt+ArrowRight",
      previousWorkspace: "Mod+Alt+ArrowLeft",
      nextTab: "Ctrl+Tab",
      previousTab: "Ctrl+Shift+Tab",
      focusPaneLeft: "Mod+Shift+Alt+ArrowLeft",
      focusPaneRight: "Mod+Shift+Alt+ArrowRight",
      focusPaneUp: "Mod+Alt+ArrowUp",
      focusPaneDown: "Mod+Alt+ArrowDown",
      toggleSidebar: "Mod+b",
      toggleAiSidebar: "Mod+j",
      closeTab: "Mod+w",
      splitDown: "Mod+\\",
      splitRight: "Mod+Shift+\\",
      closePane: "Mod+Shift+w",
      switchTab: "Mod+1-9",
      restoreClosedTab: "Mod+Shift+t",
      terminalSearch: "Primary+f",
      terminalLinks: "Mod+Shift+u",
      terminalCopyMode: "Mod+Shift+x",
      terminalQuickSelect: "Mod+Shift+y",
      sessionSidebar: "Mod+Shift+b",
      previousPrompt: "Mod+Shift+ArrowUp",
      nextPrompt: "Mod+Shift+ArrowDown",
      copyLastOutput: "Mod+Shift+o",
      generateCommand: "Primary+i",
      reclaimTakeover: "Mod+Shift+e",
      openSettings: "Mod+,",
      focusAiInput: "Mod+Shift+a",
      lockVault: "Mod+Shift+l",
      lockApp: null,
      terminalFontIncrease: "Mod+=",
      terminalFontDecrease: "Mod+-",
      terminalFontReset: "Mod+0",
      toggleZoomPane: "Mod+Shift+Enter",
      equalizeSplits: "Mod+Alt+=",
      showShortcuts: "Mod+/",
    });
  });

  it("matches the macOS extras only while the macOS default is in effect", () => {
    harness.mac = true;
    expect(matchAppKeybinding(new KeyboardEvent("keydown", { key: "+", code: "Equal", metaKey: true, shiftKey: true })))
      .toEqual({ action: "terminalFontIncrease", digit: null });
    harness.mac = false;
    expect(matchAppKeybinding(new KeyboardEvent("keydown", { key: "+", code: "Equal", ctrlKey: true, shiftKey: true })))
      .toEqual({ action: "terminalFontIncrease", digit: null });
  });

  it("never treats an AltGr character on the Ctrl+Alt defaults as a shortcut", () => {
    // AltGr reports Ctrl+Alt on Windows, and layouts type braces, brackets, backslash and tilde with AltGr+7/8/9/0/-/=.
    const keys: Array<[string, string]> = [
      ["7", "Digit7"], ["8", "Digit8"], ["9", "Digit9"], ["0", "Digit0"], ["1", "Digit1"], ["-", "Minus"], ["=", "Equal"],
    ];
    for (const [key, code] of keys) {
      const plain = keyEvent({ key, code, ctrlKey: true, altKey: true });
      const altGr = { ...plain, getModifierState: (name: string) => name === "AltGraph" };
      expect(matchAppKeybinding(altGr), `${code} app`).toBeNull();
      expect(matchTerminalWithheldKeybinding(altGr), `${code} terminal`).toBeNull();
    }
    // The same chord without AltGr is still a shortcut, so the check above is not vacuous.
    expect(matchAppKeybinding(keyEvent({ key: "1", code: "Digit1", ctrlKey: true, altKey: true }))?.action).toBe("switchTab");
  });
});
