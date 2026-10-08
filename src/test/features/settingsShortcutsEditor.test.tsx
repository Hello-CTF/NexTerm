/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { act, createElement } from "react";
import { click, mount, type MountedView } from "./reactTestUtils";
import { ShortcutsCard } from "../../features/settings/ShortcutsCard";
import {
  KEYBINDING_ACTIONS,
  getKeybinding,
  matchAppKeybinding,
  resetAllKeybindings,
} from "../../app/keybindings";
import { getInputPrefs, setSelectionAutoCopy } from "../../app/preferences";

const STORAGE_KEY = "nexterm.keybindings.v1";
const PREFS_KEY = "nexterm.inputPrefs.v1";

let mounted: MountedView | undefined;

function keyDown(target: EventTarget, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

function row(id: string): HTMLElement {
  const el = mounted?.container.querySelector<HTMLElement>(`[data-shortcut-row="${id}"]`);
  if (!el) throw new Error(`row not found: ${id}`);
  return el;
}

function kbdText(id: string): string {
  return row(id).querySelector(".nx-kbd")?.textContent ?? "";
}

function editButton(id: string): HTMLButtonElement {
  const btn = row(id).querySelector<HTMLButtonElement>(`button[aria-label="修改快捷键：${rowLabel(id)}"]`);
  if (!btn) throw new Error(`edit button not found: ${id}`);
  return btn;
}

function rowLabel(id: string): string {
  return row(id).querySelector(".min-w-0.flex-1")?.textContent ?? "";
}

function captureInput(): HTMLInputElement | null {
  return mounted?.container.querySelector<HTMLInputElement>('input[aria-label^="捕获快捷键："]') ?? null;
}

function show(): void {
  mounted = mount(createElement(ShortcutsCard));
}

beforeEach(() => {
  localStorage.clear();
  resetAllKeybindings();
  setSelectionAutoCopy(false);
  document.body.replaceChildren();
  show();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  resetAllKeybindings();
  setSelectionAutoCopy(false);
});

describe("shortcuts editor", () => {
  it("renders every action with its default binding", () => {
    expect(mounted?.container.querySelectorAll("[data-shortcut-row]").length).toBe(
      KEYBINDING_ACTIONS.length,
    );
    expect(kbdText("commandPalette")).toBe("Ctrl+Shift+P");
    expect(kbdText("quickConnect")).toBe("Ctrl+Shift+K");
    expect(kbdText("newTerminal")).toBe("Ctrl+T");
    expect(kbdText("terminalSearch")).toBe("Ctrl+F");
    expect(kbdText("reclaimTakeover")).toBe("Esc");
    expect(kbdText("switchTab")).toBe("Ctrl+1…9");
  });

  it("captures a new binding and persists it", () => {
    click(editButton("commandPalette"));
    const input = captureInput();
    expect(input).not.toBeNull();
    keyDown(input as HTMLInputElement, "o", { code: "KeyO", ctrlKey: true, shiftKey: true });
    expect(getKeybinding("commandPalette")).toBe("Mod+Shift+o");
    expect(kbdText("commandPalette")).toBe("Ctrl+Shift+O");
    const stored = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "{}") as Record<string, unknown>;
    expect(stored.commandPalette).toBe("Mod+Shift+o");
  });

  it("keeps listening on pure modifier presses", () => {
    click(editButton("commandPalette"));
    const input = captureInput();
    keyDown(input as HTMLInputElement, "Control", { code: "ControlLeft", ctrlKey: true });
    expect(captureInput()).not.toBeNull();
    expect(getKeybinding("commandPalette")).toBe("Mod+Shift+p");
  });

  it("rejects bare single keys with an inline reason", () => {
    click(editButton("commandPalette"));
    const input = captureInput();
    keyDown(input as HTMLInputElement, "a", { code: "KeyA" });
    expect(getKeybinding("commandPalette")).toBe("Mod+Shift+p");
    expect(captureInput()).not.toBeNull();
    expect(mounted?.container.textContent).toContain("修饰键");
  });

  it("cancels capture with Escape", () => {
    click(editButton("commandPalette"));
    keyDown(captureInput() as HTMLInputElement, "Escape");
    expect(captureInput()).toBeNull();
    expect(getKeybinding("commandPalette")).toBe("Mod+Shift+p");
  });

  it("clears a binding with Backspace", () => {
    click(editButton("closeTab"));
    keyDown(captureInput() as HTMLInputElement, "Backspace");
    expect(getKeybinding("closeTab")).toBeNull();
    expect(kbdText("closeTab")).toBe("未绑定");
  });

  it("flags conflicts with the other action and explains resolution", () => {
    click(editButton("commandPalette"));
    keyDown(captureInput() as HTMLInputElement, "t", { code: "KeyT", ctrlKey: true });
    expect(getKeybinding("commandPalette")).toBe("Mod+t");
    const badge = row("commandPalette").querySelector(".nx-badge-amber");
    expect(badge?.textContent).toContain("冲突");
    expect(row("newTerminal").querySelector(".nx-badge-amber")).not.toBeNull();
    expect(mounted?.container.textContent).toContain("排在前面的动作生效");
  });

  it("restores a single row to its default", () => {
    click(editButton("newTerminal"));
    keyDown(captureInput() as HTMLInputElement, "n", { code: "KeyN", ctrlKey: true });
    expect(kbdText("newTerminal")).toBe("Ctrl+N");
    const reset = row("newTerminal").querySelector<HTMLButtonElement>(
      'button[aria-label="恢复默认快捷键：新建本地终端"]',
    );
    expect(reset).not.toBeNull();
    click(reset as HTMLButtonElement);
    expect(kbdText("newTerminal")).toBe("Ctrl+T");
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull();
  });

  it("resets all bindings at once", () => {
    click(editButton("newTerminal"));
    keyDown(captureInput() as HTMLInputElement, "n", { code: "KeyN", ctrlKey: true });
    click(editButton("toggleSidebar"));
    keyDown(captureInput() as HTMLInputElement, "b", { code: "KeyB", altKey: true });
    const resetAll = [...(mounted?.container.querySelectorAll("button") ?? [])].find(
      (b) => b.textContent?.trim() === "全部恢复默认",
    );
    expect(resetAll).not.toBeUndefined();
    click(resetAll as HTMLButtonElement);
    expect(kbdText("newTerminal")).toBe("Ctrl+T");
    expect(kbdText("toggleSidebar")).toBe("Ctrl+B");
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull();
  });

  it("auto-copy preference is off by default and persists toggles", () => {
    const checkbox = mounted?.container.querySelector<HTMLInputElement>(
      'input[aria-label="选中自动复制"]',
    );
    expect(checkbox?.checked).toBe(false);
    expect(getInputPrefs().selectionAutoCopy).toBe(false);
    click(checkbox as HTMLInputElement);
    expect(getInputPrefs().selectionAutoCopy).toBe(true);
    const stored = JSON.parse(localStorage.getItem(PREFS_KEY) ?? "{}") as Record<string, unknown>;
    expect(stored.selectionAutoCopy).toBe(true);
    const after = mounted?.container.querySelector<HTMLInputElement>(
      'input[aria-label="选中自动复制"]',
    );
    expect(after?.checked).toBe(true);
  });

  it("captures switchTab digits as the full 1-9 range and they trigger tabs 1/2/9", () => {
    click(editButton("switchTab"));
    keyDown(captureInput() as HTMLInputElement, "2", { code: "Digit2", altKey: true });
    expect(getKeybinding("switchTab")).toBe("Alt+1-9");
    expect(kbdText("switchTab")).toBe("Alt+1…9");

    const hit = (digit: string) =>
      matchAppKeybinding(new KeyboardEvent("keydown", { key: digit, altKey: true }));
    expect(hit("1")).toEqual({ action: "switchTab", digit: 1 });
    expect(hit("2")).toEqual({ action: "switchTab", digit: 2 });
    expect(hit("9")).toEqual({ action: "switchTab", digit: 9 });
    expect(hit("3")).toEqual({ action: "switchTab", digit: 3 });
  });

  it("round-trips a shifted-digit capture into a switchTab hit", () => {
    click(editButton("switchTab"));
    keyDown(captureInput() as HTMLInputElement, "!", { code: "Digit1", ctrlKey: true, shiftKey: true });
    expect(getKeybinding("switchTab")).toBe("Mod+Shift+1-9");
    const hit = matchAppKeybinding(
      new KeyboardEvent("keydown", { key: "!", code: "Digit1", ctrlKey: true, shiftKey: true }),
    );
    expect(hit).toEqual({ action: "switchTab", digit: 1 });
  });

  it("rejects non-digit capture for switchTab with an inline reason", () => {
    click(editButton("switchTab"));
    keyDown(captureInput() as HTMLInputElement, "q", { code: "KeyQ", altKey: true });
    expect(getKeybinding("switchTab")).toBe("Mod+1-9");
    expect(captureInput()).not.toBeNull();
    expect(mounted?.container.textContent).toContain("只支持数字键");
  });

  it("mentions the fixed SQL run shortcut", () => {
    expect(mounted?.container.textContent).toContain("SQL 编辑器内运行固定为 Ctrl+Enter");
  });
});
