/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  chat: vi.fn(),
  cancel: vi.fn(),
  confirm: vi.fn(),
  answer: vi.fn(),
  hitlSnapshot: vi.fn(),
  hitlEvents: vi.fn(),
  runs: vi.fn(),
  run: vi.fn(),
  runEvents: vi.fn(),
  getPermission: vi.fn(),
  setPermission: vi.fn(),
  conversationList: vi.fn(),
  conversationDelete: vi.fn(),
  messages: vi.fn(),
  overview: vi.fn(),
  usageSummary: vi.fn(),
  assetGet: vi.fn(),
  promptText: vi.fn(),
  ask: vi.fn(),
  askChoice: vi.fn(),
  takeoverEnter: vi.fn(),
  takeoverRun: vi.fn(),
  takeoverExit: vi.fn(),
  toast: vi.fn(),
  dispose: vi.fn(),
  channels: [] as { onEvent: (ev: Record<string, unknown>) => void }[],
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
    run: mocks.run,
    runEvents: mocks.runEvents,
    getPermission: mocks.getPermission,
    setPermission: mocks.setPermission,
    conversationList: mocks.conversationList,
    conversationDelete: mocks.conversationDelete,
    messages: mocks.messages,
    takeoverEnter: mocks.takeoverEnter,
    takeoverRun: mocks.takeoverRun,
    takeoverExit: mocks.takeoverExit,
  },
  assetApi: { get: mocks.assetGet },
  modelApi: { overview: mocks.overview, usageSummary: mocks.usageSummary, activate: vi.fn() },
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
  disposeChannel: mocks.dispose,
  onChannelReopen: () => () => undefined,
}));

vi.mock("../../ui/dialogs", () => ({
  ask: mocks.ask,
  askChoice: mocks.askChoice,
  promptText: mocks.promptText,
}));

import { AiSidebar } from "../../features/ai/AiSidebar";
import { aiTabBusyIds, aiTabRunControls, clearAiTabRuns } from "../../features/ai/runOwnership";
import { useUi, type WorkspaceSpec } from "../../app/store";

let view: MountedView | null = null;

function installWorkspaces(specs: WorkspaceSpec[]): string[] {
  const ids = specs.map((spec) => useUi.getState().ensureWorkspace(spec));
  useUi.setState({
    activeWorkspaceId: ids[0] ?? null,
    aiBoards: Object.fromEntries(
      ids.map((id, index) => [
        id,
        { tabs: [{ id: `ai-tab-${index}`, title: "新会话" }], activeTabId: `ai-tab-${index}` },
      ]),
    ),
  });
  return ids;
}

async function mountSidebar(props: { sessionId?: string; tabId?: string } = {}): Promise<void> {
  view = mount(createElement(AiSidebar, props));
  await flush();
}

function headerText(): string {
  return view?.container.querySelector("aside > div")?.textContent ?? "";
}

async function send(text: string): Promise<void> {
  const input = view?.container.querySelector("textarea");
  if (!input) throw new Error("message input not found");
  setInputValue(input, text);
  click(view!.container.querySelector('button[title="发送 (Enter)"]')!);
  await flush();
}

function lastChatScope(): Record<string, unknown> {
  const call = mocks.chat.mock.calls.at(-1);
  if (!call) throw new Error("chat was not called");
  return (call[0] as { scope: Record<string, unknown> }).scope;
}

