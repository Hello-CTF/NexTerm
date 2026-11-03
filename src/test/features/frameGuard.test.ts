/** @vitest-environment jsdom */

import { describe, expect, it, vi } from "vitest";
import { isFramed, startUnlessFramed } from "../../app/frameGuard";

describe("isFramed", () => {
  it("is false when the window is its own top", () => {
    const win = {} as Window;
    Object.assign(win, { self: win, top: win });
    expect(isFramed(win)).toBe(false);
  });

  it("is true when top is another window", () => {
    const win = { self: {}, top: {} } as unknown as Window;
    expect(isFramed(win)).toBe(true);
  });

  it("is true when reading top throws", () => {
    const win = { self: {} } as unknown as Window;
    Object.defineProperty(win, "top", {
      get() {
        throw new Error("SecurityError");
      },
    });
    expect(isFramed(win)).toBe(true);
  });
});

describe("startUnlessFramed", () => {
  it("starts the app inside a frame outside the desktop transport", () => {
    const start = vi.fn();
    const win = { self: {}, top: {} } as unknown as Window;
    const body = document.createElement("body");
    startUnlessFramed(win, body, start, { desktop: false });
    expect(start).toHaveBeenCalledTimes(1);
    expect(body.textContent).toBe("");
  });

  it("does not start the desktop app in a frame and renders a static notice", () => {
    const start = vi.fn();
    const win = { self: {}, top: {} } as unknown as Window;
    const body = document.createElement("body");
    startUnlessFramed(win, body, start, { desktop: true });
    expect(start).not.toHaveBeenCalled();
    expect(body.textContent).toContain("NexTerm 不能在框架中打开");
  });

  it("starts the app at top level", () => {
    const start = vi.fn();
    const win = {} as Window;
    Object.assign(win, { self: win, top: win });
    const body = document.createElement("body");
    startUnlessFramed(win, body, start, { desktop: true });
    expect(start).toHaveBeenCalledTimes(1);
    expect(body.textContent).toBe("");
  });
});
