import { afterAll, afterEach, beforeEach, expect, vi } from "vitest";

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
