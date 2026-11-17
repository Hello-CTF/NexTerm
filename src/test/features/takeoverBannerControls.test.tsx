/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  pause: vi.fn(),
  exit: vi.fn(),
  cancel: vi.fn(),
  toast: vi.fn(),
  mac: false,
}));

vi.mock("../../ipc/commands", () => ({
  aiApi: { takeoverPause: mocks.pause, takeoverExit: mocks.exit, takeoverCancel: mocks.cancel },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));

vi.mock("../../app/platform", async (importOriginal) => {
  const original = await importOriginal<typeof import("../../app/platform")>();
  return { ...original, isMac: () => mocks.mac, modHint: () => (mocks.mac ? "⌘" : "Ctrl") };
});

import { TakeoverBanner, registerTakeoverResumeHandler, withTakeoverPaused } from "../../app/TakeoverBanner";
import { useUi, type TakeoverState } from "../../app/store";

const base: TakeoverState = {
  tabId: "tab-1",
  jobId: "job-1",
  token: "token-1",
  task: "install nginx",
  allowWrite: true,
  startedAt: Date.now(),
};

function buttons(view: MountedView): HTMLButtonElement[] {
  return [...view.container.querySelectorAll("button")];
}

function toggle(view: MountedView): HTMLButtonElement {
  return buttons(view)[0];
}

function reclaim(view: MountedView): HTMLButtonElement {
  return buttons(view)[1];
}

function press(button: HTMLButtonElement) {
  act(() => button.click());
}

describe("TakeoverBanner controls", () => {
  let view: MountedView | null = null;

  beforeEach(() => {
    vi.clearAllMocks();
    mocks.mac = false;
    mocks.exit.mockResolvedValue(undefined);
    mocks.cancel.mockResolvedValue(undefined);
    localStorage.clear();
    useUi.setState({ takeover: null, pushToast: mocks.toast });
  });

  afterEach(() => {
    view?.unmount();
    view = null;
    act(() => useUi.getState().setTakeover(null));
  });

  function show(takeover: TakeoverState = base) {
    act(() => useUi.getState().setTakeover(takeover));
    view = mount(createElement(TakeoverBanner));
    return view;
  }

  it("shows pausing until the run reports paused, then offers continue", async () => {
    let reply!: (value: { phase: string }) => void;
    mocks.pause.mockImplementation(() => new Promise((resolve) => (reply = resolve)));
    const banner = show();
    expect(toggle(banner).textContent).toBe("暂停");

    press(toggle(banner));
    expect(mocks.pause).toHaveBeenCalledExactlyOnceWith("tab-1", "token-1");
    expect(toggle(banner).disabled).toBe(true);
    expect(toggle(banner).textContent).toBe("暂停中…");
    expect(banner.container.textContent).toContain("正在暂停");

    await act(async () => reply({ phase: "pausing" }));
    expect(toggle(banner).textContent).toBe("暂停中…");

    // The run's paused event lands through the AI panel as withTakeoverPaused.
    act(() => useUi.getState().setTakeover(withTakeoverPaused(useUi.getState().takeover!, true)));
    expect(toggle(banner).disabled).toBe(false);
    expect(toggle(banner).textContent).toBe("继续");
    expect(banner.container.textContent).toContain("AI 接管已暂停");
  });

  it("does not claim a pause while the AI waits for a confirmation", async () => {
    mocks.pause.mockResolvedValue({ phase: "confirm" });
    const banner = show();
    press(toggle(banner));
    await flush();
    expect(useUi.getState().takeover?.paused).not.toBe(true);
    expect(useUi.getState().takeover?.control).toBeUndefined();
    expect(toggle(banner).textContent).toBe("暂停");
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("确认"));
  });

  it("resumes the same job through the panel handler and keeps it paused when that fails", async () => {
    const handler = vi.fn().mockRejectedValueOnce(new Error("接管仍在暂停中，AI 完成当前步骤后才能继续")).mockResolvedValue(undefined);
    const unregister = registerTakeoverResumeHandler("job-1", handler);
    try {
      const banner = show(withTakeoverPaused(base, true));
      press(toggle(banner));
      await flush();
      expect(handler).toHaveBeenCalledOnce();
      expect(useUi.getState().takeover?.paused).toBe(true);
      expect(toggle(banner).disabled).toBe(false);
      expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("恢复接管失败"));

      press(toggle(banner));
      await flush();
      expect(handler).toHaveBeenCalledTimes(2);
      expect(useUi.getState().takeover?.paused).toBe(false);
      expect(toggle(banner).textContent).toBe("暂停");
    } finally {
      unregister();
    }
  });

  it("reclaims once with the user's reason and leaves no banner", async () => {
    const banner = show();
    press(reclaim(banner));
    press(reclaim(banner));
    await flush();
    expect(mocks.exit).toHaveBeenCalledExactlyOnceWith("tab-1", "token-1", "用户收回控制权");
    expect(mocks.cancel).not.toHaveBeenCalled();
    expect(useUi.getState().takeover).toBeNull();
    expect(mocks.toast).toHaveBeenCalledExactlyOnceWith("info", "已收回终端控制权，AI 已停止");
    expect(banner.container.textContent).toBe("");
  });

  it("labels the reclaim shortcut with the platform's modifier", () => {
    let banner = show();
    expect(reclaim(banner).textContent).toContain("Ctrl+Shift+E");
    expect(reclaim(banner).getAttribute("aria-keyshortcuts")).toBe("Control+Shift+E");
    banner.unmount();

    mocks.mac = true;
    view = mount(createElement(TakeoverBanner));
    banner = view;
    expect(reclaim(banner).textContent).toContain("⌘+Shift+E");
    expect(reclaim(banner).getAttribute("aria-keyshortcuts")).toBe("Meta+Shift+E");
  });
});
