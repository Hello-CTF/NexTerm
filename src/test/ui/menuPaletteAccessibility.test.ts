/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, StrictMode } from "react";
import {
  click,
  flush,
  mount,
  setInputValue,
  type MountedView,
} from "../features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  addTab: vi.fn(),
  setSessions: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: { list: mocks.list },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));

import { CommandPalette } from "../../app/CommandPalette";
import { ContextMenu, type ContextMenuState } from "../../ui/ContextMenu";
import { useUi } from "../../app/store";

function keyDown(target: EventTarget, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", {
    key,
    bubbles: true,
    cancelable: true,
    ...init,
  });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

function menuItem(container: ParentNode, label: string): HTMLButtonElement {
  const item = [...container.querySelectorAll<HTMLButtonElement>(".nx-menu-item")].find(
    (candidate) => candidate.querySelector(".nx-menu-label")?.textContent === label,
  );
  if (!item) throw new Error(`Menu item not found: ${label}`);
  return item;
}

function hasRenderedLayout(element: HTMLElement): boolean {
  for (let current: HTMLElement | null = element; current; current = current.parentElement) {
    if (current.hidden || current.style.display === "none") return false;
  }
  return true;
}

function installLayoutRects(): () => void {
  const original = HTMLElement.prototype.getClientRects;
  HTMLElement.prototype.getClientRects = function (this: HTMLElement): DOMRectList {
    return hasRenderedLayout(this)
      ? ([{ width: 10, height: 10 }] as unknown as DOMRectList)
      : ([] as unknown as DOMRectList);
  };
  return () => {
    HTMLElement.prototype.getClientRects = original;
  };
}

