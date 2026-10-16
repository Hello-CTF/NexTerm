import { describe, expect, it } from "vitest";
import {
  VIRTUAL_ITEM_ESTIMATE_PX,
  VIRTUAL_ITEM_GAP_PX,
  VIRTUAL_OVERSCAN,
  VIRTUAL_THRESHOLD,
  centeredScrollTop,
  windowRange,
} from "../features/ai/conversationVirtual";

describe("windowRange", () => {
  it("renders every item below the threshold without spacers", () => {
    expect(windowRange(VIRTUAL_THRESHOLD, [], 0)).toEqual({
      active: false,
      start: 0,
      end: VIRTUAL_THRESHOLD,
      padTop: 0,
      padBottom: 0,
    });
  });

  it("derives spacers from the measured offsets of the rendered window", () => {
    const items = [
      { index: 40, start: 5000, end: 5300 },
      { index: 41, start: 5310, end: 5340 },
      { index: 42, start: 5350, end: 5450 },
    ];
    expect(windowRange(200, items, 40000)).toEqual({
      active: true,
      start: 40,
      end: 43,
      padTop: 5000,
      padBottom: 40000 - 5450,
    });
  });

  it("covers both list ends without negative or leftover padding", () => {
    const top = windowRange(200, [{ index: 0, start: 0, end: 88 }], 20000);
    expect(top.padTop).toBe(0);
    const bottom = windowRange(200, [{ index: 199, start: 19900, end: 20000 }], 20000);
    expect(bottom.end).toBe(200);
    expect(bottom.padBottom).toBe(0);
  });

  it("clamps padBottom when the window overshoots the total size", () => {
    const items = [{ index: 99, start: 9000, end: 12000 }];
    expect(windowRange(100, items, 11000).padBottom).toBe(0);
  });

  it("falls back to an estimated head window until the virtualizer reports items", () => {
    const count = VIRTUAL_THRESHOLD + 50;
    const end = Math.min(count, VIRTUAL_OVERSCAN * 2);
    expect(windowRange(count, [], 0)).toEqual({
      active: true,
      start: 0,
      end,
      padTop: 0,
      padBottom: (count - end) * (VIRTUAL_ITEM_ESTIMATE_PX + VIRTUAL_ITEM_GAP_PX),
    });
  });
});

describe("centeredScrollTop", () => {
  it("centers items of any real height on measured geometry instead of the estimate", () => {
    expect(centeredScrollTop(100, 500, 400, 1000, 30)).toBe(500 + 900 - 185);
    expect(centeredScrollTop(100, 500, 400, 1000, 300)).toBe(500 + 900 - 50);
  });

  it("clamps at zero and tolerates zero-height viewports", () => {
    expect(centeredScrollTop(0, 0, 400, 10, 30)).toBe(0);
    expect(centeredScrollTop(0, 0, 0, 10, 30)).toBe(25);
  });
});
