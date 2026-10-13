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

describe("长按转上下文菜单", () => {
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

  it("单指按住 500ms 在起始坐标派发 contextmenu", () => {
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

  it("500ms 前抬手不派发", () => {
    const { el, menus } = mount();
    touch("touchstart", 100, 200, el);
    vi.advanceTimersByTime(300);
    touch("touchend", 100, 200, el);
    vi.advanceTimersByTime(1000);
    expect(menus).toHaveLength(0);
  });

  it("移动超过 12px 取消, 容差内仍触发", () => {
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

  it("多指触摸不启动长按计时", () => {
    const { el, menus } = mount();
    touch("touchstart", 100, 200, el, 2);
    vi.advanceTimersByTime(1000);
    expect(menus).toHaveLength(0);
  });

  it("xterm 区域长按不抢占自带的文本选择", () => {
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

  it("菜单弹出后吞掉抬手的那次 click", () => {
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

  it("未弹出菜单时 click 正常通过", () => {
    const { el } = mount();
    const clicks: MouseEvent[] = [];
    el.addEventListener("click", (event) => clicks.push(event as MouseEvent));
    const click = dispatchClick(el);
    expect(clicks).toHaveLength(1);
    expect(click.defaultPrevented).toBe(false);
  });

  it("计时到期前元素脱离文档不派发", () => {
    const { el, menus } = mount();
    touch("touchstart", 100, 200, el);
    el.remove();
    vi.advanceTimersByTime(1000);
    expect(menus).toHaveLength(0);
  });
});
