/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
}));

import { AppearanceCard } from "../../features/settings/AppearanceCard";
import { useUi } from "../../app/store";
import { clickButton, mount, type MountedView } from "./reactTestUtils";

function resetButton(container: ParentNode): HTMLButtonElement {
  const button = [...container.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === "恢复默认",
  ) as HTMLButtonElement | undefined;
  if (!button) throw new Error("恢复默认 button not found");
  return button;
}

describe("AppearanceCard 恢复默认", () => {
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

  it("把主题恢复为跟随系统并持久化，布局等其他本地数据不受影响", () => {
    localStorage.setItem("nexterm.layout.v1", JSON.stringify({ leftWidth: 300, rightWidth: 400 }));
    mounted = mount(createElement(AppearanceCard));

    const button = resetButton(mounted.container);
    expect(button.disabled).toBe(false);
    expect(button.getAttribute("aria-label")).toBe("恢复默认主题与外观");

    clickButton(mounted.container, "恢复默认");

    expect(useUi.getState().themeMode).toBe("system");
    expect(localStorage.getItem("nexterm.theme.v1")).toBe("system");
    expect(localStorage.getItem("nexterm.layout.v1")).toBe(
      JSON.stringify({ leftWidth: 300, rightWidth: 400 }),
    );
    expect(resetButton(mounted.container).disabled).toBe(true);
    const system = [...mounted.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "跟随系统",
    );
    expect(system?.getAttribute("aria-pressed")).toBe("true");
  });

  it("已是默认主题时按钮禁用", () => {
    useUi.getState().setThemeMode("system");
    mounted = mount(createElement(AppearanceCard));
    expect(resetButton(mounted.container).disabled).toBe(true);
  });

  it("恢复默认后重新加载模块仍是默认值", async () => {
    mounted = mount(createElement(AppearanceCard));
    clickButton(mounted.container, "恢复默认");
    expect(localStorage.getItem("nexterm.theme.v1")).toBe("system");

    vi.resetModules();
    const theme = await import("../../app/theme");
    const store = await import("../../app/store");

    expect(theme.getThemeMode()).toBe("system");
    expect(store.useUi.getState().themeMode).toBe("system");
  });
});
