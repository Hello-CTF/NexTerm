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

// Timers a test file leaves armed can fire after its jsdom environment is torn down and throw "window/document is
// not defined" as unhandled errors (e.g. @wailsio/runtime's drag.js environment poll, a debounced awareness scan).
// Track the timers each file creates and clear them at file teardown.
const openTimers = new Set<ReturnType<typeof setTimeout>>();
if (typeof window !== "undefined") {
  const realSetTimeout = globalThis.setTimeout;
  const realSetInterval = globalThis.setInterval;
  globalThis.setTimeout = ((...args: Parameters<typeof setTimeout>) => {
    const id = realSetTimeout(...args);
    openTimers.add(id);
    return id;
  }) as typeof setTimeout;
  globalThis.setInterval = ((...args: Parameters<typeof setInterval>) => {
    const id = realSetInterval(...args);
    openTimers.add(id);
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
  for (const id of openTimers) clearTimeout(id);
  openTimers.clear();
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
