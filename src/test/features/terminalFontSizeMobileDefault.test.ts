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

describe("移动端终端默认字号", () => {
  it("桌面 UA 保持内置默认 13", async () => {
    setUA(DESKTOP_UA);
    const prefs = await loadPrefs();
    expect(prefs.getAppearancePrefs().terminalFontSize).toBe(13);
  });

  it("移动端 UA 且未显式设置时默认为 12", async () => {
    setUA(IPHONE_UA);
    const prefs = await loadPrefs();
    expect(prefs.getAppearancePrefs().terminalFontSize).toBe(12);
  });

  it("移动端上用户显式设置的本地值仍然优先", async () => {
    setUA(IPHONE_UA);
    localStorage.setItem(KEY, JSON.stringify({ terminalFontSize: 18 }));
    const prefs = await loadPrefs();
    expect(prefs.getAppearancePrefs().terminalFontSize).toBe(18);
  });

  it("移动端上恢复默认后回到 12", async () => {
    setUA(IPHONE_UA);
    localStorage.setItem(KEY, JSON.stringify({ terminalFontSize: 18 }));
    const prefs = await loadPrefs();
    prefs.resetAppearancePrefs();
    expect(prefs.getAppearancePrefs().terminalFontSize).toBe(12);
    expect(localStorage.getItem(KEY)).toBeNull();
  });
});
