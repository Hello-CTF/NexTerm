/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it, vi } from "vitest";

const KEY = "nexterm.appearance.v1";

async function loadPrefs() {
  vi.resetModules();
  const theme = await import("../app/theme");
  const prefs = await import("../app/preferences");
  return { theme, prefs };
}

function rootStyle(name: string): string {
  return document.documentElement.style.getPropertyValue(name);
}

function textFactor(): number {
  return Number(rootStyle("--nx-ui-text-factor"));
}

beforeEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-nx-theme");
  document.documentElement.removeAttribute("data-nx-term-theme");
  document.documentElement.style.removeProperty("--nx-ui-font-size");
  document.documentElement.style.removeProperty("--nx-ui-scale");
});

describe("appearance prefs defaults", () => {
  it("starts from the documented defaults and applies them to the document", async () => {
    const { prefs } = await loadPrefs();
    expect(prefs.getAppearancePrefs()).toEqual({
      uiFontPreset: 13,
      uiFontScale: 1,
      terminalFontSize: 13,
      terminalTheme: "dark",
    });
    expect(localStorage.getItem(KEY)).toBeNull();
    expect(rootStyle("--nx-ui-font-size")).toBe("13px");
    expect(rootStyle("--nx-ui-scale")).toBe("1");
    expect(textFactor()).toBe(1);
    expect(document.documentElement.dataset.nxTermTheme).toBe("dark");
    expect(prefs.getResolvedTerminalTheme()).toBe("dark");
  });

  it("falls back to defaults for corrupt storage", async () => {
    localStorage.setItem(KEY, "{not json");
    const { prefs } = await loadPrefs();
    expect(prefs.getAppearancePrefs().uiFontPreset).toBe(13);
    expect(prefs.getAppearancePrefs().terminalTheme).toBe("dark");
  });

  it("sanitizes out-of-range and unknown values", async () => {
    localStorage.setItem(
      KEY,
      JSON.stringify({
        uiFontPreset: "big",
        uiFontScale: 99,
        terminalFontSize: 99,
        terminalTheme: "blue",
      }),
    );
    const { prefs } = await loadPrefs();
    expect(prefs.getAppearancePrefs()).toEqual({
      uiFontPreset: 13,
      uiFontScale: 2,
      terminalFontSize: 32,
      terminalTheme: "dark",
    });

    localStorage.setItem(
      KEY,
      JSON.stringify({ uiFontScale: 0.1, terminalFontSize: 5 }),
    );
    const reloaded = await loadPrefs();
    expect(reloaded.prefs.getAppearancePrefs().uiFontScale).toBe(1);
    expect(reloaded.prefs.getAppearancePrefs().terminalFontSize).toBe(8);
  });

  it("snaps a stored multiplier to the nearest supported step", async () => {
    localStorage.setItem(KEY, JSON.stringify({ uiFontScale: 1.3 }));
    const { prefs } = await loadPrefs();
    expect(prefs.getAppearancePrefs().uiFontScale).toBe(1.25);
  });
});

