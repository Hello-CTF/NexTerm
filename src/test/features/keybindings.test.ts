/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it } from "vitest";
import {
  KEYBINDING_ACTIONS,
  bindingConflicts,
  captureBinding,
  formatBinding,
  getKeybinding,
  loadKeybindings,
  matchAppKeybinding,
  matchBinding,
  matchKeybinding,
  normalizeBinding,
  parseBinding,
  resetAllKeybindings,
  resetKeybinding,
  setKeybinding,
  type KeyEventLike,
} from "../../app/keybindings";

const STORAGE_KEY = "nexterm.keybindings.v1";

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

function storedOverrides(): Record<string, unknown> {
  const raw = localStorage.getItem(STORAGE_KEY);
  return raw ? (JSON.parse(raw) as Record<string, unknown>) : {};
}

beforeEach(() => {
  localStorage.clear();
  resetAllKeybindings();
});

describe("parseBinding / normalizeBinding", () => {
  it("parses modifier combos and normalizes order", () => {
    expect(normalizeBinding("Shift+Mod+p")).toBe("Mod+Shift+p");
    expect(normalizeBinding("Mod+Shift+P")).toBe("Mod+Shift+p");
    expect(normalizeBinding("Alt+Mod+1")).toBe("Mod+Alt+1");
  });

  it("rejects invalid bindings", () => {
    expect(parseBinding("")).toBeNull();
    expect(parseBinding("p")).toBeNull();
    expect(parseBinding("Mod+")).toBeNull();
    expect(parseBinding("Foo+P")).toBeNull();
    expect(parseBinding("Mod+Ctrl+P")).toBeNull();
    expect(parseBinding("Mod+Mod+P")).toBeNull();
    expect(parseBinding("Mod+Shift+Shift+P")).toBeNull();
    expect(parseBinding("Mod+üü")).toBeNull();
  });

  it("accepts named keys and F-keys without modifiers", () => {
    expect(normalizeBinding("Escape")).toBe("Escape");
    expect(normalizeBinding("F5")).toBe("F5");
    expect(normalizeBinding("Mod+F24")).toBe("Mod+F24");
  });
});

describe("formatBinding", () => {
  it("renders Mod and Primary with the platform hint and uppercases letters", () => {
    expect(formatBinding("Mod+Shift+P")).toBe("Ctrl+Shift+P");
    expect(formatBinding("Primary+F")).toBe("Ctrl+F");
    expect(formatBinding("Escape")).toBe("Esc");
    expect(formatBinding("Mod+1-9")).toBe("Ctrl+1…9");
    expect(formatBinding("Mod+\\")).toBe("Ctrl+\\");
    expect(formatBinding(null)).toBe("未绑定");
  });
});

describe("matchBinding", () => {
  it("Mod matches Ctrl or Meta, rejects missing or extra shift/alt", () => {
    expect(matchBinding(keyEvent({ key: "k", ctrlKey: true }), "Mod+K")).toBe(true);
    expect(matchBinding(keyEvent({ key: "k", metaKey: true }), "Mod+K")).toBe(true);
    expect(matchBinding(keyEvent({ key: "k", ctrlKey: true, metaKey: true }), "Mod+K")).toBe(true);
    expect(matchBinding(keyEvent({ key: "k" }), "Mod+K")).toBe(false);
    expect(matchBinding(keyEvent({ key: "k", ctrlKey: true, shiftKey: true }), "Mod+K")).toBe(false);
    expect(matchBinding(keyEvent({ key: "k", ctrlKey: true, altKey: true }), "Mod+K")).toBe(false);
  });

  it("Primary on non-mac is Ctrl without Meta", () => {
    expect(matchBinding(keyEvent({ key: "f", ctrlKey: true }), "Primary+F")).toBe(true);
    expect(matchBinding(keyEvent({ key: "F", ctrlKey: true, shiftKey: true }), "Primary+F")).toBe(false);
    expect(matchBinding(keyEvent({ key: "f", metaKey: true }), "Primary+F")).toBe(false);
    expect(matchBinding(keyEvent({ key: "f", ctrlKey: true, metaKey: true }), "Primary+F")).toBe(false);
    expect(matchBinding(keyEvent({ key: "f", ctrlKey: true, altKey: true }), "Primary+F")).toBe(false);
  });

  it("matches case-insensitively for letters and requires exact modifiers", () => {
    expect(matchBinding(keyEvent({ key: "P", ctrlKey: true, shiftKey: true }), "Mod+Shift+P")).toBe(true);
    expect(matchBinding(keyEvent({ key: "p", ctrlKey: true }), "Mod+Shift+P")).toBe(false);
    expect(matchBinding(keyEvent({ key: "p", ctrlKey: true, shiftKey: true }), "Mod+P")).toBe(false);
  });

  it("matches the 1-9 digit range and rejects 0", () => {
    for (const digit of "123456789") {
      expect(matchBinding(keyEvent({ key: digit, ctrlKey: true }), "Mod+1-9")).toBe(true);
    }
    expect(matchBinding(keyEvent({ key: "0", ctrlKey: true }), "Mod+1-9")).toBe(false);
    expect(matchBinding(keyEvent({ key: "1", altKey: true }), "Mod+1-9")).toBe(false);
  });

  it("matches bare Escape and F-keys without modifiers only", () => {
    expect(matchBinding(keyEvent({ key: "Escape" }), "Escape")).toBe(true);
    expect(matchBinding(keyEvent({ key: "Escape", ctrlKey: true }), "Escape")).toBe(false);
    expect(matchBinding(keyEvent({ key: "F5" }), "F5")).toBe(true);
    expect(matchBinding(keyEvent({ key: "F5", shiftKey: true }), "F5")).toBe(false);
  });
});

