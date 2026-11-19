/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { chooseSelectOption, click, clickButton, flush, mount, setInputValue, waitForRendered, type MountedView } from "./reactTestUtils";
import type { AiConversationSummaryDto } from "../../ipc/types";

const mocks = vi.hoisted(() => ({
  overview: vi.fn(),
  approvals: vi.fn(),
  exportConversations: vi.fn(),
  messages: vi.fn(),
  runs: vi.fn(),
  remove: vi.fn(),
  profiles: vi.fn(),
  ask: vi.fn(),
  pickSavePath: vi.fn(),
  finishSave: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    aiApi: {
      ...actual.aiApi,
      conversationOverview: mocks.overview,
      conversationApprovals: mocks.approvals,
      conversationExport: mocks.exportConversations,
      messages: mocks.messages,
      runs: mocks.runs,
      conversationDelete: mocks.remove,
    },
    modelApi: { ...actual.modelApi, overview: mocks.profiles },
  };
});
vi.mock("../../ui/dialogs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ui/dialogs")>()),
  ask: mocks.ask,
  pickSavePath: mocks.pickSavePath,
  finishSave: mocks.finishSave,
}));
vi.mock("../../features/hosts/hostLabel", () => ({
  useHostLabeler: () => (id: string | undefined) => ({ "asset-web": "web-01", "asset-db": "db-01" })[id ?? ""] ?? "",
}));

import { useUi, GLOBAL_AI_BOARD_KEY, type AppTab, type Workspace } from "../../app/store";
import { AiWorkbenchView } from "../../features/ai/workbench/AiWorkbenchView";
import { HostAiHistoryPanel } from "../../features/ai/workbench/HostAiHistoryPanel";

function conversation(overrides: Partial<AiConversationSummaryDto> = {}): AiConversationSummaryDto {
  return {
    id: "conv-web",
    title: "Restart nginx",
    assetId: "asset-web",
    status: "completed",
    runCount: 1,
    lastRunAt: 1_700_000_000_000,
    createdAt: 1_700_000_000_000,
    updatedAt: 1_700_000_100_000,
    ...overrides,
  };
}

const WEB = conversation();
const DB = conversation({ id: "conv-db", title: "Check replication", assetId: "asset-db", status: "failed" });

const MESSAGES = [
  { id: "m1", conversationId: "conv-web", role: "user", content: { role: "user", content: "please restart nginx" }, tokensIn: null, tokensOut: null, createdAt: 1 },
  { id: "m2", conversationId: "conv-web", role: "toolCall", content: { type: "toolCall", id: "call-1", name: "exec_commands" }, tokensIn: null, tokensOut: null, createdAt: 2 },
  { id: "m3", conversationId: "conv-web", role: "toolResult", content: { type: "toolResult", id: "call-1", tool: "exec_commands", ok: true, summary: "restarted" }, tokensIn: null, tokensOut: null, createdAt: 3 },
  { id: "m4", conversationId: "conv-web", role: "fileChange", content: { type: "fileChange", path: "/etc/nginx/nginx.conf", before: "a", after: "b" }, tokensIn: null, tokensOut: null, createdAt: 4 },
  { id: "m5", conversationId: "conv-web", role: "assistant", content: { role: "assistant", content: "nginx is running again" }, tokensIn: null, tokensOut: null, createdAt: 5 },
];

const RUN = {
  id: "run-1", conversationId: "conv-web", status: "completed", attempt: 1, seq: 3, planMode: false, source: "chat", profileId: "profile-1",
  answer: "", turns: 2, tokensIn: 1200, tokensOut: 300, cacheCreationTokens: 0, latencyMs: 0, retries: 1, failures: 0,
  createdAt: 1_700_000_000_000, updatedAt: 1_700_000_004_000, finishedAt: 1_700_000_004_000,
};

let mounted: MountedView | undefined;
const text = () => document.body.textContent ?? "";
const lastOverview = () => mocks.overview.mock.calls[mocks.overview.mock.calls.length - 1]?.[0] as Record<string, unknown>;

