import { afterAll, vi } from "vitest";

afterAll(async () => {
  if (typeof window === "undefined") return;
  if (vi.isFakeTimers()) {
    await vi.advanceTimersByTimeAsync(75);
    return;
  }
  await new Promise<void>((resolve) => {
    setTimeout(resolve, 75);
  });
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
