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
vi.mock("../../ui/dialogs", () => ({ ask: vi.fn(), promptText: vi.fn() }));

import { AiSidebar } from "../../features/ai/AiSidebar";
import { useUi } from "../../app/store";

let rafQueue: { cb: FrameRequestCallback; cancelled: boolean }[] = [];

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
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
  mocks.conversationList.mockResolvedValue([]);
  mocks.conversationDelete.mockResolvedValue(undefined);
  mocks.messages.mockResolvedValue([]);
  mocks.runs.mockResolvedValue([]);
  mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
  useUi.setState({
    rightOpen: true,
    aiBusy: false,
    takeover: null,
    pushToast: mocks.toast,
    workspaces: [],
    aiBoards: {},
    sessions: [],
  });
  mounted = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
});
afterEach(() => {
  vi.unstubAllGlobals();
  mounted?.unmount();
  mounted = undefined;
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

describe("AiSidebar 历史会话状态", () => {
  it("加载失败时显示错误与重试，而不是「还没有历史会话」", async () => {
    mocks.conversationList.mockRejectedValueOnce(new Error("会话库不可用"));
    await openHistory();
    await flushUntil(() => text().includes("历史会话加载失败 · 会话库不可用"));
    expect(text()).not.toContain("还没有历史会话");
  });

  it("重试后恢复，真空才显示「还没有历史会话」", async () => {
    mocks.conversationList.mockRejectedValueOnce(new Error("会话库不可用"));
    await openHistory();
    await flushUntil(() => text().includes("历史会话加载失败"));
    const retry = [...mounted!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "重试",
    );
    expect(retry).toBeTruthy();
    click(retry!);
    await flushUntil(() => text().includes("还没有历史会话"));
    expect(mocks.conversationList).toHaveBeenCalledTimes(2);
  });

  it("加载成功后渲染会话条目", async () => {
    mocks.conversationList.mockResolvedValueOnce([
      { id: "c1", title: "排查 nginx", updatedAt: 1700000000000 },
    ]);
    await openHistory();
    await flushUntil(() => text().includes("排查 nginx"));
  });
});