describe("menu and command palette accessibility", () => {
  let mounted: MountedView | undefined;
  let uninstallLayoutRects: (() => void) | undefined;

  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    document.body.style.overflow = "";
    uninstallLayoutRects = installLayoutRects();
    mocks.list.mockResolvedValue([]);
    useUi.setState({
      sessions: [],
      addTab: mocks.addTab,
      setSessions: mocks.setSessions,
      pushToast: mocks.toast,
    });
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
    uninstallLayoutRects?.();
    uninstallLayoutRects = undefined;
    document.body.style.overflow = "";
    document.body.replaceChildren();
  });

  it("keeps palette focus in the combobox and guards navigation and execution during IME", async () => {
    const trigger = document.body.appendChild(document.createElement("button"));
    trigger.focus();
    const onClose = vi.fn();
    mounted = mount(createElement(CommandPalette, { onClose }));
    await flush();

    const input = mounted.container.querySelector<HTMLInputElement>('[role="combobox"]');
    const dialog = mounted.container.querySelector<HTMLElement>('[role="dialog"]');
    if (!input || !dialog) throw new Error("Palette controls not found");
    const firstId = input.getAttribute("aria-activedescendant");

    expect(document.activeElement).toBe(input);
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    expect(input.getAttribute("aria-controls")).toBe(
      mounted.container.querySelector('[role="listbox"]')?.id,
    );
    expect(firstId).toBeTruthy();
    expect(document.getElementById(firstId ?? "")?.getAttribute("aria-selected")).toBe("true");

    keyDown(input, "ArrowDown");
    const secondId = input.getAttribute("aria-activedescendant");
    expect(secondId).not.toBe(firstId);
    expect(document.getElementById(secondId ?? "")?.getAttribute("aria-selected")).toBe("true");

    keyDown(input, "ArrowDown", { isComposing: true });
    expect(input.getAttribute("aria-activedescendant")).toBe(secondId);
    keyDown(input, "Enter", { isComposing: true });
    keyDown(input, "Enter", { repeat: true });
    expect(onClose).not.toHaveBeenCalled();
    expect(mocks.addTab).not.toHaveBeenCalled();

    setInputValue(input, "设置");
    keyDown(input, "Enter");
    keyDown(input, "Enter");
    expect(mocks.addTab).toHaveBeenCalledOnce();
    expect(mocks.addTab).toHaveBeenCalledWith({
      id: "settings",
      kind: "settings",
      title: "设置",
      closable: true,
    });
    expect(onClose).toHaveBeenCalledOnce();

    mounted.unmount();
    mounted = undefined;
    await flush();
    expect(document.activeElement).toBe(trigger);
  });

  it("traps Tab and Shift+Tab on the combobox so focus matches the active option", async () => {
    const trigger = document.body.appendChild(document.createElement("button"));
    trigger.focus();
    mounted = mount(createElement(CommandPalette, { onClose: vi.fn() }));
    await flush();

    const input = mounted.container.querySelector<HTMLInputElement>('[role="combobox"]');
    if (!input) throw new Error("Combobox not found");
    const activeOption = document.getElementById(input.getAttribute("aria-activedescendant") ?? "");
    expect(activeOption?.tabIndex).toBe(-1);
    const listbox = mounted.container.querySelector<HTMLElement>('[role="listbox"]');
    expect(listbox?.tabIndex).toBe(-1);

    const tab = keyDown(input, "Tab");
    expect(tab.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(input);
    const shiftTab = keyDown(input, "Tab", { shiftKey: true });
    expect(shiftTab.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(input);
    expect(document.activeElement?.getAttribute("role")).not.toBe("option");

    setInputValue(input, "设置");
    keyDown(input, "Tab");
    keyDown(input, "Tab", { shiftKey: true });
    expect(document.activeElement).toBe(input);
    keyDown(input, "Enter");
    expect(mocks.addTab).toHaveBeenCalledExactlyOnceWith({
      id: "settings",
      kind: "settings",
      title: "设置",
      closable: true,
    });
  });

  it("surfaces deferred asset loading errors through an inline alert", async () => {
    mocks.list.mockRejectedValue(new Error("offline"));
    mounted = mount(createElement(CommandPalette, { onClose: vi.fn() }));

    await flush();
    expect(mocks.list).toHaveBeenCalledOnce();
    expect(mounted.container.textContent).toContain("offline");
    expect(mounted.container.querySelector('[role="combobox"]')).not.toBeNull();
  });

  it("supports keyboard-only submenu navigation and restores the parent item", () => {
    const state: ContextMenuState = {
      x: 12,
      y: 20,
      title: "服务器",
      items: [
        { kind: "group", label: "动作" },
        { kind: "item", label: "不可用", disabled: true },
        { kind: "item", label: "一级" },
        {
          kind: "item",
          label: "更多",
          submenu: [
            { kind: "item", label: "子项一" },
            { kind: "item", label: "子项二" },
          ],
        },
        { kind: "separator" },
        { kind: "item", label: "删除", danger: true },
      ],
    };
    mounted = mount(createElement(ContextMenu, { state, onClose: vi.fn() }));

    const first = menuItem(mounted.container, "一级");
    const more = menuItem(mounted.container, "更多");
    expect(document.activeElement).toBe(first);
    expect(mounted.container.querySelector('[role="menu"]')?.getAttribute("aria-label")).toBe("服务器");
    expect(mounted.container.querySelector('[role="separator"]')).not.toBeNull();

    keyDown(first, "ArrowDown");
    expect(document.activeElement).toBe(more);
    keyDown(more, "ArrowRight");
    const child = menuItem(mounted.container, "子项一");
    expect(document.activeElement).toBe(child);
    expect(more.getAttribute("aria-expanded")).toBe("true");
    expect(more.getAttribute("aria-haspopup")).toBe("menu");
    expect(more.getAttribute("aria-controls")).toBe(child.closest(".nx-submenu")?.id);

    keyDown(child, "ArrowDown");
    expect(document.activeElement).toBe(menuItem(mounted.container, "子项二"));
    keyDown(document.activeElement as HTMLElement, "ArrowLeft");
    expect(document.activeElement).toBe(more);
    expect(more.getAttribute("aria-expanded")).toBe("false");

    click(more);
    expect(more.getAttribute("aria-expanded")).toBe("true");
    expect(document.activeElement).toBe(menuItem(mounted.container, "子项一"));
    expect(more.querySelector(".nx-menu-arrow")?.getAttribute("aria-hidden")).toBe("true");
  });

  it("closes a menu before its action and never runs that action twice", () => {
    const calls: string[] = [];
    const state: ContextMenuState = {
      x: 0,
      y: 0,
      items: [{ kind: "item", label: "执行", onSelect: () => calls.push("select") }],
    };
    const onClose = vi.fn(() => {
      calls.push("close");
    });
    mounted = mount(createElement(ContextMenu, { state, onClose }));
    const item = menuItem(mounted.container, "执行");

    click(item);
    click(item);

    expect(calls).toEqual(["close", "select"]);
    expect(onClose).toHaveBeenCalledOnce();
    keyDown(window, "Escape", { isComposing: true });
    keyDown(window, "Escape", { repeat: true });
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("restores the invoking control when a menu unmounts", async () => {
    const trigger = document.body.appendChild(document.createElement("button"));
    trigger.focus();
    mounted = mount(
      createElement(ContextMenu, {
        state: { x: 0, y: 0, items: [{ kind: "item", label: "动作" }] },
        onClose: vi.fn(),
      }),
    );
    expect(document.activeElement).not.toBe(trigger);

    mounted.unmount();
    mounted = undefined;
    await flush();
    expect(document.activeElement).toBe(trigger);
  });

  it("restores the palette invoker after StrictMode effect replay", async () => {
    const trigger = document.body.appendChild(document.createElement("button"));
    trigger.focus();
    mounted = mount(
      createElement(
        StrictMode,
        null,
        createElement(CommandPalette, { onClose: vi.fn() }),
      ),
    );
    await flush();
    expect(document.activeElement).toBe(
      mounted.container.querySelector('[role="combobox"]'),
    );

    mounted.unmount();
    mounted = undefined;
    await flush();
    expect(document.activeElement).toBe(trigger);
  });

  it.each([
    { shiftKey: false, direction: "next" },
    { shiftKey: true, direction: "previous" },
  ])("moves to the $direction page control when Tab closes a menu", async ({ shiftKey }) => {
    const before = document.createElement("button");
    const trigger = document.createElement("button");
    const after = document.createElement("button");
    document.body.append(before, trigger, after);
    trigger.focus();
    mounted = mount(
      createElement(ContextMenu, {
        state: { x: 0, y: 0, items: [{ kind: "item", label: "动作" }] },
        onClose: vi.fn(),
      }),
    );

    keyDown(menuItem(mounted.container, "动作"), "Tab", { shiftKey });
    mounted.unmount();
    mounted = undefined;
    await flush();
    expect(document.activeElement).toBe(shiftKey ? before : after);
  });

  it("hands Shift+Tab to the visible previous control, not a hidden button or BODY", async () => {
    const expand = document.createElement("button");
    expand.textContent = "展开";
    const hiddenPanel = document.createElement("div");
    hiddenPanel.style.display = "none";
    const hiddenDelete = document.createElement("button");
    hiddenDelete.textContent = "删除";
    hiddenPanel.appendChild(hiddenDelete);
    const trigger = document.createElement("button");
    document.body.append(expand, hiddenPanel, trigger);
    trigger.focus();
    mounted = mount(
      createElement(ContextMenu, {
        state: { x: 0, y: 0, items: [{ kind: "item", label: "动作" }] },
        onClose: vi.fn(),
      }),
    );

    keyDown(menuItem(mounted.container, "动作"), "Tab", { shiftKey: true });
    mounted.unmount();
    mounted = undefined;
    await flush();
    expect(document.activeElement).toBe(expand);
    expect(document.activeElement).not.toBe(hiddenDelete);
    expect(document.activeElement).not.toBe(document.body);
  });
});
