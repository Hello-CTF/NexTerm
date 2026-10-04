/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, Fragment } from "react";
import { flush, mount, type MountedView } from "../features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  cancel: vi.fn(),
  takeoverExit: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  aiApi: { cancel: mocks.cancel, takeoverExit: mocks.takeoverExit },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));

import { TakeoverBanner } from "../../app/TakeoverBanner";
import { DialogHost } from "../../ui/DialogHost";
import { useUi, type TakeoverState } from "../../app/store";
import { resetAllKeybindings, setKeybinding } from "../../app/keybindings";

const takeover: TakeoverState = {
  tabId: "terminal-1",
  token: "ownership-token",
  task: "检查服务状态",
  allowWrite: true,
  startedAt: Date.now(),
};

function keyDown(key: string, init: KeyboardEventInit = {}): void {
  const event = new KeyboardEvent("keydown", {
    key,
    bubbles: true,
    cancelable: true,
    ...init,
  });
  act(() => {
    window.dispatchEvent(event);
  });
}

describe("takeover accessibility and overlay priority", () => {
  let mounted: MountedView | undefined;

  beforeEach(() => {
    vi.clearAllMocks();
    mocks.takeoverExit.mockResolvedValue(undefined);
    document.body.replaceChildren();
    useUi.setState({
      takeover,
      appDialog: null,
      appChoice: null,
      textPrompt: null,
      toasts: [],
      pushToast: mocks.toast,
    });
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
    resetAllKeybindings();
    useUi.setState({ takeover: null, appDialog: null, toasts: [] });
    document.body.replaceChildren();
  });

  it("announces the reclaim key by default and omits it when unbound", async () => {
    mounted = mount(createElement(TakeoverBanner));
    const status = () => mounted?.container.querySelector('[role="status"]');
    expect(status()?.textContent).toContain("按 Esc 可立即夺回控制权");
    expect(status()?.textContent).not.toContain("未绑定");

    act(() => setKeybinding("reclaimTakeover", null));
    expect(status()?.textContent).not.toContain("未绑定");
    expect(status()?.textContent).not.toContain("按 ");
    expect(status()?.textContent).toContain("可立即夺回控制权");
    const reclaimButton = [...(mounted.container.querySelectorAll("button") ?? [])].find(
      (b) => b.getAttribute("aria-keyshortcuts") !== null,
    );
    expect(reclaimButton).toBeUndefined();
  });

  it("announces the static takeover state without making the timer live", async () => {
    mounted = mount(createElement(TakeoverBanner));
    const region = mounted.container.querySelector('[role="region"]');
    const status = mounted.container.querySelector('[role="status"]');

    expect(region?.getAttribute("aria-label")).toBe("AI 终端接管状态");
    expect(region?.getAttribute("aria-live")).toBeNull();
    expect(status?.textContent).toContain("AI 正在操作此终端，可写");
    expect(mounted.container.querySelector('[role="timer"]')?.getAttribute("aria-label")).toContain("已用时");

    keyDown("Escape", { isComposing: true });
    keyDown("Escape", { repeat: true });
    expect(mocks.takeoverExit).not.toHaveBeenCalled();
    keyDown("Escape");
    expect(mocks.takeoverExit).toHaveBeenCalledExactlyOnceWith(
      "terminal-1",
      "ownership-token",
      "用户按 Esc 夺回",
    );
    await flush();
    expect(useUi.getState().takeover).toBeNull();
  });

  it("lets a visible dialog consume Escape before takeover", async () => {
    const resolve = vi.fn();
    mounted = mount(
      createElement(
        Fragment,
        null,
        createElement(TakeoverBanner),
        createElement(DialogHost),
      ),
    );
    act(() => {
      useUi.setState({
        appDialog: { kind: "ask", message: "先关闭确认框", level: "warning", resolve },
      });
    });

    keyDown("Escape");
    expect(resolve).toHaveBeenCalledExactlyOnceWith(false);
    expect(mocks.takeoverExit).not.toHaveBeenCalled();
    expect(useUi.getState().takeover).toEqual(takeover);

    keyDown("Escape");
    expect(mocks.takeoverExit).toHaveBeenCalledOnce();
    await flush();
    expect(useUi.getState().takeover).toBeNull();
  });
});
