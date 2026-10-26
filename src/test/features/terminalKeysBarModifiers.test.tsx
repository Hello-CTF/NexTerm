/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { createElement } from "react";
import { click, mount, type MountedView } from "./reactTestUtils";
import { TerminalKeysBar } from "../../features/terminal/TerminalKeysBar";
import { TERMINAL_KEY_CATALOG, terminalKeySequence } from "../../features/terminal/terminalKeys";
import { ctrlByte, encodeWithModifiers } from "../../features/terminal/terminalInput/keyEncoding";
import {
  applyStickyModifiersToTyped,
  clearStickyModifiers,
  getStickyModifiers,
} from "../../features/terminal/terminalInput/stickyModifiers";
import { act } from "react";

let mounted: MountedView | null = null;
let sent: string[] = [];

function renderBar(): MountedView {
  sent = [];
  mounted = mount(createElement(TerminalKeysBar, { onSend: (data: string) => sent.push(data), onFocus: () => {} }));
  return mounted;
}

function button(label: string, root: ParentNode = document): HTMLButtonElement {
  const found = [...root.querySelectorAll<HTMLButtonElement>("button")].find(
    (candidate) => candidate.getAttribute("aria-label") === label || candidate.textContent?.trim() === label,
  );
  if (!found) throw new Error(`button not found: ${label}`);
  return found;
}

beforeEach(() => {
  localStorage.clear();
  clearStickyModifiers();
});

afterEach(() => {
  mounted?.unmount();
  mounted = null;
  clearStickyModifiers();
});

describe("key bar sticky modifiers", () => {
  it("applies a latched Ctrl to the next character typed on the system keyboard, then releases it", () => {
    renderBar();
    click(button("Ctrl 修饰键"));
    expect(button("Ctrl 修饰键").getAttribute("aria-pressed")).toBe("true");
    let typed = "";
    act(() => {
      typed = applyStickyModifiersToTyped("c");
    });
    expect(typed).toBe("\x03");
    expect(button("Ctrl 修饰键").getAttribute("aria-pressed")).toBe("false");
    expect(applyStickyModifiersToTyped("c")).toBe("c");
  });

  it("applies Ctrl+Alt together and leaves multi-character input such as a paste untouched", () => {
    renderBar();
    click(button("Ctrl 修饰键"));
    click(button("Alt 修饰键"));
    expect(applyStickyModifiersToTyped("ls -la")).toBe("ls -la");
    expect(getStickyModifiers()).toEqual({ ctrl: true, alt: true });
    let typed = "";
    act(() => {
      typed = applyStickyModifiersToTyped("r");
    });
    expect(typed).toBe("\x1b\x12");
  });

  it("encodes bar keys with the same encoder as typed characters", () => {
    renderBar();
    click(button("Ctrl 修饰键"));
    click(button("C"));
    click(button("Alt 修饰键"));
    click(button("上方向键"));
    click(button("Ctrl 修饰键"));
    click(button("Ctrl 修饰键"));
    click(button("D"));
    expect(sent).toEqual([encodeWithModifiers("c", { ctrl: true, alt: false }), "\x1b\x1b[A", "d"]);
    for (const key of TERMINAL_KEY_CATALOG) {
      expect(terminalKeySequence(key, { ctrl: true, alt: false })).toBe(ctrlByte(key.data) ?? key.data);
    }
  });

  it("drops a latch when the bar unmounts so it cannot alter later typing", () => {
    renderBar();
    click(button("Alt 修饰键"));
    mounted?.unmount();
    mounted = null;
    expect(applyStickyModifiersToTyped("x")).toBe("x");
  });

  it("clears the latch when the system keyboard button is used", () => {
    renderBar();
    click(button("Ctrl 修饰键"));
    click(button("聚焦终端并打开系统键盘"));
    expect(getStickyModifiers()).toEqual({ ctrl: false, alt: false });
  });
});

describe("shared key encoding", () => {
  it.each([
    ["a", "\x01"],
    ["Z", "\x1a"],
    ["\\", "\x1c"],
    ["[", "\x1b"],
    ["]", "\x1d"],
    [" ", "\x00"],
    ["?", "\x7f"],
    ["/", "\x1f"],
    ["3", "\x1b"],
    ["8", "\x7f"],
  ])("Ctrl+%j is %j", (char, expected) => {
    expect(ctrlByte(char)).toBe(expected);
  });

  it("has no control form for other characters and multi-byte keys", () => {
    expect(ctrlByte("1")).toBeNull();
    expect(ctrlByte("é")).toBeNull();
    expect(ctrlByte("\x1b[A")).toBeNull();
    expect(encodeWithModifiers("\x1b[A", { ctrl: true, alt: false })).toBe("\x1b[A");
    expect(encodeWithModifiers("。", { ctrl: false, alt: true })).toBe("\x1b。");
  });
});
