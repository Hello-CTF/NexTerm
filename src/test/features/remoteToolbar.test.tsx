/** @vitest-environment jsdom */

import { act, createElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { RemoteToolbar, type RemoteToolbarProps } from "../../features/remote/RemoteToolbar";
import { KEY_COMBOS } from "../../features/remote/keys";
import type { ModifierKey } from "../../features/remote/remoteTypes";
import { mount, type MountedView } from "./reactTestUtils";

function props(patch: Partial<RemoteToolbarProps> = {}): RemoteToolbarProps {
  return {
    protocol: "rdp",
    visible: true,
    scale: "fit",
    onScale: vi.fn(),
    held: new Set<ModifierKey>(),
    onToggleHeld: vi.fn(),
    onCombo: vi.fn(),
    clipboardEnabled: true,
    onSendClipboard: vi.fn(),
    onFetchClipboard: vi.fn(),
    resolution: "follow",
    onResolution: vi.fn(),
    viewOnly: false,
    onViewOnly: vi.fn(),
    captured: true,
    onReleaseKeyboard: vi.fn(),
    fullscreen: false,
    canFullscreen: true,
    onFullscreen: vi.fn(),
    touch: false,
    touchMode: "direct",
    onTouchMode: vi.fn(),
    onDisconnect: vi.fn(),
    onPinnedChange: vi.fn(),
    onReveal: vi.fn(),
    ...patch,
  };
}

let view: MountedView | undefined;

afterEach(() => {
  view?.unmount();
  view = undefined;
  document.body.replaceChildren();
});

function render(initial: RemoteToolbarProps) {
  view = mount(createElement(RemoteToolbar, initial));
  return {
    rerender: (next: RemoteToolbarProps) => act(() => view!.root.render(createElement(RemoteToolbar, next))),
  };
}

const toolbar = () => document.querySelector<HTMLElement>("[data-testid='remote-toolbar']")!;

function button(name: string): HTMLButtonElement {
  const found = [...toolbar().querySelectorAll<HTMLButtonElement>("button")].find(
    (candidate) => candidate.getAttribute("aria-label") === name || candidate.textContent?.trim() === name,
  );
  if (!found) throw new Error(`no toolbar button ${name}`);
  return found;
}

function click(element: HTMLElement) {
  act(() => element.click());
}

function menuItem(label: string): HTMLElement {
  const found = [...document.querySelectorAll<HTMLElement>("[role='menuitem']")].find((item) => item.textContent?.includes(label));
  if (!found) throw new Error(`no menu item ${label}`);
  return found;
}

const isDisabled = (element: HTMLElement) => (element as HTMLButtonElement).disabled || element.getAttribute("aria-disabled") === "true";

function typeInto(input: HTMLInputElement, value: string) {
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

describe("RemoteToolbar visibility", () => {
  it("is hidden when not revealed and nothing pins it", () => {
    render(props({ visible: false }));
    expect(toolbar().dataset.visible).toBe("false");
    expect(toolbar().className).toContain("pointer-events-none");
  });

  it("is shown while revealed", () => {
    render(props({ visible: true }));
    expect(toolbar().dataset.visible).toBe("true");
  });

  it("stays on screen and reports a pin while a modifier is held, and unpins when released", () => {
    const onPinnedChange = vi.fn();
    const ui = render(props({ visible: false, onPinnedChange }));
    expect(onPinnedChange).toHaveBeenLastCalledWith(false);
    ui.rerender(props({ visible: false, onPinnedChange, held: new Set<ModifierKey>(["ControlLeft"]) }));
    expect(toolbar().dataset.visible).toBe("true");
    expect(onPinnedChange).toHaveBeenLastCalledWith(true);
    expect(button("Ctrl").getAttribute("aria-pressed")).toBe("true");
    expect(button("Alt").getAttribute("aria-pressed")).toBe("false");
    ui.rerender(props({ visible: false, onPinnedChange }));
    expect(toolbar().dataset.visible).toBe("false");
    expect(onPinnedChange).toHaveBeenLastCalledWith(false);
  });

  it("pins while a menu is open and unpins when the menu closes", () => {
    const onPinnedChange = vi.fn();
    render(props({ visible: false, onPinnedChange }));
    click(button("缩放"));
    expect(onPinnedChange).toHaveBeenLastCalledWith(true);
    expect(toolbar().dataset.visible).toBe("true");
    act(() => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(document.querySelector("[role='menu']")).toBeNull();
    expect(onPinnedChange).toHaveBeenLastCalledWith(false);
  });

  it("keeps the remote screen focused: a toolbar mouse down is not allowed to move focus", () => {
    render(props());
    const event = new MouseEvent("mousedown", { bubbles: true, cancelable: true });
    act(() => {
      button("缩放").dispatchEvent(event);
    });
    expect(event.defaultPrevented).toBe(true);
  });
});

describe("RemoteToolbar scale menu", () => {
  it("lists fit and the fixed zoom levels, marks the current one and selects", () => {
    const onScale = vi.fn();
    render(props({ onScale, scale: 1.25 }));
    click(button("缩放"));
    expect(menuItem("125%").textContent).toContain("当前");
    expect(menuItem("适应窗口").textContent).not.toContain("当前");
    click(menuItem("150%"));
    expect(onScale).toHaveBeenCalledWith(1.5);
    expect(document.querySelector("[role='menu']")).toBeNull();
  });

  it("selects fit", () => {
    const onScale = vi.fn();
    render(props({ onScale, scale: 2 }));
    click(button("缩放"));
    click(menuItem("适应窗口"));
    expect(onScale).toHaveBeenCalledWith("fit");
  });

  it("disables the zooms VNC cannot show but leaves fit and 100 percent", () => {
    const onScale = vi.fn();
    render(props({ protocol: "vnc", onScale }));
    click(button("缩放"));
    for (const label of ["50%", "75%", "125%", "150%", "200%"]) expect(isDisabled(menuItem(label))).toBe(true);
    expect(isDisabled(menuItem("100%"))).toBe(false);
    expect(isDisabled(menuItem("适应窗口"))).toBe(false);
    click(menuItem("50%"));
    expect(onScale).not.toHaveBeenCalled();
    click(menuItem("100%"));
    expect(onScale).toHaveBeenCalledWith(1);
  });
});

describe("RemoteToolbar modifiers", () => {
  it("toggles a modifier and cannot hold one in read-only mode", () => {
    const onToggleHeld = vi.fn();
    const ui = render(props({ onToggleHeld }));
    click(button("Shift"));
    expect(onToggleHeld).toHaveBeenCalledWith("ShiftLeft");
    ui.rerender(props({ onToggleHeld, viewOnly: true }));
    for (const name of ["Ctrl", "Alt", "Shift", "Win"]) expect(button(name).disabled).toBe(true);
  });
});

describe("RemoteToolbar key combinations", () => {
  const dialog = () => document.querySelector<HTMLElement>("[role='dialog']")!;
  const search = () => dialog().querySelector<HTMLInputElement>("input")!;

  it("opens a grouped list, focuses the search and sends the picked combo, then closes", () => {
    const onCombo = vi.fn();
    render(props({ onCombo }));
    click(button("按键"));
    expect(document.activeElement).toBe(search());
    expect(dialog().querySelectorAll("[role='group']").length).toBeGreaterThan(1);
    const target = KEY_COMBOS.find((combo) => combo.label === "Alt+Tab")!;
    click([...dialog().querySelectorAll<HTMLButtonElement>("button")].find((item) => item.textContent?.includes("Alt+Tab"))!);
    expect(onCombo).toHaveBeenCalledWith(target);
    expect(document.querySelector("[role='dialog']")).toBeNull();
  });

  it("narrows the list by the search text and says when nothing matches", () => {
    render(props());
    click(button("按键"));
    typeInto(search(), "alt+tab");
    const labels = [...dialog().querySelectorAll("button .font-mono")].map((node) => node.textContent);
    expect(labels).toEqual(["Alt+Tab"]);
    typeInto(search(), "zzzz-no-such-combo");
    expect(dialog().textContent).toContain("没有匹配的组合键");
  });

  it("closes on Escape and when the pointer goes down outside, but not inside", () => {
    render(props());
    click(button("按键"));
    act(() => {
      search().dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    });
    expect(dialog()).not.toBeNull();
    act(() => {
      search().dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(document.querySelector("[role='dialog']")).toBeNull();
    click(button("按键"));
    act(() => {
      document.body.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    });
    expect(document.querySelector("[role='dialog']")).toBeNull();
  });

  it("toggles closed on a second press and gives way to a menu", () => {
    render(props());
    click(button("按键"));
    expect(button("按键").getAttribute("aria-pressed")).toBe("true");
    click(button("按键"));
    expect(document.querySelector("[role='dialog']")).toBeNull();
    click(button("按键"));
    click(button("缩放"));
    expect(document.querySelector("[role='dialog']")).toBeNull();
    expect(document.querySelector("[role='menu']")).not.toBeNull();
  });
});

describe("RemoteToolbar clipboard", () => {
  it("sends and fetches the clipboard", () => {
    const onSendClipboard = vi.fn();
    const onFetchClipboard = vi.fn();
    render(props({ onSendClipboard, onFetchClipboard }));
    click(button("剪贴板"));
    click(menuItem("发送本机剪贴板到远程"));
    expect(onSendClipboard).toHaveBeenCalledTimes(1);
    click(button("剪贴板"));
    click(menuItem("取回远程剪贴板到本机"));
    expect(onFetchClipboard).toHaveBeenCalledTimes(1);
  });

  it("does not send in read-only mode but still lets the remote text be fetched", () => {
    const onSendClipboard = vi.fn();
    render(props({ viewOnly: true, onSendClipboard }));
    click(button("剪贴板"));
    expect(isDisabled(menuItem("发送本机剪贴板到远程"))).toBe(true);
    expect(isDisabled(menuItem("取回远程剪贴板到本机"))).toBe(false);
    click(menuItem("发送本机剪贴板到远程"));
    expect(onSendClipboard).not.toHaveBeenCalled();
  });

  it("disables both directions and explains when the asset turned clipboard off", () => {
    render(props({ clipboardEnabled: false }));
    click(button("剪贴板"));
    expect(isDisabled(menuItem("发送本机剪贴板到远程"))).toBe(true);
    expect(isDisabled(menuItem("取回远程剪贴板到本机"))).toBe(true);
    expect(menuItem("此资产已关闭剪贴板")).toBeDefined();
  });
});

describe("RemoteToolbar more menu", () => {
  it("toggles read-only in both directions", () => {
    const onViewOnly = vi.fn();
    const ui = render(props({ onViewOnly }));
    click(button("更多"));
    click(menuItem("只读模式"));
    expect(onViewOnly).toHaveBeenLastCalledWith(true);
    ui.rerender(props({ onViewOnly, viewOnly: true }));
    click(button("更多"));
    click(menuItem("退出只读模式"));
    expect(onViewOnly).toHaveBeenLastCalledWith(false);
  });

  it("releases the keyboard only while it is captured", () => {
    const onReleaseKeyboard = vi.fn();
    const ui = render(props({ onReleaseKeyboard, captured: false }));
    click(button("更多"));
    expect(isDisabled(menuItem("释放键盘"))).toBe(true);
    click(menuItem("释放键盘"));
    expect(onReleaseKeyboard).not.toHaveBeenCalled();
    ui.rerender(props({ onReleaseKeyboard, captured: true }));
    click(button("更多"));
    click(menuItem("释放键盘"));
    expect(onReleaseKeyboard).toHaveBeenCalledTimes(1);
  });

  it("disconnects from the menu and from the close button", () => {
    const onDisconnect = vi.fn();
    render(props({ onDisconnect }));
    click(button("更多"));
    click(menuItem("断开连接"));
    expect(onDisconnect).toHaveBeenCalledTimes(1);
    click(button("断开连接"));
    expect(onDisconnect).toHaveBeenCalledTimes(2);
  });

  it("offers RDP resolutions, marks the current one and selects presets", () => {
    const onResolution = vi.fn();
    render(props({ onResolution, resolution: { width: 1600, height: 900 } }));
    click(button("更多"));
    expect(menuItem("1600 x 900").textContent).toContain("当前");
    expect(menuItem("跟随窗口大小").textContent).not.toContain("当前");
    click(menuItem("1920 x 1080"));
    expect(onResolution).toHaveBeenCalledWith({ width: 1920, height: 1080 });
    click(button("更多"));
    click(menuItem("跟随窗口大小"));
    expect(onResolution).toHaveBeenLastCalledWith("follow");
  });

  it("has no resolution choices for VNC", () => {
    render(props({ protocol: "vnc" }));
    click(button("更多"));
    expect(document.body.textContent).not.toContain("1920 x 1080");
  });

  it("offers the touch mode only on touch devices", () => {
    const onTouchMode = vi.fn();
    const ui = render(props({ touch: false, onTouchMode }));
    click(button("更多"));
    expect(document.body.textContent).not.toContain("虚拟触控板");
    act(() => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    ui.rerender(props({ touch: true, touchMode: "trackpad", onTouchMode }));
    click(button("更多"));
    expect(menuItem("虚拟触控板").textContent).toContain("当前");
    click(menuItem("直接点按"));
    expect(onTouchMode).toHaveBeenCalledWith("direct");
  });
});

describe("RemoteToolbar fullscreen", () => {
  it("shows the fullscreen button only when possible and reports its state", () => {
    const onFullscreen = vi.fn();
    const ui = render(props({ canFullscreen: false, onFullscreen }));
    expect(toolbar().querySelector("[aria-label='全屏']")).toBeNull();
    ui.rerender(props({ canFullscreen: true, fullscreen: true, onFullscreen }));
    expect(button("全屏").getAttribute("aria-pressed")).toBe("true");
    click(button("全屏"));
    expect(onFullscreen).toHaveBeenCalledTimes(1);
  });
});
