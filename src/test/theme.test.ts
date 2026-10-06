/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { installMatchMediaStub } from "./setup";

vi.mock("../ipc/commands", () => ({
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
}));

interface MediaControl {
  set(light: boolean): void;
  listenerCount(): number;
}

function installMatchMedia(initialLight: boolean): MediaControl {
  const state = { matches: initialLight, listeners: new Set<() => void>() };
  const mql = {
    get matches() {
      return state.matches;
    },
    media: "(prefers-color-scheme: light)",
    addEventListener: (_type: string, cb: () => void) => {
      state.listeners.add(cb);
    },
    removeEventListener: (_type: string, cb: () => void) => {
      state.listeners.delete(cb);
    },
    addListener: (cb: () => void) => {
      state.listeners.add(cb);
    },
    removeListener: (cb: () => void) => {
      state.listeners.delete(cb);
    },
    onchange: null,
    dispatchEvent: () => true,
  };
  (window as unknown as Record<string, unknown>).matchMedia = () => mql;
  return {
    set(light: boolean) {
      state.matches = light;
      for (const cb of [...state.listeners]) cb();
    },
    listenerCount: () => state.listeners.size,
  };
}

async function loadTheme() {
  vi.resetModules();
  return await import("../app/theme");
}

beforeEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-nx-theme");
  installMatchMediaStub();
});

describe("theme mode", () => {
  it("defaults to system resolving dark without a saved preference", async () => {
    const theme = await loadTheme();
    expect(theme.getThemeMode()).toBe("system");
    expect(theme.getResolvedTheme()).toBe("dark");
  });

  it("falls back to system for invalid saved values", async () => {
    localStorage.setItem("nexterm.theme.v1", "blue");
    const theme = await loadTheme();
    expect(theme.getThemeMode()).toBe("system");
  });

  it("restores the saved mode", async () => {
    localStorage.setItem("nexterm.theme.v1", "light");
    const theme = await loadTheme();
    expect(theme.getThemeMode()).toBe("light");
    expect(theme.getResolvedTheme()).toBe("light");
  });

  it("persists, applies and notifies on setThemeMode", async () => {
    const theme = await loadTheme();
    const seen: [string, string][] = [];
    const unsubscribe = theme.subscribeTheme((m, r) => seen.push([m, r]));

    theme.setThemeMode("light");
    expect(localStorage.getItem("nexterm.theme.v1")).toBe("light");
    expect(document.documentElement.dataset.nxTheme).toBe("light");
    expect(seen).toEqual([["light", "light"]]);

    unsubscribe();
    theme.setThemeMode("dark");
    expect(seen).toEqual([["light", "light"]]);
  });

  it("tracks system changes while in system mode", async () => {
    const media = installMatchMedia(false);
    localStorage.setItem("nexterm.theme.v1", "system");
    const theme = await loadTheme();
    theme.initTheme();
    const seen: [string, string][] = [];
    theme.subscribeTheme((m, r) => seen.push([m, r]));

    expect(theme.getResolvedTheme()).toBe("dark");
    media.set(true);
    expect(theme.getResolvedTheme()).toBe("light");
    expect(document.documentElement.dataset.nxTheme).toBe("light");
    expect(seen).toEqual([["system", "light"]]);
  });

  it("ignores system changes while an explicit mode is active", async () => {
    const media = installMatchMedia(false);
    const theme = await loadTheme();
    theme.initTheme();
    theme.setThemeMode("dark");
    const seen: [string, string][] = [];
    theme.subscribeTheme((m, r) => seen.push([m, r]));

    media.set(true);
    expect(theme.getResolvedTheme()).toBe("dark");
    expect(document.documentElement.dataset.nxTheme).toBe("dark");
    expect(seen).toEqual([]);
  });

  it("subscribes to system changes only once across initTheme calls", async () => {
    const media = installMatchMedia(false);
    const theme = await loadTheme();
    theme.initTheme();
    theme.initTheme();
    expect(media.listenerCount()).toBe(1);
  });
});

describe("theme store wiring", () => {
  it("mirrors persisted mode into the ui store and follows system changes", async () => {
    const media = installMatchMedia(false);
    localStorage.setItem("nexterm.theme.v1", "system");
    vi.resetModules();
    const { useUi } = await import("../app/store");

    expect(useUi.getState().themeMode).toBe("system");
    expect(useUi.getState().resolvedTheme).toBe("dark");

    media.set(true);
    expect(useUi.getState().resolvedTheme).toBe("light");

    useUi.getState().setThemeMode("dark");
    expect(useUi.getState().themeMode).toBe("dark");
    expect(useUi.getState().resolvedTheme).toBe("dark");
    expect(localStorage.getItem("nexterm.theme.v1")).toBe("dark");
    expect(document.documentElement.dataset.nxTheme).toBe("dark");

    media.set(true);
    expect(useUi.getState().resolvedTheme).toBe("dark");
  });

  it("initializes the store from a persisted non-system mode", async () => {
    installMatchMedia(false);
    localStorage.setItem("nexterm.theme.v1", "light");
    vi.resetModules();
    const { useUi } = await import("../app/store");
    expect(useUi.getState().themeMode).toBe("light");
    expect(useUi.getState().resolvedTheme).toBe("light");
    expect(document.documentElement.dataset.nxTheme).toBe("light");
  });
});
