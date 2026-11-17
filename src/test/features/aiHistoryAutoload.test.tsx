/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, createElement, useCallback, useRef, useState } from "react";
import { mount, type MountedView } from "./reactTestUtils";
import { useHistoryAutoload, HISTORY_AUTOLOAD_TOP_PX } from "../../features/ai/historyAutoload";

const ITEM_HEIGHT = 20;
const PAGE = 5;

let view: MountedView | undefined;
let restoreRect: (() => void) | undefined;

afterEach(() => {
  view?.unmount();
  view = undefined;
  restoreRect?.();
  restoreRect = undefined;
});

interface Harness {
  log: HTMLElement;
  scroll: (top: number) => void;
  loadOlder: ReturnType<typeof vi.fn>;
  anchorIndex: ReturnType<typeof vi.fn>;
  acknowledgeGrowth: ReturnType<typeof vi.fn>;
  holdPosition: ReturnType<typeof vi.fn>;
  scrollTop: () => number;
}

// The harness lays items out at ITEM_HEIGHT each from the top of the list; each load prepends PAGE items.
function mountHarness(initialStart: number, initialCount: number): Harness {
  let scrollTop = 0;
  const loadOlder = vi.fn();
  const anchorIndex = vi.fn();
  const acknowledgeGrowth = vi.fn();
  const holdPosition = vi.fn();
  let log: HTMLElement | null = null;

  const original = HTMLElement.prototype.getBoundingClientRect;
  HTMLElement.prototype.getBoundingClientRect = function (this: HTMLElement) {
    const index = this.dataset.conversationIndex;
    const top = index === undefined ? 0 : Number(index) * ITEM_HEIGHT - scrollTop;
    const height = index === undefined ? 100 : ITEM_HEIGHT;
    return { top, bottom: top + height, height, left: 0, right: 100, width: 100, x: 0, y: top, toJSON: () => ({}) } as DOMRect;
  };
  restoreRect = () => {
    HTMLElement.prototype.getBoundingClientRect = original;
  };

  function List() {
    const [state, setState] = useState({ start: initialStart, count: initialCount });
    const autoload = useHistoryAutoload({
      windowStart: state.start,
      itemCount: state.count,
      loadOlder: () => {
        loadOlder();
        if (state.start <= 0) return false;
        setState({ start: Math.max(0, state.start - PAGE), count: state.count + PAGE });
        return true;
      },
      holdPosition,
      anchorIndex,
      acknowledgeGrowth,
    });
    const countRef = useRef(state.count);
    countRef.current = state.count;
    const { scrollRef } = autoload;
    const ref = useCallback(
      (node: HTMLElement | null) => {
        log = node;
        if (node) {
          Object.defineProperty(node, "scrollTop", {
            configurable: true,
            get: () => scrollTop,
            set: (value: number) => {
              scrollTop = value;
            },
          });
          Object.defineProperty(node, "scrollHeight", { configurable: true, get: () => countRef.current * ITEM_HEIGHT });
          Object.defineProperty(node, "clientHeight", { configurable: true, get: () => 100 });
        }
        scrollRef(node);
      },
      [scrollRef],
    );
    return createElement(
      "div",
      { role: "log", ref },
      Array.from({ length: state.count }, (_, index) =>
        createElement("div", { key: index, "data-conversation-index": index }, `item ${index}`),
      ),
    );
  }

  view = mount(createElement(List));
  return {
    log: log!,
    scroll: (top: number) => {
      scrollTop = top;
      log!.dispatchEvent(new Event("scroll"));
    },
    loadOlder,
    anchorIndex,
    acknowledgeGrowth,
    holdPosition,
    scrollTop: () => scrollTop,
  };
}

describe("history autoload", () => {
  it("loads the previous page near the top and keeps the first visible item in place", () => {
    const harness = mountHarness(10, 20);

    act(() => harness.scroll(200));
    expect(harness.loadOlder).not.toHaveBeenCalled();

    // Item 1 is the first visible item, 10px above the top edge.
    act(() => harness.scroll(HISTORY_AUTOLOAD_TOP_PX - 18));
    expect(harness.loadOlder).toHaveBeenCalledTimes(1);
    expect(harness.holdPosition).toHaveBeenCalled();
    // It is now item 6: 120px down, so the view moves by the five prepended items.
    expect(harness.scrollTop()).toBe(HISTORY_AUTOLOAD_TOP_PX - 18 + PAGE * ITEM_HEIGHT);
    expect(harness.anchorIndex).not.toHaveBeenCalled();
    expect(harness.acknowledgeGrowth).toHaveBeenCalled();
    expect(harness.log.querySelectorAll("[data-conversation-index]")).toHaveLength(25);
  });

  it("loads one page per render and stops when nothing older remains", () => {
    const harness = mountHarness(5, 20);

    act(() => {
      harness.scroll(0);
      harness.scroll(0);
    });
    expect(harness.loadOlder).toHaveBeenCalledTimes(1);
    expect(harness.log.querySelectorAll("[data-conversation-index]")).toHaveLength(25);

    act(() => harness.scroll(0));
    expect(harness.loadOlder).toHaveBeenCalledTimes(1);
  });

  it("does not load when the list does not scroll", () => {
    const harness = mountHarness(5, 3);
    act(() => harness.scroll(0));
    expect(harness.loadOlder).not.toHaveBeenCalled();
  });
});
