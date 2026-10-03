import { describe, expect, it } from "vitest";
import {
  clampSplitRatio,
  splitRatioAt,
  splitRatioForKey,
  workspaceViewport,
} from "../../features/terminal/workspaceLayout";

describe("responsive workspace layout", () => {
  it.each([
    { width: 360, overlay: true, compact: true },
    { width: 560, overlay: true, compact: true },
    { width: 820, overlay: true, compact: false },
    { width: 1440, overlay: false, compact: false },
  ])("derives sidebar and density policy at $width px", ({ width, overlay, compact }) => {
    const layout = workspaceViewport(width, false);
    expect(layout.overlaySidebars).toBe(overlay);
    expect(layout.compact).toBe(compact);
    expect(layout.terminalKeys).toBe(compact);
  });

  it("enables terminal keys for coarse pointers at any width", () => {
    expect(workspaceViewport(1440, true).terminalKeys).toBe(true);
    expect(workspaceViewport(1440, true).overlaySidebars).toBe(false);
  });

  it("keeps split ratios usable for arbitrary positive container heights", () => {
    for (const height of [90, 240, 731, 1200]) {
      expect(splitRatioAt(height / 2, { top: 0, height }, 0.4)).toBe(0.5);
      expect(splitRatioAt(-1000, { top: 0, height }, 0.4)).toBe(0.15);
      expect(splitRatioAt(10000, { top: 0, height }, 0.4)).toBe(0.85);
    }
    expect(splitRatioAt(20, { top: 0, height: 0 }, 0.4)).toBe(0.4);
    expect(clampSplitRatio(Number.NaN, 0.4)).toBe(0.4);
  });

  it("supports deterministic keyboard resizing and reset", () => {
    expect(splitRatioForKey(0.5, "ArrowUp")).toBeCloseTo(0.48);
    expect(splitRatioForKey(0.5, "ArrowDown", true)).toBeCloseTo(0.6);
    expect(splitRatioForKey(0.5, "Home")).toBe(0.15);
    expect(splitRatioForKey(0.5, "End")).toBe(0.85);
    expect(splitRatioForKey(0.7, "Enter")).toBe(0.5);
    expect(splitRatioForKey(0.5, "a")).toBeNull();
  });
});
