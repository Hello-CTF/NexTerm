/** @vitest-environment jsdom */

import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { mount, type MountedView } from "./features/reactTestUtils";
import { useEdgeGestures } from "../app/edgeGestures";

const realMatchMedia = window.matchMedia;

beforeAll(() => {
  window.matchMedia = ((query: string) => ({
    matches: query === "(pointer: coarse)",
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia;
});

afterAll(() => {
  window.matchMedia = realMatchMedia;
});

function fireTouch(type: "touchstart" | "touchend", x: number, y: number, target?: Element): void {
  const event = new Event(type, { bubbles: true });
  const point = { clientX: x, clientY: y };
  Object.assign(event, { touches: [point], changedTouches: [point] });
  (target ?? window).dispatchEvent(event);
}

function swipe(fromX: number, fromY: number, toX: number, toY: number, target?: Element): void {
  fireTouch("touchstart", fromX, fromY, target);
  fireTouch("touchend", toX, toY, target);
}

function harness(overlayDock: "left" | "right" | null) {
  const handlers = {
    openLeft: vi.fn(),
    openRight: vi.fn(),
    closeOverlay: vi.fn(),
  };
  function Harness() {
    useEdgeGestures({ overlayDock, ...handlers });
    return null;
  }
  const view: MountedView = mount(createElement(Harness));
  return { handlers, view };
}

describe("edge swipe gestures", () => {
  let current: MountedView | null = null;
  afterEach(() => {
    current?.unmount();
    current = null;
  });

  it("right swipe from the left edge opens the left dock", () => {
    const { handlers, view } = harness(null);
    current = view;
    swipe(10, 200, 90, 205);
    expect(handlers.openLeft).toHaveBeenCalledTimes(1);
    expect(handlers.openRight).not.toHaveBeenCalled();
    expect(handlers.closeOverlay).not.toHaveBeenCalled();
  });

  it("left swipe from the right edge opens the AI dock", () => {
    const { handlers, view } = harness(null);
    current = view;
    swipe(window.innerWidth - 10, 200, window.innerWidth - 90, 205);
    expect(handlers.openRight).toHaveBeenCalledTimes(1);
    expect(handlers.openLeft).not.toHaveBeenCalled();
  });

  it("swipes starting away from the edges stay with the content", () => {
    const { handlers, view } = harness(null);
    current = view;
    swipe(200, 200, 320, 200);
    expect(handlers.openLeft).not.toHaveBeenCalled();
    expect(handlers.openRight).not.toHaveBeenCalled();
  });

  it("vertical-dominant drags are not treated as edge gestures", () => {
    const { handlers, view } = harness(null);
    current = view;
    swipe(5, 100, 60, 400);
    expect(handlers.openLeft).not.toHaveBeenCalled();
    expect(handlers.openRight).not.toHaveBeenCalled();
  });

  it("short swipes below the threshold are ignored", () => {
    const { handlers, view } = harness(null);
    current = view;
    swipe(10, 200, 30, 200);
    expect(handlers.openLeft).not.toHaveBeenCalled();
  });

  it("reverse swipe on the backdrop closes the open left overlay", () => {
    const backdrop = document.createElement("div");
    backdrop.className = "nx-dock-backdrop";
    document.body.appendChild(backdrop);
    const { handlers, view } = harness("left");
    current = view;
    swipe(400, 200, 280, 200, backdrop);
    expect(handlers.closeOverlay).toHaveBeenCalledTimes(1);
    expect(handlers.openLeft).not.toHaveBeenCalled();
    backdrop.remove();
  });

  it("reverse swipe on the backdrop closes the open right overlay", () => {
    const backdrop = document.createElement("div");
    backdrop.className = "nx-dock-backdrop";
    document.body.appendChild(backdrop);
    const { handlers, view } = harness("right");
    current = view;
    swipe(300, 200, 420, 200, backdrop);
    expect(handlers.closeOverlay).toHaveBeenCalledTimes(1);
    expect(handlers.openRight).not.toHaveBeenCalled();
    backdrop.remove();
  });

  it("reverse swipe starting in the content keeps the overlay open", () => {
    const { handlers, view } = harness("left");
    current = view;
    swipe(400, 200, 280, 200);
    expect(handlers.closeOverlay).not.toHaveBeenCalled();
  });

  it("forward swipe from the edge keeps the overlay open instead of closing it", () => {
    const { handlers, view } = harness("left");
    current = view;
    swipe(5, 200, 120, 200);
    expect(handlers.closeOverlay).not.toHaveBeenCalled();
    expect(handlers.openLeft).toHaveBeenCalledTimes(1);
  });
});
