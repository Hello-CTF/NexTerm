/** @vitest-environment jsdom */

import { act, createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { click, flush, mount, setInputValue, type MountedView } from "./reactTestUtils";

const dialogs = vi.hoisted(() => ({ ask: vi.fn() }));

vi.mock("../../ui/dialogs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ui/dialogs")>()),
  ask: dialogs.ask,
}));

import { TerminalKeysBar } from "../../features/terminal/TerminalKeysBar";
import { reloadTerminalInputMode, setTerminalInputMode } from "../../features/terminal/terminalInputMode";
import { saveQuickCommands, quickCommandBytes } from "../../features/terminal/quickCommands";
import { TERMINAL_KEY_CATALOG, terminalKeySequence } from "../../features/terminal/terminalKeys";
import { clearStickyModifiers } from "../../features/terminal/terminalInput/stickyModifiers";
import { registerAccountPreferenceStore } from "../../app/preferences";

let mounted: MountedView | null = null;
let keys: string[];
let lines: Array<[string, boolean]>;
let terminalFocus: number;

function renderBar(): MountedView {
  keys = [];
  lines = [];
  terminalFocus = 0;
  mounted = mount(
    createElement(TerminalKeysBar, {
      onSend: (data: string) => keys.push(data),
      onSendText: (text: string, submit: boolean) => lines.push([text, submit]),
      onFocus: () => {
        terminalFocus += 1;
      },
    }),
  );
  return mounted;
}

function button(label: string): HTMLButtonElement {
  const found = [...document.querySelectorAll<HTMLButtonElement>("button")].find(
    (candidate) => candidate.getAttribute("aria-label") === label || candidate.textContent?.trim() === label,
  );
  if (!found) throw new Error(`button not found: ${label}`);
  return found;
}

function composer(): HTMLInputElement | null {
  return document.querySelector<HTMLInputElement>('input[aria-label="缓冲输入内容"]');
}

function pressEnter(input: HTMLInputElement, init: KeyboardEventInit & { keyCode?: number } = {}) {
  act(() => {
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true, ...init }));
  });
}

beforeEach(() => {
  localStorage.clear();
  registerAccountPreferenceStore(null);
  setTerminalInputMode("direct");
  clearStickyModifiers();
  dialogs.ask.mockReset().mockResolvedValue(true);
});

afterEach(() => {
  mounted?.unmount();
  mounted = null;
  setTerminalInputMode("direct");
  localStorage.clear();
});

describe("buffered input", () => {
  it("is off by default and toggles on with one tap, focusing the line field", () => {
    renderBar();
    expect(composer()).toBeNull();
    expect(button("缓冲输入").getAttribute("aria-pressed")).toBe("false");

    click(button("缓冲输入"));
    expect(button("缓冲输入").getAttribute("aria-pressed")).toBe("true");
    expect(document.activeElement).toBe(composer());

    click(button("缓冲输入"));
    expect(composer()).toBeNull();
    expect(terminalFocus).toBe(1);
  });

  it("sends the edited line with Enter and clears the field", () => {
    renderBar();
    click(button("缓冲输入"));
    setInputValue(composer()!, "ls -la");
    pressEnter(composer()!);
    expect(lines).toEqual([["ls -la", true]]);
    expect(composer()!.value).toBe("");
    expect(keys).toEqual([]);
  });

  it("sends without Enter from its own button", () => {
    renderBar();
    click(button("缓冲输入"));
    setInputValue(composer()!, "git commit -m ");
    click(button("发送但不回车"));
    expect(lines).toEqual([["git commit -m ", false]]);
  });

  it("takes dictated and autocorrected text as one line and ignores the Enter that commits an IME composition", () => {
    renderBar();
    click(button("缓冲输入"));
    const field = composer()!;
    // Dictation inserts whole phrases and autocorrect replaces words; the field only sees value changes.
    setInputValue(field, "echo hello");
    setInputValue(field, "echo hello world from dictation");
    pressEnter(field, { isComposing: true });
    pressEnter(field, { keyCode: 229 });
    expect(lines).toEqual([]);
    pressEnter(field);
    expect(lines).toEqual([["echo hello world from dictation", true]]);
  });

  it("sends a bare Enter from an empty line", () => {
    renderBar();
    click(button("缓冲输入"));
    click(button("发送并回车"));
    click(button("发送但不回车"));
    expect(lines).toEqual([["", true]]);
  });

  it("remembers the choice on this device", () => {
    renderBar();
    click(button("缓冲输入"));
    mounted!.unmount();
    reloadTerminalInputMode();
    renderBar();
    expect(composer()).not.toBeNull();
    expect(localStorage.getItem("nexterm.terminalInputMode.v1")).toBe("buffered");
  });

  it("focuses the line field instead of the terminal from the keyboard button", () => {
    renderBar();
    click(button("缓冲输入"));
    (document.activeElement as HTMLElement | null)?.blur();
    click(button("聚焦终端并打开系统键盘"));
    expect(document.activeElement).toBe(composer());
    expect(terminalFocus).toBe(0);
  });
});

