import { afterAll, afterEach, beforeEach, expect, vi } from "vitest";

// jsdom has no canvas: xterm's DOM renderer measures character widths through a 2D context when a terminal opens, so a
// measuring-only context stands in for the one jsdom cannot provide.
if (typeof HTMLCanvasElement !== "undefined") {
  const getContext = HTMLCanvasElement.prototype.getContext;
  HTMLCanvasElement.prototype.getContext = function (this: HTMLCanvasElement, type: string, ...args: unknown[]) {
    if (type !== "2d") return Reflect.apply(getContext, this, [type, ...args]) as RenderingContext | null;
    return { font: "", measureText: (text: string) => ({ width: text.length * 8 }) } as unknown as CanvasRenderingContext2D;
  } as typeof getContext;
}

// @wailsio/runtime arms a 50ms setInterval at import time (drag.js polling for its environment) and never clears it
// unless it fires. A test file that finishes sooner lets the tick run after the jsdom window is gone and throw
// "window is not defined" as an unhandled error. Track the intervals a file creates and clear them at file teardown.
const openIntervals = new Set<ReturnType<typeof setInterval>>();
if (typeof window !== "undefined") {
  const realSetInterval = globalThis.setInterval;
  globalThis.setInterval = ((...args: Parameters<typeof setInterval>) => {
    const id = realSetInterval(...args);
    openIntervals.add(id);
    return id;
  }) as typeof setInterval;
}

const reactWarnings: string[] = [];
const REACT_WARNING = /not wrapped in act|overlapping act|unmounted component|Can't perform a React state update|testing environment is not configured to support act/;
const originalConsoleError = console.error;

console.error = (...args: unknown[]) => {
  const text = args.map(String).join(" ");
  if (REACT_WARNING.test(text)) reactWarnings.push(text);
  originalConsoleError(...args);
};

beforeEach(() => {
  reactWarnings.length = 0;
});

afterEach(() => {
  if (vi.isFakeTimers()) vi.clearAllTimers();
  vi.useRealTimers();
  expect(reactWarnings, "React act/unmounted-update warnings").toEqual([]);
});

afterAll(() => {
  for (const id of openIntervals) clearInterval(id);
  openIntervals.clear();
  console.error = originalConsoleError;
  expect(reactWarnings, "React act/unmounted-update warnings during teardown").toEqual([]);
});

export function installMatchMediaStub(): void {
  if (typeof window === "undefined") return;
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
}

installMatchMediaStub();
