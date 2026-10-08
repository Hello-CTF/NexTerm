/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it } from "vitest";
import {
  KEYBINDING_ACTIONS,
  bindingConflicts,
  bindingsConflict,
  captureBinding,
  captureBindingForAction,
  formatBinding,
  formatBindingAria,
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

  it("round-trips capture into match for shifted digits and Alt letters", () => {
    const shiftedDigit = keyEvent({ key: "!", code: "Digit1", ctrlKey: true, shiftKey: true });
    const digitCapture = captureBinding(shiftedDigit);
    expect(digitCapture).toEqual({ kind: "binding", binding: "Mod+Shift+1" });
    expect(matchBinding(shiftedDigit, "Mod+Shift+1")).toBe(true);

    const altLetter = keyEvent({ key: "å", code: "KeyA", altKey: true });
    const letterCapture = captureBinding(altLetter);
    expect(letterCapture).toEqual({ kind: "binding", binding: "Alt+a" });
    expect(matchBinding(altLetter, "Alt+a")).toBe(true);

    const nonUsLayout = keyEvent({ key: "z", code: "KeyY", ctrlKey: true });
    const layoutCapture = captureBinding(nonUsLayout);
    expect(layoutCapture).toEqual({ kind: "binding", binding: "Mod+y" });
    expect(matchBinding(nonUsLayout, "Mod+y")).toBe(true);
    expect(matchBinding(nonUsLayout, "Mod+z")).toBe(false);
  });
});

describe("captureBindingForAction", () => {
  it("normalizes switchTab digits to the 1-9 range, keeping modifiers", () => {
    expect(captureBindingForAction(keyEvent({ key: "2", code: "Digit2", altKey: true }), "switchTab"))
      .toEqual({ kind: "binding", binding: "Alt+1-9" });
    expect(captureBindingForAction(keyEvent({ key: "1", code: "Digit1", ctrlKey: true }), "switchTab"))
      .toEqual({ kind: "binding", binding: "Mod+1-9" });
    expect(captureBindingForAction(keyEvent({ key: "9", code: "Digit9", metaKey: true, shiftKey: true }), "switchTab"))
      .toEqual({ kind: "binding", binding: "Mod+Shift+1-9" });
  });

  it("rejects non-digit keys for switchTab", () => {
    const result = captureBindingForAction(keyEvent({ key: "q", code: "KeyQ", altKey: true }), "switchTab");
    expect(result.kind).toBe("invalid");
  });

  it("leaves other actions untouched", () => {
    expect(captureBindingForAction(keyEvent({ key: "o", code: "KeyO", ctrlKey: true }), "commandPalette"))
      .toEqual({ kind: "binding", binding: "Mod+o" });
  });
});

describe("formatBindingAria", () => {
  it("maps modifiers to ARIA names on the current platform", () => {
    expect(formatBindingAria("Mod+j")).toBe("Control+J");
    expect(formatBindingAria("Primary+f")).toBe("Control+F");
    expect(formatBindingAria("Ctrl+Shift+r")).toBe("Control+Shift+R");
    expect(formatBindingAria("Alt+a")).toBe("Alt+A");
    expect(formatBindingAria("Escape")).toBe("Escape");
    expect(formatBindingAria(null)).toBeNull();
    expect(formatBindingAria("not a binding")).toBeNull();
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
    setKeybinding("quickConnect", "Mod+Shift+K");
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
    expect(bindingConflicts(undefined, true)).toEqual([]);
  });

  it("detects actions sharing one binding", () => {
    setKeybinding("quickConnect", "Mod+Shift+P");
    const conflicts = bindingConflicts();
    expect(conflicts).toHaveLength(1);
    expect(conflicts[0].bindings).toEqual(["Mod+Shift+p", "Mod+Shift+p"]);
    expect(conflicts[0].actions.map((a) => a.id)).toEqual(["commandPalette", "quickConnect"]);
  });

  it("detects Mod and Primary overlapping the same key on both platforms", () => {
    setKeybinding("commandPalette", "Mod+f");
    for (const mac of [false, true]) {
      const conflicts = bindingConflicts(undefined, mac);
      expect(conflicts, `mac=${mac}`).toHaveLength(1);
      expect(conflicts[0].actions.map((a) => a.id)).toEqual(["commandPalette", "terminalSearch"]);
    }
  });

  it("detects a single digit overlapping the switchTab 1-9 range", () => {
    setKeybinding("newTerminal", "Mod+1");
    const conflicts = bindingConflicts();
    expect(conflicts).toHaveLength(1);
    expect(conflicts[0].actions.map((a) => a.id)).toEqual(["newTerminal", "switchTab"]);
  });

  it("keeps distinct modifiers, shifted variants and unrelated keys conflict-free", () => {
    expect(bindingsConflict("Mod+Shift+p", "Mod+p", false)).toBe(false);
    expect(bindingsConflict("Mod+k", "Alt+k", false)).toBe(false);
    expect(bindingsConflict("Mod+k", "Mod+j", false)).toBe(false);
    expect(bindingsConflict("Escape", "Mod+Escape", false)).toBe(false);
    expect(bindingsConflict("Mod+1", "Mod+2", false)).toBe(false);
  });

  it("resolves Primary against explicit Ctrl/Meta per platform", () => {
    expect(bindingsConflict("Primary+f", "Ctrl+f", false)).toBe(true);
    expect(bindingsConflict("Primary+f", "Ctrl+f", true)).toBe(false);
    expect(bindingsConflict("Primary+f", "Meta+f", true)).toBe(true);
    expect(bindingsConflict("Primary+f", "Meta+f", false)).toBe(false);
    expect(bindingsConflict("Ctrl+f", "Meta+f", true)).toBe(true);
    expect(bindingsConflict("Ctrl+f", "Meta+f", false)).toBe(true);
  });
});

describe("matchAppKeybinding", () => {
  it("maps keys to app actions in catalog order", () => {
    expect(matchAppKeybinding(keyEvent({ key: "k", ctrlKey: true, shiftKey: true })))
      .toEqual({ action: "quickConnect", digit: null });
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

  it("extracts digits through the shared logical key for shifted and non-US forms", () => {
    setKeybinding("switchTab", "Mod+Shift+1-9");
    expect(matchAppKeybinding(keyEvent({ key: "!", code: "Digit1", ctrlKey: true, shiftKey: true })))
      .toEqual({ action: "switchTab", digit: 1 });
    expect(matchAppKeybinding(keyEvent({ key: "@", code: "Digit2", ctrlKey: true, shiftKey: true })))
      .toEqual({ action: "switchTab", digit: 2 });
    resetAllKeybindings();
    expect(matchAppKeybinding(keyEvent({ key: "&", code: "Digit1", ctrlKey: true })))
      .toEqual({ action: "switchTab", digit: 1 });
    expect(matchAppKeybinding(keyEvent({ key: "é", code: "Digit2", ctrlKey: true })))
      .toEqual({ action: "switchTab", digit: 2 });
    expect(matchAppKeybinding(keyEvent({ key: "9", code: "Digit9", ctrlKey: true })))
      .toEqual({ action: "switchTab", digit: 9 });
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