describe("quick commands in the key bar", () => {
  async function openQuickCommands() {
    click(button("快捷命令"));
    await flush();
  }

  it("sends a command with and without Enter", async () => {
    await saveQuickCommands([
      { label: "状态", text: "git status", appendEnter: true },
      { label: "前缀", text: "sudo ", appendEnter: false },
    ]);
    renderBar();
    await openQuickCommands();
    click(button("发送快捷命令 状态"));
    await flush();
    await openQuickCommands();
    click(button("发送快捷命令 前缀"));
    await flush();
    expect(lines).toEqual([["git status", true], ["sudo ", false]]);
    expect(dialogs.ask).not.toHaveBeenCalled();
  });

  it("asks before sending a command with several lines and sends nothing when declined", async () => {
    await saveQuickCommands([{ label: "两步", text: "cd /tmp\nls", appendEnter: true }]);
    renderBar();
    dialogs.ask.mockResolvedValueOnce(false);
    await openQuickCommands();
    click(button("发送快捷命令 两步"));
    await flush();
    expect(dialogs.ask).toHaveBeenCalledTimes(1);
    expect(String(dialogs.ask.mock.calls[0][0])).toContain("2 行");
    expect(lines).toEqual([]);

    await openQuickCommands();
    click(button("发送快捷命令 两步"));
    await flush();
    expect(lines).toEqual([["cd /tmp\nls", true]]);
  });

  it("adds a command from the panel's manage view", async () => {
    renderBar();
    await openQuickCommands();
    click(button("管理快捷命令"));
    click(button("添加快捷命令"));
    const dialog = document.querySelector('[role="dialog"]')!;
    const [label] = dialog.querySelectorAll<HTMLInputElement>("input.nx-input");
    setInputValue(label, "日志");
    setInputValue(dialog.querySelector<HTMLTextAreaElement>("textarea")!, "tail -f app.log");
    click(button("保存"));
    await flush();
    click(button("返回发送"));
    click(button("发送快捷命令 日志"));
    await flush();
    expect(lines).toEqual([["tail -f app.log", true]]);
  });

  it("encodes a command as a bracketed paste with Enter after the end marker", () => {
    expect(quickCommandBytes("make", true, true)).toBe("\x1b[200~make\x1b[201~\r");
    expect(quickCommandBytes("make", false, true)).toBe("\x1b[200~make\x1b[201~");
    expect(quickCommandBytes("make", true, false)).toBe("make\r");
  });

  it("sends plain bytes without the terminal's paste path", async () => {
    await saveQuickCommands([{ label: "退出", text: "exit", appendEnter: true }]);
    keys = [];
    mounted = mount(createElement(TerminalKeysBar, { onSend: (data: string) => keys.push(data), onFocus: () => {} }));
    await openQuickCommands();
    click(button("发送快捷命令 退出"));
    await flush();
    expect(keys).toEqual(["exit\r"]);
  });
});

describe("choice keys", () => {
  it.each([
    ["digit-1", "1"],
    ["digit-2", "2"],
    ["digit-3", "3"],
    ["y", "y"],
    ["n", "n"],
    ["enter", "\r"],
  ])("%s sends %j", (id, bytes) => {
    const key = TERMINAL_KEY_CATALOG.find((candidate) => candidate.id === id);
    expect(key).toBeDefined();
    expect(terminalKeySequence(key!, { ctrl: false, alt: false })).toBe(bytes);
  });

  it("sends the choice keys from the bar once enabled, with a latched Alt", () => {
    localStorage.setItem("nexterm.terminalKeys.v1", JSON.stringify(["digit-1", "y", "enter"]));
    renderBar();
    click(button("数字 1（选择第 1 项）"));
    click(button("字母 y（确认）"));
    click(button("Alt 修饰键"));
    click(button("回车"));
    expect(keys).toEqual(["1", "y", "\x1b\r"]);
  });
});
