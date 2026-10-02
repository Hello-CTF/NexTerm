/** @vitest-environment jsdom */

// 跟随滚动：用可编程的滚动尺寸（jsdom 无布局，全部显式指定）驱动真实 hook。
import { afterEach, describe, expect, it } from "vitest";
import { act, createElement } from "react";
import {
  FOLLOW_BOTTOM_THRESHOLD_PX,
  isNearBottom,
  useConversationFollow,
  type ConversationFollow,
} from "../../features/ai/conversationFollow";
import { click, mount, type MountedView } from "./reactTestUtils";

interface Harness {
  scroller: HTMLDivElement;
  follow: () => ConversationFollow;
  setRevision: (revision: number) => void;
  setMetrics: (metrics: { scrollTop?: number; scrollHeight?: number; clientHeight?: number }) => void;
  scrollCalls: () => number[];
  dispatchScroll: () => void;
}

function mountFollow(view: MountedView): Harness {
  let latest: ConversationFollow | null = null;
  let revision = 0;
  const scrollCalls: number[] = [];
  const metrics = { scrollTop: 0, scrollHeight: 0, clientHeight: 0 };

  function Probe() {
    const follow = useConversationFollow({ current: scroller }, revision);
    latest = follow;
    return createElement(
      "button",
      { id: "jump", onClick: follow.jumpToLatest, style: { display: follow.newOutput ? "block" : "none" } },
      "↓ 新输出",
    );
  }

  const scroller = document.createElement("div");
  view.container.append(scroller);
  for (const key of ["scrollTop", "scrollHeight", "clientHeight"] as const) {
    Object.defineProperty(scroller, key, {
      configurable: true,
      get: () => metrics[key],
      set: (value: number) => {
        metrics[key] = value;
      },
    });
  }
  scroller.scrollTo = ((options?: ScrollToOptions) => {
    const top = Number(options?.top ?? 0);
    scrollCalls.push(top);
    metrics.scrollTop = top;
  }) as typeof scroller.scrollTo;

  const render = () => {
    act(() => {
      view.root.render(createElement(Probe));
    });
  };
  render();
  return {
    scroller,
    follow: () => {
      if (!latest) throw new Error("follow not ready");
      return latest;
    },
    setRevision: (next) => {
      revision = next;
      render();
    },
    setMetrics: (patch) => {
      Object.assign(metrics, patch);
    },
    scrollCalls: () => scrollCalls,
    dispatchScroll: () => {
      act(() => {
        scroller.dispatchEvent(new Event("scroll"));
      });
    },
  };
}

describe("isNearBottom", () => {
  it("treats the threshold as inclusive and counts only the distance to the bottom", () => {
    const base = { scrollHeight: 1000, clientHeight: 200 };
    expect(isNearBottom({ ...base, scrollTop: 800 })).toBe(true);
    expect(isNearBottom({ ...base, scrollTop: 800 - FOLLOW_BOTTOM_THRESHOLD_PX })).toBe(true);
    expect(isNearBottom({ ...base, scrollTop: 800 - FOLLOW_BOTTOM_THRESHOLD_PX - 1 })).toBe(false);
    expect(isNearBottom({ ...base, scrollTop: 0 })).toBe(false);
  });
});

describe("useConversationFollow", () => {
  let view: MountedView | null = null;
  afterEach(() => {
    view?.unmount();
    view = null;
  });

  function setup() {
    view = mount(createElement("div"));
    return mountFollow(view);
  }

  it("auto-scrolls on new content only while following", () => {
    const h = setup();
    h.setMetrics({ scrollHeight: 1000, clientHeight: 200, scrollTop: 800 });
    h.setRevision(1);
    expect(h.scrollCalls().at(-1)).toBe(1000);
    const callsWhileFollowing = h.scrollCalls().length;
    // 用户上翻 ⇒ 停止跟随。
    h.setMetrics({ scrollTop: 100 });
    h.dispatchScroll();
    expect(h.follow().following).toBe(false);
    // 新内容到来：不再调用 scrollTo，位置保持，亮新输出入口。
    h.setMetrics({ scrollHeight: 1400 });
    h.setRevision(2);
    expect(h.scrollCalls().length).toBe(callsWhileFollowing);
    expect(h.follow().newOutput).toBe(true);
    expect((view!.container.querySelector("#jump") as HTMLElement).style.display).toBe("block");
  });

  it("jump-to-latest resumes following and hides the affordance", () => {
    const h = setup();
    h.setMetrics({ scrollHeight: 1000, clientHeight: 200, scrollTop: 800 });
    h.setRevision(1);
    h.setMetrics({ scrollTop: 100 });
    h.dispatchScroll();
    h.setMetrics({ scrollHeight: 1400 });
    h.setRevision(2);
    expect(h.follow().newOutput).toBe(true);
    click(view!.container.querySelector("#jump")!);
    expect(h.scrollCalls().at(-1)).toBe(1400);
    expect(h.follow().following).toBe(true);
    expect(h.follow().newOutput).toBe(false);
    // 恢复跟随后，新内容再次自动滚动。
    h.setMetrics({ scrollHeight: 1800 });
    h.setRevision(3);
    expect(h.scrollCalls().at(-1)).toBe(1800);
  });

  it("scrolling back to the bottom manually also resumes following", () => {
    const h = setup();
    h.setMetrics({ scrollHeight: 1000, clientHeight: 200, scrollTop: 800 });
    h.setRevision(1);
    h.setMetrics({ scrollTop: 100 });
    h.dispatchScroll();
    h.setMetrics({ scrollHeight: 1200 });
    h.setRevision(2);
    expect(h.follow().newOutput).toBe(true);
    h.setMetrics({ scrollTop: 1000 });
    h.dispatchScroll();
    expect(h.follow().following).toBe(true);
    expect(h.follow().newOutput).toBe(false);
    expect((view!.container.querySelector("#jump") as HTMLElement).style.display).toBe("none");
  });

  it("does not raise the affordance when nothing new arrived while scrolled up", () => {
    const h = setup();
    h.setMetrics({ scrollHeight: 1000, clientHeight: 200, scrollTop: 800 });
    h.setRevision(1);
    h.setMetrics({ scrollTop: 100 });
    h.dispatchScroll();
    // revision 变了但内容高度没变（例如同值覆盖）：不算新输出。
    h.setRevision(2);
    expect(h.follow().newOutput).toBe(false);
  });
});
