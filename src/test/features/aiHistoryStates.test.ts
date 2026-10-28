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
beforeEach(async () => {
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
  await flush();
});
afterEach(async () => {
  await flush();
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
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

describe("AiSidebar history conversation states", () => {
  it("shows an error and retry instead of the empty-history message when loading fails", async () => {
    mocks.conversationList.mockRejectedValueOnce(new Error("Conversation store unavailable"));
    await openHistory();
    await flushUntil(() => text().includes("无法加载历史会话：Conversation store unavailable"));
    expect(text()).not.toContain("尚无历史会话");
  });

  it("shows the empty-history message only after a successful retry with no conversations", async () => {
    mocks.conversationList.mockRejectedValueOnce(new Error("Conversation store unavailable"));
    await openHistory();
    await flushUntil(() => text().includes("无法加载历史会话"));
    const retry = [...mounted!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "重试",
    );
    expect(retry).toBeTruthy();
    click(retry!);
    await flushUntil(() => text().includes("尚无历史会话"));
    expect(mocks.conversationList).toHaveBeenCalledTimes(2);
  });

  it("renders conversation entries after loading succeeds", async () => {
    mocks.conversationList.mockResolvedValueOnce([
      { id: "c1", title: "Investigate nginx", updatedAt: 1700000000000 },
    ]);
    await openHistory();
    await flushUntil(() => text().includes("Investigate nginx"));
  });
});
