/** @vitest-environment jsdom */

// AgentToolCard production mounting: toolCall/toolResult events carrying the
// M110 verified roster handle render the reviewed card joined with the real
// M131 roster contract (host/OSC 7 cwd, presence, production, approval),
// while non-roster or ambiguous calls keep the plain bubble and claim no
// roster identity. The cross-restart HITL resume path joins the same handle.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, flushMacrotask, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  call: vi.fn(),
  assetList: vi.fn(),
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
  terminalApi: {},
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

import { AiSidebar } from "../../features/ai/AiSidebar";
import { clearAgentRosterSnapshots } from "../../features/ai/agentRosterSnapshot";
import { aiTabBusyIds, aiTabRunControls, clearAiTabRuns } from "../../features/ai/runOwnership";
import { GLOBAL_AI_BOARD_KEY, useUi } from "../../app/store";

function rosterListRow(handle = "build", tabId = "tab-agent-1") {
  return {
    entry: {
      id: "row-1",
      conversationId: "conv-1",
      handle,
      alias: handle,
      tabId,
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

function runRow(id: string, status: string, extra: Record<string, unknown> = {}) {
  return {
    id,
    conversationId: "conv-1",
    status,
    attempt: 1,
    seq: 1,
    planMode: false,
    source: "chat",
    answer: "",
    turns: 1,
    tokensIn: 5,
    tokensOut: 0,
    cacheCreationTokens: 0,
    latencyMs: 50,
    retries: 0,
    failures: 0,
    createdAt: 1,
    updatedAt: 1,
    finishedAt: 1,
    ...extra,
  };
}

async function send(view: MountedView, text: string) {
  const textarea = view.container.querySelector("textarea");
  if (!textarea) throw new Error("input textarea not found");
  setInputValue(textarea, text);
  click(view.container.querySelector('button[title="发送 (Enter)"]')!);
  await flush();
}

// Emitting starts async work (roster snapshot refresh, HITL replay); keep an
// act scope open across a macrotask so those state updates land inside act.
async function emit(ev: Record<string, unknown>, index = -1) {
  const channel = mocks.channels.at(index);
  if (!channel) throw new Error("no fake channel");
  await act(async () => {
    channel.onEvent(ev);
    await flushMacrotask();
  });
}

// Approval clicks start the async confirm; keep an act scope open across a
// macrotask so its state updates land inside act.
async function clickButtonFlush(scope: ParentNode, text: string): Promise<void> {
  const button = [...scope.querySelectorAll("button")].find(
    (candidate) => candidate.textContent?.trim() === text,
  );
  if (!button) throw new Error(`button not found: ${text}`);
  await act(async () => {
    button.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await flushMacrotask();
  });
}

function agentCard(view: MountedView, callId: string): HTMLElement | null {
  return view.container.querySelector<HTMLElement>(`[data-call-id="${callId}"]`);
}

describe("AiSidebar agent tool card mounting", () => {
  let view: MountedView | null = null;

  beforeEach(async () => {
    clearAiTabRuns([...aiTabBusyIds, ...aiTabRunControls.keys()]);
    clearAgentRosterSnapshots();
    vi.clearAllMocks();
    mocks.channels.length = 0;
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
    mocks.runEvents.mockResolvedValue([]);
    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    mocks.conversationList.mockResolvedValue([]);
    mocks.conversationDelete.mockResolvedValue(undefined);
    mocks.messages.mockResolvedValue([]);
    mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
    mocks.cronList.mockResolvedValue([]);
    mocks.ask.mockResolvedValue(true);
    mocks.assetList.mockResolvedValue([{ id: "asset-1", tags: "env:production" }]);
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

  it("renders the reviewed card for a handle-verified tool call joined with the real roster", async () => {
    await send(view!, "deploy please");
    await emit({ type: "toolCall", id: "call-1", name: "exec_commands", display: "exec_commands", handle: "build" });
    await flushUntil(() => agentCard(view!, "call-1") !== null);
    // The roster snapshot loads through the real adapter for the join.
    await flushUntil(() => agentCard(view!, "call-1")!.textContent?.includes("build@web-01:/srv/app") === true);
    const card = agentCard(view!, "call-1")!;
    expect(card.textContent).toContain("生产环境");
    // Attached is the normal state: the reviewed card shows no presence chip
    // for it, only the joined identity and the production badge.
    expect(card.textContent).toContain("exec_commands");
    expect(mocks.call).toHaveBeenCalledWith("ai_roster_list", { conversationId: "conv-1" });

    await emit({
      type: "toolResult",
      id: "call-1",
      ok: true,
      summary: "部署完成",
      text: "部署完成\n完整输出",
      exitCode: 0,
      handle: "build",
    });
    await flushUntil(() => agentCard(view!, "call-1")!.textContent?.includes("部署完成") === true);
    expect(agentCard(view!, "call-1")!.textContent).toContain("0");
  });

  it("keeps the plain bubble for tool calls without a verified handle", async () => {
    await send(view!, "check something");
    await emit({ type: "toolCall", id: "call-1", name: "read_file", display: "read_file" });
    await emit({ type: "toolResult", id: "call-1", ok: true, summary: "文件内容", text: "文件内容", exitCode: 0 });
    await flush();
    expect(agentCard(view!, "call-1")).toBeNull();
    expect(view!.container.textContent).toContain("read_file");
    expect(view!.container.textContent).not.toContain("build@");
  });

  it("fails closed on conflicting handles for one call instead of borrowing either", async () => {
    await send(view!, "deploy");
    await emit({ type: "toolCall", id: "call-1", name: "exec_commands", display: "exec_commands", handle: "build" });
    await emit({ type: "toolResult", id: "call-1", ok: true, summary: "done", text: "done", handle: "deploy" });
    await flush();
    expect(agentCard(view!, "call-1")).toBeNull();
  });

  it("renders unknown identity when the roster has no row for the handle", async () => {
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") return Promise.resolve([]);
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      if (cmd === "ai_run_history") return Promise.resolve({ entries: [], nextCursor: null });
      if (cmd === "ai_run_recovery_summary") return Promise.resolve(null);
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    await send(view!, "deploy");
    await emit({ type: "toolCall", id: "call-1", name: "exec_commands", display: "exec_commands", handle: "ghost" });
    await flushUntil(() => agentCard(view!, "call-1") !== null);
    const card = agentCard(view!, "call-1")!;
    const text = card.textContent ?? "";
    expect(text).toContain("ghost");
    expect(text).not.toContain("ghost@");
    expect(text).toContain("无法验证");
    // An empty list cannot evidence persistence: no claimed non-persistent
    // target, and automatic writes stay forbidden without evidence.
    expect(text).not.toContain("非持久目标");
    expect(text).toContain("自动写入已禁止");
  });

  it("keeps unknown identity when the roster list fails to load", async () => {
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") return Promise.reject(new Error("backend down"));
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      if (cmd === "ai_run_history") return Promise.resolve({ entries: [], nextCursor: null });
      if (cmd === "ai_run_recovery_summary") return Promise.resolve(null);
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    await send(view!, "deploy");
    await emit({ type: "toolCall", id: "call-1", name: "exec_commands", display: "exec_commands", handle: "build" });
    await flushUntil(() => agentCard(view!, "call-1") !== null);
    // The failed load neither spins retries nor claims roster metadata.
    await flush();
    const card = agentCard(view!, "call-1")!;
    const text = card.textContent ?? "";
    expect(text).toContain("build");
    expect(text).not.toContain("build@");
    expect(text).toContain("无法验证");
    expect(text).not.toContain("非持久目标");
    expect(text).toContain("自动写入已禁止");
    expect(mocks.call.mock.calls.filter((call) => call[0] === "ai_roster_list").length).toBeLessThanOrEqual(2);
  });

  it("scopes handles to their run: a second run reusing the call id borrows nothing", async () => {
    await send(view!, "first deploy");
    await emit({ type: "toolCall", id: "c1", name: "exec_commands", display: "exec_commands", handle: "build" });
    await flushUntil(() => agentCard(view!, "c1") !== null);
    await flushUntil(() => agentCard(view!, "c1")!.textContent?.includes("build@web-01:/srv/app") === true);
    await emit({ type: "done", answer: "first done" });
    await flush();

    // Second run reuses the same call id without any handle: its tool item
    // keeps the plain bubble and never borrows the first run's identity.
    await send(view!, "second deploy");
    await emit({ type: "toolCall", id: "c1", name: "exec_commands", display: "exec_commands" });
    await emit({ type: "toolResult", id: "c1", ok: true, summary: "第二轮输出", text: "第二轮输出", exitCode: 0 });
    await flush();
    expect(view!.container.querySelectorAll('[data-call-id="c1"]')).toHaveLength(1);
    expect(view!.container.textContent).toContain("第二轮输出");

    // A second run with its own verified handle renders its own card; the
    // first run's card keeps its original identity.
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") {
        return Promise.resolve([
          rosterListRow(),
          { ...rosterListRow("deploy", "tab-agent-2"), host: "web-02", cwd: "/srv/b" },
        ]);
      }
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      if (cmd === "ai_run_history") return Promise.resolve({ entries: [], nextCursor: null });
      if (cmd === "ai_run_recovery_summary") return Promise.resolve(null);
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    await emit({ type: "toolCall", id: "c2", name: "exec_commands", display: "exec_commands", handle: "deploy" });
    await flushUntil(() => agentCard(view!, "c2") !== null);
    await flushUntil(() => agentCard(view!, "c2")!.textContent?.includes("deploy@web-02:/srv/b") === true);
    expect(agentCard(view!, "c1")!.textContent).toContain("build@web-01:/srv/app");
  });

  it("resolves the card's pending approval through the real HITL delegate", async () => {
    await send(view!, "deploy");
    await emit({ type: "toolCall", id: "call-1", name: "exec_commands", display: "exec_commands", handle: "build" });
    await emit({
      type: "confirmRequired",
      id: "call-1",
      tool: "exec_commands",
      rendered: "$ deploy --prod",
      confirmationNonce: "n-1",
      requestId: "req-1",
      attempt: 1,
    });
    await flushUntil(() => agentCard(view!, "call-1")?.textContent?.includes("等待你确认") === true);
    const card = agentCard(view!, "call-1")!;
    expect(card.querySelector('[title^="请求 req-1"]')).not.toBeNull();

    await clickButtonFlush(card, "批准一次");
    await flushUntil(() => mocks.confirm.mock.calls.length > 0);
    expect(mocks.confirm).toHaveBeenCalledWith({
      jobId: "job-1",
      callId: "call-1",
      nonce: "n-1",
      decision: "allow",
    });
    await flushUntil(() => agentCard(view!, "call-1")!.textContent?.includes("已批准一次") === true);
  });

  it("gates the card approval on the typed production environment names", async () => {
    await send(view!, "deploy");
    await emit({ type: "toolCall", id: "call-1", name: "exec_commands", display: "exec_commands", handle: "build" });
    await emit({
      type: "confirmRequired",
      id: "call-1",
      tool: "exec_commands",
      rendered: "$ deploy --prod",
      confirmationNonce: "n-1",
      requestId: "req-1",
      attempt: 1,
      requiredEnvironmentNames: ["production"],
    });
    await flushUntil(() => agentCard(view!, "call-1")?.textContent?.includes("等待你确认") === true);

    // The delegate refuses before the challenge is answered with typed names.
    await clickButtonFlush(agentCard(view!, "call-1")!, "批准一次");
    await flushUntil(() =>
      mocks.toast.mock.calls.some(
        (call) => call[0] === "error" && String(call[1]).includes("生产环境挑战未完成"),
      ),
    );
    expect(mocks.confirm).not.toHaveBeenCalled();

    const envInput = view!.container.querySelector<HTMLInputElement>('input[aria-label="输入环境名 production"]');
    if (!envInput) throw new Error("environment input not found");
    setInputValue(envInput, "production");
    await flush();
    await clickButtonFlush(agentCard(view!, "call-1")!, "批准一次");
    await flushUntil(() => mocks.confirm.mock.calls.length > 0);
    expect(mocks.confirm).toHaveBeenCalledWith({
      jobId: "job-1",
      callId: "call-1",
      nonce: "n-1",
      decision: "allow",
      environmentNames: ["production"],
    });
  });

  it("keeps each conversation's own roster identity across AI tab switches (A→B→A)", async () => {
    // Two AI tabs with bound conversations, each producing a handle-verified
    // tool card. The per-conversation snapshot slots must survive the other
    // tab's load, so switching back never falls back to unknown.
    mocks.chat.mockImplementation((args: { conversationId?: string }) =>
      Promise.resolve({ jobId: `job-${args.conversationId}`, conversationId: args.conversationId ?? "conv-a" }),
    );
    mocks.conversationList.mockResolvedValue([
      { id: "conv-a", title: "Conv A", createdAt: 0, updatedAt: 0 },
      { id: "conv-b", title: "Conv B", createdAt: 0, updatedAt: 0 },
    ]);
    mocks.messages.mockImplementation((id: string) =>
      Promise.resolve([
        {
          id: `m-${id}`,
          conversationId: id,
          role: "user",
          content: { role: "user", content: `hello ${id}`, imageCount: 0 },
          tokensIn: null,
          tokensOut: null,
          createdAt: 1,
        },
      ]),
    );
    mocks.call.mockImplementation((cmd: string, args?: Record<string, unknown>) => {
      if (cmd === "ai_roster_list") {
        const conversationId = (args as { conversationId?: string }).conversationId;
        if (conversationId === "conv-a") {
          return Promise.resolve([
            { ...rosterListRow("build-a", "tab-a1"), host: "web-a", cwd: "/srv/a" },
          ]);
        }
        return Promise.resolve([
          { ...rosterListRow("build-b", "tab-b1"), host: "web-b", cwd: "/srv/b" },
        ]);
      }
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      if (cmd === "ai_run_history") return Promise.resolve({ entries: [], nextCursor: null });
      if (cmd === "ai_run_recovery_summary") return Promise.resolve(null);
      if (cmd === "ai_watch_list") return Promise.resolve([]);
      if (cmd === "ai_watch_targets") return Promise.resolve([]);
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    act(() => {
      useUi.setState({
        aiBoards: {
          [GLOBAL_AI_BOARD_KEY]: {
            tabs: [
              { id: "tab-a", title: "Conv A", conversationId: "conv-a" },
              { id: "tab-b", title: "Conv B", conversationId: "conv-b" },
            ],
            activeTabId: "tab-a",
          },
        },
      });
    });
    view?.unmount();
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flush();

    // Tab A produces its card and loads conv-a's snapshot.
    await send(view!, "deploy from A");
    await emit({ type: "toolCall", id: "call-a", name: "exec_commands", display: "exec_commands", handle: "build-a" });
    await flushUntil(() => agentCard(view!, "call-a")?.textContent?.includes("build-a@web-a:/srv/a") === true);

    // Switch to tab B, produce its card and load conv-b's snapshot.
    act(() => {
      useUi.getState().setActiveAiTab(GLOBAL_AI_BOARD_KEY, "tab-b");
    });
    await flush();
    await send(view!, "deploy from B");
    await emit({ type: "toolCall", id: "call-b", name: "exec_commands", display: "exec_commands", handle: "build-b" });
    await flushUntil(() => agentCard(view!, "call-b")?.textContent?.includes("build-b@web-b:/srv/b") === true);

    // Switch back to A with no new tool events: A's card must still use
    // conv-a's own rows, not an unknown fallback.
    act(() => {
      useUi.getState().setActiveAiTab(GLOBAL_AI_BOARD_KEY, "tab-a");
    });
    await flush();
    expect(agentCard(view!, "call-a")!.textContent).toContain("build-a@web-a:/srv/a");
    expect(agentCard(view!, "call-a")!.textContent).not.toContain("无法验证");
    // And back to B: B's identity is intact as well.
    act(() => {
      useUi.getState().setActiveAiTab(GLOBAL_AI_BOARD_KEY, "tab-b");
    });
    await flush();
    expect(agentCard(view!, "call-b")!.textContent).toContain("build-b@web-b:/srv/b");
    expect(agentCard(view!, "call-b")!.textContent).not.toContain("无法验证");
  });

  it("joins the same handle across the cross-restart HITL resume", async () => {
    // A conversation restored after a restart: the interrupted run resumes
    // with its pending question through the real restore path.
    const interrupt = {
      id: "req-9",
      runId: "job-2",
      checkpointId: "job-2",
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
    mocks.runs.mockResolvedValue([runRow("job-2", "interrupted", { finishedAt: undefined })]);
    mocks.runEvents.mockResolvedValue([
      {
        type: "confirmRequired",
        id: "call-9",
        tool: "exec_commands",
        rendered: "$ deploy --prod",
        confirmationNonce: "n-9",
        requestId: "req-9",
        attempt: 1,
        seq: 1,
      },
    ]);
    mocks.hitlSnapshot.mockResolvedValue({
      runId: "job-2",
      checkpointId: "job-2",
      status: "running",
      attempt: 1,
      seq: 1,
      pending: [interrupt],
    });
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "Demo", createdAt: 0, updatedAt: 0 }]);
    mocks.messages.mockResolvedValue([
      { id: "m1", conversationId: "conv-1", role: "user", content: { role: "user", content: "部署", imageCount: 0, jobId: "job-2" }, tokensIn: null, tokensOut: null, createdAt: 1 },
    ]);
    act(() => {
      useUi.setState({
        aiBoards: {
          [GLOBAL_AI_BOARD_KEY]: {
            tabs: [{ id: "tab-bound", title: "Demo", conversationId: "conv-1" }],
            activeTabId: "tab-bound",
          },
        },
      });
    });
    view?.unmount();
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flushUntil(() => (view!.container.textContent ?? "").includes("需要你确认"));

    // The resumed run emits the tool call and its result with the same
    // verified handle the journal carried across the restart.
    await emit({ type: "toolCall", id: "call-9", name: "exec_commands", display: "exec_commands", handle: "build" });
    await flushUntil(() => agentCard(view!, "call-9") !== null);
    await flushUntil(() => agentCard(view!, "call-9")!.textContent?.includes("build@web-01:/srv/app") === true);
    const card = agentCard(view!, "call-9")!;
    expect(card.textContent).toContain("生产环境");
    expect(card.textContent).toContain("等待你确认");
    expect(card.querySelector('[title^="请求 req-9"]')).not.toBeNull();

    await emit({
      type: "toolResult",
      id: "call-9",
      ok: true,
      summary: "部署完成",
      text: "部署完成",
      exitCode: 0,
      handle: "build",
    });
    await flushUntil(() => agentCard(view!, "call-9")!.textContent?.includes("部署完成") === true);

    await clickButtonFlush(agentCard(view!, "call-9")!, "批准一次");
    await flushUntil(() => mocks.confirm.mock.calls.length > 0);
    expect(mocks.confirm.mock.calls[0][0]).toEqual({
      jobId: "job-2",
      callId: "call-9",
      nonce: "n-9",
      decision: "allow",
    });
    // Drain the resolution chain (interaction resolve, title sync) inside act.
    await flush();
  });
});
