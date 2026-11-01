/** @vitest-environment jsdom */

import { act, createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { RemoteTouchLayer } from "../../features/remote/RemoteTouchLayer";
import { LONG_PRESS_MS, type TouchMode } from "../../features/remote/touchGestures";
import { mount, type MountedView } from "./reactTestUtils";

type P = { x: number; y: number };

let view: MountedView | undefined;
let target: HTMLElement;
let seen: Array<{ type: string; x: number; y: number; button: number; buttons: number; deltaY?: number }>;
let onPinch: Mock<(factor: number) => void>;
let onPan: Mock<(dx: number, dy: number) => void>;
let onFocusRequest: Mock<() => void>;

function layerProps(mode: TouchMode, getTarget: () => HTMLElement | null = () => target) {
  return { mode, getTarget, onPinch, onPan, onFocusRequest };
}

function mountLayer(mode: TouchMode = "direct", getTarget?: () => HTMLElement | null) {
  view = mount(createElement(RemoteTouchLayer, layerProps(mode, getTarget)));
  return {
    layer: view.container.querySelector<HTMLElement>("[data-testid='remote-touch-layer']")!,
    setMode: (next: TouchMode) => act(() => view!.root.render(createElement(RemoteTouchLayer, layerProps(next, getTarget)))),
  };
}

function touch(layer: HTMLElement, type: string, points: P[]): Event {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(event, "touches", { value: points.map((p) => ({ clientX: p.x, clientY: p.y })) });
  act(() => {
    layer.dispatchEvent(event);
  });
  return event;
}

const mouseOnly = () => seen.filter((entry) => entry.type !== "wheel");

beforeEach(() => {
  vi.useFakeTimers();
  // The layer passes view: window; under vitest's jsdom global that object is not a jsdom Window, so the event
  // constructor rejects it. Every other property reaches the event unchanged.
  const NativeMouseEvent = MouseEvent;
  vi.stubGlobal(
    "MouseEvent",
    class extends NativeMouseEvent {
      constructor(type: string, init: MouseEventInit = {}) {
        const { view: _view, ...rest } = init;
        super(type, rest);
      }
    },
  );
  seen = [];
  onPinch = vi.fn();
  onPan = vi.fn();
  onFocusRequest = vi.fn();
  target = document.createElement("canvas");
  document.body.append(target);
  for (const type of ["mousemove", "mousedown", "mouseup"]) {
    target.addEventListener(type, (event) => {
      const mouse = event as MouseEvent;
      seen.push({ type, x: mouse.clientX, y: mouse.clientY, button: mouse.button, buttons: mouse.buttons });
    });
  }
  target.addEventListener("wheel", (event) => {
    const wheel = event as WheelEvent;
    seen.push({ type: "wheel", x: wheel.clientX, y: wheel.clientY, button: 0, buttons: 0, deltaY: wheel.deltaY });
  });
});

afterEach(() => {
  view?.unmount();
  view = undefined;
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.replaceChildren();
});

describe("RemoteTouchLayer", () => {
  it("turns a direct tap into one move, press and release at the touched point, and asks for focus", () => {
    const { layer } = mountLayer("direct");
    const start = touch(layer, "touchstart", [{ x: 30, y: 40 }]);
    const end = touch(layer, "touchend", []);
    expect(start.defaultPrevented).toBe(true);
    expect(end.defaultPrevented).toBe(true);
    expect(onFocusRequest).toHaveBeenCalledTimes(1);
    expect(seen.map((entry) => `${entry.type}:${entry.button}:${entry.buttons}`)).toEqual(["mousemove:0:0", "mousedown:0:1", "mouseup:0:0"]);
    expect(seen.every((entry) => entry.x === 30 && entry.y === 40)).toBe(true);
  });

  it("does not fire the tap twice when the long-press timer would still be pending", () => {
    const { layer } = mountLayer("direct");
    touch(layer, "touchstart", [{ x: 5, y: 5 }]);
    touch(layer, "touchend", []);
    vi.advanceTimersByTime(LONG_PRESS_MS * 2);
    expect(seen.filter((entry) => entry.type === "mousedown")).toHaveLength(1);
    expect(seen.filter((entry) => entry.button === 2)).toHaveLength(0);
  });

  it("drags with the left button held in direct mode and releases it on lift", () => {
    const { layer } = mountLayer("direct");
    touch(layer, "touchstart", [{ x: 10, y: 10 }]);
    const move = touch(layer, "touchmove", [{ x: 80, y: 10 }]);
    expect(move.defaultPrevented).toBe(true);
    touch(layer, "touchend", []);
    expect(seen.map((entry) => `${entry.type}:${entry.buttons}`)).toEqual(["mousemove:0", "mousedown:1", "mousemove:1", "mouseup:0"]);
    expect(seen[2]).toMatchObject({ x: 80, y: 10 });
  });

  it("moves the cursor by the finger distance in trackpad mode and clicks at the cursor", () => {
    const { layer } = mountLayer("trackpad");
    const rect = layer.getBoundingClientRect();
    const cursor = { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 };
    touch(layer, "touchstart", [{ x: 100, y: 100 }]);
    touch(layer, "touchmove", [{ x: 130, y: 120 }]);
    touch(layer, "touchend", []);
    expect(seen).toHaveLength(1);
    expect(seen[0]).toMatchObject({ type: "mousemove", x: cursor.x + 30, y: cursor.y + 20 });
    touch(layer, "touchstart", [{ x: 400, y: 400 }]);
    touch(layer, "touchend", []);
    expect(seen.filter((entry) => entry.type === "mousedown")[0]).toMatchObject({ x: cursor.x + 30, y: cursor.y + 20 });
  });

  it("follows a mode switch without remounting", () => {
    const { layer, setMode } = mountLayer("direct");
    setMode("trackpad");
    touch(layer, "touchstart", [{ x: 400, y: 400 }]);
    touch(layer, "touchend", []);
    expect(seen.find((entry) => entry.type === "mousedown")).not.toMatchObject({ x: 400, y: 400 });
  });

  it("right-clicks on a long press and does not also left-click on release", () => {
    const { layer } = mountLayer("direct");
    touch(layer, "touchstart", [{ x: 7, y: 8 }]);
    act(() => {
      vi.advanceTimersByTime(LONG_PRESS_MS + 1);
    });
    touch(layer, "touchend", []);
    expect(mouseOnly().map((entry) => `${entry.type}:${entry.button}:${entry.buttons}`)).toEqual([
      "mousemove:0:0",
      "mousedown:2:2",
      "mouseup:2:0",
    ]);
  });

  it("scrolls the remote with two fingers moving together and does not click", () => {
    const { layer } = mountLayer("direct");
    touch(layer, "touchstart", [{ x: 10, y: 100 }, { x: 60, y: 100 }]);
    touch(layer, "touchmove", [{ x: 10, y: 70 }, { x: 60, y: 70 }]);
    touch(layer, "touchend", [{ x: 60, y: 70 }]);
    touch(layer, "touchend", []);
    expect(seen).toHaveLength(1);
    expect(seen[0]).toMatchObject({ type: "wheel", deltaY: 30 });
    expect(onPinch).not.toHaveBeenCalled();
  });

  it("reports a pinch and its pan to the pane instead of the remote", () => {
    const { layer } = mountLayer("direct");
    touch(layer, "touchstart", [{ x: 100, y: 100 }, { x: 150, y: 100 }]);
    touch(layer, "touchmove", [{ x: 80, y: 100 }, { x: 170, y: 100 }]);
    expect(onPinch).toHaveBeenCalledTimes(1);
    expect(onPinch.mock.calls[0][0]).toBeCloseTo(1.8);
    expect(onPan).toHaveBeenCalledWith(0, 0);
    expect(seen).toHaveLength(0);
  });

  it("ignores events when the remote canvas is not available", () => {
    const { layer } = mountLayer("direct", () => null);
    touch(layer, "touchstart", [{ x: 1, y: 1 }]);
    touch(layer, "touchend", []);
    expect(seen).toHaveLength(0);
  });

  it("releases a held drag on touchcancel", () => {
    const { layer } = mountLayer("direct");
    touch(layer, "touchstart", [{ x: 10, y: 10 }]);
    touch(layer, "touchmove", [{ x: 80, y: 10 }]);
    touch(layer, "touchcancel", []);
    expect(seen[seen.length - 1]).toMatchObject({ type: "mouseup", button: 0, buttons: 0 });
  });

  it("releases a held drag and stops listening when it unmounts", () => {
    const { layer } = mountLayer("direct");
    touch(layer, "touchstart", [{ x: 10, y: 10 }]);
    touch(layer, "touchmove", [{ x: 80, y: 10 }]);
    view!.unmount();
    view = undefined;
    expect(seen[seen.length - 1]).toMatchObject({ type: "mouseup" });
    const count = seen.length;
    touch(layer, "touchstart", [{ x: 1, y: 1 }]);
    touch(layer, "touchend", []);
    expect(seen).toHaveLength(count);
    expect(onFocusRequest).toHaveBeenCalledTimes(1);
  });

  it("does not leave a long-press timer behind after unmount", () => {
    const { layer } = mountLayer("direct");
    touch(layer, "touchstart", [{ x: 7, y: 8 }]);
    view!.unmount();
    view = undefined;
    expect(vi.getTimerCount()).toBe(0);
    vi.advanceTimersByTime(LONG_PRESS_MS * 2);
    expect(seen.filter((entry) => entry.button === 2)).toHaveLength(0);
  });

  it("suppresses the browser context menu over the remote screen", () => {
    const { layer } = mountLayer("direct");
    const event = new MouseEvent("contextmenu", { bubbles: true, cancelable: true });
    act(() => {
      layer.dispatchEvent(event);
    });
    expect(event.defaultPrevented).toBe(true);
  });
});
