/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const KEY = "nexterm.appearance.v1";
const DESKTOP_UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36";
const IPHONE_UA =
  "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1";
const ORIGINAL_UA = window.navigator.userAgent;

function setUA(ua: string): void {
  Object.defineProperty(window.navigator, "userAgent", { value: ua, configurable: true });
}

async function loadPrefs() {
  vi.resetModules();
  return import("../../app/preferences");
}

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  setUA(ORIGINAL_UA);
});

describe("default terminal font size on mobile", () => {
  it("keeps the built-in default of 13 for desktop user agents", async () => {
    setUA(DESKTOP_UA);
    const prefs = await loadPrefs();
    expect(prefs.getAppearancePrefs().terminalFontSize).toBe(13);
  });

  it("defaults to 12 for mobile user agents without an explicit setting", async () => {
    setUA(IPHONE_UA);
    const prefs = await loadPrefs();
    expect(prefs.getAppearancePrefs().terminalFontSize).toBe(12);
  });

  it("prefers an explicitly configured local value on mobile", async () => {
    setUA(IPHONE_UA);
    localStorage.setItem(KEY, JSON.stringify({ terminalFontSize: 18 }));
    const prefs = await loadPrefs();
    expect(prefs.getAppearancePrefs().terminalFontSize).toBe(18);
  });

  it("returns to 12 after resetting preferences on mobile", async () => {
    setUA(IPHONE_UA);
    localStorage.setItem(KEY, JSON.stringify({ terminalFontSize: 18 }));
    const prefs = await loadPrefs();
    prefs.resetAppearancePrefs();
    expect(prefs.getAppearancePrefs().terminalFontSize).toBe(12);
    expect(localStorage.getItem(KEY)).toBeNull();
  });
});
