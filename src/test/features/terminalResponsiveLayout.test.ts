import { describe, expect, it } from "vitest";
import {
  clampSplitRatio,
  splitAllowedForHeight,
  splitRatioAt,
  splitRatioForKey,
  workspaceViewport,
  WORKSPACE_SPLIT_MIN_HEIGHT,
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

  it("allows split by default and reports the viewport height", () => {
    const layout = workspaceViewport(390, true);
    expect(layout.splitAllowed).toBe(true);
    expect(layout.height).toBe(Number.POSITIVE_INFINITY);
    expect(workspaceViewport(390, true, 844).height).toBe(844);
    expect(workspaceViewport(390, true, 844).splitAllowed).toBe(true);
    expect(workspaceViewport(390, true, 844).terminalKeys).toBe(true);
  });

  it("blocks new splits at insufficient viewport heights", () => {
    expect(WORKSPACE_SPLIT_MIN_HEIGHT).toBe(480);
    expect(splitAllowedForHeight(320)).toBe(false);
    expect(splitAllowedForHeight(400)).toBe(false);
    expect(splitAllowedForHeight(479)).toBe(false);
    expect(splitAllowedForHeight(480)).toBe(true);
    expect(splitAllowedForHeight(844)).toBe(true);
    expect(splitAllowedForHeight(Number.NaN)).toBe(true);
    expect(splitAllowedForHeight(Number.POSITIVE_INFINITY)).toBe(true);
    expect(workspaceViewport(568, false, 320).splitAllowed).toBe(false);
    expect(workspaceViewport(1280, false, 320).splitAllowed).toBe(false);
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
