/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, mountWithClient, type MountedView } from "./reactTestUtils";
import { AppHeader } from "../../app/shell/AppHeader";
import { resetAllKeybindings, setKeybinding } from "../../app/keybindings";

let mounted: MountedView | undefined;

function render(onOpenPalette = vi.fn()): MountedView {
  mounted = mountWithClient(
    createElement(AppHeader, {
      ws: null,
      activeTab: null,
      rightDockOpen: false,
      commandPaletteLabel: "Ctrl+Shift+P",
      onOpenWorkspaceSwitcher: vi.fn(),
      onToggleRight: vi.fn(),
      onOpenPalette,
    }),
  );
  return mounted;
}

beforeEach(() => {
  localStorage.clear();
  resetAllKeybindings();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  resetAllKeybindings();
});

describe("header search pill", () => {
  it("shows a labelled search pill with the palette keys and opens the palette", () => {
    const onOpenPalette = vi.fn();
    const view = render(onOpenPalette);
    const pill = view.container.querySelector<HTMLButtonElement>(".nx-header-search");
    expect(pill).not.toBeNull();
    expect(pill!.getAttribute("aria-label")).toMatch(/^跳转面板/);
    expect(pill!.textContent).toContain("搜索会话、主机或命令");
    expect([...pill!.querySelectorAll(".nx-kbd")].map((key) => key.textContent)).toEqual(["Ctrl", "Shift", "P"]);
    expect(pill!.style.getPropertyValue("--wails-draggable")).toBe("no-drag");
    click(pill!);
    expect(onOpenPalette).toHaveBeenCalledTimes(1);
  });

  it("follows a rebound palette key and drops the chips when it is unbound", () => {
    act(() => setKeybinding("commandPalette", "Alt+k"));
    const view = render();
    const keys = () => [...view.container.querySelectorAll(".nx-header-search .nx-kbd")].map((key) => key.textContent);
    expect(keys()).toEqual(["Alt", "K"]);
    act(() => setKeybinding("commandPalette", null));
    expect(keys()).toEqual([]);
  });
});
