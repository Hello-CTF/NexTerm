/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, mount, waitFor, type MountedView } from "../features/reactTestUtils";
import { ContextMenu, type ContextMenuState } from "../../ui/ContextMenu";

function stubPointer(coarse: boolean): void {
  window.matchMedia = ((query: string) => ({
    matches: coarse && query.includes("pointer: coarse"),
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
}

function menuItem(container: ParentNode, label: string): HTMLButtonElement {
  const item = [...container.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find(
    (candidate) => candidate.querySelector(".nx-menu-label")?.textContent === label,
  );
  if (!item) throw new Error(`Menu item not found: ${label}`);
  return item;
}

function menuLabels(container: ParentNode): string[] {
  return [...container.querySelectorAll('[role="menuitem"]')].map(
    (item) => item.querySelector(".nx-menu-label")?.textContent ?? "",
  );
}

function touch(target: Element, type: string, clientY: number): void {
  const event = new Event(type, { bubbles: true, cancelable: true });
  (event as unknown as { touches: Array<{ clientY: number }> }).touches = [{ clientY }];
  act(() => {
    target.dispatchEvent(event);
  });
}

function dragGrabber(container: ParentNode, distance: number): void {
  const grabber = container.querySelector<HTMLElement>(".nx-menu-sheet-grabber");
  if (!grabber) throw new Error("grabber not found");
  touch(grabber, "touchstart", 300);
  touch(grabber, "touchmove", 300 + distance);
  touch(grabber, "touchend", 300 + distance);
}

function sheetState(): ContextMenuState {
  return {
    x: 8,
    y: 8,
    title: "服务器",
    items: [
      { kind: "item", label: "一级" },
      {
        kind: "item",
        label: "更多",
        submenu: [
          { kind: "item", label: "子项一" },
          { kind: "item", label: "子项二" },
        ],
      },
    ],
  };
}

describe("ContextMenu bottom sheet on coarse pointers", () => {
  let mounted: MountedView | undefined;

  beforeEach(() => {
    document.body.replaceChildren();
    document.body.style.overflow = "";
    stubPointer(true);
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
    document.body.style.overflow = "";
    document.body.replaceChildren();
  });

  it("renders the bottom sheet with title instead of the floating menu", () => {
    mounted = mount(createElement(ContextMenu, { state: sheetState(), onClose: vi.fn() }));

    expect(mounted.container.querySelector(".nx-menu")).toBeNull();
    const sheet = mounted.container.querySelector<HTMLElement>(".nx-menu-sheet");
    expect(sheet).not.toBeNull();
    expect(sheet?.closest(".nx-overlay")).not.toBeNull();
    expect(sheet?.getAttribute("role")).toBe("menu");
    expect(mounted.container.querySelector(".nx-menu-sheet-title")?.textContent).toBe("服务器");
    expect(document.activeElement).toBe(menuItem(mounted.container, "一级"));
  });

  it("keeps the floating menu on fine pointers", () => {
    stubPointer(false);
    mounted = mount(createElement(ContextMenu, { state: sheetState(), onClose: vi.fn() }));

    expect(mounted.container.querySelector(".nx-menu-sheet")).toBeNull();
    expect(mounted.container.querySelector(".nx-menu")).not.toBeNull();
  });

  it("opens a submenu inline with a back control instead of a flyout", () => {
    mounted = mount(createElement(ContextMenu, { state: sheetState(), onClose: vi.fn() }));

    click(menuItem(mounted.container, "更多"));

    expect(mounted.container.querySelector(".nx-submenu")).toBeNull();
    expect(menuLabels(mounted.container)).toEqual(["子项一", "子项二"]);
    expect(mounted.container.querySelector(".nx-menu-sheet-back")?.textContent).toContain("更多");
    expect(document.activeElement).toBe(menuItem(mounted.container, "子项一"));

    click(mounted.container.querySelector(".nx-menu-sheet-back") as HTMLElement);
    expect(menuLabels(mounted.container)).toEqual(["一级", "更多"]);
  });

  it("opens the inline submenu with ArrowRight and goes back with ArrowLeft", () => {
    mounted = mount(createElement(ContextMenu, { state: sheetState(), onClose: vi.fn() }));

    const more = menuItem(mounted.container, "更多");
    act(() => {
      more.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true, cancelable: true }));
    });
    expect(menuLabels(mounted.container)).toEqual(["子项一", "子项二"]);

    act(() => {
      menuItem(mounted!.container, "子项一").dispatchEvent(
        new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true, cancelable: true }),
      );
    });
    expect(menuLabels(mounted.container)).toEqual(["一级", "更多"]);
  });

  it("runs onSelect and closes when a leaf item is tapped", () => {
    const onSelect = vi.fn();
    const onClose = vi.fn();
    const state = sheetState();
    state.items[0] = { kind: "item", label: "一级", onSelect };
    mounted = mount(createElement(ContextMenu, { state, onClose }));

    click(menuItem(mounted.container, "一级"));
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("closes on Escape and on backdrop pointerdown", () => {
    const onClose = vi.fn();
    mounted = mount(createElement(ContextMenu, { state: sheetState(), onClose }));

    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(onClose).toHaveBeenCalledTimes(1);

    act(() => {
      mounted!.container
        .querySelector(".nx-menu-sheet-overlay")
        ?.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("follows the grabber drag and closes past the threshold, otherwise snaps back", async () => {
    const onClose = vi.fn();
    mounted = mount(createElement(ContextMenu, { state: sheetState(), onClose }));
    const sheet = mounted.container.querySelector<HTMLElement>(".nx-menu-sheet");

    dragGrabber(mounted.container, 60);
    await flush();
    expect(onClose).not.toHaveBeenCalled();
    expect(sheet?.style.transform).toBe("");

    dragGrabber(mounted.container, 200);
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
  });
});
