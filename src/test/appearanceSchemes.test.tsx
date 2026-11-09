/** @vitest-environment jsdom */
import { readFileSync } from "node:fs";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";

vi.mock("../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
}));

import { AppearanceCard } from "../features/settings/AppearanceCard";
import { useUi } from "../app/store";
import {
  getAppearancePrefs,
  registerAccountPreferenceStore,
  resetAppearancePrefs,
  setAccent,
  setTerminalCustomSchemes,
  setTerminalScheme,
  setTerminalTheme,
  type AccountPreferenceStore,
} from "../app/preferences";
import type { CustomTerminalScheme } from "../features/terminal/terminalSchemes";
import { click, flushMacrotask, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

const KEY = "nexterm.appearance.v1";
const ACCENT_KEY = "nexterm.accent.v1";
const ANSI16 = Array.from({ length: 16 }, (_, i) => `#2${i.toString(16)}2020`);

function customScheme(id: string, name: string, background = "#121212"): CustomTerminalScheme {
  return { id, name, background, foreground: "#e0e0e0", ansi: ANSI16 };
}

function root(): HTMLElement {
  return document.documentElement;
}

function group(container: ParentNode, label: string): HTMLElement {
  const el = [...container.querySelectorAll('[role="group"]')].find((g) => g.getAttribute("aria-label") === label);
  if (!el) throw new Error(`group not found: ${label}`);
  return el as HTMLElement;
}

function swatch(container: ParentNode, groupLabel: string, name: string): HTMLButtonElement {
  const button = group(container, groupLabel).querySelector(`button[aria-label="${name}"]`);
  if (!button) throw new Error(`swatch not found: ${groupLabel} / ${name}`);
  return button as HTMLButtonElement;
}

function memoryStore() {
  const overrides: Record<string, unknown> = {};
  const store: AccountPreferenceStore = {
    getView: vi.fn(async () => ({ defaults: {}, overrides: { ...overrides } })),
    putOverrides: vi.fn(async (set: Record<string, unknown>) => {
      Object.assign(overrides, set);
    }),
    deleteOverrides: vi.fn(async (keys: string[]) => {
      for (const key of keys) delete overrides[key];
    }),
  };
  return { store, overrides };
}

beforeEach(() => {
  registerAccountPreferenceStore(null);
  localStorage.clear();
  resetAppearancePrefs();
  setTerminalCustomSchemes([]);
  localStorage.clear();
  useUi.getState().setThemeMode("dark");
});

describe("terminal scheme and accent preferences", () => {
  it("defaults to the NexTerm schemes and the shipped accent without overrides", () => {
    const prefs = getAppearancePrefs();
    expect(prefs.terminalSchemeDark).toBe("nexterm-dark");
    expect(prefs.terminalSchemeLight).toBe("nexterm-light");
    expect(prefs.terminalCustomSchemes).toEqual([]);
    expect(prefs.accent).toBe("indigo");
    expect(root().dataset.nxTermScheme).toBeUndefined();
    expect(root().dataset.nxAccent).toBeUndefined();
    expect(root().style.getPropertyValue("--nx-accent-dark-accent")).toBe("");
  });

  it("keeps working with synced values from older clients and drops invalid new values", async () => {
    vi.resetModules();
    localStorage.setItem(KEY, JSON.stringify({ terminalTheme: "light", terminalContrast: "high" }));
    const fresh = await import("../app/preferences");
    expect(fresh.getAppearancePrefs()).toMatchObject({
      terminalTheme: "light",
      terminalContrast: "high",
      terminalSchemeDark: "nexterm-dark",
      terminalSchemeLight: "nexterm-light",
      terminalCustomSchemes: [],
      accent: "indigo",
    });

    vi.resetModules();
    localStorage.setItem(
      KEY,
      JSON.stringify({ terminalSchemeDark: "Not Valid", terminalSchemeLight: 7, terminalCustomSchemes: "x", accent: "red" }),
    );
    const sanitized = await import("../app/preferences");
    expect(sanitized.getAppearancePrefs()).toMatchObject({
      terminalSchemeDark: "nexterm-dark",
      terminalSchemeLight: "nexterm-light",
      terminalCustomSchemes: [],
      accent: "indigo",
    });

    vi.resetModules();
    localStorage.setItem(KEY, JSON.stringify({ terminalSchemeDark: "scheme-from-newer-client" }));
    const future = await import("../app/preferences");
    expect(future.getAppearancePrefs().terminalSchemeDark).toBe("scheme-from-newer-client");
    expect(root().dataset.nxTermScheme).toBeUndefined();
  });

  it("applies the scheme of the resolved terminal theme to the terminal pane variables", () => {
    setTerminalScheme("dark", "nord");
    setTerminalScheme("light", "solarized-light");
    expect(root().dataset.nxTermScheme).toBe("nord");
    expect(root().style.getPropertyValue("--nx-term-scheme-bg")).toBe("#2e3440");

    setTerminalTheme("light");
    expect(root().dataset.nxTermTheme).toBe("light");
    expect(root().dataset.nxTermScheme).toBe("solarized-light");
    expect(root().style.getPropertyValue("--nx-term-scheme-bg")).toBe("#fdf6e3");
    expect(root().style.getPropertyValue("--nx-term-scheme-fg")).toBe("#657b83");

    setTerminalScheme("light", "nexterm-light");
    expect(root().dataset.nxTermScheme).toBeUndefined();
    expect(root().style.getPropertyValue("--nx-term-scheme-bg")).toBe("");
  });

  it("follows the interface theme with separate light and dark schemes", () => {
    setTerminalScheme("dark", "dracula");
    setTerminalScheme("light", "github-light");
    setTerminalTheme("interface");
    expect(root().dataset.nxTermScheme).toBe("dracula");
    act(() => useUi.getState().setThemeMode("light"));
    expect(root().dataset.nxTermScheme).toBe("github-light");
  });

  it("sets the accent variables for a preset, mirrors them for the bootstrap and clears them for the default", () => {
    setAccent("teal");
    expect(root().dataset.nxAccent).toBe("teal");
    expect(root().style.getPropertyValue("--nx-accent-dark-accent")).toBe("#14b8a6");
    expect(root().style.getPropertyValue("--nx-accent-light-600")).toBe("#0f766e");
    expect(JSON.parse(localStorage.getItem(ACCENT_KEY) ?? "{}")["--nx-accent-dark-accent"]).toBe("#14b8a6");

    setAccent("indigo");
    expect(root().dataset.nxAccent).toBeUndefined();
    expect(root().style.getPropertyValue("--nx-accent-dark-accent")).toBe("");
    expect(localStorage.getItem(ACCENT_KEY)).toBeNull();
  });

  it("syncs the new keys to the account store", async () => {
    const { store, overrides } = memoryStore();
    registerAccountPreferenceStore(store);
    await act(async () => {
      await flushMacrotask();
    });
    setTerminalScheme("dark", "tokyo-night");
    setAccent("violet");
    setTerminalCustomSchemes([customScheme("custom-one", "Mine")]);
    expect(overrides["appearance.terminalSchemeDark"]).toBe("tokyo-night");
    expect(overrides["appearance.accent"]).toBe("violet");
    expect(overrides["appearance.terminalCustomSchemes"]).toEqual([customScheme("custom-one", "Mine")]);
    registerAccountPreferenceStore(null);
  });

  it("returns a slot to its default when its custom scheme is deleted, and reset keeps imported schemes", () => {
    setTerminalCustomSchemes([customScheme("custom-one", "Mine"), customScheme("custom-two", "Other")]);
    setTerminalScheme("dark", "custom-one");
    setAccent("green");
    expect(root().dataset.nxTermScheme).toBe("custom-one");

    setTerminalCustomSchemes([customScheme("custom-two", "Other")]);
    expect(getAppearancePrefs().terminalSchemeDark).toBe("nexterm-dark");
    expect(root().dataset.nxTermScheme).toBeUndefined();

    setTerminalScheme("dark", "custom-two");
    resetAppearancePrefs();
    expect(getAppearancePrefs()).toMatchObject({ terminalSchemeDark: "nexterm-dark", accent: "indigo" });
    expect(getAppearancePrefs().terminalCustomSchemes.map((item) => item.id)).toEqual(["custom-two"]);
  });
});

describe("AppearanceCard schemes and accent", () => {
  let mounted: MountedView | undefined;

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("selects a scheme per slot from the swatch grid", () => {
    mounted = mount(createElement(AppearanceCard));
    const c = mounted.container;
    expect(swatch(c, "深色终端配色", "NexTerm Dark").getAttribute("aria-pressed")).toBe("true");
    expect(group(c, "深色终端配色").querySelector('button[aria-label="Solarized Light"]')).toBeNull();

    click(swatch(c, "深色终端配色", "Catppuccin Mocha"));
    expect(getAppearancePrefs().terminalSchemeDark).toBe("catppuccin-mocha");
    expect(swatch(c, "深色终端配色", "Catppuccin Mocha").getAttribute("aria-pressed")).toBe("true");
    expect(swatch(c, "深色终端配色", "NexTerm Dark").getAttribute("aria-pressed")).toBe("false");

    click(swatch(c, "浅色终端配色", "Gruvbox Light"));
    expect(getAppearancePrefs().terminalSchemeLight).toBe("gruvbox-light");
    expect(getAppearancePrefs().terminalSchemeDark).toBe("catppuccin-mocha");
  });

  it("selects an accent preset", () => {
    mounted = mount(createElement(AppearanceCard));
    const button = [...group(mounted.container, "强调色").querySelectorAll("button")].find((b) => b.textContent?.includes("紫色"));
    if (!button) throw new Error("accent button not found");
    click(button);
    expect(getAppearancePrefs().accent).toBe("violet");
    expect(button.getAttribute("aria-pressed")).toBe("true");
  });

  it("imports scheme files, reports files it cannot read, and renames and deletes custom schemes", async () => {
    mounted = mount(createElement(AppearanceCard));
    const c = mounted.container;
    const input = c.querySelector('input[type="file"]') as HTMLInputElement;
    const text = readFileSync("src/test/fixtures/terminal-schemes/Dracula.itermcolors", "utf8");
    const files = [new File([text], "Dracula.itermcolors"), new File(["background = #000000"], "broken.conf")];
    Object.defineProperty(input, "files", { configurable: true, value: files });
    await act(async () => {
      input.dispatchEvent(new Event("change", { bubbles: true }));
      for (let i = 0; i < 5; i++) await flushMacrotask();
    });

    const status = c.querySelector('[role="status"]');
    expect(status?.textContent).toContain("已导入 1 个配色");
    expect(status?.textContent).toContain("broken.conf");
    const [imported] = getAppearancePrefs().terminalCustomSchemes;
    expect(imported).toMatchObject({ name: "Dracula", background: "#282a36" });
    expect(getAppearancePrefs().terminalSchemeDark).toBe(imported.id);
    const importedSwatches = group(c, "深色终端配色").querySelectorAll('button[aria-label="Dracula"]');
    expect([...importedSwatches].some((b) => b.getAttribute("aria-pressed") === "true")).toBe(true);

    const name = c.querySelector('input[aria-label="配色名称"]') as HTMLInputElement;
    setInputValue(name, "  My Dracula ");
    act(() => {
      name.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
    });
    expect(getAppearancePrefs().terminalCustomSchemes[0].name).toBe("My Dracula");

    click(c.querySelector('button[aria-label="删除配色 My Dracula"]') as HTMLButtonElement);
    expect(getAppearancePrefs().terminalCustomSchemes).toEqual([]);
    expect(getAppearancePrefs().terminalSchemeDark).toBe("nexterm-dark");
  });
});
