/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flush, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  getPermission: vi.fn(),
  conversationList: vi.fn(),
  conversationDelete: vi.fn(),
  messages: vi.fn(),
  runs: vi.fn(),
  overview: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../../ipc/commands", () => ({
  aiApi: {
    chat: vi.fn(),
    cancel: vi.fn(),
    confirm: vi.fn(),
    answer: vi.fn(),
    hitlSnapshot: vi.fn(),
    hitlEvents: vi.fn(),
    getPermission: mocks.getPermission,
    conversationList: mocks.conversationList,
    conversationDelete: mocks.conversationDelete,
    messages: mocks.messages,
    runs: mocks.runs,
    takeoverEnter: vi.fn(),
    takeoverRun: vi.fn(),
    takeoverExit: vi.fn(),
  },
  modelApi: { overview: mocks.overview, activate: vi.fn() },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, promptText: vi.fn() }));

import { AiSidebar, runStatusOf } from "../../features/ai/AiSidebar";
import { useUi } from "../../app/store";
import type { AiRunDto } from "../../ipc/types";

let rafQueue: { cb: FrameRequestCallback; cancelled: boolean }[] = [];

function runOf(overrides: Partial<AiRunDto>): AiRunDto {
  return {
    id: "run-1",
    conversationId: "c-1",
    status: "completed",
    attempt: 1,
    seq: 0,
    planMode: false,
    source: "chat",
    answer: "",
    turns: 0,
    tokensIn: 0,
    tokensOut: 0,
    cacheCreationTokens: 0,
    latencyMs: 0,
    retries: 0,
    failures: 0,
    createdAt: 1000,
    updatedAt: 1000,
    ...overrides,
  };
}

describe("runStatusOf", () => {
  it("flags running and unfinished interrupted runs as in-flight", () => {
    expect(runStatusOf([])).toBeNull();
    expect(runStatusOf([runOf({ status: "completed" })])).toBeNull();
    expect(runStatusOf([runOf({ status: "failed" })])).toBeNull();
    expect(runStatusOf([runOf({ status: "interrupted", finishedAt: 1500 })])).toBeNull();
    expect(runStatusOf([runOf({ status: "interrupted" })])).toBe("interrupted");
    expect(runStatusOf([runOf({ status: "running" })])).toBe("running");
    expect(
      runStatusOf([runOf({ status: "interrupted" }), runOf({ id: "r2", status: "running" })]),
    ).toBe("running");
  });
});

describe("AiSidebar 历史会话运行状态", () => {
  let mounted: MountedView | undefined;

  beforeEach(() => {
    document.body.replaceChildren();
    rafQueue = [];
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      rafQueue.push({ cb, cancelled: false });
      return rafQueue.length;
    });
    vi.stubGlobal("cancelAnimationFrame", (id: number) => {
      const entry = rafQueue[id - 1];
      if (entry) entry.cancelled = true;
    });
    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    mocks.conversationList.mockResolvedValue([
      { id: "c-1", title: "在途会话", updatedAt: 0 },
      { id: "c-2", title: "空闲会话", updatedAt: 0 },
    ]);
    mocks.conversationDelete.mockResolvedValue(undefined);
    mocks.messages.mockResolvedValue([]);
    mocks.runs.mockImplementation(async (id: string) =>
      id === "c-1" ? [runOf({ status: "running" })] : [],
    );
    mocks.ask.mockResolvedValue(true);
    localStorage.clear();
    useUi.setState({
      rightOpen: true,
      aiBusy: false,
      takeover: null,
      pushToast: mocks.toast,
      workspaces: [],
      sessions: [],
    });
    mounted = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    mounted?.unmount();
    mounted = undefined;
    localStorage.clear();
  });

  function text(): string {
    return mounted?.container.textContent ?? "";
  }

  async function openHistory() {
    const btn = mounted!.container.querySelector<HTMLButtonElement>('button[title="历史会话"]');
    if (!btn) throw new Error("history button not found");
    click(btn);
    await flush();
  }

  it("给在途会话显示运行中徽章，空闲会话无徽章", async () => {
    await openHistory();
    await flushUntil(() => text().includes("运行中"));
    expect(text()).toContain("在途会话");
    expect(text()).toContain("空闲会话");
    const badges = [...mounted!.container.querySelectorAll(".nx-badge")].map(
      (b) => b.textContent?.trim(),
    );
    expect(badges).toContain("运行中");
    expect(badges).not.toContain("待恢复");
  });

  it("中断未结束的会话显示待恢复徽章", async () => {
    mocks.runs.mockImplementation(async (id: string) =>
      id === "c-1" ? [runOf({ status: "interrupted" })] : [],
    );
    await openHistory();
    await flushUntil(() => text().includes("待恢复"));
  });

  it("徽章不混进打开按钮的文本里", async () => {
    await openHistory();
    await flushUntil(() => text().includes("运行中"));
    const row = [...mounted!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "在途会话",
    );
    expect(row).toBeTruthy();
  });

  it("阻止删除有在途运行的会话，且不弹确认框", async () => {
    await openHistory();
    await flushUntil(() => text().includes("运行中"));

    click(mounted!.container.querySelector('button[title="删除「在途会话」"]')!);
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("正在运行"));
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.conversationDelete).not.toHaveBeenCalled();
    expect(text()).toContain("在途会话");
  });

  it("阻止删除有中断待恢复运行的会话", async () => {
    mocks.runs.mockImplementation(async (id: string) =>
      id === "c-1" ? [runOf({ status: "interrupted" })] : [],
    );
    await openHistory();
    await flushUntil(() => text().includes("待恢复"));

    click(mounted!.container.querySelector('button[title="删除「在途会话」"]')!);
    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("中断待恢复"));
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.conversationDelete).not.toHaveBeenCalled();
  });

  it("空闲会话照常走确认删除", async () => {
    await openHistory();
    await flushUntil(() => text().includes("运行中"));

    click(mounted!.container.querySelector('button[title="删除「空闲会话」"]')!);
    await flushUntil(() => mocks.ask.mock.calls.length > 0);
    expect(mocks.ask).toHaveBeenCalledWith(expect.stringContaining("空闲会话"), expect.anything());
    await flushUntil(() => mocks.conversationDelete.mock.calls.length > 0);
    expect(mocks.conversationDelete).toHaveBeenCalledWith("c-2");
    expect(text()).not.toContain("空闲会话");
  });

  it("运行状态查询失败时按空闲处理，不阻塞删除", async () => {
    mocks.runs.mockRejectedValue(new Error("网络抖动"));
    await openHistory();
    await flush();

    click(mounted!.container.querySelector('button[title="删除「在途会话」"]')!);
    await flushUntil(() => mocks.ask.mock.calls.length > 0);
    expect(mocks.ask).toHaveBeenCalled();
  });
});
