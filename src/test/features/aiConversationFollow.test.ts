/** @vitest-environment jsdom */

import { afterEach, describe, expect, it } from "vitest";
import { act, createElement } from "react";
import {
  FOLLOW_BOTTOM_THRESHOLD_PX,
  isNearBottom,
  useConversationFollow,
  type ConversationFollow,
} from "../../features/ai/conversationFollow";
import { click, mount, type MountedView } from "./reactTestUtils";

interface ScrollerHandle {
  el: HTMLDivElement;
  setMetrics: (metrics: { scrollTop?: number; scrollHeight?: number; clientHeight?: number }) => void;
  scrollCalls: () => number[];
  dispatchScroll: () => void;
}

interface Harness {
  follow: () => ConversationFollow;
  attach: () => ScrollerHandle;
  setRevision: (revision: number) => void;
  setVisible: (visible: boolean) => void;
}

function mountFollow(view: MountedView): Harness {
  let latest: ConversationFollow | null = null;
  let revision = 0;
  let visible = true;

  function Probe() {
    const follow = useConversationFollow(revision);
    latest = follow;
    if (!visible) return null;
    return createElement(
      "div",
      { "data-scroller": "1", ref: follow.scrollRef },
      createElement(
        "button",
        {
          id: "jump",
          onClick: follow.jumpToLatest,
          style: { display: follow.newOutput ? "block" : "none" },
        },
        "↓ 新输出",
      ),
    );
  }

  const render = () => {
    act(() => {
      view.root.render(createElement(Probe));
    });
  };
  render();

  return {
    follow: () => {
      if (!latest) throw new Error("follow not ready");
      return latest;
    },
    attach: () => {
      const el = view.container.querySelector("[data-scroller]") as HTMLDivElement | null;
      if (!el) throw new Error("scroller not mounted");
      const metrics = { scrollTop: 0, scrollHeight: 0, clientHeight: 0 };
      const scrollCalls: number[] = [];
      for (const key of ["scrollTop", "scrollHeight", "clientHeight"] as const) {
        Object.defineProperty(el, key, {
          configurable: true,
          get: () => metrics[key],
          set: (value: number) => {
            metrics[key] = value;
          },
        });
      }
      el.scrollTo = ((options?: ScrollToOptions) => {
        const top = Number(options?.top ?? 0);
        scrollCalls.push(top);
        metrics.scrollTop = top;
      }) as typeof el.scrollTo;
      return {
        el,
        setMetrics: (patch) => {
          Object.assign(metrics, patch);
        },
        scrollCalls: () => scrollCalls,
        dispatchScroll: () => {
          act(() => {
            el.dispatchEvent(new Event("scroll"));
          });
        },
      };
    },
    setRevision: (next) => {
      revision = next;
      render();
    },
    setVisible: (next) => {
      visible = next;
      render();
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
    const sc = h.attach();
    sc.setMetrics({ scrollHeight: 1000, clientHeight: 200, scrollTop: 800 });
    h.setRevision(1);
    expect(sc.scrollCalls().at(-1)).toBe(1000);
    const callsWhileFollowing = sc.scrollCalls().length;
    sc.setMetrics({ scrollTop: 100 });
    sc.dispatchScroll();
    expect(h.follow().following).toBe(false);
    sc.setMetrics({ scrollHeight: 1400 });
    h.setRevision(2);
    expect(sc.scrollCalls().length).toBe(callsWhileFollowing);
    expect(h.follow().newOutput).toBe(true);
    expect((view!.container.querySelector("#jump") as HTMLElement).style.display).toBe("block");
  });

  it("jump-to-latest resumes following and hides the affordance", () => {
    const h = setup();
    const sc = h.attach();
    sc.setMetrics({ scrollHeight: 1000, clientHeight: 200, scrollTop: 800 });
    h.setRevision(1);
    sc.setMetrics({ scrollTop: 100 });
    sc.dispatchScroll();
    sc.setMetrics({ scrollHeight: 1400 });
    h.setRevision(2);
    expect(h.follow().newOutput).toBe(true);
    click(view!.container.querySelector("#jump")!);
    expect(sc.scrollCalls().at(-1)).toBe(1400);
    expect(h.follow().following).toBe(true);
    expect(h.follow().newOutput).toBe(false);
    sc.setMetrics({ scrollHeight: 1800 });
    h.setRevision(3);
    expect(sc.scrollCalls().at(-1)).toBe(1800);
  });

  it("scrolling back to the bottom manually also resumes following", () => {
    const h = setup();
    const sc = h.attach();
    sc.setMetrics({ scrollHeight: 1000, clientHeight: 200, scrollTop: 800 });
    h.setRevision(1);
    sc.setMetrics({ scrollTop: 100 });
    sc.dispatchScroll();
    sc.setMetrics({ scrollHeight: 1200 });
    h.setRevision(2);
    expect(h.follow().newOutput).toBe(true);
    sc.setMetrics({ scrollTop: 1000 });
    sc.dispatchScroll();
    expect(h.follow().following).toBe(true);
    expect(h.follow().newOutput).toBe(false);
    expect((view!.container.querySelector("#jump") as HTMLElement).style.display).toBe("none");
  });

  it("does not raise the affordance when nothing new arrived while scrolled up", () => {
    const h = setup();
    const sc = h.attach();
    sc.setMetrics({ scrollHeight: 1000, clientHeight: 200, scrollTop: 800 });
    h.setRevision(1);
    sc.setMetrics({ scrollTop: 100 });
    sc.dispatchScroll();
    h.setRevision(2);
    expect(h.follow().newOutput).toBe(false);
  });

  it("reattaches the listener to a replacement element and honors scroll-up there", () => {
    const h = setup();
    const first = h.attach();
    first.setMetrics({ scrollHeight: 1000, clientHeight: 200, scrollTop: 800 });
    h.setRevision(1);
    expect(first.scrollCalls().at(-1)).toBe(1000);

    h.setVisible(false);
    h.setVisible(true);
    const second = h.attach();
    expect(second.el).not.toBe(first.el);

    second.setMetrics({ scrollHeight: 1000, clientHeight: 200, scrollTop: 800 });
    second.setMetrics({ scrollTop: 100 });
    second.dispatchScroll();
    expect(h.follow().following).toBe(false);
    second.setMetrics({ scrollHeight: 1400 });
    h.setRevision(2);
    expect(second.scrollCalls()).toEqual([]);
    expect(h.follow().newOutput).toBe(true);

    first.setMetrics({ scrollTop: 800 });
    first.dispatchScroll();
    expect(h.follow().following).toBe(false);
  });
});
