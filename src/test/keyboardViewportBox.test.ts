/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import { keyboardViewportBox, visualViewportInset } from "../app/App";

describe("keyboardViewportBox", () => {
  it("returns null when no keyboard overlays the page", () => {
    expect(keyboardViewportBox(844, 0, 0, false)).toBeNull();
    expect(keyboardViewportBox(Number.NaN, 300, 0, false)).toBeNull();
  });

  it("matches the visual viewport exactly for visualViewport readings", () => {
    const vvHeight = 500;
    const offsetTop = 0;
    const inset = visualViewportInset(844, vvHeight, offsetTop);
    expect(keyboardViewportBox(844, inset, offsetTop, false)).toEqual({ top: 0, height: vvHeight });
  });

  it("follows the visual viewport when the page is scrolled", () => {
    const inset = visualViewportInset(844, 480, 24);
    expect(keyboardViewportBox(844, inset, 24, false)).toEqual({ top: 24, height: 480 });
  });

  it("ignores the visual viewport offset for VirtualKeyboard geometry", () => {
    expect(keyboardViewportBox(844, 344, 30, true)).toEqual({ top: 0, height: 500 });
  });

  it("never returns an empty or negative box", () => {
    expect(keyboardViewportBox(844, 844, 0, true)).toBeNull();
    expect(keyboardViewportBox(844, 400, Number.NaN, false)).toEqual({ top: 0, height: 444 });
  });
});

import { defaultRightOpen } from "../app/store";

describe("defaultRightOpen", () => {
  it("opens the AI dock by default on desktop and wide touch screens", () => {
    expect(defaultRightOpen(1280, false)).toBe(true);
    expect(defaultRightOpen(900, false)).toBe(true);
    expect(defaultRightOpen(1366, true)).toBe(true);
  });

  it("keeps it closed on first run for touch screens narrower than 1280px", () => {
    expect(defaultRightOpen(1180, true)).toBe(false);
    expect(defaultRightOpen(820, true)).toBe(false);
    expect(defaultRightOpen(390, true)).toBe(false);
  });
});
