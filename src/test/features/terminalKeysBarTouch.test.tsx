/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, mount, type MountedView } from "./reactTestUtils";
import { TerminalKeysBar } from "../../features/terminal/TerminalKeysBar";
import {
  KEY_REPEAT_DELAY_MS,
  KEY_REPEAT_INTERVAL_MS,
  KEY_REPEAT_SLOP_PX,
} from "../../features/terminal/useKeyAutoRepeat";
import {
  STICKY_MODIFIER_DOUBLE_TAP_MS,
  applyStickyModifiersToTyped,
  clearStickyModifiers,
  getStickyModifiers,
} from "../../features/terminal/terminalInput/stickyModifiers";

const UP = "\x1b[A";

let mounted: MountedView | null = null;
let sent: string[] = [];
let focused = 0;

function renderBar() {
  sent = [];
  focused = 0;
  mounted = mount(
    createElement(TerminalKeysBar, {
      onSend: (data: string) => sent.push(data),
      onFocus: () => {
        focused += 1;
      },
    }),
  );
}

function byAria(label: string): HTMLButtonElement {
  const found = document.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`);
  if (!found) throw new Error(`button not found: ${label}`);
  return found;
}

function keyByLabel(label: string): HTMLButtonElement {
  const found = [...document.querySelectorAll<HTMLButtonElement>(".nx-terminal-keys > button")].find(
    (candidate) => candidate.textContent?.trim() === label,
  );
  if (!found) throw new Error(`key not found: ${label}`);
  return found;
}

function pointer(target: Element, type: string, init: { x?: number; y?: number; id?: number; button?: number } = {}) {
  const event = new MouseEvent(type, {
    bubbles: true,
    cancelable: true,
    button: init.button ?? 0,
    clientX: init.x ?? 0,
    clientY: init.y ?? 0,
    // React derives pointerleave from pointerout towards an element outside the button.
    relatedTarget: type === "pointerout" ? document.body : null,
  });
  Object.defineProperty(event, "pointerId", { value: init.id ?? 1 });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

function advance(ms: number) {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
}

beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear();
  clearStickyModifiers();
});

afterEach(() => {
  mounted?.unmount();
  mounted = null;
  clearStickyModifiers();
  delete document.documentElement.dataset.nxKeyboard;
  vi.useRealTimers();
});

describe("key bar long-press repeat", () => {
  it("repeats a held arrow key and the closing click adds no extra key", () => {
    renderBar();
    const up = byAria("上方向键");
    const down = pointer(up, "pointerdown");
    expect(down.defaultPrevented).toBe(true);
    advance(KEY_REPEAT_DELAY_MS - 1);
    expect(sent).toEqual([]);
    advance(1);
    expect(sent).toEqual([UP]);
    advance(KEY_REPEAT_INTERVAL_MS * 3);
    expect(sent).toEqual([UP, UP, UP, UP]);
    pointer(up, "pointerup");
    click(up);
    advance(KEY_REPEAT_INTERVAL_MS * 4);
    expect(sent).toEqual([UP, UP, UP, UP]);
    // The next ordinary tap sends exactly one key again.
    pointer(up, "pointerdown");
    pointer(up, "pointerup");
    click(up);
    expect(sent).toEqual([UP, UP, UP, UP, UP]);
  });

  it.each(["pointercancel", "pointerout"])("stops repeating on %s", (type) => {
    renderBar();
    const up = byAria("上方向键");
    pointer(up, "pointerdown");
    advance(KEY_REPEAT_DELAY_MS + KEY_REPEAT_INTERVAL_MS);
    expect(sent).toHaveLength(2);
    pointer(up, type);
    advance(KEY_REPEAT_INTERVAL_MS * 5);
    expect(sent).toHaveLength(2);
  });

  it("cancels the hold when the finger pans the bar beyond the slop", () => {
    renderBar();
    const up = byAria("上方向键");
    pointer(up, "pointerdown", { x: 100, y: 10 });
    pointer(up, "pointermove", { x: 100 + KEY_REPEAT_SLOP_PX - 2, y: 10 });
    pointer(up, "pointermove", { x: 100 + KEY_REPEAT_SLOP_PX + 5, y: 10 });
    advance(KEY_REPEAT_DELAY_MS * 3);
    expect(sent).toEqual([]);
  });

  it("does not repeat keys such as Esc, and a tap still sends one key", () => {
    renderBar();
    const esc = byAria("Escape");
    pointer(esc, "pointerdown");
    advance(KEY_REPEAT_DELAY_MS * 3);
    expect(sent).toEqual([]);
    pointer(esc, "pointerup");
    click(esc);
    expect(sent).toEqual(["\x1b"]);
  });

  it("applies a one-shot Alt to every repeat of the hold and releases it afterwards", () => {
    renderBar();
    click(byAria("Alt 修饰键"));
    const up = byAria("上方向键");
    pointer(up, "pointerdown");
    advance(KEY_REPEAT_DELAY_MS + KEY_REPEAT_INTERVAL_MS);
    pointer(up, "pointerup");
    expect(sent).toEqual([`\x1b${UP}`, `\x1b${UP}`]);
    expect(getStickyModifiers()).toEqual({ ctrl: false, alt: false });
  });

  it("stops repeating when the bar unmounts", () => {
    renderBar();
    pointer(byAria("上方向键"), "pointerdown");
    advance(KEY_REPEAT_DELAY_MS);
    mounted?.unmount();
    mounted = null;
    advance(KEY_REPEAT_INTERVAL_MS * 5);
    expect(sent).toEqual([UP]);
  });
});

describe("key bar modifier lock", () => {
  it("locks Ctrl on a double tap until it is tapped again", () => {
    renderBar();
    click(byAria("Ctrl 修饰键"));
    advance(STICKY_MODIFIER_DOUBLE_TAP_MS - 50);
    click(byAria("Ctrl 修饰键"));
    const locked = byAria("Ctrl 修饰键（已锁定）");
    expect(locked.getAttribute("aria-pressed")).toBe("true");
    expect(locked.classList.contains("is-locked")).toBe(true);

    click(keyByLabel("C"));
    click(keyByLabel("D"));
    let typed = "";
    act(() => {
      typed = applyStickyModifiersToTyped("z");
    });
    expect([...sent, typed]).toEqual(["\x03", "\x04", "\x1a"]);
    // Other bar actions keep the lock.
    click(byAria("聚焦终端并打开系统键盘"));
    expect(byAria("Ctrl 修饰键（已锁定）").getAttribute("aria-pressed")).toBe("true");

    click(byAria("Ctrl 修饰键（已锁定）"));
    const released = byAria("Ctrl 修饰键");
    expect(released.getAttribute("aria-pressed")).toBe("false");
    expect(released.classList.contains("is-locked")).toBe(false);
    click(keyByLabel("C"));
    expect(sent.at(-1)).toBe("c");
  });

  it("keeps a single tap one-shot and does not lock across a used latch", () => {
    renderBar();
    click(byAria("Alt 修饰键"));
    click(keyByLabel("C"));
    click(byAria("Alt 修饰键"));
    // The previous tap's latch was used by C, so this pair is a fresh single tap.
    expect(byAria("Alt 修饰键").getAttribute("aria-pressed")).toBe("true");
    expect(byAria("Alt 修饰键").classList.contains("is-locked")).toBe(false);
    click(keyByLabel("C"));
    click(keyByLabel("C"));
    expect(sent).toEqual(["\x1bc", "\x1bc", "c"]);
  });

  it("drops a lock when the bar unmounts", () => {
    renderBar();
    click(byAria("Alt 修饰键"));
    click(byAria("Alt 修饰键"));
    expect(getStickyModifiers()).toEqual({ ctrl: false, alt: true });
    mounted?.unmount();
    mounted = null;
    expect(applyStickyModifiersToTyped("x")).toBe("x");
  });
});

describe("key bar dialogs and the soft keyboard", () => {
  function closeDialog(label: string) {
    click(byAria(`关闭${label}`));
  }

  it("refocuses the terminal when a dialog opened over the soft keyboard closes", async () => {
    renderBar();
    document.documentElement.dataset.nxKeyboard = "open";
    const commands = byAria("快捷命令");
    click(commands);
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
    closeDialog("快捷命令");
    await act(async () => {
      await Promise.resolve();
    });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(focused).toBe(1);
    expect(document.activeElement).not.toBe(commands);

    click(byAria("配置终端按键"));
    click([...document.querySelectorAll<HTMLButtonElement>(".nx-modal-footer button")].find((b) => b.textContent === "完成")!);
    await act(async () => {
      await Promise.resolve();
    });
    expect(focused).toBe(2);
  });

  it("returns focus to the bar button when the soft keyboard was closed", async () => {
    renderBar();
    const commands = byAria("快捷命令");
    click(commands);
    closeDialog("快捷命令");
    await act(async () => {
      await Promise.resolve();
    });
    expect(focused).toBe(0);
    expect(document.activeElement).toBe(commands);
  });
});
