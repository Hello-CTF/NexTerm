/** @vitest-environment jsdom */

import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { ContextMenu, type ContextMenuState, type MenuItem } from "../../ui/ContextMenu";
import { mount, type MountedView } from "./reactTestUtils";

const realMatchMedia = window.matchMedia;
let coarse = true;

beforeAll(() => {
  window.matchMedia = ((query: string) => ({
    matches: query === "(pointer: coarse)" && coarse,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia;
});

afterAll(() => {
  window.matchMedia = realMatchMedia;
});

function menuState(items: MenuItem[]): ContextMenuState {
  return { x: 40, y: 60, items, title: "主机 ops" };
}

function clickItem(container: ParentNode, label: string): void {
  const button = [...container.querySelectorAll(".nx-menu-item")].find(
    (candidate) => candidate.querySelector(".nx-menu-label")?.textContent === label,
  );
  if (!button) throw new Error(`Menu item not found: ${label}`);
  act(() => {
    button.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

function itemLabels(container: ParentNode): Array<string | null | undefined> {
  return [...container.querySelectorAll(".nx-menu-item")].map(
    (button) => button.querySelector(".nx-menu-label")?.textContent,
  );
}

function fireTouch(el: Element, type: string, y: number): void {
  const event = new Event(type, { bubbles: true, cancelable: true });
  const point = { clientX: 0, clientY: y };
  Object.assign(event, { touches: [point], changedTouches: [point] });
  act(() => {
    el.dispatchEvent(event);
  });
}

describe("ContextMenu 粗指针 action sheet", () => {
  let view: MountedView | undefined;

  afterEach(() => {
    view?.unmount();
    view = undefined;
    coarse = true;
  });

  it("粗指针渲染底部 sheet, 细指针渲染浮动菜单", () => {
    view = mount(
      createElement(ContextMenu, { state: menuState([{ kind: "item", label: "复制" }]), onClose: () => {} }),
    );
    expect(view.container.querySelector(".nx-menu-sheet")).not.toBeNull();
    expect(view.container.querySelector(".nx-menu")).toBeNull();
    expect(view.container.querySelector(".nx-menu-sheet-title")?.textContent).toBe("主机 ops");
    view.unmount();
    view = undefined;

    coarse = false;
    view = mount(
      createElement(ContextMenu, { state: menuState([{ kind: "item", label: "复制" }]), onClose: () => {} }),
    );
    expect(view.container.querySelector(".nx-menu")).not.toBeNull();
    expect(view.container.querySelector(".nx-menu-sheet")).toBeNull();
  });

  it("点击普通项触发 onSelect 并关闭", () => {
    const onSelect = vi.fn();
    const onClose = vi.fn();
    view = mount(
      createElement(ContextMenu, { state: menuState([{ kind: "item", label: "复制", onSelect }]), onClose }),
    );
    clickItem(view.container, "复制");
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("子菜单逐级下钻, 返回键回到根列表", () => {
    view = mount(
      createElement(ContextMenu, {
        state: menuState([
          { kind: "item", label: "复制" },
          { kind: "item", label: "更多", submenu: [{ kind: "item", label: "重命名" }] },
        ]),
        onClose: () => {},
      }),
    );
    clickItem(view.container, "更多");
    expect(itemLabels(view.container)).toEqual(["重命名"]);
    const back = view.container.querySelector(".nx-menu-sheet-back");
    expect(back?.textContent).toContain("更多");
    act(() => {
      back!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(itemLabels(view.container)).toEqual(["复制", "更多"]);
  });

  it("下钻后选中子项照常触发 onSelect", () => {
    const onRename = vi.fn();
    const onClose = vi.fn();
    view = mount(
      createElement(ContextMenu, {
        state: menuState([
          { kind: "item", label: "更多", submenu: [{ kind: "item", label: "重命名", onSelect: onRename }] },
        ]),
        onClose,
      }),
    );
    clickItem(view.container, "更多");
    clickItem(view.container, "重命名");
    expect(onRename).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("Escape 关闭 sheet", () => {
    const onClose = vi.fn();
    view = mount(
      createElement(ContextMenu, { state: menuState([{ kind: "item", label: "复制" }]), onClose }),
    );
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("grabber 下拉超过阈值后延时关闭", () => {
    const onClose = vi.fn();
    view = mount(
      createElement(ContextMenu, { state: menuState([{ kind: "item", label: "复制" }]), onClose }),
    );
    vi.useFakeTimers();
    try {
      const grabber = view.container.querySelector(".nx-menu-sheet-grabber")!;
      fireTouch(grabber, "touchstart", 100);
      fireTouch(grabber, "touchmove", 320);
      fireTouch(grabber, "touchend", 320);
      expect(onClose).not.toHaveBeenCalled();
      act(() => {
        vi.advanceTimersByTime(200);
      });
      expect(onClose).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("grabber 下拉未过阈值回弹不关闭", () => {
    const onClose = vi.fn();
    view = mount(
      createElement(ContextMenu, { state: menuState([{ kind: "item", label: "复制" }]), onClose }),
    );
    const grabber = view.container.querySelector(".nx-menu-sheet-grabber")!;
    const sheet = view.container.querySelector(".nx-menu-sheet") as HTMLElement;
    fireTouch(grabber, "touchstart", 100);
    fireTouch(grabber, "touchmove", 150);
    expect(sheet.style.transform).toBe("translateY(50px)");
    fireTouch(grabber, "touchend", 150);
    expect(onClose).not.toHaveBeenCalled();
    expect(sheet.style.transform).toBe("");
  });
});
