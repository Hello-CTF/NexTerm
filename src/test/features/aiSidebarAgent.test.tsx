/** @vitest-environment jsdom */

// AiSidebar production mounting: the reviewed agent panels and the M128 watch
// panel run inside the real sidebar against the real production adapters
// (typed IPC only), with the transport mocked at the commands seam.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, clickButton, deferred, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  call: vi.fn(),
  assetList: vi.fn(),
  listLive: vi.fn(),
  listDurable: vi.fn(),
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
  cronList: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
  listenEvent: vi.fn(),
  channels: [] as { onEvent: (ev: Record<string, unknown>) => void }[],
  rosterProps: null as { api: import("../../features/ai/agentApi").AgentRosterApi } | null,
}));

vi.mock("../../ipc/commands", () => ({
  call: mocks.call,
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
  },
  modelApi: { overview: mocks.overview, activate: vi.fn() },
  assetApi: { list: mocks.assetList },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: { listLive: mocks.listLive, listDurable: mocks.listDurable },
}));
vi.mock("../../ipc/events", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ipc/events")>()),
  listenEvent: mocks.listenEvent,
  createAiChannel: (onEvent: (ev: Record<string, unknown>) => void) => {
    const channel = { onEvent };
    mocks.channels.push(channel);
    return channel;
  },
  disposeChannel: vi.fn(),
  onChannelReopen: () => () => undefined,
}));
vi.mock("../../ipc/cron", () => ({
  cronApi: {
    list: mocks.cronList,
    register: vi.fn(),
    setEnabled: vi.fn(),
    unregister: vi.fn(),
  },
  cronTimeoutMs: (job: { timeout: number }) => Math.round(job.timeout / 1_000_000),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, promptText: vi.fn() }));
// Capture the production roster api the sidebar builds, so the approval
// delegate can be exercised exactly as the review contract consumes it.
vi.mock("../../features/ai/AgentRosterPanel", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../features/ai/AgentRosterPanel")>();
  const { createElement: create } = await import("react");
  return {
    ...actual,
    AgentRosterPanel: (props: Parameters<typeof actual.AgentRosterPanel>[0]) => {
      mocks.rosterProps = { api: props.api };
      return create(actual.AgentRosterPanel, props);
    },
  };
});

import { AiSidebar } from "../../features/ai/AiSidebar";
import { useUi } from "../../app/store";

let convSeq = 0;

function textOf(view: MountedView): string {
  return view.container.textContent ?? "";
}

function tasksButton(view: MountedView): HTMLButtonElement {
  const button = view.container.querySelector<HTMLButtonElement>('button[aria-label^="后台任务"]');
  if (!button) throw new Error("background tasks button not found");
  return button;
}
const agentButton = tasksButton;
const watchButton = tasksButton;

async function send(view: MountedView, text: string) {
  const textarea = view.container.querySelector("textarea");
  if (!textarea) throw new Error("input textarea not found");
  setInputValue(textarea, text);
  click(view.container.querySelector('button[title="发送 (Enter)"]')!);
  await flush();
}

function emit(ev: Record<string, unknown>) {
  const channel = mocks.channels[mocks.channels.length - 1];
  if (!channel) throw new Error("no fake channel");
  act(() => channel.onEvent(ev));
}

function rosterListRow() {
  return {
    entry: {
      id: "row-1",
      conversationId: "conv-1",
      handle: "build",
      alias: "build",
      tabId: "tab-agent-1",
      state: "active",
      incarnation: "inc-1",
      jobId: "job-1",
      createdAt: 1_760_000_111_000,
      updatedAt: 1_760_000_222_000,
      releasedAt: null,
    },
    presence: "connected",
    assetId: "asset-1",
    host: "web-01",
    cwd: "/srv/app",
  };
}

