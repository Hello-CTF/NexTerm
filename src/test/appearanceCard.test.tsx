/** @vitest-environment jsdom */
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
  setTerminalFontSize,
  setTerminalTheme,
  setUiFontPreset,
  setUiFontScale,
} from "../app/preferences";
import { click, clickButton, mount, type MountedView } from "./features/reactTestUtils";

const KEY = "nexterm.appearance.v1";

function group(container: ParentNode, label: string): HTMLElement {
  const el = [...container.querySelectorAll('[role="group"]')].find(
    (g) => g.getAttribute("aria-label") === label,
  );
  if (!el) throw new Error(`group not found: ${label}`);
  return el as HTMLElement;
}

function groupButton(container: ParentNode, label: string, text: string): HTMLButtonElement {
  const button = [...group(container, label).querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === text,
  );
  if (!button) throw new Error(`button not found: ${label} / ${text}`);
  return button as HTMLButtonElement;
}

describe("AppearanceCard 外观偏好", () => {
  let mounted: MountedView | undefined;

  beforeEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute("data-nx-theme");
    useUi.getState().setThemeMode("dark");
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("renders the defaults as active across all groups", () => {
    mounted = mount(createElement(AppearanceCard));
    const c = mounted.container;
    expect(groupButton(c, "界面字号", "13").getAttribute("aria-pressed")).toBe("true");
    expect(groupButton(c, "界面文字倍率", "100%").getAttribute("aria-pressed")).toBe("true");
    expect(groupButton(c, "终端与编辑器主题", "深色").getAttribute("aria-pressed")).toBe("true");
    expect(groupButton(c, "界面主题", "深色").getAttribute("aria-pressed")).toBe("true");
    expect(c.querySelector('[aria-label="减小终端字号"]')).not.toBeNull();
    expect(c.textContent).toContain("13px");
  });

  it("applies the interface font size preset and persists it", () => {
    mounted = mount(createElement(AppearanceCard));
    click(groupButton(mounted.container, "界面字号", "14.5"));

    expect(getAppearancePrefs().uiFontPreset).toBe(14.5);
    expect(JSON.parse(localStorage.getItem(KEY) ?? "{}").uiFontPreset).toBe(14.5);
    expect(document.documentElement.style.getPropertyValue("--nx-ui-font-size")).toBe("14.5px");
    expect(groupButton(mounted.container, "界面字号", "14.5").getAttribute("aria-pressed")).toBe("true");
  });

  it("applies the multiplier on top of the preset", () => {
    mounted = mount(createElement(AppearanceCard));
    click(groupButton(mounted.container, "界面文字倍率", "150%"));

    expect(getAppearancePrefs().uiFontScale).toBe(1.5);
    expect(document.documentElement.style.getPropertyValue("--nx-ui-scale")).toBe("1.5");
    expect(JSON.parse(localStorage.getItem(KEY) ?? "{}").uiFontScale).toBe(1.5);
  });

  it("switches the terminal theme without touching the interface theme", () => {
    mounted = mount(createElement(AppearanceCard));
    click(groupButton(mounted.container, "终端与编辑器主题", "浅色"));

    expect(getAppearancePrefs().terminalTheme).toBe("light");
    expect(document.documentElement.dataset.nxTermTheme).toBe("light");
    expect(useUi.getState().themeMode).toBe("dark");
    expect(document.documentElement.dataset.nxTheme).toBe("dark");
  });

  it("steps the terminal font size within 12-18 and disables at the bounds", () => {
    mounted = mount(createElement(AppearanceCard));
    const c = mounted.container;
    const minus = () => c.querySelector<HTMLButtonElement>('[aria-label="减小终端字号"]');
    const plus = () => c.querySelector<HTMLButtonElement>('[aria-label="增大终端字号"]');
    expect(minus()?.disabled).toBe(false);
    expect(plus()?.disabled).toBe(false);

    click(plus() as HTMLButtonElement);
    expect(getAppearancePrefs().terminalFontSize).toBe(14);

    act(() => setTerminalFontSize(18));
    expect(plus()?.disabled).toBe(true);
    click(minus() as HTMLButtonElement);
    expect(getAppearancePrefs().terminalFontSize).toBe(17);

    act(() => setTerminalFontSize(12));
    expect(minus()?.disabled).toBe(true);
  });

  it("reset restores theme and appearance defaults and spares unrelated keys", () => {
    localStorage.setItem("nexterm.layout.v1", JSON.stringify({ leftWidth: 300, rightWidth: 400 }));
    setUiFontPreset(12);
    setUiFontScale(2);
    setTerminalFontSize(17);
    setTerminalTheme("light");
    useUi.getState().setThemeMode("light");
    mounted = mount(createElement(AppearanceCard));

    const button = [...mounted.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "恢复默认",
    ) as HTMLButtonElement;
    expect(button.disabled).toBe(false);
    click(button);

    expect(useUi.getState().themeMode).toBe("system");
    expect(localStorage.getItem("nexterm.theme.v1")).toBe("system");
    expect(getAppearancePrefs()).toEqual({
      uiFontPreset: 13,
      uiFontScale: 1,
      terminalFontSize: 13,
      terminalTheme: "dark",
    });
    expect(localStorage.getItem(KEY)).toBeNull();
    expect(localStorage.getItem("nexterm.layout.v1")).toBe(
      JSON.stringify({ leftWidth: 300, rightWidth: 400 }),
    );
    expect(document.documentElement.style.getPropertyValue("--nx-ui-scale")).toBe("1");
    expect(document.documentElement.dataset.nxTermTheme).toBe("dark");
  });

  it("keeps the terminal theme isolated from interface theme flips unless following", () => {
    mounted = mount(createElement(AppearanceCard));
    const c = mounted.container;

    act(() => useUi.getState().setThemeMode("light"));
    expect(document.documentElement.dataset.nxTermTheme).toBe("dark");

    click(groupButton(c, "终端与编辑器主题", "跟随界面"));
    expect(document.documentElement.dataset.nxTermTheme).toBe("light");
    expect(c.textContent).toContain("当前浅色");

    act(() => useUi.getState().setThemeMode("dark"));
    expect(document.documentElement.dataset.nxTermTheme).toBe("dark");
    expect(c.textContent).toContain("当前深色");
  });

  it("clickButton helper still finds the reset control by its visible label", () => {
    mounted = mount(createElement(AppearanceCard));
    act(() => setUiFontScale(1.25));
    clickButton(mounted.container, "恢复默认");
    expect(getAppearancePrefs().uiFontScale).toBe(1);
  });
});