describe("appearance prefs persistence", () => {
  it("persists every setter and restores after a module reload", async () => {
    const { prefs } = await loadPrefs();
    prefs.setUiFontPreset(14.5);
    prefs.setUiFontScale(1.5);
    prefs.setTerminalFontSize(16);
    prefs.setTerminalTheme("light");
    expect(JSON.parse(localStorage.getItem(KEY) ?? "{}")).toEqual({
      uiFontPreset: 14.5,
      uiFontScale: 1.5,
      terminalFontSize: 16,
      terminalTheme: "light",
    });
    expect(rootStyle("--nx-ui-font-size")).toBe("14.5px");
    expect(rootStyle("--nx-ui-scale")).toBe("1.5");
    expect(textFactor()).toBeCloseTo((1.5 * 14.5) / 13, 10);
    expect(document.documentElement.dataset.nxTermTheme).toBe("light");

    const reloaded = await loadPrefs();
    expect(reloaded.prefs.getAppearancePrefs()).toEqual({
      uiFontPreset: 14.5,
      uiFontScale: 1.5,
      terminalFontSize: 16,
      terminalTheme: "light",
    });
  });

  it("clamps the terminal font size into 8-32", async () => {
    const { prefs } = await loadPrefs();
    prefs.setTerminalFontSize(99);
    expect(prefs.getAppearancePrefs().terminalFontSize).toBe(32);
    prefs.setTerminalFontSize(1);
    expect(prefs.getAppearancePrefs().terminalFontSize).toBe(8);
  });

  it("persists a custom interface font size at 0.5 steps", async () => {
    const { prefs } = await loadPrefs();
    prefs.setUiFontPreset(15.5);
    expect(prefs.getAppearancePrefs().uiFontPreset).toBe(15.5);
    expect(rootStyle("--nx-ui-font-size")).toBe("15.5px");

    const reloaded = await loadPrefs();
    expect(reloaded.prefs.getAppearancePrefs().uiFontPreset).toBe(15.5);

    prefs.setUiFontPreset(99);
    expect(prefs.getAppearancePrefs().uiFontPreset).toBe(32);
    prefs.setUiFontPreset(15.26);
    expect(prefs.getAppearancePrefs().uiFontPreset).toBe(15.5);
  });

  it("notifies subscribers on change and stays silent on no-op sets", async () => {
    const { prefs } = await loadPrefs();
    const seen: unknown[] = [];
    const unsubscribe = prefs.subscribeAppearancePrefs(() => seen.push(prefs.getAppearancePrefs()));
    prefs.setUiFontScale(2);
    prefs.setUiFontScale(2);
    prefs.setTerminalFontSize(13);
    expect(seen).toEqual([{ uiFontPreset: 13, uiFontScale: 2, terminalFontSize: 13, terminalTheme: "dark" }]);
    unsubscribe();
    prefs.setUiFontScale(1);
    expect(seen).toHaveLength(1);
  });

  it("reset restores defaults, removes the key and notifies", async () => {
    const { prefs } = await loadPrefs();
    prefs.setUiFontPreset(12);
    prefs.setUiFontScale(1.75);
    prefs.setTerminalFontSize(17);
    prefs.setTerminalTheme("interface");
    const seen: number[] = [];
    const unsubscribe = prefs.subscribeAppearancePrefs(() => seen.push(1));

    prefs.resetAppearancePrefs();

    expect(prefs.getAppearancePrefs()).toEqual({
      uiFontPreset: 13,
      uiFontScale: 1,
      terminalFontSize: 13,
      terminalTheme: "dark",
    });
    expect(localStorage.getItem(KEY)).toBeNull();
    expect(rootStyle("--nx-ui-font-size")).toBe("13px");
    expect(rootStyle("--nx-ui-scale")).toBe("1");
    expect(textFactor()).toBe(1);
    expect(seen).toEqual([1]);
    unsubscribe();
  });
});

describe("text factor composition", () => {
  it("multiplies the preset and the scale into one unitless factor", async () => {
    const { prefs } = await loadPrefs();
    expect(prefs.uiTextFactor()).toBe(1);

    prefs.setUiFontPreset(14.5);
    expect(prefs.uiTextFactor()).toBeCloseTo(14.5 / 13, 10);
    expect(textFactor()).toBeCloseTo(14.5 / 13, 10);

    prefs.setUiFontScale(2);
    expect(prefs.uiTextFactor()).toBeCloseTo((2 * 14.5) / 13, 10);
    expect(textFactor()).toBeCloseTo((2 * 14.5) / 13, 10);

    prefs.setUiFontPreset(12);
    expect(prefs.uiTextFactor()).toBeCloseTo((2 * 12) / 13, 10);
  });

  it("tracks the factor through reset", async () => {
    const { prefs } = await loadPrefs();
    prefs.setUiFontPreset(12);
    prefs.setUiFontScale(1.75);
    expect(textFactor()).toBeCloseTo((1.75 * 12) / 13, 10);
    prefs.resetAppearancePrefs();
    expect(textFactor()).toBe(1);
  });
});

describe("terminal theme resolution", () => {
  it("keeps an explicit dark terminal theme when the interface theme flips", async () => {
    const { theme, prefs } = await loadPrefs();
    prefs.setTerminalTheme("dark");
    const seen: number[] = [];
    const unsubscribe = prefs.subscribeAppearancePrefs(() => seen.push(1));

    theme.setThemeMode("light");

    expect(theme.getResolvedTheme()).toBe("light");
    expect(prefs.getResolvedTerminalTheme()).toBe("dark");
    expect(document.documentElement.dataset.nxTheme).toBe("light");
    expect(document.documentElement.dataset.nxTermTheme).toBe("dark");
    expect(seen).toEqual([]);
    unsubscribe();
  });

  it("follows the interface theme only in interface mode", async () => {
    const { theme, prefs } = await loadPrefs();
    prefs.setTerminalTheme("interface");
    expect(document.documentElement.dataset.nxTermTheme).toBe("dark");

    theme.setThemeMode("light");
    expect(prefs.getResolvedTerminalTheme()).toBe("light");
    expect(document.documentElement.dataset.nxTermTheme).toBe("light");

    theme.setThemeMode("dark");
    expect(document.documentElement.dataset.nxTermTheme).toBe("dark");
  });

  it("resolves the interface mode from the system theme when set to system", async () => {
    const { theme, prefs } = await loadPrefs();
    prefs.setTerminalTheme("interface");
    theme.setThemeMode("system");
    expect(prefs.getResolvedTerminalTheme()).toBe(theme.getResolvedTheme());
  });
});