describe("captureBinding", () => {
  it("captures modifier combos from code when available", () => {
    expect(captureBinding(keyEvent({ key: "p", code: "KeyP", ctrlKey: true, shiftKey: true })))
      .toEqual({ kind: "binding", binding: "Mod+Shift+p" });
    expect(captureBinding(keyEvent({ key: "!", code: "Digit1", ctrlKey: true, shiftKey: true })))
      .toEqual({ kind: "binding", binding: "Mod+Shift+1" });
    expect(captureBinding(keyEvent({ key: "3", code: "Digit3", altKey: true })))
      .toEqual({ kind: "binding", binding: "Alt+3" });
  });

  it("reports pure modifier presses without binding", () => {
    expect(captureBinding(keyEvent({ key: "Control", code: "ControlLeft", ctrlKey: true })))
      .toEqual({ kind: "modifier" });
  });

  it("rejects bare single characters", () => {
    const result = captureBinding(keyEvent({ key: "a", code: "KeyA" }));
    expect(result.kind).toBe("invalid");
  });

  it("captures bare named keys and F-keys", () => {
    expect(captureBinding(keyEvent({ key: "F5", code: "F5" })))
      .toEqual({ kind: "binding", binding: "F5" });
    expect(captureBinding(keyEvent({ key: "Escape", code: "Escape" })))
      .toEqual({ kind: "binding", binding: "Escape" });
  });
});

describe("persistence", () => {
  it("round-trips overrides through localStorage", () => {
    setKeybinding("newTerminal", "Mod+Shift+T");
    expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t");
    expect(storedOverrides()).toEqual({ newTerminal: "Mod+Shift+t" });
    resetKeybinding("newTerminal");
    expect(getKeybinding("newTerminal")).toBe("Mod+t");
    expect(storedOverrides()).toEqual({});
  });

  it("stores explicit unbound actions as null", () => {
    setKeybinding("closeTab", null);
    expect(getKeybinding("closeTab")).toBeNull();
    expect(storedOverrides()).toEqual({ closeTab: null });
    expect(matchKeybinding(keyEvent({ key: "w", ctrlKey: true }), "closeTab")).toBe(false);
  });

  it("treats setting the default as removing the override", () => {
    setKeybinding("globalSearch", "Mod+K");
    expect(storedOverrides()).toEqual({});
  });

  it("drops invalid and unknown entries when loading", () => {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({
        newTerminal: "not a binding",
        bogusAction: "Mod+K",
        commandPalette: null,
        toggleSidebar: "Shift+Mod+B",
      }),
    );
    loadKeybindings();
    expect(getKeybinding("newTerminal")).toBe("Mod+t");
    expect(getKeybinding("commandPalette")).toBeNull();
    expect(getKeybinding("toggleSidebar")).toBe("Mod+Shift+b");
    expect(storedOverrides()).toEqual({
      newTerminal: "not a binding",
      bogusAction: "Mod+K",
      commandPalette: null,
      toggleSidebar: "Shift+Mod+B",
    });
  });
});

describe("bindingConflicts", () => {
  it("defaults have no conflicts", () => {
    expect(bindingConflicts()).toEqual([]);
  });

  it("detects actions sharing one binding", () => {
    setKeybinding("globalSearch", "Mod+Shift+P");
    const conflicts = bindingConflicts();
    expect(conflicts).toHaveLength(1);
    expect(conflicts[0].binding).toBe("Mod+Shift+p");
    expect(conflicts[0].actions.map((a) => a.id).sort()).toEqual(["commandPalette", "globalSearch"]);
  });
});

describe("matchAppKeybinding", () => {
  it("maps keys to app actions in catalog order", () => {
    expect(matchAppKeybinding(keyEvent({ key: "k", ctrlKey: true })))
      .toEqual({ action: "globalSearch", digit: null });
    expect(matchAppKeybinding(keyEvent({ key: "2", ctrlKey: true })))
      .toEqual({ action: "switchTab", digit: 2 });
    expect(matchAppKeybinding(keyEvent({ key: "b", ctrlKey: true })))
      .toEqual({ action: "toggleSidebar", digit: null });
    expect(matchAppKeybinding(keyEvent({ key: "q", ctrlKey: true }))).toBeNull();
  });

  it("skips unbound actions", () => {
    setKeybinding("switchTab", null);
    expect(matchAppKeybinding(keyEvent({ key: "2", ctrlKey: true }))).toBeNull();
  });
});

describe("catalog sanity", () => {
  it("every action default parses", () => {
    for (const action of KEYBINDING_ACTIONS) {
      expect(parseBinding(action.defaultBinding), action.id).not.toBeNull();
      expect(normalizeBinding(action.defaultBinding), action.id).toBe(action.defaultBinding);
    }
  });
});
