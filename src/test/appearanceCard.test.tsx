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
  resetAppearancePrefs,
  setTerminalFontSize,
  setTerminalTheme,
  setUiFontPreset,
  setUiFontScale,
} from "../app/preferences";
import { click, clickButton, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

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

function advancedToggle(container: ParentNode): HTMLButtonElement {
  const button = [...container.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === "高级设置",
  );
  if (!button) throw new Error("高级设置 toggle not found");
  return button as HTMLButtonElement;
}

function ensureAdvancedOpen(container: ParentNode): void {
  const toggle = advancedToggle(container);
  if (toggle.getAttribute("aria-expanded") === "false") click(toggle);
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
    ensureAdvancedOpen(c);
    expect(groupButton(c, "界面字号", "13").getAttribute("aria-pressed")).toBe("true");
    expect(groupButton(c, "界面文字倍率", "100%").getAttribute("aria-pressed")).toBe("true");
    expect(groupButton(c, "终端与编辑器主题", "深色").getAttribute("aria-pressed")).toBe("true");
    expect(groupButton(c, "界面主题", "深色").getAttribute("aria-pressed")).toBe("true");
    expect(c.querySelector('[aria-label="减小终端字号"]')).not.toBeNull();
    expect(c.textContent).toContain("13px");
  });

  it("applies the interface font size preset and persists it", () => {
    mounted = mount(createElement(AppearanceCard));
    ensureAdvancedOpen(mounted.container);
    click(groupButton(mounted.container, "界面字号", "14.5"));

    expect(getAppearancePrefs().uiFontPreset).toBe(14.5);
    expect(JSON.parse(localStorage.getItem(KEY) ?? "{}").uiFontPreset).toBe(14.5);
    expect(document.documentElement.style.getPropertyValue("--nx-ui-font-size")).toBe("14.5px");
    expect(groupButton(mounted.container, "界面字号", "14.5").getAttribute("aria-pressed")).toBe("true");
  });

  it("自定义界面字号：选中后出现数字输入，提交后持久化并标记自定义", () => {
    act(() => resetAppearancePrefs());
    mounted = mount(createElement(AppearanceCard));
    const c = mounted.container;
    ensureAdvancedOpen(c);
    expect(c.querySelector('input[aria-label="自定义界面字号"]')).toBeNull();

    click(groupButton(c, "界面字号", "自定义"));
    const input = c.querySelector<HTMLInputElement>('input[aria-label="自定义界面字号"]');
    expect(input).not.toBeNull();
    expect(input!.value).toBe("13");

    act(() => input!.focus());
    setInputValue(input!, "15.5");
    act(() => input!.blur());
    expect(getAppearancePrefs().uiFontPreset).toBe(15.5);
    expect(JSON.parse(localStorage.getItem(KEY) ?? "{}").uiFontPreset).toBe(15.5);
    expect(document.documentElement.style.getPropertyValue("--nx-ui-font-size")).toBe("15.5px");
    expect(groupButton(c, "界面字号", "自定义").getAttribute("aria-pressed")).toBe("true");
    expect(groupButton(c, "界面字号", "13").getAttribute("aria-pressed")).toBe("false");

    click(groupButton(c, "界面字号", "14.5"));
    expect(getAppearancePrefs().uiFontPreset).toBe(14.5);
    expect(c.querySelector('input[aria-label="自定义界面字号"]')).toBeNull();
  });

  it("applies the multiplier on top of the preset", () => {
    mounted = mount(createElement(AppearanceCard));
    ensureAdvancedOpen(mounted.container);
    click(groupButton(mounted.container, "界面文字倍率", "150%"));

    expect(getAppearancePrefs().uiFontScale).toBe(1.5);
    expect(document.documentElement.style.getPropertyValue("--nx-ui-scale")).toBe("1.5");
    expect(JSON.parse(localStorage.getItem(KEY) ?? "{}").uiFontScale).toBe(1.5);
  });

  it("switches the terminal theme without touching the interface theme", () => {
    mounted = mount(createElement(AppearanceCard));
    ensureAdvancedOpen(mounted.container);
    click(groupButton(mounted.container, "终端与编辑器主题", "浅色"));

    expect(getAppearancePrefs().terminalTheme).toBe("light");
    expect(document.documentElement.dataset.nxTermTheme).toBe("light");
    expect(useUi.getState().themeMode).toBe("dark");
    expect(document.documentElement.dataset.nxTheme).toBe("dark");
  });

  it("steps the terminal font size within 8-32 and disables at the bounds", () => {
    mounted = mount(createElement(AppearanceCard));
    const c = mounted.container;
    ensureAdvancedOpen(c);
    const minus = () => c.querySelector<HTMLButtonElement>('[aria-label="减小终端字号"]');
    const plus = () => c.querySelector<HTMLButtonElement>('[aria-label="增大终端字号"]');
    expect(minus()?.disabled).toBe(false);
    expect(plus()?.disabled).toBe(false);

    click(plus() as HTMLButtonElement);
    expect(getAppearancePrefs().terminalFontSize).toBe(14);

    act(() => setTerminalFontSize(32));
    expect(plus()?.disabled).toBe(true);
    click(minus() as HTMLButtonElement);
    expect(getAppearancePrefs().terminalFontSize).toBe(31);

    act(() => setTerminalFontSize(8));
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
    ensureAdvancedOpen(mounted.container);

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
    ensureAdvancedOpen(c);

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
    ensureAdvancedOpen(mounted.container);
    clickButton(mounted.container, "恢复默认");
    expect(getAppearancePrefs().uiFontScale).toBe(1);
  });

  it("高级设置默认折叠可手动切换；非默认只在挂载时自动展开一次", () => {
    act(() => resetAppearancePrefs());
    mounted = mount(createElement(AppearanceCard));
    const c = mounted.container;
    const toggle = advancedToggle(c);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(c.querySelector('[aria-label="界面字号"]')).toBeNull();

    click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(c.querySelector('[aria-label="界面字号"]')).not.toBeNull();

    click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(c.querySelector('[aria-label="界面字号"]')).toBeNull();

    act(() => setUiFontPreset(14.5));
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(c.querySelector('[aria-label="界面字号"]')).toBeNull();

    mounted.unmount();
    mounted = mount(createElement(AppearanceCard));
    expect(advancedToggle(mounted.container).getAttribute("aria-expanded")).toBe("true");
  });
});
