/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
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
    task: "安装 nginx",
    allowWrite: true,
    startedAt: 1,
  },
}));

vi.hoisted(() => {
  localStorage.setItem("nexterm.takeover.v1", JSON.stringify(mocks.seeded));
});

vi.mock("../../ipc/commands", () => ({
  aiApi: { cancel: mocks.cancel, takeoverExit: mocks.exit },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: { snapshot: mocks.snapshot },
}));

import { TakeoverBanner, restoredTakeoverAlive, stealBack } from "../../app/TakeoverBanner";
import { useUi, type TakeoverState } from "../../app/store";

async function flushProbe() {
  for (let i = 0; i < 5; i++) await flush();
}

function takeover(overrides: Partial<TakeoverState> = {}): TakeoverState {
  return { ...mocks.seeded, startedAt: Date.now(), ...overrides };
}

describe("restoredTakeoverAlive", () => {
  it("快照里没有接管结束标记时认为接管仍存活", async () => {
    const snap = vi.fn().mockResolvedValue({ text: "deploy@web-01:~$ " });
    await expect(restoredTakeoverAlive(takeover(), snap)).resolves.toBe(true);
    expect(snap).toHaveBeenCalledWith("tab-a");
  });

  it("快照里出现接管结束标记时认为接管已结束", async () => {
    const snap = vi.fn().mockResolvedValue({ text: "[接管结束: 用户退出]\r\ndeploy@web-01:~$ " });
    await expect(restoredTakeoverAlive(takeover(), snap)).resolves.toBe(false);
  });

  it("快照持续失败时按存活处理，不误清横幅", async () => {
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

describe("TakeoverBanner 接管恢复", () => {
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

  it("重载后恢复横幅；快照显示接管已结束时清掉横幅并提示", async () => {
    mocks.snapshot.mockResolvedValue({ text: "[接管结束: 用户退出]\r\n$ " });
    view = mount(createElement(TakeoverBanner));
    expect(view.container.textContent).toContain("AI 正在操作此终端");

    await flushProbe();
    expect(mocks.snapshot).toHaveBeenCalledWith("tab-a");
    expect(view.container.textContent).not.toContain("AI 正在操作此终端");
    expect(useUi.getState().takeover).toBeNull();
    expect(mocks.toast).toHaveBeenCalledWith("info", "接管已结束");
    expect(localStorage.getItem("nexterm.takeover.v1")).toBeNull();
  });

  it("新的接管不触发存活探测，避免误清进行中的横幅", async () => {
    useUi.getState().setTakeover(takeover({ tabId: "tab-b", token: "token-b" }));
    view = mount(createElement(TakeoverBanner));
    await flushProbe();
    expect(mocks.snapshot).not.toHaveBeenCalled();
    expect(view.container.textContent).toContain("AI 正在操作此终端");
    useUi.getState().setTakeover(null);
  });
});

describe("stealBack 对已结束的接管保持诚实", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.cancel.mockResolvedValue(undefined);
    useUi.setState({ pushToast: mocks.toast });
  });

  it("令牌过期时清横幅并提示已结束，而不是报错", async () => {
    useUi.getState().setTakeover(takeover({ token: "stale" }));
    mocks.exit.mockRejectedValue(new Error("接管令牌已过期"));
    await stealBack();
    expect(useUi.getState().takeover).toBeNull();
    expect(mocks.toast).toHaveBeenCalledWith("info", "接管已结束，终端已在你手中");
  });

  it("任务不存在时同样按已结束处理", async () => {
    useUi.getState().setTakeover(takeover());
    mocks.exit.mockRejectedValue(new Error("接管任务不存在或已结束"));
    await stealBack();
    expect(useUi.getState().takeover).toBeNull();
    expect(mocks.toast).toHaveBeenCalledWith("info", "接管已结束，终端已在你手中");
  });

  it("其他失败仍然保留横幅并报错", async () => {
    const current = takeover();
    useUi.getState().setTakeover(current);
    mocks.exit.mockRejectedValue(new Error("offline"));
    await stealBack();
    expect(useUi.getState().takeover).toBe(current);
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringMatching(/可能仍在操作/));
  });
});
