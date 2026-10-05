import { describe, expect, it } from "vitest";
import {
  VIRTUAL_ITEM_ESTIMATE_PX,
  VIRTUAL_OVERSCAN,
  VIRTUAL_THRESHOLD,
  virtualOffsetForIndex,
  virtualRange,
} from "../features/ai/conversationVirtual";

describe("virtualRange", () => {
  it("renders every item below the threshold without spacers", () => {
    const range = virtualRange(VIRTUAL_THRESHOLD, 0, 400);
    expect(range).toEqual({
      active: false,
      start: 0,
      end: VIRTUAL_THRESHOLD,
      padTop: 0,
      padBottom: 0,
    });
  });

  it("windows long conversations around the scroll position", () => {
    const scrollTop = 100 * VIRTUAL_ITEM_ESTIMATE_PX;
    const range = virtualRange(1000, scrollTop, 400);
    const visible = Math.ceil(400 / VIRTUAL_ITEM_ESTIMATE_PX) + VIRTUAL_OVERSCAN * 2;
    expect(range.active).toBe(true);
    expect(range.start).toBe(100 - VIRTUAL_OVERSCAN);
    expect(range.end).toBe(100 - VIRTUAL_OVERSCAN + visible);
    expect(range.padTop).toBe((100 - VIRTUAL_OVERSCAN) * VIRTUAL_ITEM_ESTIMATE_PX);
    expect(range.padBottom).toBe(
      (1000 - (100 - VIRTUAL_OVERSCAN + visible)) * VIRTUAL_ITEM_ESTIMATE_PX,
    );
  });

  it("clamps the window at both ends", () => {
    const top = virtualRange(500, 0, 300);
    expect(top.start).toBe(0);
    expect(top.padTop).toBe(0);
    const bottom = virtualRange(500, 500 * VIRTUAL_ITEM_ESTIMATE_PX, 300);
    expect(bottom.end).toBe(500);
    expect(bottom.padBottom).toBe(0);
  });

  it("tolerates zero and negative metrics", () => {
    const range = virtualRange(100, -50, 0);
    expect(range.start).toBe(0);
    expect(range.end).toBe(VIRTUAL_OVERSCAN * 2);
  });
});

describe("virtualOffsetForIndex", () => {
  it("maps an item index to its estimated scroll offset", () => {
    expect(virtualOffsetForIndex(10)).toBe(10 * VIRTUAL_ITEM_ESTIMATE_PX);
    expect(virtualOffsetForIndex(-3)).toBe(0);
  });
});
