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
  bindingConflicts,
  formatBinding,
  formatBindingAria,
  getKeybinding,
  matchAppKeybinding,
  matchBinding,
  resetAllKeybindings,
  setKeybinding,
  type KeyEventLike,
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
      { action: "splitDown", binding: "Mod+\\", key: "\\", code: "Backslash", ctrlKey: true },
      { action: "splitRight", binding: "Mod+Shift+\\", key: "|", code: "Backslash", ctrlKey: true, shiftKey: true },
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
    setKeybinding("commandPalette", "Mod+f");
    expect(bindingConflicts(undefined, false)).toHaveLength(1);
    expect(bindingConflicts(undefined, true)).toHaveLength(1);
  });
});