function workspace(id: string, kind: Workspace["kind"], tabs: AppTab[] = [], extra: Partial<Workspace> = {}): Workspace {
  const paneId = `${id}-pane`;
  return {
    id,
    kind,
    title: id,
    panes: [{ id: paneId, tabs, activeTabId: tabs[0]?.id ?? null }],
    activePaneId: paneId,
    closable: true,
    ...extra,
  } as Workspace;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  Element.prototype.scrollIntoView = vi.fn();
  mocks.overview.mockImplementation(async (request: { assetId?: string; status?: string; unlinked?: boolean }) => {
    const items = [WEB, DB].filter(
      (item) => (!request.assetId || item.assetId === request.assetId) && (!request.status || item.status === request.status) && !request.unlinked,
    );
    return { items, total: items.length, hosts: [{ assetId: "asset-web", count: 1 }, { assetId: "asset-db", count: 1 }] };
  });
  mocks.messages.mockResolvedValue(MESSAGES);
  mocks.runs.mockResolvedValue([RUN]);
  mocks.approvals.mockResolvedValue([
    { runId: "run-1", callId: "call-1", kind: "confirm", tool: "exec_commands", rendered: "systemctl restart nginx", decision: "allow", requestedAt: 1, resolvedAt: 2 },
  ]);
  mocks.profiles.mockResolvedValue({ profiles: [{ id: "profile-1", name: "Work", model: "gpt-x" }], activeId: "profile-1" });
  mocks.exportConversations.mockResolvedValue({ path: "/save/out", matched: 2, exported: 2, skipped: { running: 0 }, files: 2, bytes: 10 });
  mocks.remove.mockResolvedValue(undefined);
  mocks.ask.mockResolvedValue(true);
  mocks.pickSavePath.mockResolvedValue("/save/out");
  mocks.finishSave.mockResolvedValue("/save/out");
  useUi.setState({ workspaces: [], activeWorkspaceId: null, aiBoards: {}, rightOpen: false });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

async function selectConversation(title: string) {
  await waitForRendered(() => expect(text()).toContain(title));
  const row = [...document.querySelectorAll<HTMLButtonElement>('ul[aria-label="AI 会话列表"] button')].find((button) => button.textContent?.includes(title));
  if (!row) throw new Error(`row not found: ${title}`);
  click(row);
  await waitForRendered(() => expect(text()).toContain("nginx is running again"));
}

describe("HostAiHistoryPanel", () => {
  it("lists only this host's conversations, without filters", async () => {
    mounted = mount(createElement(HostAiHistoryPanel, { visible: true, assetId: "asset-web" }));
    await waitForRendered(() => expect(text()).toContain("Restart nginx"));
    expect(text()).not.toContain("Check replication");
    expect(lastOverview()).toMatchObject({ assetId: "asset-web" });
    expect(document.querySelector('button[aria-label="按主机筛选"]')).toBeNull();
    expect(document.querySelector('button[aria-label="按状态筛选"]')).toBeNull();
  });

  it("goes to the global workbench in the tools workspace with this host preselected", async () => {
    const host = workspace("ws-host", "session", [], { sessionId: "s1", assetId: "asset-web" });
    useUi.setState({ workspaces: [host], activeWorkspaceId: host.id });
    mounted = mount(createElement(HostAiHistoryPanel, { visible: true, assetId: "asset-web" }));
    await waitForRendered(() => expect(text()).toContain("Restart nginx"));
    clickButton(document.body, "去到全局");
    await flush();
    const state = useUi.getState();
    const tools = state.workspaces.find((candidate) => candidate.kind === "tools");
    const tab = tools?.panes.flatMap((pane) => pane.tabs).find((candidate) => candidate.kind === "aiWorkbench");
    expect(tab).toMatchObject({ id: "aiWorkbench", assetId: "asset-web" });
    expect(state.activeWorkspaceId).toBe(tools?.id);
  });
});

describe("conversation list width", () => {
  function listWidth(): string {
    return document.querySelector<HTMLElement>('[data-testid="host-ai-history-list"]')?.style.getPropertyValue("--nx-ai-history-list-w") ?? "";
  }

  it("resizes the host history list from its separator and keeps the width for the next view", async () => {
    localStorage.removeItem("nexterm.pane-width.v1.host-ai-history-list");
    mounted = mount(createElement(HostAiHistoryPanel, { visible: true, assetId: "asset-web" }));
    await waitForRendered(() => expect(text()).toContain("Restart nginx"));
    expect(listWidth()).toBe("280px");
    const separator = document.querySelector<HTMLElement>('[role="separator"][aria-label="调整会话列表宽度"]');
    if (!separator) throw new Error("list separator not found");
    act(() => {
      separator.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", shiftKey: true, bubbles: true }));
    });
    expect(listWidth()).toBe("230px");
    mounted.unmount();
    mounted = mount(createElement(HostAiHistoryPanel, { visible: true, assetId: "asset-web" }));
    await waitForRendered(() => expect(text()).toContain("Restart nginx"));
    expect(listWidth()).toBe("230px");
  });
});

describe("AiWorkbenchView", () => {
  it("preselects the host it was opened with and filters by status, text and the unlinked group", async () => {
    mounted = mount(createElement(AiWorkbenchView, { visible: true, assetId: "asset-db" }));
    await waitForRendered(() => expect(text()).toContain("Check replication"));
    expect(lastOverview()).toMatchObject({ assetId: "asset-db" });
    expect(text()).not.toContain("Restart nginx");

    chooseSelectOption(document.body, "按主机筛选", "全部主机");

    await flush();
    await waitForRendered(() => expect(text()).toContain("Restart nginx"));
    expect(lastOverview().assetId).toBeUndefined();

    chooseSelectOption(document.body, "按状态筛选", "失败");

    await flush();
    await waitForRendered(() => expect(lastOverview()).toMatchObject({ status: "failed" }));
    await waitForRendered(() => expect(text()).not.toContain("Restart nginx"));

    setInputValue(document.querySelector<HTMLInputElement>('input[aria-label="搜索 AI 会话"]')!, "replication");
    // The search is debounced; its timer and the reload it starts run inside act.
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 350));
    });
    await flush();
    await waitForRendered(() => expect(lastOverview()).toMatchObject({ status: "failed", query: "replication" }));

    chooseSelectOption(document.body, "按主机筛选", "未关联主机");

    await flush();
    await waitForRendered(() => expect(lastOverview()).toMatchObject({ unlinked: true }));
    expect(lastOverview().assetId).toBeUndefined();

    chooseSelectOption(document.body, "按时间筛选", "最近 7 天");

    await flush();
    await waitForRendered(() => expect(typeof lastOverview().updatedFrom).toBe("number"));
  });

  it("shows the selected conversation read-only with its runs, file changes and approvals", async () => {
    mounted = mount(createElement(AiWorkbenchView, { visible: true }));
    await selectConversation("Restart nginx");
    expect(mocks.messages).toHaveBeenCalledWith("conv-web");
    expect(mocks.messages).toHaveBeenCalledTimes(1);
    expect(text()).toContain("please restart nginx");
    expect(document.querySelector("textarea")).toBeNull();
    expect(document.querySelector('button[aria-label="编辑并重新发送"]')).toBeNull();
    const details = document.querySelector('[data-testid="ai-workbench-details"]')!;
    await waitForRendered(() => expect(details.textContent).toContain("systemctl restart nginx"));
    expect(details.textContent).toContain("/etc/nginx/nginx.conf");
    expect(details.textContent).toContain("已允许一次");
    expect(details.textContent).toContain("Work · gpt-x");
    expect(details.textContent).toContain("重试 1 次");
    expect(details.textContent).toContain("web-01");
  });

  it("continues the conversation in the AI dock", async () => {
    mounted = mount(createElement(AiWorkbenchView, { visible: true }));
    await selectConversation("Restart nginx");
    clickButton(document.body, "在侧边栏继续");
    const state = useUi.getState();
    const board = state.aiBoards[GLOBAL_AI_BOARD_KEY];
    const bound = board.tabs.filter((tab) => tab.conversationId === "conv-web");
    expect(bound).toHaveLength(1);
    expect(board.activeTabId).toBe(bound[0].id);
    expect(state.rightOpen).toBe(true);

    clickButton(document.body, "在侧边栏继续");
    expect(useUi.getState().aiBoards[GLOBAL_AI_BOARD_KEY].tabs.filter((tab) => tab.conversationId === "conv-web")).toHaveLength(1);
  });

  it("hints that a running conversation is confirmed and stopped in the dock", async () => {
    mocks.overview.mockResolvedValue({ items: [{ ...WEB, status: "running" }], total: 1, hosts: [] });
    mounted = mount(createElement(AiWorkbenchView, { visible: true }));
    await selectConversation("Restart nginx");
    expect(text()).toContain("确认操作和停止请在侧边栏中进行");
    expect(document.querySelector("textarea")).toBeNull();
  });

  it("deletes through the guarded flow and keeps a conversation whose run is going", async () => {
    mounted = mount(createElement(AiWorkbenchView, { visible: true }));
    await selectConversation("Restart nginx");
    mocks.runs.mockResolvedValueOnce([{ ...RUN, status: "running", finishedAt: undefined }]);
    click(document.querySelector('button[aria-label="删除会话「Restart nginx」"]')!);
    await flush();
    expect(mocks.remove).not.toHaveBeenCalled();
    expect(mocks.ask).not.toHaveBeenCalled();

    click(document.querySelector('button[aria-label="删除会话「Restart nginx」"]')!);
    await waitForRendered(() => expect(mocks.remove).toHaveBeenCalledWith("conv-web"));
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    await waitForRendered(() => expect(document.querySelector('ul[aria-label="AI 会话列表"]')?.textContent).not.toContain("Restart nginx"));
    expect(text()).toContain("选择左侧的会话查看内容");
  });

  it("exports the selected conversation as Markdown", async () => {
    mounted = mount(createElement(AiWorkbenchView, { visible: true }));
    await selectConversation("Restart nginx");
    clickButton(document.body, "导出 Markdown");
    await waitForRendered(() => expect(mocks.exportConversations).toHaveBeenCalled());
    expect(mocks.pickSavePath).toHaveBeenCalledWith("Restart-nginx.md");
    expect(mocks.exportConversations.mock.calls[0][0]).toMatchObject({
      format: "md",
      ids: ["conv-web"],
      path: "/save/out",
      hostNames: { "asset-web": "web-01" },
      labels: expect.objectContaining({ user: "用户" }),
    });
    await waitForRendered(() => expect(mocks.finishSave).toHaveBeenCalledWith("/save/out", "Restart-nginx.md"));
  });

  it("batch exports with the current filters as the default selection", async () => {
    mounted = mount(createElement(AiWorkbenchView, { visible: true, assetId: "asset-web" }));
    await waitForRendered(() => expect(text()).toContain("Restart nginx"));
    chooseSelectOption(document.body, "按状态筛选", "已完成");
    await flush();
    await waitForRendered(() => expect(lastOverview()).toMatchObject({ status: "completed" }));
    clickButton(document.body, "批量导出");
    await flush();
    await waitForRendered(() => expect(mocks.exportConversations).toHaveBeenCalled());
    expect(mocks.exportConversations.mock.calls[0][0]).toMatchObject({ dryRun: true, assetId: "asset-web", status: "completed", format: "zip" });
    const dialog = document.querySelector('[data-testid="ai-archive-dialog"]')!;
    await waitForRendered(() => expect(dialog.textContent).toContain("将导出 2 个"));
    click([...dialog.querySelectorAll("button")].find((button) => button.textContent === "导出")!);
    await flush();
    await waitForRendered(() => expect(mocks.finishSave).toHaveBeenCalled());
    const final = mocks.exportConversations.mock.calls.find((call) => !call[0].dryRun)![0];
    expect(final).toMatchObject({ path: "/save/out", assetId: "asset-web", status: "completed" });
    expect(mocks.pickSavePath.mock.calls[0][0]).toMatch(/^nexterm-ai-web-01-all-\d{8}\.zip$/);
  });
});
