/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { createElement } from "react";
import { act } from "react";
import { click, flush, mount, type MountedView } from "./reactTestUtils";
import { DEFAULT_TERMINAL_KEY_IDS } from "../../features/terminal/terminalKeys";
import { TerminalKeysBar } from "../../features/terminal/TerminalKeysBar";

const STORAGE_KEY = "nexterm.terminalKeys.v1";

let mounted: MountedView | null = null;
let sent: string[];
let focused: number;

function renderBar() {
  sent = [];
  focused = 0;
  mounted = mount(
    createElement(TerminalKeysBar, {
      onSend: (data) => sent.push(data),
      onFocus: () => {
        focused += 1;
      },
    }),
  );
  return mounted;
}

function barButtons(): HTMLButtonElement[] {
  return [...mounted!.container.querySelectorAll<HTMLButtonElement>(".nx-terminal-keys > button")];
}

function keyButton(label: string): HTMLButtonElement {
  const button = barButtons().find((candidate) => candidate.textContent?.trim() === label);
  if (!button) throw new Error(`key button not found: ${label}`);
  return button;
}

function barLabels(): string[] {
  return barButtons().map((button) => button.textContent?.trim() ?? "");
}

function buttonByAria(label: string): HTMLButtonElement {
  const button = [...document.querySelectorAll<HTMLButtonElement>("button")].find(
    (candidate) => candidate.getAttribute("aria-label") === label,
  );
  if (!button) throw new Error(`button not found by aria-label: ${label}`);
  return button;
}

function dialog(): HTMLElement | null {
  return document.querySelector<HTMLElement>('[role="dialog"][aria-modal="true"]');
}

function openConfig() {
  click(buttonByAria("配置终端按键"));
}

function toggleCheckbox(container: ParentNode, ariaLabel: string) {
  const box = container.querySelector<HTMLInputElement>(`input[type="checkbox"][aria-label="${ariaLabel}"]`);
  if (!box) throw new Error(`checkbox not found: ${ariaLabel}`);
  act(() => {
    box.click();
  });
}

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  mounted?.unmount();
  mounted = null;
  localStorage.clear();
});

describe("TerminalKeysBar default keys", () => {
  it("renders every default key in the default order with Ctrl/Alt and keyboard buttons", () => {
    renderBar();
    const labels = barLabels();
    expect(labels.slice(0, 2)).toEqual(["Ctrl", "Alt"]);
    expect(labels.slice(2)).toEqual([
      "Esc",
      "Tab",
      "↑",
      "↓",
      "→",
      "←",
      "Home",
      "End",
      "PgUp",
      "PgDn",
      "C",
      "D",
      "Z",
      "^L",
      "^R",
    ]);
    expect(buttonByAria("聚焦终端并打开系统键盘")).toBeTruthy();
    expect(buttonByAria("配置终端按键")).toBeTruthy();
  });

  it("sends the exact byte sequence for plain and dedicated-Ctrl keys", () => {
    renderBar();
    click(keyButton("PgUp"));
    click(keyButton("^L"));
    click(keyButton("^R"));
    click(keyButton("Esc"));
    expect(sent).toEqual(["\x1b[5~", "\x0c", "\x12", "\x1b"]);
  });

  it("composes one-shot Ctrl/Alt modifiers and resets them after a key", () => {
    renderBar();
    const ctrl = buttonByAria("Ctrl 修饰键");
    const alt = buttonByAria("Alt 修饰键");
    expect(ctrl.getAttribute("aria-pressed")).toBe("false");
    expect(alt.getAttribute("aria-pressed")).toBe("false");

    click(ctrl);
    expect(ctrl.getAttribute("aria-pressed")).toBe("true");
    expect(sent).toEqual([]);

    click(keyButton("C"));
    expect(sent).toEqual(["\x03"]);
    expect(ctrl.getAttribute("aria-pressed")).toBe("false");

    click(keyButton("C"));
    expect(sent).toEqual(["\x03", "c"]);

    click(alt);
    click(keyButton("C"));
    expect(sent).toEqual(["\x03", "c", "\x1bc"]);
    expect(alt.getAttribute("aria-pressed")).toBe("false");

    click(ctrl);
    click(alt);
    click(keyButton("C"));
    expect(sent).toEqual(["\x03", "c", "\x1bc", "\x1b\x03"]);
    expect(ctrl.getAttribute("aria-pressed")).toBe("false");
    expect(alt.getAttribute("aria-pressed")).toBe("false");
  });

  it("composes Alt with navigation keys as an ESC prefix", () => {
    renderBar();
    click(buttonByAria("Alt 修饰键"));
    click(keyButton("↑"));
    expect(sent).toEqual(["\x1b\x1b[A"]);
  });

  it("keyboard button clears modifiers and focuses the terminal without sending input", () => {
    renderBar();
    click(buttonByAria("Ctrl 修饰键"));
    click(buttonByAria("Alt 修饰键"));
    click(buttonByAria("聚焦终端并打开系统键盘"));
    expect(focused).toBe(1);
    expect(sent).toEqual([]);
    expect(buttonByAria("Ctrl 修饰键").getAttribute("aria-pressed")).toBe("false");
    expect(buttonByAria("Alt 修饰键").getAttribute("aria-pressed")).toBe("false");
  });
});

