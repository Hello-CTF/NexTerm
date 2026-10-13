/** @vitest-environment jsdom */

import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { isLandscapeFullScreen } from "../app/platform";

const realMatchMedia = window.matchMedia;
let coarse = false;

beforeAll(() => {
  window.matchMedia = ((query: string) => ({
    matches: query === "(pointer: coarse)" && coarse,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia;
});

afterAll(() => {
  window.matchMedia = realMatchMedia;
  setViewport(1024, 768);
});

function setViewport(width: number, height: number): void {
  Object.defineProperty(window, "innerWidth", { value: width, configurable: true, writable: true });
  Object.defineProperty(window, "innerHeight", { value: height, configurable: true, writable: true });
}

describe("isLandscapeFullScreen", () => {
  it("粗指针横屏且短边小于 500 判定为横屏全屏", () => {
    coarse = true;
    setViewport(800, 360);
    expect(isLandscapeFullScreen()).toBe(true);
  });

  it("粗指针竖屏不判定", () => {
    coarse = true;
    setViewport(360, 800);
    expect(isLandscapeFullScreen()).toBe(false);
  });

  it("粗指针横屏但短边不小于 500 不判定", () => {
    coarse = true;
    setViewport(1024, 600);
    expect(isLandscapeFullScreen()).toBe(false);
  });

  it("非粗指针一律不判定", () => {
    coarse = false;
    setViewport(800, 360);
    expect(isLandscapeFullScreen()).toBe(false);
  });
});
