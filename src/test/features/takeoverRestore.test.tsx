/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  cancel: vi.fn(),
  exit: vi.fn(),
  snapshot: vi.fn(),
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
  aiApi: { takeoverCancel: mocks.cancel, takeoverExit: mocks.exit },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: { snapshot: mocks.snapshot },
}));

import { TakeoverBanner, restoredTakeoverAlive, stealBack, takeoverEndedIn } from "../../app/TakeoverBanner";
import { useUi, type TakeoverState } from "../../app/store";

async function flushProbe() {
  for (let i = 0; i < 5; i++) await flush();
}

function takeover(overrides: Partial<TakeoverState> = {}): TakeoverState {
  return { ...mocks.seeded, startedAt: Date.now(), ...overrides };
}

describe("takeoverEndedIn", () => {
  it("reports no end for a fresh terminal without markers", () => {
    expect(takeoverEndedIn("deploy@web-01:~$ ")).toBe(false);
  });

  it("treats only a previous takeover end marker as ended", () => {
    expect(takeoverEndedIn("$ \r\n[接管结束: 任务完成]\r\ndeploy@web-01:~$ ")).toBe(true);
  });

  it("keeps the current takeover alive when a previous end marker and the current EnterBanner share the snapshot", () => {
    const text =
      "[接管结束: 任务完成]\r\n[AI 正在操作此终端 - 按 Esc 或任意键暂停]\r\ndeploy@web-01:~$ ";
    expect(takeoverEndedIn(text)).toBe(false);
  });

  it("reports ended only when an end marker appears after EnterBanner", () => {
    const text =
      "[AI 正在操作此终端 - 按 Esc 或任意键暂停]\r\n$ \r\n[接管结束: 用户退出]\r\n$ ";
    expect(takeoverEndedIn(text)).toBe(true);
  });

  it("reports ended for multiple historical end markers without EnterBanner", () => {
    const text = "[接管结束: 任务完成]\r\n[接管结束: 用户退出]\r\n$ ";
    expect(takeoverEndedIn(text)).toBe(true);
  });
});

describe("restoredTakeoverAlive", () => {
  it("keeps a restored takeover alive when the snapshot has no end marker", async () => {
    const snap = vi.fn().mockResolvedValue({ text: "deploy@web-01:~$ " });
    await expect(restoredTakeoverAlive(takeover(), snap)).resolves.toBe(true);
    expect(snap).toHaveBeenCalledWith("tab-a");
  });

  it("treats a restored takeover as ended when the snapshot contains an end marker", async () => {
    const snap = vi.fn().mockResolvedValue({ text: "[接管结束: 用户退出]\r\ndeploy@web-01:~$ " });
    await expect(restoredTakeoverAlive(takeover(), snap)).resolves.toBe(false);
  });

  it("treats a takeover as alive during persistent snapshot failures instead of clearing the banner incorrectly", async () => {
    vi.useFakeTimers();
    try {
      const snap = vi.fn().mockRejectedValue(new Error("offline"));
      const pending = restoredTakeoverAlive(takeover(), snap);
      await vi.advanceTimersByTimeAsync(3_000);
      await expect(pending).resolves.toBe(true);
      expect(snap).toHaveBeenCalledTimes(3);
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
    mocks.snapshot.mockResolvedValue({ text: "deploy@web-01:~$ " });
    useUi.setState({ pushToast: mocks.toast });
  });

  afterEach(() => {
    view?.unmount();
    view = null;
  });

  it("restores the banner after reload, then clears it and notifies when the snapshot shows the takeover ended", async () => {
    vi.resetModules();
    localStorage.setItem("nexterm.takeover.v1", JSON.stringify(mocks.seeded));
    const { TakeoverBanner: ReloadedTakeoverBanner } = await import("../../app/TakeoverBanner");
    const { useUi: reloadedUi } = await import("../../app/store");
    reloadedUi.setState({ pushToast: mocks.toast });
    mocks.snapshot.mockResolvedValue({ text: "[接管结束: 用户退出]\r\n$ " });
    view = mount(createElement(ReloadedTakeoverBanner));
    expect(view.container.textContent).toContain("AI 正在操作此终端");

    await flushProbe();
    expect(mocks.snapshot).toHaveBeenCalledWith("tab-a");
    expect(view.container.textContent).not.toContain("AI 正在操作此终端");
    expect(reloadedUi.getState().takeover).toBeNull();
    expect(mocks.toast).toHaveBeenCalledWith("info", "接管已结束");
    expect(localStorage.getItem("nexterm.takeover.v1")).toBeNull();
  });

  it("does not probe liveness for a new takeover, avoiding clearing an active banner", async () => {
    useUi.getState().setTakeover(takeover({ tabId: "tab-b", token: "token-b" }));
    view = mount(createElement(TakeoverBanner));
    await flushProbe();
    expect(mocks.snapshot).not.toHaveBeenCalled();
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
    expect(useUi.getState().takeover).toBe(current);
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringMatching(/可能仍在操作/));
  });
});
