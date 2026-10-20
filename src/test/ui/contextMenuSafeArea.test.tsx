/** @vitest-environment jsdom */

import { act, createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ContextMenu } from "../../ui/ContextMenu";
import { mount, type MountedView } from "../features/reactTestUtils";

const realMatchMedia = window.matchMedia;
const realGetComputedStyle = window.getComputedStyle;
const realInnerHeight = window.innerHeight;
const realInnerWidth = window.innerWidth;
let mounted: MountedView | undefined;
let menuHeight = 200;
let anchorTop = 705;

beforeEach(() => {
  menuHeight = 200;
  anchorTop = 705;
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia;
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  Object.defineProperty(window, "innerWidth", { configurable: true, value: 1000 });
  vi.spyOn(window, "getComputedStyle").mockImplementation((element, pseudo) => {
    if (element instanceof HTMLElement && element.style.paddingBottom === "var(--nx-safe-bottom)") {
      return { paddingBottom: "34px" } as CSSStyleDeclaration;
    }
    return realGetComputedStyle.call(window, element, pseudo);
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    return this.classList.contains("nx-menu") ? menuHeight : 0;
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.classList.contains("nx-menu") ? 200 : 0;
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.restoreAllMocks();
  window.matchMedia = realMatchMedia;
  Object.defineProperty(window, "innerHeight", { configurable: true, value: realInnerHeight });
  Object.defineProperty(window, "innerWidth", { configurable: true, value: realInnerWidth });
});

function mountMainMenu(onClose: () => void = vi.fn()): HTMLElement {
  mounted = mount(createElement(ContextMenu, {
    state: { x: 40, y: 580, items: [{ kind: "item", label: "Copy" }] },
    onClose,
  }));
  const menu = mounted.container.querySelector<HTMLElement>(".nx-menu");
  if (!menu) throw new Error("main menu not found");
  return menu;
}

function openSubmenu(naturalHeight: number, onClose: () => void = vi.fn(), nested = false): HTMLElement {
  const realGetBoundingClientRect = HTMLElement.prototype.getBoundingClientRect;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.classList.contains("nx-menu-row")) {
      const nestedRow = this.parentElement?.classList.contains("nx-submenu");
      return new DOMRect(nestedRow ? 900 : 698, anchorTop, 100, 40);
    }
    if (this.classList.contains("nx-submenu")) return new DOMRect(800, anchorTop - 5, 100, naturalHeight);
    return realGetBoundingClientRect.call(this);
  });
  mounted = mount(createElement(ContextMenu, {
    state: {
      x: 40,
      y: 100,
      items: [{
        kind: "item",
        label: "More",
        submenu: nested
          ? [{ kind: "item", label: "Nested", submenu: [{ kind: "item", label: "Leaf" }] }]
          : [{ kind: "item", label: "Rename" }],
      }],
    },
    onClose,
  }));

  const more = mounted.container.querySelector<HTMLElement>(".nx-menu-item");
  act(() => {
    more?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
  if (nested) {
    const nestedItem = [...mounted.container.querySelectorAll<HTMLElement>(".nx-submenu .nx-menu-item")]
      .find((element) => element.textContent?.includes("Nested"));
    act(() => {
      nestedItem?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    const nestedSubmenu = mounted.container.querySelectorAll<HTMLElement>(".nx-submenu")[1];
    if (!nestedSubmenu) throw new Error("nested submenu not found");
    return nestedSubmenu;
  }
  const submenu = mounted.container.querySelector<HTMLElement>(".nx-submenu");
  if (!submenu) throw new Error("submenu not found");
  return submenu;
}

function wheel(target: Element): void {
  act(() => {
    target.dispatchEvent(new WheelEvent("wheel", { bubbles: true }));
  });
}

function scrollMainMenu(): void {
  const menu = mounted!.container.querySelector<HTMLElement>(".nx-menu");
  anchorTop = 555;
  act(() => {
    if (menu) {
      menu.scrollTop = 150;
      menu.dispatchEvent(new Event("scroll"));
    }
  });
}

function expectVisibleBottom(element: HTMLElement, naturalHeight: number): void {
  const style = getComputedStyle(element);
  const top = Number.parseFloat(style.top);
  const maxHeight = Number.parseFloat(style.maxHeight);
  expect(maxHeight).toBe(750);
  expect(style.overflowY).toBe("auto");
  expect(top + Math.min(naturalHeight, maxHeight)).toBe(758);
}

describe("floating context menu safe area", () => {
  it("clamps a fine-pointer menu above the measured bottom inset", () => {
    const menu = mountMainMenu();

    expect(menu.style.top).toBe("558px");
    expect(menu.style.visibility).toBe("visible");
    expectVisibleBottom(menu, 200);
  });

  it("limits an oversized main menu and clamps its visible height", () => {
    menuHeight = 900;
    const menu = mountMainMenu();

    expect(menu.style.top).toBe("8px");
    expectVisibleBottom(menu, 900);
  });

  it("moves an overflowing fixed submenu above the same inset", () => {
    const submenu = openSubmenu(150);

    expect(submenu.style.position).toBe("fixed");
    expect(submenu.style.left).toBe("800px");
    expect(submenu.style.top).toBe("608px");
    expect(submenu.parentElement?.classList.contains("nx-menu-row")).toBe(true);
    expectVisibleBottom(submenu, 150);
  });

  it("limits an oversized fixed submenu without leaving it inside the scroll clipping", () => {
    const submenu = openSubmenu(900);

    expect(submenu.style.position).toBe("fixed");
    expect(submenu.style.top).toBe("8px");
    expect(submenu.parentElement?.classList.contains("nx-menu-row")).toBe(true);
    expectVisibleBottom(submenu, 900);
  });

  it("keeps the main menu open for internal wheel and closes for external wheel", () => {
    const onClose = vi.fn();
    const menu = mountMainMenu(onClose);

    wheel(menu);
    expect(onClose).not.toHaveBeenCalled();
    wheel(document.body);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("keeps an oversized fixed submenu open for internal wheel", () => {
    const onClose = vi.fn();
    const submenu = openSubmenu(900, onClose);

    wheel(submenu);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("keeps a nested fixed submenu open for internal wheel", () => {
    const onClose = vi.fn();
    const submenu = openSubmenu(150, onClose, true);

    wheel(submenu);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("repositions a fixed submenu after its main menu scrolls", () => {
    const submenu = openSubmenu(150);

    scrollMainMenu();
    expect(submenu.style.top).toBe("550px");
  });

  it("repositions a nested fixed submenu and preserves horizontal flip after scrolling", () => {
    const submenu = openSubmenu(150, vi.fn(), true);

    expect(submenu.style.left).toBe("798px");
    expect(submenu.style.top).toBe("608px");
    scrollMainMenu();
    expect(submenu.style.left).toBe("798px");
    expect(submenu.style.top).toBe("550px");
  });
});
