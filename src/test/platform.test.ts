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
  it("matches coarse-pointer landscape viewports whose short edge is below 500", () => {
    coarse = true;
    setViewport(800, 360);
    expect(isLandscapeFullScreen()).toBe(true);
  });

  it("does not match coarse-pointer portrait viewports", () => {
    coarse = true;
    setViewport(360, 800);
    expect(isLandscapeFullScreen()).toBe(false);
  });

  it("does not match coarse-pointer landscape viewports whose short edge is at least 500", () => {
    coarse = true;
    setViewport(1024, 600);
    expect(isLandscapeFullScreen()).toBe(false);
  });

  it("never matches non-coarse pointers", () => {
    coarse = false;
    setViewport(800, 360);
    expect(isLandscapeFullScreen()).toBe(false);
  });
});
