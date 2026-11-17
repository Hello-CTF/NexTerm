/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  cancel: vi.fn(),
  exit: vi.fn(),
  status: vi.fn(),
  toast: vi.fn(),
  seeded: {
    tabId: "tab-a",
    jobId: "job-a",
    token: "token-a",
    task: "install nginx",
    allowWrite: true,
    startedAt: 1,
  },
}));

vi.hoisted(() => {
  localStorage.setItem("nexterm.takeover.v1", JSON.stringify(mocks.seeded));
});

vi.mock("../../ipc/commands", () => ({
  aiApi: { takeoverCancel: mocks.cancel, takeoverExit: mocks.exit, takeoverStatus: mocks.status },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));

import { TakeoverBanner, restoredTakeoverStatus, stealBack } from "../../app/TakeoverBanner";
import { useUi, type TakeoverState } from "../../app/store";

async function flushProbe() {
  for (let i = 0; i < 5; i++) await flush();
}

function takeover(overrides: Partial<TakeoverState> = {}): TakeoverState {
  return { ...mocks.seeded, startedAt: Date.now(), ...overrides };
}

describe("restoredTakeoverStatus", () => {
  it("asks the backend with the restored ownership token", async () => {
    const status = vi.fn().mockResolvedValue({ active: true, jobId: "job-a", phase: "running" });
    await expect(restoredTakeoverStatus(takeover(), status)).resolves.toEqual({ active: true, jobId: "job-a", phase: "running" });
    expect(status).toHaveBeenCalledWith("tab-a", "token-a");
  });

  it("keeps the banner (no verdict) during persistent status failures instead of clearing it incorrectly", async () => {
    vi.useFakeTimers();
    try {
      const status = vi.fn().mockRejectedValue(new Error("offline"));
      const pending = restoredTakeoverStatus(takeover(), status);
      await vi.advanceTimersByTimeAsync(3_000);
      await expect(pending).resolves.toBeNull();
      expect(status).toHaveBeenCalledTimes(3);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("TakeoverBanner takeover restoration", () => {
  let view: MountedView | null = null;

  beforeEach(() => {
    vi.clearAllMocks();
    mocks.cancel.mockResolvedValue(undefined);
    mocks.exit.mockResolvedValue(undefined);
    mocks.status.mockResolvedValue({ active: true, jobId: "job-a", phase: "running" });
    useUi.setState({ pushToast: mocks.toast });
  });

  afterEach(() => {
    view?.unmount();
    view = null;
  });

  it("restores the banner after reload, then clears it and notifies when the backend reports the takeover ended", async () => {
    vi.resetModules();
    localStorage.setItem("nexterm.takeover.v1", JSON.stringify(mocks.seeded));
    const { TakeoverBanner: ReloadedTakeoverBanner } = await import("../../app/TakeoverBanner");
    const { useUi: reloadedUi } = await import("../../app/store");
    reloadedUi.setState({ pushToast: mocks.toast });
    mocks.status.mockResolvedValue({ active: false, phase: "inactive" });
    view = mount(createElement(ReloadedTakeoverBanner));
    expect(view.container.textContent).toContain("AI 正在操作此终端");

    await flushProbe();
    expect(mocks.status).toHaveBeenCalledWith("tab-a", "token-a");
    expect(view.container.textContent).not.toContain("AI 正在操作此终端");
    expect(reloadedUi.getState().takeover).toBeNull();
    expect(mocks.toast).toHaveBeenCalledWith("info", "接管已结束");
    expect(localStorage.getItem("nexterm.takeover.v1")).toBeNull();
  });

  it("shows a restored takeover the backend reports paused as paused", async () => {
    vi.resetModules();
    localStorage.setItem("nexterm.takeover.v1", JSON.stringify(mocks.seeded));
    const { TakeoverBanner: ReloadedTakeoverBanner } = await import("../../app/TakeoverBanner");
    const { useUi: reloadedUi } = await import("../../app/store");
    reloadedUi.setState({ pushToast: mocks.toast });
    mocks.status.mockResolvedValue({ active: true, jobId: "job-a", phase: "paused" });
    view = mount(createElement(ReloadedTakeoverBanner));

    await flushProbe();
    expect(reloadedUi.getState().takeover?.paused).toBe(true);
    expect(view.container.textContent).toContain("AI 接管已暂停");
    act(() => reloadedUi.getState().setTakeover(null));
  });

  it("does not probe liveness for a new takeover, avoiding clearing an active banner", async () => {
    useUi.getState().setTakeover(takeover({ tabId: "tab-b", token: "token-b" }));
    view = mount(createElement(TakeoverBanner));
    await flushProbe();
    expect(mocks.status).not.toHaveBeenCalled();
    expect(view.container.textContent).toContain("AI 正在操作此终端");
    act(() => useUi.getState().setTakeover(null));
  });
});

describe("stealBack reports ended takeovers truthfully", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.cancel.mockResolvedValue(undefined);
    useUi.setState({ pushToast: mocks.toast });
  });

  it("clears the banner and reports ended instead of erroring for an expired token", async () => {
    useUi.getState().setTakeover(takeover({ token: "stale" }));
    mocks.exit.mockRejectedValue(new Error("接管令牌已过期"));
    await stealBack();
    expect(useUi.getState().takeover).toBeNull();
    expect(mocks.cancel).toHaveBeenCalledWith("job-a");
    expect(mocks.toast).toHaveBeenCalledWith("info", "接管已结束，终端控制权已恢复。");
  });

  it("also treats a missing task as ended", async () => {
    useUi.getState().setTakeover(takeover());
    mocks.exit.mockRejectedValue(new Error("接管任务不存在或已结束"));
    await stealBack();
    expect(useUi.getState().takeover).toBeNull();
    expect(mocks.toast).toHaveBeenCalledWith("info", "接管已结束，终端控制权已恢复。");
  });

  it("keeps the banner and reports errors for other failures", async () => {
    const current = takeover();
    useUi.getState().setTakeover(current);
    mocks.exit.mockRejectedValue(new Error("offline"));
    await stealBack();
    expect(useUi.getState().takeover).toEqual(current);
    expect(useUi.getState().takeover?.control).toBeUndefined();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringMatching(/可能仍在操作/));
  });
});
