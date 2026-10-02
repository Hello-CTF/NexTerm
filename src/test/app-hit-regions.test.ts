/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  isWailsDragRegionTarget,
  wailsDragRegionStyle,
  wailsNoDragRegionStyle,
} from "../app/platform";

function applyStyle(element: HTMLElement, style: object): void {
  for (const [property, value] of Object.entries(style)) {
    element.style.setProperty(property, String(value));
  }
}

function wouldStartWailsDrag(element: Element): boolean {
  return getComputedStyle(element).getPropertyValue("--wails-draggable").trim() === "drag";
}

function dragRegion(): HTMLDivElement {
  const region = document.createElement("div");
  region.setAttribute("data-wails-drag-region", "");
  applyStyle(region, wailsDragRegionStyle);
  return region;
}

function noDragButton(className: string): HTMLButtonElement {
  const button = document.createElement("button");
  button.className = className;
  applyStyle(button, wailsNoDragRegionStyle);
  return button;
}

beforeEach(() => {
  document.body.replaceChildren();
});

describe("标题栏交互命中区域", () => {
  it("按钮及其后代不会成为拖动句柄，空白区域仍可拖动", () => {
    const region = dragRegion();
    const tab = noDragButton("nx-ws");
    const label = document.createElement("span");
    label.textContent = "workspace";
    tab.append(label);
    const spacer = dragRegion();
    region.append(tab, spacer);
    document.body.append(region);

    expect(wouldStartWailsDrag(tab)).toBe(false);
    expect(wouldStartWailsDrag(label)).toBe(false);
    expect(wouldStartWailsDrag(spacer)).toBe(true);
  });

  it("标签和动作按钮照常接收 click，双击不会最大化", () => {
    const root = document.createElement("div");
    const region = dragRegion();
    const tab = noDragButton("nx-ws");
    const label = document.createElement("span");
    label.textContent = "workspace";
    tab.append(label);
    const action = noDragButton("nx-icon-btn");
    region.append(tab, action);
    root.append(region);
    document.body.append(root);

    const tabClick = vi.fn();
    const actionClick = vi.fn();
    const maximize = vi.fn();
    tab.addEventListener("click", tabClick);
    action.addEventListener("click", actionClick);
    root.addEventListener("dblclick", (event) => {
      if (isWailsDragRegionTarget(event.target)) maximize();
    });

    for (const target of [tab, label, action]) {
      target.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      target.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    }

    expect(tabClick).toHaveBeenCalledTimes(2);
    expect(actionClick).toHaveBeenCalledTimes(1);
    expect(maximize).not.toHaveBeenCalled();
  });

  it("只有事件目标自身是拖动区域时才切换最大化", () => {
    const root = document.createElement("div");
    const spacer = dragRegion();
    const descendant = document.createElement("span");
    descendant.textContent = "child without its own drag marker";
    spacer.append(descendant);
    root.append(spacer);
    document.body.append(root);
    const maximize = vi.fn();
    root.addEventListener("dblclick", (event) => {
      if (isWailsDragRegionTarget(event.target)) maximize();
    });

    descendant.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    expect(maximize).not.toHaveBeenCalled();

    spacer.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    expect(maximize).toHaveBeenCalledTimes(1);
  });
});
