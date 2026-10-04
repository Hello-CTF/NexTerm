/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import { virtualKeyboardInset, visualViewportInset } from "../../app/App";

describe("mobile keyboard inset policy (resizes-visual baseline)", () => {
  it("computes occlusion from visual viewport shrink", () => {
    expect(visualViewportInset(844, 844, 0)).toBe(0);
    expect(visualViewportInset(844, 500, 0)).toBe(344);
    expect(visualViewportInset(844, 500, 20)).toBe(324);
    expect(visualViewportInset(844, 900, 0)).toBe(0);
  });

  it("tolerates non-finite viewport readings", () => {
    expect(visualViewportInset(Number.NaN, 500, 0)).toBe(0);
    expect(visualViewportInset(844, Number.NaN, 0)).toBe(0);
    expect(visualViewportInset(844, 500, Number.NaN)).toBe(344);
    expect(visualViewportInset(Number.POSITIVE_INFINITY, 500, 0)).toBe(0);
  });

  it("computes occlusion from VirtualKeyboard geometry when available", () => {
    expect(virtualKeyboardInset(844, 500, 344)).toBe(344);
    expect(virtualKeyboardInset(844, 844, 0)).toBe(0);
    expect(virtualKeyboardInset(844, 0, 0)).toBe(0);
    expect(virtualKeyboardInset(844, 900, 100)).toBe(0);
    expect(virtualKeyboardInset(844, -40, 344)).toBe(844);
    expect(virtualKeyboardInset(844, 500, Number.NaN)).toBe(0);
    expect(virtualKeyboardInset(Number.NaN, 500, 344)).toBe(0);
  });
});