describe("AiSidebar agent production mounting", () => {
  let view: MountedView | null = null;

  beforeEach(async () => {
    convSeq = 0;
    mocks.channels.length = 0;
    mocks.rosterProps = null;
    mocks.chat.mockImplementation(() => {
      convSeq += 1;
      return Promise.resolve({ jobId: `job-${convSeq}`, conversationId: `conv-${convSeq}` });
    });
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
    mocks.runEvents.mockResolvedValue([]);
    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    mocks.conversationList.mockResolvedValue([]);
    mocks.conversationDelete.mockResolvedValue(undefined);
    mocks.messages.mockResolvedValue([]);
    mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
    mocks.cronList.mockResolvedValue([]);
    mocks.ask.mockResolvedValue(true);
    mocks.assetList.mockResolvedValue([{ id: "asset-1", tags: "env:production" }]);
    mocks.listLive.mockResolvedValue([]);
    mocks.listDurable.mockResolvedValue([]);
    mocks.listenEvent.mockImplementation(() => Promise.resolve(vi.fn()));
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") return Promise.resolve([rosterListRow()]);
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      if (cmd === "ai_watch_list") return Promise.resolve([]);
      if (cmd === "ai_watch_targets") return Promise.resolve([]);
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    localStorage.clear();
    useUi.setState({
      rightOpen: true,
      aiBusy: false,
      takeover: null,
      pushToast: mocks.toast,
      workspaces: [],
      aiBoards: {},
      sessions: [],
      activeWorkspaceId: null,
    });
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flush();
  });

  afterEach(async () => {
    await flush();
    view?.unmount();
    view = null;
  });

  it("hides the background tasks button while there is nothing to manage", async () => {
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") return Promise.resolve([]);
      if (cmd === "ai_watch_list") return Promise.resolve([]);
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    expect(view!.container.querySelector('button[aria-label^="后台任务"]')).toBeNull();
    await send(view!, "Question A");
    emit({ type: "done", answer: "Answer A" });
    await flush();
    expect(view!.container.querySelector('button[aria-label^="后台任务"]')).toBeNull();
  });

  it("shows the button with a count once the conversation has a live sub-agent", async () => {
    await send(view!, "Question A");
    emit({ type: "done", answer: "Answer A" });
    await flushUntil(() => view!.container.querySelector('button[aria-label^="后台任务"]') !== null);
    await flush();
    expect(tasksButton(view!).getAttribute("aria-label")).toBe("后台任务，共 1 项");
    expect(tasksButton(view!).getAttribute("aria-expanded")).toBe("false");
  });

  it("mounts the sub-agent section on real IPC after a conversation starts", async () => {
    await send(view!, "Question A");
    emit({ type: "done", answer: "Answer A" });
    await flushUntil(() => view!.container.querySelector('button[aria-label^="后台任务"]') !== null);

    click(agentButton(view!));
    await flushUntil(() => view!.container.querySelector('[data-handle="build"]') !== null);
    expect(textOf(view!)).toContain("子代理与终端会话");
    expect(textOf(view!)).not.toContain("Run 历史");
    expect(mocks.call).toHaveBeenCalledWith("ai_roster_list", { conversationId: "conv-1" });
    const row = view!.container.querySelector('[data-handle="build"]');
    expect(row).not.toBeNull();
    expect(row!.textContent).toContain("生产环境");

    click(agentButton(view!));
    await flush();
    expect(textOf(view!)).not.toContain("子代理与终端会话");
  });

  it("mentions a roster handle into the composer through the production flow", async () => {
    await send(view!, "Question A");
    emit({ type: "done", answer: "Answer A" });
    await flush();
    click(agentButton(view!));
    await flushUntil(() => view!.container.querySelector('[data-handle="build"]') !== null);

    const row = view!.container.querySelector('[data-handle="build"]') as HTMLElement;
    clickButton(row, "提及");
    const textarea = view!.container.querySelector("textarea")!;
    expect(textarea.value).toContain("@build");
  });

  it("opens the roster terminal as a tab bound to the real backend tab", async () => {
    await send(view!, "Question A");
    emit({ type: "done", answer: "Answer A" });
    await flush();
    act(() => {
      useUi.setState({
        sessions: [
          {
            id: "s1",
            assetId: "asset-1",
            name: "prod-host",
            kind: "ssh",
            status: "connected",
            tabs: [],
            createdAt: 0,
          },
        ],
      });
    });
    mocks.listLive.mockResolvedValue([
      {
        tabId: "tab-agent-1",
        sessionId: "s1",
        sessionName: "prod-host",
        sessionKind: "ssh",
        cols: 80,
        rows: 24,
        sizeOwner: null,
        subscribers: 1,
        viewers: 0,
        exited: false,
      },
    ]);
    click(agentButton(view!));
    await flushUntil(() => view!.container.querySelector('[data-handle="build"]') !== null);

    const row = view!.container.querySelector('[data-handle="build"]') as HTMLElement;
    clickButton(row, "打开");
    await flushUntil(() =>
      useUi.getState().workspaces.some((workspace) =>
        workspace.panes.some((pane) => pane.tabs.some((tab) => tab.tabId === "tab-agent-1")),
      ),
    );
    const tabs = useUi.getState().workspaces.flatMap((workspace) => workspace.panes.flatMap((pane) => pane.tabs));
    const opened = tabs.find((tab) => tab.tabId === "tab-agent-1");
    expect(opened?.kind).toBe("terminal");
    expect(opened?.sessionId).toBe("s1");
    expect(opened?.title).toBe("build");
  });

  it("mounts the watch panel on the real watchPanelApi", async () => {
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_watch_list") {
        return Promise.resolve([
          {
            id: "w-1",
            targetId: "target-1",
            name: "ready-watch",
            condition: { type: "contains", pattern: "ready" },
            status: "armed",
            repeat: false,
            anchorSeq: 5,
            generation: 1,
            revision: 2,
            updatedAt: 0,
          },
        ]);
      }
      if (cmd === "ai_watch_targets") return Promise.resolve([]);
      if (cmd === "ai_roster_list") return Promise.resolve([rosterListRow()]);
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });

    await send(view!, "Question A");
    emit({ type: "done", answer: "Answer A" });
    await flushUntil(() => view!.container.querySelector('button[aria-label^="后台任务"]') !== null);
    click(watchButton(view!));
    await flushUntil(() => textOf(view!).includes("ready-watch"));
    expect(mocks.call).toHaveBeenCalledWith("ai_watch_list");
    // No raw owner id or English product title leaks into the panel.
    expect(textOf(view!).toLowerCase()).not.toContain("local");
    expect(textOf(view!)).not.toContain("Watch");

    click(watchButton(view!));
    await flush();
    expect(textOf(view!)).not.toContain("ready-watch");
  });

  it("resolves approvals through the pending HITL interaction with mapped decisions", async () => {
    await send(view!, "Question A");
    await flushUntil(() => useUi.getState().aiBusy === true);
    emit({
      type: "confirmRequired",
      id: "call-9",
      tool: "exec_commands",
      rendered: "$ rm -rf /tmp/x",
      confirmationNonce: "n-9",
      requestId: "req-9",
      attempt: 1,
    });
    await flushUntil(() => textOf(view!).includes("需要你确认"));
    click(agentButton(view!));
    await flushUntil(() => mocks.rosterProps !== null);
    const api = mocks.rosterProps!.api;

    await act(async () => {
      await api.resolveApproval("req-9", "approve_once");
    });
    expect(mocks.confirm).toHaveBeenCalledWith({
      jobId: "job-1",
      callId: "call-9",
      nonce: "n-9",
      decision: "allow",
    });
    await expect(api.resolveApproval("req-gone", "deny")).rejects.toMatchObject({ code: "conflict" });
  });

  it("sends the user's typed production environment names through the approval delegate", async () => {
    await send(view!, "Question A");
    await flushUntil(() => useUi.getState().aiBusy === true);
    emit({
      type: "confirmRequired",
      id: "call-9",
      tool: "exec_commands",
      rendered: "$ systemctl restart nginx",
      confirmationNonce: "n-9",
      requestId: "req-9",
      attempt: 1,
      requiredEnvironmentNames: ["production"],
    });
    await flushUntil(() => textOf(view!).includes("需要你确认"));
    click(agentButton(view!));
    await flushUntil(() => view!.container.querySelector('[data-handle="build"]') !== null);
    await flush();
    const api = mocks.rosterProps!.api;

    // The challenge is incomplete: the delegate refuses before any backend call.
    await act(async () => {
      await expect(api.resolveApproval("req-9", "approve_once")).rejects.toMatchObject({
        code: "conflict",
        message: expect.stringContaining("生产环境挑战未完成"),
      });
    });
    expect(mocks.confirm).not.toHaveBeenCalled();

    // The user's actual typed names — and only those — reach the backend.
    const envInput = view!.container.querySelector<HTMLInputElement>('input[aria-label="输入环境名 production"]');
    if (!envInput) throw new Error("environment input not found");
    setInputValue(envInput, "production");
    await flush();
    await act(async () => {
      await api.resolveApproval("req-9", "approve_once");
    });
    expect(mocks.confirm).toHaveBeenCalledWith({
      jobId: "job-1",
      callId: "call-9",
      nonce: "n-9",
      decision: "allow",
      environmentNames: ["production"],
    });
  });

  it("explains managed-device confirmation in plain Chinese while the typed value stays managed-device", async () => {
    await send(view!, "Question A");
    await flushUntil(() => useUi.getState().aiBusy === true);
    emit({
      type: "confirmRequired",
      id: "call-9",
      tool: "exec_commands",
      rendered: "$ uptime",
      confirmationNonce: "n-9",
      requestId: "req-9",
      attempt: 1,
      requiredEnvironmentNames: ["managed-device"],
    });
    await flushUntil(() => textOf(view!).includes("需要你确认"));
    const text = textOf(view!);
    expect(text).toContain("受管设备（无环境标签，按生产环境处理）");
    expect(text).toContain("每台主机都需要输入确认");
    const envInput = view!.container.querySelector<HTMLInputElement>('input[aria-label="输入环境名 managed-device"]');
    if (!envInput) throw new Error("environment input not found");
    setInputValue(envInput, "managed-device");
    await flush();
    expect(view!.container.textContent).not.toContain("环境名不匹配");
  });

  it("replays HITL state and surfaces the failure when an approval submission fails", async () => {
    await send(view!, "Question A");
    await flushUntil(() => useUi.getState().aiBusy === true);
    emit({
      type: "confirmRequired",
      id: "call-9",
      tool: "exec_commands",
      rendered: "$ rm -rf /tmp/x",
      confirmationNonce: "n-9",
      requestId: "req-9",
      attempt: 1,
    });
    await flushUntil(() => textOf(view!).includes("需要你确认"));
    click(agentButton(view!));
    await flushUntil(() => view!.container.querySelector('[data-handle="build"]') !== null);
    await flush();
    const api = mocks.rosterProps!.api;

    const hitlCallsBefore = mocks.hitlEvents.mock.calls.length + mocks.hitlSnapshot.mock.calls.length;
    // The backend still holds the pending request: the replay after a failed
    // submission re-presents the same card instead of losing it.
    const interrupt = {
      id: "req-9",
      runId: "job-1",
      checkpointId: "job-1",
      checkpointHash: "hash-9",
      targetId: "target-9",
      callId: "call-9",
      tool: "exec_commands",
      kind: "confirm",
      parameters: {},
      parameterHash: "phash-9",
      nonce: "n-9",
      createdAt: "2026-10-06T10:00:00Z",
      expiresAt: "2099-10-06T10:05:00Z",
      attempt: 1,
      seq: 1,
    };
    mocks.hitlSnapshot.mockResolvedValue({
      runId: "job-1",
      checkpointId: "job-1",
      status: "running",
      attempt: 1,
      seq: 1,
      pending: [interrupt],
    });
    mocks.hitlEvents.mockResolvedValue([
      {
        type: "confirmRequired",
        id: "call-9",
        tool: "exec_commands",
        rendered: "$ rm -rf /tmp/x",
        confirmationNonce: "n-9",
        requestId: "req-9",
        attempt: 1,
        seq: 1,
      },
    ]);
    mocks.confirm.mockRejectedValueOnce(new Error("backend down"));
    await act(async () => {
      await expect(api.resolveApproval("req-9", "approve_once")).rejects.toMatchObject({ code: "internal" });
    });
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("无法提交选择"));
    // The failure replays the HITL state instead of resolving the card away.
    await flushUntil(
      () => mocks.hitlEvents.mock.calls.length + mocks.hitlSnapshot.mock.calls.length > hitlCallsBefore,
    );
    expect(textOf(view!)).toContain("需要你确认");

    // The pending card survives the failure and a retry reaches the backend.
    await act(async () => {
      await api.resolveApproval("req-9", "deny");
    });
    expect(mocks.confirm).toHaveBeenLastCalledWith({
      jobId: "job-1",
      callId: "call-9",
      nonce: "n-9",
      decision: "deny",
    });
  });

  function sessionRow(id: string, kind: string, status: string) {
    return { id, assetId: `asset-${id}`, name: id, kind, status, tabs: [], createdAt: 0 };
  }

  function durableHit(tabId: string) {
    return [
      {
        id: tabId,
        incarnation: "inc-tab",
        createdAt: 1_760_000_000_000,
        metadataRevision: 0,
        cols: 80,
        rows: 24,
        dead: false,
        exitCode: null,
        finishedAt: null,
      },
    ];
  }

  async function openBuildRow() {
    await send(view!, "Question A");
    emit({ type: "done", answer: "Answer A" });
    await flush();
    click(agentButton(view!));
    await flushUntil(() => view!.container.querySelector('[data-handle="build"]') !== null);
    clickButton(view!.container.querySelector('[data-handle="build"]') as HTMLElement, "打开");
  }

  function openedAgentTab() {
    return useUi
      .getState()
      .workspaces.flatMap((workspace) => workspace.panes.flatMap((pane) => pane.tabs))
      .find((tab) => tab.tabId === "tab-agent-1");
  }

  it("opens a durable-only roster terminal probing only connected ssh/device sessions", async () => {
    act(() => {
      useUi.setState({
        sessions: [
          sessionRow("s-ssh", "ssh", "connected") as never,
          sessionRow("s-local", "local", "connected") as never,
          sessionRow("s-down", "ssh", "disconnected") as never,
        ],
      });
    });
    mocks.listLive.mockResolvedValue([]);
    mocks.listDurable.mockImplementation((sessionId: string) => {
      if (sessionId === "s-ssh") return Promise.resolve(durableHit("tab-agent-1"));
      return Promise.reject(new Error(`must not probe ${sessionId}`));
    });

    await openBuildRow();
    await flushUntil(() => openedAgentTab() !== undefined);
    expect(openedAgentTab()?.sessionId).toBe("s-ssh");
    expect(mocks.listDurable).toHaveBeenCalledTimes(1);
    expect(mocks.listDurable).toHaveBeenCalledWith("s-ssh");
  });

  it("never lets one session's durable failure mask another session's hit", async () => {
    act(() => {
      useUi.setState({
        sessions: [
          sessionRow("s-broken", "ssh", "connected") as never,
          sessionRow("s-device", "device", "connected") as never,
        ],
      });
    });
    mocks.listLive.mockResolvedValue([]);
    mocks.listDurable.mockImplementation((sessionId: string) => {
      if (sessionId === "s-broken") return Promise.reject(new Error("session gone"));
      if (sessionId === "s-device") return Promise.resolve(durableHit("tab-agent-1"));
      return Promise.reject(new Error(`must not probe ${sessionId}`));
    });

    await openBuildRow();
    await flushUntil(() => openedAgentTab() !== undefined);
    expect(openedAgentTab()?.sessionId).toBe("s-device");
  });

  it("fails closed when multiple sessions claim the same durable tab", async () => {
    act(() => {
      useUi.setState({
        sessions: [
          sessionRow("s-a", "ssh", "connected") as never,
          sessionRow("s-b", "device", "connected") as never,
        ],
      });
    });
    mocks.listLive.mockResolvedValue([]);
    mocks.listDurable.mockImplementation(() => Promise.resolve(durableHit("tab-agent-1")));

    await openBuildRow();
    await flushUntil(() =>
      mocks.toast.mock.calls.some(
        (call) => call[0] === "error" && String(call[1]).includes("多个会话"),
      ),
    );
    expect(openedAgentTab()).toBeUndefined();
  });

  it("reports a diagnosable error when no session hits, including search failures", async () => {
    act(() => {
      useUi.setState({
        sessions: [
          sessionRow("s-empty", "ssh", "connected") as never,
          sessionRow("s-broken", "device", "connected") as never,
        ],
      });
    });
    mocks.listLive.mockResolvedValue([]);
    mocks.listDurable.mockImplementation((sessionId: string) => {
      if (sessionId === "s-broken") return Promise.reject(new Error("session gone"));
      return Promise.resolve([]);
    });

    await openBuildRow();
    await flushUntil(() =>
      mocks.toast.mock.calls.some(
        (call) => call[0] === "error" && String(call[1]).includes("搜索失败"),
      ),
    );
    expect(openedAgentTab()).toBeUndefined();
    const message = String(mocks.toast.mock.calls.find((call) => String(call[1]).includes("搜索失败"))?.[1]);
    expect(message).toContain("无匹配");
  });

  function switchToConversationB(rosterForB: () => Promise<unknown>) {
    mocks.conversationList.mockResolvedValue([{ id: "conv-b", title: "Conversation B", updatedAt: 0 }]);
    mocks.call.mockImplementation((cmd: string, args?: Record<string, unknown>) => {
      if (cmd === "ai_roster_list") {
        return (args as { conversationId?: string }).conversationId === "conv-b"
          ? rosterForB()
          : Promise.resolve([rosterListRow()]);
      }
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      if (cmd === "ai_watch_list") return Promise.resolve([]);
      if (cmd === "ai_watch_targets") return Promise.resolve([]);
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    click(view!.container.querySelector('button[title="历史会话"]')!);
    return flushUntil(() => textOf(view!).includes("Conversation B")).then(() => {
      const button = [...view!.container.querySelectorAll("button")].find(
        (candidate) => candidate.getAttribute("title") === "Conversation B",
      );
      if (!button) throw new Error("conversation B entry not found");
      click(button);
    });
  }

  it("never shows or mentions the previous conversation's roster rows while the next one loads", async () => {
    const bList = deferred<unknown>();
    await send(view!, "Question A");
    emit({ type: "done", answer: "Answer A" });
    await flush();
    click(agentButton(view!));
    await flushUntil(() => view!.container.querySelector('[data-handle="build"]') !== null);

    await switchToConversationB(() => bList.promise);
    await flushUntil(() => view!.container.textContent?.includes("加载子代理列表…") === true);
    // The first committed frame of conversation B carries none of A's rows,
    // and no stale row can be mentioned while B is still loading.
    expect(view!.container.querySelector('[data-handle="build"]')).toBeNull();
    expect(
      [...view!.container.querySelectorAll("button")].some(
        (button) => button.textContent?.trim() === "提及",
      ),
    ).toBe(false);

    await act(async () => {
      bList.resolve([
        {
          entry: { ...rosterListRow().entry, id: "row-b", handle: "deploy", tabId: "tab-b" },
          presence: "connected",
          assetId: "",
        },
      ]);
    });
    await flushUntil(() => view!.container.querySelector('[data-handle="deploy"]') !== null);
    expect(view!.container.querySelector('[data-handle="build"]')).toBeNull();
  });

  it("shows the load error without resurrecting previous rows when the next conversation fails", async () => {
    const bList = deferred<unknown>();
    await send(view!, "Question A");
    emit({ type: "done", answer: "Answer A" });
    await flush();
    click(agentButton(view!));
    await flushUntil(() => view!.container.querySelector('[data-handle="build"]') !== null);

    await switchToConversationB(() => bList.promise);
    await flushUntil(() => view!.container.textContent?.includes("加载子代理列表…") === true);
    expect(view!.container.querySelector('[data-handle="build"]')).toBeNull();

    await act(async () => {
      bList.reject(new Error("backend down"));
    });
    await flushUntil(() => view!.container.textContent?.includes("加载失败") === true);
    expect(view!.container.querySelector('[data-handle="build"]')).toBeNull();
    expect(view!.container.querySelector('[data-handle="deploy"]')).toBeNull();
    expect(
      [...view!.container.querySelectorAll("button")].some(
        (button) => button.textContent?.trim() === "提及",
      ),
    ).toBe(false);
  });
});
