/** @vitest-environment jsdom */

import { afterEach, describe, expect, it } from "vitest";
import { act, createElement } from "react";
import { mount, type MountedView } from "./features/reactTestUtils";
import { isLandscapeFullScreen, useLandscapeFullScreen } from "../app/platform";

function stubViewport(coarse: boolean, width: number, height: number): void {
  window.matchMedia = ((query: string) => ({
    matches: coarse && query === "(pointer: coarse)",
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
  Object.defineProperty(window, "innerWidth", { configurable: true, value: width });
  Object.defineProperty(window, "innerHeight", { configurable: true, value: height });
}

describe("isLandscapeFullScreen", () => {
  it("phone landscape with a short side under 500px qualifies", () => {
    stubViewport(true, 844, 390);
    expect(isLandscapeFullScreen()).toBe(true);
  });

  it("phone portrait does not qualify", () => {
    stubViewport(true, 390, 844);
    expect(isLandscapeFullScreen()).toBe(false);
  });

  it("tablets with a short side of 500px or more never qualify", () => {
    stubViewport(true, 1024, 768);
    expect(isLandscapeFullScreen()).toBe(false);
  });

  it("fine pointers never qualify", () => {
    stubViewport(false, 844, 390);
    expect(isLandscapeFullScreen()).toBe(false);
  });
});

describe("useLandscapeFullScreen", () => {
  let view: MountedView | null = null;
  afterEach(() => {
    view?.unmount();
    view = null;
  });

  it("tracks rotation live via resize events", () => {
    stubViewport(true, 844, 390);
    function Probe() {
      return createElement("span", { "data-full": String(useLandscapeFullScreen()) });
    }
    view = mount(createElement(Probe));
    expect(view.container.querySelector("[data-full='true']")).not.toBeNull();

    stubViewport(true, 390, 844);
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });
    expect(view.container.querySelector("[data-full='false']")).not.toBeNull();

    stubViewport(true, 844, 390);
    act(() => {
      window.dispatchEvent(new Event("orientationchange"));
    });
    expect(view.container.querySelector("[data-full='true']")).not.toBeNull();
  });
});
