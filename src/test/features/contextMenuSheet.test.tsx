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
  return { x: 40, y: 60, items, title: "Host ops" };
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

describe("ContextMenu coarse-pointer action sheet", () => {
  let view: MountedView | undefined;

  afterEach(() => {
    view?.unmount();
    view = undefined;
    coarse = true;
  });

  it("renders a bottom sheet for coarse pointers and a floating menu for fine pointers", () => {
    view = mount(
      createElement(ContextMenu, { state: menuState([{ kind: "item", label: "Copy" }]), onClose: () => {} }),
    );
    expect(view.container.querySelector(".nx-menu-sheet")).not.toBeNull();
    expect(view.container.querySelector(".nx-menu")).toBeNull();
    expect(view.container.querySelector(".nx-menu-sheet-title")?.textContent).toBe("Host ops");
    view.unmount();
    view = undefined;

    coarse = false;
    view = mount(
      createElement(ContextMenu, { state: menuState([{ kind: "item", label: "Copy" }]), onClose: () => {} }),
    );
    expect(view.container.querySelector(".nx-menu")).not.toBeNull();
    expect(view.container.querySelector(".nx-menu-sheet")).toBeNull();
  });

  it("selecting a regular item calls onSelect and closes the menu", () => {
    const onSelect = vi.fn();
    const onClose = vi.fn();
    view = mount(
      createElement(ContextMenu, { state: menuState([{ kind: "item", label: "Copy", onSelect }]), onClose }),
    );
    clickItem(view.container, "Copy");
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("drills down through submenus and returns to the root list with the back button", () => {
    view = mount(
      createElement(ContextMenu, {
        state: menuState([
          { kind: "item", label: "Copy" },
          { kind: "item", label: "More", submenu: [{ kind: "item", label: "Rename" }] },
        ]),
        onClose: () => {},
      }),
    );
    clickItem(view.container, "More");
    expect(itemLabels(view.container)).toEqual(["Rename"]);
    const back = view.container.querySelector(".nx-menu-sheet-back");
    expect(back?.textContent).toContain("More");
    act(() => {
      back!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(itemLabels(view.container)).toEqual(["Copy", "More"]);
  });

  it("selecting a child item after drilling down still calls onSelect", () => {
    const onRename = vi.fn();
    const onClose = vi.fn();
    view = mount(
      createElement(ContextMenu, {
        state: menuState([
          { kind: "item", label: "More", submenu: [{ kind: "item", label: "Rename", onSelect: onRename }] },
        ]),
        onClose,
      }),
    );
    clickItem(view.container, "More");
    clickItem(view.container, "Rename");
    expect(onRename).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("Escape closes the sheet", () => {
    const onClose = vi.fn();
    view = mount(
      createElement(ContextMenu, { state: menuState([{ kind: "item", label: "Copy" }]), onClose }),
    );
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("pulling the grabber past the threshold closes the sheet after a delay", () => {
    const onClose = vi.fn();
    view = mount(
      createElement(ContextMenu, { state: menuState([{ kind: "item", label: "Copy" }]), onClose }),
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

  it("pulling the grabber below the threshold springs back without closing", () => {
    const onClose = vi.fn();
    view = mount(
      createElement(ContextMenu, { state: menuState([{ kind: "item", label: "Copy" }]), onClose }),
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