describe("AiSidebar database workspace scope", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    clearAiTabRuns([...aiTabBusyIds, ...aiTabRunControls.keys()]);
    mocks.channels.length = 0;
    localStorage.clear();
    mocks.chat.mockResolvedValue({ jobId: "job-1", conversationId: "conv-1" });
    mocks.cancel.mockResolvedValue(undefined);
    mocks.confirm.mockResolvedValue(undefined);
    mocks.answer.mockResolvedValue(undefined);
    mocks.hitlSnapshot.mockResolvedValue({
      runId: "job-1",
      checkpointId: "job-1",
      status: "running",
      attempt: 1,
      seq: 0,
      pending: [],
    });
    mocks.hitlEvents.mockResolvedValue([]);
    mocks.runs.mockResolvedValue([]);
    mocks.run.mockResolvedValue({});
    mocks.runEvents.mockResolvedValue([]);
    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    mocks.setPermission.mockResolvedValue(undefined);
    mocks.conversationList.mockResolvedValue([]);
    mocks.conversationDelete.mockResolvedValue(undefined);
    mocks.messages.mockResolvedValue([]);
    mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
    mocks.usageSummary.mockResolvedValue([]);
    mocks.assetGet.mockImplementation(async (id: string) => ({
      id,
      name: id === "asset-a" ? "Orders" : "Cache",
      kind: id === "asset-a" ? "mysql" : "redis",
    }));
    mocks.promptText.mockResolvedValue("检查数据库");
    mocks.ask.mockResolvedValue(true);
    mocks.askChoice.mockResolvedValue("allow");
    mocks.takeoverEnter.mockResolvedValue({ token: "token-1" });
    mocks.takeoverRun.mockResolvedValue({ jobId: "job-takeover", token: "token-2" });
    mocks.takeoverExit.mockResolvedValue(undefined);
    useUi.setState({
      rightOpen: true,
      rightWidth: 352,
      aiBusy: false,
      aiAccountEpoch: 0,
      takeover: null,
      pushToast: mocks.toast,
      workspaces: [],
      activeWorkspaceId: null,
      aiBoards: {},
      sessions: [
        { id: "session-1", assetId: "asset-session", name: "Host", kind: "ssh", status: "connected", tabs: [], createdAt: 1 },
      ],
    });
  });

  afterEach(async () => {
    await flush();
    view?.unmount();
    view = null;
    clearAiTabRuns([...aiTabBusyIds, ...aiTabRunControls.keys()]);
    localStorage.clear();
  });

  it.each([
    ["mysql", "MySQL"],
    ["postgres", "PostgreSQL"],
    ["redis", "Redis"],
  ] as const)("shows the asset name and %s kind in the header", async (dbKind, kindLabel) => {
    const assetId = `asset-${dbKind}`;
    mocks.assetGet.mockResolvedValue({ id: assetId, name: "Orders", kind: dbKind });
    installWorkspaces([
      { kind: "db", title: "Orders", connId: `conn-${dbKind}`, assetId, dbKind },
    ]);
    await mountSidebar();

    await flushUntil(() => headerText().includes(`数据库：Orders · ${kindLabel}`));
    expect(headerText()).toContain(`数据库：Orders · ${kindLabel}`);
    expect(mocks.assetGet).toHaveBeenCalledWith(assetId);
  });

  it("sends connId and assetId in chat and permission scopes", async () => {
    installWorkspaces([
      { kind: "db", title: "Orders", connId: "conn-a", assetId: "asset-a", dbKind: "mysql" },
    ]);
    await mountSidebar();
    await send("列出数据表");

    expect(lastChatScope()).toEqual({
      sessionId: undefined,
      tabId: undefined,
      connId: "conn-a",
      assetId: "asset-a",
    });
    expect(mocks.getPermission).toHaveBeenCalledWith({
      sessionId: undefined,
      tabId: undefined,
      connId: "conn-a",
      assetId: "asset-a",
    });
    expect(mocks.getPermission).toHaveBeenCalledWith();
  });

  it("updates the label and chat scope after switching database workspaces", async () => {
    const [, boardB] = installWorkspaces([
      { kind: "db", title: "Orders", connId: "conn-a", assetId: "asset-a", dbKind: "mysql" },
      { kind: "db", title: "Cache", connId: "conn-b", assetId: "asset-b", dbKind: "redis" },
    ]);
    await mountSidebar();
    await flushUntil(() => headerText().includes("数据库：Orders · MySQL"));

    act(() => useUi.getState().setActiveWorkspace(boardB));
    await flushUntil(() => headerText().includes("数据库：Cache · Redis"));
    await send("读取缓存");

    expect(headerText()).toContain("数据库：Cache · Redis");
    expect(lastChatScope()).toMatchObject({ connId: "conn-b", assetId: "asset-b" });
  });

  it("adds the current session assetId but not connId to a host scope", async () => {
    installWorkspaces([{ kind: "session", title: "Host", sessionId: "session-1" }]);
    await mountSidebar({ sessionId: "session-1", tabId: "tab-1" });
    await send("检查负载");

    expect(lastChatScope()).toEqual({
      sessionId: "session-1",
      tabId: "tab-1",
      assetId: "asset-session",
    });
    expect(lastChatScope()).not.toHaveProperty("connId");
  });

  it("falls back to connId and the workspace kind when the asset is missing", async () => {
    mocks.assetGet.mockRejectedValue(new Error("asset not found"));
    installWorkspaces([
      { kind: "db", title: "Old database", connId: "conn-missing", assetId: "asset-missing", dbKind: "postgres" },
    ]);
    await mountSidebar();

    await flushUntil(() => mocks.assetGet.mock.calls.length === 1);
    await flush();
    expect(headerText()).toContain("数据库：conn-missing · PostgreSQL");
    expect(headerText()).not.toContain("当前实例 / 全局");
  });

  it("falls back to connId when a database workspace has no assetId", async () => {
    installWorkspaces([{ kind: "db", title: "Temporary database", connId: "conn-inline", dbKind: "redis" }]);
    await mountSidebar();

    expect(headerText()).toContain("数据库：conn-inline · Redis");
    expect(mocks.assetGet).not.toHaveBeenCalled();
  });

  it("keeps terminal takeover payloads unchanged", async () => {
    installWorkspaces([{ kind: "session", title: "Host", sessionId: "session-1" }]);
    await mountSidebar({ sessionId: "session-1", tabId: "tab-1" });
    click(view!.container.querySelector('button[aria-label="更多"]')!);
    const takeoverButton = [...view!.container.querySelectorAll("button")].find((button) =>
      button.title.startsWith("终端接管（实验性功能）"),
    );
    if (!takeoverButton) throw new Error("takeover button not found");
    click(takeoverButton);
    await flushUntil(() => mocks.takeoverRun.mock.calls.length === 1);

    expect(mocks.takeoverEnter).toHaveBeenCalledWith("tab-1");
    const args = mocks.takeoverRun.mock.calls[0][0] as Record<string, unknown>;
    expect(args).toMatchObject({
      tabId: "tab-1",
      token: "token-1",
      instruction: "检查数据库",
      allowWrite: true,
    });
    expect(args).not.toHaveProperty("scope");

    act(() => mocks.channels[0].onEvent({ type: "done", answer: "完成" }));
    await flush();
  });
});
