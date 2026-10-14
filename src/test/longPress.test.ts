/** @vitest-environment jsdom */

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { installLongPressContextMenu } from "../app/longPress";

function touch(type: string, x: number, y: number, target: Element, count = 1): void {
  const event = new Event(type, { bubbles: true, cancelable: true });
  const point = { clientX: x, clientY: y };
  const points = Array.from({ length: count }, () => point);
  Object.assign(event, { touches: points, changedTouches: points });
  target.dispatchEvent(event);
}

describe("long press to context menu", () => {
  let host: HTMLDivElement | null = null;

  beforeAll(() => {
    vi.useFakeTimers();
    installLongPressContextMenu();
  });

  afterEach(() => {
    vi.clearAllTimers();
    host?.remove();
    host = null;
  });

  function mount(): { el: HTMLDivElement; menus: MouseEvent[] } {
    host = document.createElement("div");
    document.body.append(host);
    const menus: MouseEvent[] = [];
    host.addEventListener("contextmenu", (event) => menus.push(event as MouseEvent));
    return { el: host, menus };
  }

  function dispatchClick(el: Element): MouseEvent {
    const click = new MouseEvent("click", { bubbles: true, cancelable: true });
    el.dispatchEvent(click);
    return click;
  }

  it("dispatches contextmenu at the initial coordinates after a 500ms single-touch hold", () => {
    const { el, menus } = mount();
    touch("touchstart", 100, 200, el);
    vi.advanceTimersByTime(499);
    expect(menus).toHaveLength(0);
    vi.advanceTimersByTime(1);
    expect(menus).toHaveLength(1);
    expect(menus[0].clientX).toBe(100);
    expect(menus[0].clientY).toBe(200);
    expect(menus[0].button).toBe(2);
    dispatchClick(el);
  });

  it("does not dispatch when released before 500ms", () => {
    const { el, menus } = mount();
    touch("touchstart", 100, 200, el);
    vi.advanceTimersByTime(300);
    touch("touchend", 100, 200, el);
    vi.advanceTimersByTime(1000);
    expect(menus).toHaveLength(0);
  });

  it("cancels after movement beyond 12px but still triggers within tolerance", () => {
    const { el, menus } = mount();
    touch("touchstart", 100, 200, el);
    touch("touchmove", 113, 200, el);
    vi.advanceTimersByTime(1000);
    expect(menus).toHaveLength(0);

    touch("touchstart", 100, 200, el);
    touch("touchmove", 112, 211, el);
    vi.advanceTimersByTime(500);
    expect(menus).toHaveLength(1);
    dispatchClick(el);
  });

  it("does not start the long-press timer for multi-touch", () => {
    const { el, menus } = mount();
    touch("touchstart", 100, 200, el, 2);
    vi.advanceTimersByTime(1000);
    expect(menus).toHaveLength(0);
  });

  it("does not override xterm's built-in text selection on long press", () => {
    const { el, menus } = mount();
    const term = document.createElement("div");
    term.className = "xterm";
    const inner = document.createElement("div");
    term.append(inner);
    el.append(term);
    touch("touchstart", 100, 200, inner);
    vi.advanceTimersByTime(1000);
    expect(menus).toHaveLength(0);
  });

  it("swallows the release click after opening the menu", () => {
    const { el, menus } = mount();
    const clicks: MouseEvent[] = [];
    el.addEventListener("click", (event) => clicks.push(event as MouseEvent));
    touch("touchstart", 100, 200, el);
    vi.advanceTimersByTime(500);
    expect(menus).toHaveLength(1);
    const click = dispatchClick(el);
    expect(clicks).toHaveLength(0);
    expect(click.defaultPrevented).toBe(true);
  });

  it("allows click through when no menu opens", () => {
    const { el } = mount();
    const clicks: MouseEvent[] = [];
    el.addEventListener("click", (event) => clicks.push(event as MouseEvent));
    const click = dispatchClick(el);
    expect(clicks).toHaveLength(1);
    expect(click.defaultPrevented).toBe(false);
  });

  it("does not dispatch if the element leaves the document before the timer expires", () => {
    const { el, menus } = mount();
    touch("touchstart", 100, 200, el);
    el.remove();
    vi.advanceTimersByTime(1000);
    expect(menus).toHaveLength(0);
  });
});
