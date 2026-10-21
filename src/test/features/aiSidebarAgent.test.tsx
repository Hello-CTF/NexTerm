/** @vitest-environment jsdom */

// AiSidebar production mounting: the reviewed agent panels and the M128 watch
// panel run inside the real sidebar against the real production adapters
// (typed IPC only), with the transport mocked at the commands seam.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, clickButton, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

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

function agentButton(view: MountedView): HTMLButtonElement {
  const button = view.container.querySelector<HTMLButtonElement>(
    'button[title="当前会话的 Agent 句柄与 run 历史"]',
  ) ?? view.container.querySelector<HTMLButtonElement>(
    'button[title="Agent 句柄挂在会话上：先发送消息开始一个会话"]',
  );
  if (!button) throw new Error("agent entry button not found");
  return button;
}

function watchButton(view: MountedView): HTMLButtonElement {
  const button = view.container.querySelector<HTMLButtonElement>('button[title="Watch 监视"]');
  if (!button) throw new Error("watch entry button not found");
  return button;
}

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
      if (cmd === "ai_run_history") return Promise.resolve({ entries: [], nextCursor: null });
      if (cmd === "ai_run_recovery_summary") return Promise.resolve(null);
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

  it("disables the agent entry point without a conversation", async () => {
    expect(agentButton(view!).disabled).toBe(true);
  });

  it("mounts the roster and history panels on real IPC after a conversation starts", async () => {
    await send(view!, "Question A");
    emit({ type: "done", answer: "Answer A" });
    await flush();
    expect(agentButton(view!).disabled).toBe(false);

    click(agentButton(view!));
    await flushUntil(() => view!.container.querySelector('[data-handle="build"]') !== null);
    expect(textOf(view!)).toContain("Agent 句柄");
    expect(textOf(view!)).toContain("Agent Run 历史");
    expect(mocks.call).toHaveBeenCalledWith("ai_roster_list", { conversationId: "conv-1" });
    expect(mocks.call).toHaveBeenCalledWith("ai_run_history", {
      conversationId: "conv-1",
      filter: "all",
      cursor: "",
      limit: 50,
    });
    expect(mocks.call).toHaveBeenCalledWith("ai_run_recovery_summary", { conversationId: "conv-1" });
    const row = view!.container.querySelector('[data-handle="build"]');
    expect(row).not.toBeNull();
    expect(row!.textContent).toContain("生产环境");

    click(agentButton(view!));
    await flush();
    expect(textOf(view!)).not.toContain("Agent 句柄");
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
        controller: null,
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
      if (cmd === "ai_run_history") return Promise.resolve({ entries: [], nextCursor: null });
      if (cmd === "ai_run_recovery_summary") return Promise.resolve(null);
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });

    click(watchButton(view!));
    await flushUntil(() => textOf(view!).includes("ready-watch"));
    expect(mocks.call).toHaveBeenCalledWith("ai_watch_list");
    expect(mocks.call).toHaveBeenCalledWith("ai_watch_targets");
    // The desktop identity is ownerless: the panel shows the local label and
    // the tool-data subscription was set up through the real adapter.
    expect(textOf(view!).toLowerCase()).toContain("local");
    expect(mocks.listenEvent).toHaveBeenCalledWith("ai://watch-tool-data", expect.any(Function));

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
});
