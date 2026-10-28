/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  chat: vi.fn(),
  cancel: vi.fn(),
  confirm: vi.fn(),
  answer: vi.fn(),
  hitlSnapshot: vi.fn(),
  hitlEvents: vi.fn(),
  runs: vi.fn(),
  runEvents: vi.fn(),
  getPermission: vi.fn(),
  conversationList: vi.fn(),
  conversationDelete: vi.fn(),
  messages: vi.fn(),
  overview: vi.fn(),
  promptText: vi.fn(),
  ask: vi.fn(),
  takeoverEnter: vi.fn(),
  takeoverRun: vi.fn(),
  takeoverExit: vi.fn(),
  toast: vi.fn(),
  assetList: vi.fn(),
  call: vi.fn(),
  channels: [] as { onEvent: (ev: Record<string, unknown>) => void }[],
  reopens: new Map<unknown, () => void>(),
}));

vi.mock("../../ipc/commands", () => ({
  aiApi: {
    chat: mocks.chat,
    cancel: mocks.cancel,
    confirm: mocks.confirm,
    answer: mocks.answer,
    hitlSnapshot: mocks.hitlSnapshot,
    hitlEvents: mocks.hitlEvents,
    runs: mocks.runs,
    runEvents: mocks.runEvents,
    getPermission: mocks.getPermission,
    conversationList: mocks.conversationList,
    conversationDelete: mocks.conversationDelete,
    messages: mocks.messages,
    takeoverEnter: mocks.takeoverEnter,
    takeoverRun: mocks.takeoverRun,
    takeoverExit: mocks.takeoverExit,
  },
  modelApi: { overview: mocks.overview, activate: vi.fn() },
  assetApi: { list: mocks.assetList },
  call: mocks.call,
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));
vi.mock("../../ipc/events", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ipc/events")>()),
  createAiChannel: (onEvent: (ev: Record<string, unknown>) => void) => {
    const channel = { onEvent };
    mocks.channels.push(channel);
    return channel;
  },
  disposeChannel: vi.fn(),
  onChannelReopen: (channel: unknown, cb: () => void) => {
    mocks.reopens.set(channel, cb);
    return () => mocks.reopens.delete(channel);
  },
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, promptText: mocks.promptText }));

import { AiSidebar } from "../../features/ai/AiSidebar";
import { aiTabBusyIds, aiTabRunControls, clearAiTabRuns } from "../../features/ai/runOwnership";
import { resetProductionClassificationCache } from "../../features/production/productionSettings";
import { useUi } from "../../app/store";
import { resetAllKeybindings } from "../../app/keybindings";

const SETTINGS_DTO = {
  productionEnvironments: [] as string[],
  cronConnectOptIn: false,
  cronConnectProductionOptIn: false,
  broadcastConnectOptIn: false,
  broadcastConnectProductionOptIn: false,
  targetSetConnectOptIn: false,
  targetSetConnectProductionOptIn: false,
};

let mounted: MountedView | undefined;

function seedWorkspace(assetTags: string): void {
  useUi.setState({
    rightOpen: true,
    rightWidth: 352,
    aiBusy: false,
    takeover: null,
    pushToast: mocks.toast,
    aiBoards: {},
    accountEpoch: 0,
    sessions: [{ id: "s1", assetId: "a-s1", name: "one", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }],
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title: "ws-1",
        closable: true,
        sessionId: "s1",
        assetId: "a-s1",
        panes: [{ id: "p1", activeTabId: "t1", tabs: [] }],
        activePaneId: "p1",
      },
    ],
    activeWorkspaceId: "ws1",
  });
  mocks.assetList.mockResolvedValue([{ id: "a-s1", tags: assetTags }]);
}

function notice(): HTMLElement | null {
  return mounted?.container.querySelector<HTMLElement>('[role="note"]') ?? null;
}

beforeEach(() => {
  clearAiTabRuns([...aiTabBusyIds, ...aiTabRunControls.keys()]);
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  resetAllKeybindings();
  mocks.channels.length = 0;
  mocks.reopens.clear();
  mocks.chat.mockResolvedValue({ jobId: "job-1", conversationId: "conv-1" });
  mocks.cancel.mockResolvedValue(undefined);
  mocks.confirm.mockResolvedValue(undefined);
  mocks.answer.mockResolvedValue(undefined);
  mocks.hitlSnapshot.mockResolvedValue({ runId: "job-1", checkpointId: "job-1", status: "running", attempt: 1, seq: 0, pending: [] });
  mocks.hitlEvents.mockResolvedValue([]);
  mocks.runs.mockResolvedValue([]);
  mocks.runEvents.mockResolvedValue([]);
  mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
  mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
  mocks.conversationList.mockResolvedValue([]);
  mocks.conversationDelete.mockResolvedValue(undefined);
  mocks.messages.mockResolvedValue([]);
  mocks.takeoverEnter.mockResolvedValue({ token: "tok-1" });
  mocks.takeoverRun.mockResolvedValue({ jobId: "job-t", token: "tok-2" });
  mocks.takeoverExit.mockResolvedValue(undefined);
  mocks.call.mockImplementation((cmd: string) => {
    if (cmd === "production_settings_get") return Promise.resolve(SETTINGS_DTO);
    return Promise.reject(new Error(`unexpected command ${cmd}`));
  });
  resetProductionClassificationCache();
  seedWorkspace("env:production");
});

afterEach(async () => {
  await flush();
  mounted?.unmount();
  mounted = undefined;
  resetProductionClassificationCache();
  resetAllKeybindings();
});

describe("AI scope production warning", () => {
  it("shows the write-risk notice when the scoped asset is production-classified", async () => {
    mounted = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flush();
    await flushUntil(() => notice() !== null);
    const text = notice()?.textContent ?? "";
    expect(text).toContain("生产环境写入风险");
    expect(text).toContain("production");
    expect(text).toContain("AI");
    expect(text).toContain("不会记住授权");
  });

  it("stays silent for a non-production scope and clears on account switch", async () => {
    seedWorkspace("env:staging");
    mounted = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flush();
    await flush();
    expect(notice()).toBeNull();

    mocks.assetList.mockResolvedValue([{ id: "a-s1", tags: "env:production" }]);
    act(() => {
      useUi.getState().advanceAccountEpoch();
    });
    await flush();
    await flushUntil(() => notice() !== null);
    expect(notice()?.textContent).toContain("生产环境写入风险");

    mocks.assetList.mockResolvedValue([{ id: "a-s1", tags: "env:staging" }]);
    act(() => {
      useUi.getState().advanceAccountEpoch();
    });
    await flush();
    await flushUntil(() => notice() === null);
  });
});