describe("TerminalKeysBar configuration", () => {
  it("opens an accessible dialog without arbitrary escape-sequence inputs", () => {
    renderBar();
    const config = buttonByAria("配置终端按键");
    expect(config.getAttribute("aria-haspopup")).toBe("dialog");
    expect(config.getAttribute("aria-expanded")).toBe("false");
    openConfig();
    const modal = dialog();
    expect(modal).not.toBeNull();
    expect(modal!.getAttribute("aria-label")).toBe("终端按键配置");
    expect(config.getAttribute("aria-expanded")).toBe("true");
    expect(modal!.querySelectorAll('input[type="text"], textarea')).toHaveLength(0);
    expect(modal!.textContent).toContain("^[[5~");
  });

  it("disables a key, persists the choice, and hides it from the bar", () => {
    renderBar();
    openConfig();
    toggleCheckbox(dialog()!, '在按键条中显示「向上翻页」');
    expect(barLabels()).not.toContain("PgUp");
    expect(JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "null")).toEqual(
      DEFAULT_TERMINAL_KEY_IDS.filter((id) => id !== "pageup"),
    );
  });

  it("re-enables a catalog-only key at the end of the bar", () => {
    renderBar();
    openConfig();
    toggleCheckbox(dialog()!, '在按键条中显示「Ctrl+U（删除整行）」');
    const labels = barLabels();
    expect(labels[labels.length - 1]).toBe("^U");
    expect(JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "null")).toEqual([
      ...DEFAULT_TERMINAL_KEY_IDS,
      "ctrl-u",
    ]);
  });

  it("reorders enabled keys and persists the new order", () => {
    renderBar();
    openConfig();
    click(buttonByAria("下移 Escape"));
    expect(barLabels().slice(2, 5)).toEqual(["Tab", "Esc", "↑"]);
    const stored = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "null") as string[];
    expect(stored.slice(0, 3)).toEqual(["tab", "escape", "up"]);
    click(buttonByAria("上移 Escape"));
    expect(barLabels().slice(2, 5)).toEqual(["Esc", "Tab", "↑"]);
  });

  it("reset restores the defaults and clears the stored config", () => {
    renderBar();
    openConfig();
    toggleCheckbox(dialog()!, '在按键条中显示「向上翻页」');
    toggleCheckbox(dialog()!, '在按键条中显示「向下翻页」');
    expect(localStorage.getItem(STORAGE_KEY)).not.toBeNull();
    click([...dialog()!.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === "重置默认")!);
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull();
    expect(barLabels().slice(2)).toEqual([
      "Esc",
      "Tab",
      "↑",
      "↓",
      "→",
      "←",
      "Home",
      "End",
      "PgUp",
      "PgDn",
      "C",
      "D",
      "Z",
      "^L",
      "^R",
    ]);
  });

  it("keeps the configured keys across remounts (per-device persistence)", () => {
    renderBar();
    openConfig();
    toggleCheckbox(dialog()!, '在按键条中显示「向上翻页」');
    click(buttonByAria("下移 Escape"));
    const before = barLabels();
    mounted!.unmount();

    renderBar();
    expect(barLabels()).toEqual(before);
    expect(barLabels()).not.toContain("PgUp");
  });

  it("falls back to defaults when the stored config is corrupt", () => {
    localStorage.setItem(STORAGE_KEY, "{not json");
    renderBar();
    expect(barLabels().slice(2, 4)).toEqual(["Esc", "Tab"]);

    mounted!.unmount();
    localStorage.setItem(STORAGE_KEY, JSON.stringify(["bogus", "escape", "escape", 42]));
    renderBar();
    expect(barLabels().slice(2)).toEqual(["Esc"]);
    expect(barLabels()).toHaveLength(2 + 1);
  });

  it("keeps focus on the toggled checkbox after it moves between sections", () => {
    renderBar();
    openConfig();
    toggleCheckbox(dialog()!, '在按键条中显示「向上翻页」');
    const active = document.activeElement;
    expect(active?.getAttribute("aria-label")).toBe('在按键条中显示「向上翻页」');
    expect(dialog()!.contains(active)).toBe(true);

    toggleCheckbox(dialog()!, '在按键条中显示「向上翻页」');
    expect(document.activeElement?.getAttribute("aria-label")).toBe('在按键条中显示「向上翻页」');
    expect(dialog()!.contains(document.activeElement)).toBe(true);
  });

  it("keeps focus on the reorder button, falling back to the checkbox at the boundary", () => {
    renderBar();
    openConfig();
    click(buttonByAria("下移 Escape"));
    expect(document.activeElement?.getAttribute("aria-label")).toBe("下移 Escape");
    expect(dialog()!.contains(document.activeElement)).toBe(true);

    click(buttonByAria("下移 Ctrl+L（清屏）"));
    expect(document.activeElement?.getAttribute("aria-label")).toBe('在按键条中显示「Ctrl+L（清屏）」');
    expect(dialog()!.contains(document.activeElement)).toBe(true);
  });

  it("closes the dialog with Escape and with the done button", async () => {
    renderBar();
    openConfig();
    expect(dialog()).not.toBeNull();
    act(() => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    await flush();
    expect(dialog()).toBeNull();
    expect(document.activeElement?.getAttribute("aria-label")).toBe("配置终端按键");

    openConfig();
    click([...dialog()!.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === "完成")!);
    expect(dialog()).toBeNull();
    expect(buttonByAria("配置终端按键").getAttribute("aria-expanded")).toBe("false");
  });

  it("Escape still closes after a toggle moved focus across sections", async () => {
    renderBar();
    openConfig();
    toggleCheckbox(dialog()!, '在按键条中显示「向上翻页」');
    expect(dialog()!.contains(document.activeElement)).toBe(true);
    act(() => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    await flush();
    expect(dialog()).toBeNull();
    expect(document.activeElement?.getAttribute("aria-label")).toBe("配置终端按键");
  });

  it("modifier state is cleared when the config dialog opens", () => {
    renderBar();
    click(buttonByAria("Ctrl 修饰键"));
    openConfig();
    expect(buttonByAria("Ctrl 修饰键").getAttribute("aria-pressed")).toBe("false");
    expect(sent).toEqual([]);
  });
});
