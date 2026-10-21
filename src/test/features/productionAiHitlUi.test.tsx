/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flushSync } from "react-dom";
import { click, flush, flushMacrotask, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

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

function confirmEvent(id: string, extra: Record<string, unknown> = {}) {
  return {
    type: "confirmRequired",
    id,
    tool: "exec_commands",
    args: { commands: ["systemctl restart nginx"] },
    risk: "needs_confirm",
    rendered: 'exec_commands ["systemctl restart nginx"]',
    commands: ["systemctl restart nginx"],
    confirmationNonce: `nonce-${id}`,
    requestId: `req-${id}`,
    attempt: 1,
    ...extra,
  };
}

async function show(): Promise<void> {
  mounted = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
  await flush();
}

async function sendAndEmit(events: Record<string, unknown>[]): Promise<void> {
  const input = mounted?.container.querySelector<HTMLTextAreaElement>("textarea[aria-label='消息输入']");
  if (!input) throw new Error("message input not found");
  setInputValue(input, "继续");
  const send = [...(mounted?.container.querySelectorAll("button") ?? [])].find(
    (b) => b.getAttribute("title") === "发送 (Enter)",
  );
  click(send as HTMLButtonElement);
  await flush();
  act(() => {
    for (const event of events) mocks.channels[0].onEvent(event);
  });
  await flush();
}

function environmentInput(label: string): HTMLInputElement {
  const input = mounted?.container.querySelector<HTMLInputElement>(`input[aria-label="输入环境名 ${label}"]`);
  if (!input) throw new Error(`environment input ${label} not found`);
  return input;
}

function decisionButton(text: string): HTMLButtonElement {
  const button = [...(mounted?.container.querySelectorAll("button") ?? [])].find(
    (b) => b.textContent?.trim() === text,
  );
  if (!button) throw new Error(`decision button ${text} not found`);
  return button as HTMLButtonElement;
}

// Decision clicks start the async confirm; keep an act scope open across a
// macrotask so its state updates land inside act.
async function clickAndFlush(element: Element): Promise<void> {
  await act(async () => {
    element.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await flushMacrotask();
  });
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
  mocks.assetList.mockResolvedValue([]);
  mocks.call.mockImplementation((cmd: string) => {
    if (cmd === "production_settings_get") return Promise.resolve(SETTINGS_DTO);
    return Promise.reject(new Error(`unexpected command ${cmd}`));
  });
  resetProductionClassificationCache();
  useUi.setState({
    rightOpen: true,
    rightWidth: 352,
    aiBusy: false,
    takeover: null,
    pushToast: mocks.toast,
    workspaces: [],
    aiBoards: {},
    sessions: [],
    accountEpoch: 0,
  });
});

afterEach(async () => {
  await flush();
  mounted?.unmount();
  mounted = undefined;
  resetProductionClassificationCache();
  resetAllKeybindings();
});

describe("AI HITL typed environment confirmation", () => {
  it("sends the user's typed names for a single-target production challenge", async () => {
    await show();
    await sendAndEmit([
      { type: "toolCall", id: "call-1", name: "exec_commands", display: "exec_commands", args: {} },
      confirmEvent("call-1", { requiredEnvironmentNames: ["production"] }),
    ]);

    expect(environmentInput("production")).toBeDefined();
    expect(decisionButton("允许一次").disabled).toBe(true);
    setInputValue(environmentInput("production"), "production");
    expect(decisionButton("允许一次").disabled).toBe(false);
    await clickAndFlush(decisionButton("允许一次"));

    await flushUntil(() => mocks.confirm.mock.calls.length === 1);
    expect(mocks.confirm).toHaveBeenCalledWith({
      jobId: "job-1",
      callId: "call-1",
      nonce: "nonce-call-1",
      decision: "allow",
      environmentNames: ["production"],
    });
  });

  it("sends every typed name for a multi-target production challenge", async () => {
    await show();
    await sendAndEmit([
      { type: "toolCall", id: "call-2", name: "exec_commands", display: "exec_commands", args: {} },
      confirmEvent("call-2", { requiredEnvironmentNames: ["live", "production"] }),
    ]);

    expect(decisionButton("允许一次").disabled).toBe(true);
    setInputValue(environmentInput("live"), "live");
    expect(decisionButton("允许一次").disabled).toBe(true);
    setInputValue(environmentInput("production"), "production");
    expect(decisionButton("允许一次").disabled).toBe(false);
    await clickAndFlush(decisionButton("允许一次"));

    await flushUntil(() => mocks.confirm.mock.calls.length === 1);
    expect(mocks.confirm.mock.calls[0][0].environmentNames).toEqual(["live", "production"]);
  });

  it("does not send with a missing name", async () => {
    await show();
    await sendAndEmit([
      { type: "toolCall", id: "call-3", name: "exec_commands", display: "exec_commands", args: {} },
      confirmEvent("call-3", { requiredEnvironmentNames: ["live", "production"] }),
    ]);

    setInputValue(environmentInput("live"), "live");
    expect(decisionButton("允许一次").disabled).toBe(true);
    await clickAndFlush(decisionButton("允许一次"));
    await flush();
    expect(mocks.confirm).not.toHaveBeenCalled();
  });

  it("does not send with a wrong name", async () => {
    await show();
    await sendAndEmit([
      { type: "toolCall", id: "call-4", name: "exec_commands", display: "exec_commands", args: {} },
      confirmEvent("call-4", { requiredEnvironmentNames: ["production"] }),
    ]);

    setInputValue(environmentInput("production"), "staging");
    expect(mounted?.container.textContent).toContain("环境名不匹配");
    expect(decisionButton("允许一次").disabled).toBe(true);
    await clickAndFlush(decisionButton("允许一次"));
    await flush();
    expect(mocks.confirm).not.toHaveBeenCalled();
  });

  it("does not send when the typed set collapses the required cardinality", async () => {
    await show();
    await sendAndEmit([
      { type: "toolCall", id: "call-5", name: "exec_commands", display: "exec_commands", args: {} },
      confirmEvent("call-5", { requiredEnvironmentNames: ["live", "production"] }),
    ]);

    // Same real name in both inputs: the deduplicated set no longer covers the
    // required cardinality, so nothing is sent.
    setInputValue(environmentInput("live"), "production");
    setInputValue(environmentInput("production"), "production");
    expect(decisionButton("允许一次").disabled).toBe(true);
    await clickAndFlush(decisionButton("允许一次"));
    await flush();
    expect(mocks.confirm).not.toHaveBeenCalled();
  });

  it("sends the typed names with a deny decision", async () => {
    await show();
    await sendAndEmit([
      { type: "toolCall", id: "call-6", name: "exec_commands", display: "exec_commands", args: {} },
      confirmEvent("call-6", { requiredEnvironmentNames: ["production"] }),
    ]);

    setInputValue(environmentInput("production"), "production");
    await clickAndFlush(decisionButton("拒绝"));
    await flushUntil(() => mocks.confirm.mock.calls.length === 1);
    expect(mocks.confirm).toHaveBeenCalledWith({
      jobId: "job-1",
      callId: "call-6",
      nonce: "nonce-call-6",
      decision: "deny",
      environmentNames: ["production"],
    });
  });

  it("resets typed names when a restart re-challenges with a new attempt", async () => {
    await show();
    await sendAndEmit([
      { type: "toolCall", id: "call-7", name: "exec_commands", display: "exec_commands", args: {} },
      confirmEvent("call-7", { requiredEnvironmentNames: ["production"] }),
    ]);
    setInputValue(environmentInput("production"), "production");
    expect(decisionButton("允许一次").disabled).toBe(false);

    act(() => {
      mocks.channels[0].onEvent(confirmEvent("call-7-restart", {
        requiredEnvironmentNames: ["production"],
        attempt: 2,
        requestId: "req-call-7-restart",
      }));
    });
    await flush();

    // The stale attempt's typed name never carries over: the fresh challenge
    // starts empty and locked again.
    expect(environmentInput("production").value).toBe("");
    expect(decisionButton("允许一次").disabled).toBe(true);
    setInputValue(environmentInput("production"), "production");
    await clickAndFlush(decisionButton("允许一次"));
    await flushUntil(() => mocks.confirm.mock.calls.length === 1);
    expect(mocks.confirm.mock.calls[0][0]).toMatchObject({
      callId: "call-7-restart",
      environmentNames: ["production"],
    });
  });

  it("never submits the previous card's names for a directly replaced card", async () => {
    await show();
    await sendAndEmit([
      { type: "toolCall", id: "call-r1", name: "exec_commands", display: "exec_commands", args: {} },
      confirmEvent("call-r1", { requiredEnvironmentNames: ["production"] }),
    ]);
    setInputValue(environmentInput("production"), "production");
    expect(decisionButton("允许一次").disabled).toBe(false);

    // 直接替换卡片：flushSync 让新卡立即提交，passive effect 尚未运行——
    // 新卡首帧就必须锁定（不得沿用上一卡的 canConfirm），随后提交也不得把
    // 新 nonce 与旧 names 组合发出。
    act(() => {
      flushSync(() => {
        mocks.channels[0].onEvent(confirmEvent("call-r2", { requiredEnvironmentNames: ["production"] }));
      });
      expect(decisionButton("允许一次").disabled).toBe(true);
      click(decisionButton("允许一次"));
    });
    await flush();
    expect(mocks.confirm).not.toHaveBeenCalled();

    // 为 call-r2 重新输入后，只发送 call-r2 的 names
    setInputValue(environmentInput("production"), "production");
    await clickAndFlush(decisionButton("允许一次"));
    await flushUntil(() => mocks.confirm.mock.calls.length === 1);
    expect(mocks.confirm.mock.calls[0][0]).toMatchObject({
      callId: "call-r2",
      environmentNames: ["production"],
    });
  });

  it("never sends typed names after an account switch clears the AI account", async () => {
    await show();
    await sendAndEmit([
      { type: "toolCall", id: "call-8", name: "exec_commands", display: "exec_commands", args: {} },
      confirmEvent("call-8", { requiredEnvironmentNames: ["production"] }),
    ]);
    setInputValue(environmentInput("production"), "production");

    act(() => {
      useUi.getState().resetAiAccount();
    });
    await flush();

    expect(mounted?.container.querySelector("input[aria-label='输入环境名 production']")).toBeNull();
    expect(mocks.confirm).not.toHaveBeenCalled();
  });

  it("keeps the legacy fieldless HITL flow without environmentNames", async () => {
    await show();
    await sendAndEmit([
      { type: "toolCall", id: "call-9", name: "exec_commands", display: "exec_commands", args: {} },
      confirmEvent("call-9"),
    ]);

    expect(mounted?.container.querySelector("input[aria-label='输入环境名 production']")).toBeNull();
    expect(decisionButton("允许一次").disabled).toBe(false);
    await clickAndFlush(decisionButton("允许一次"));
    await flushUntil(() => mocks.confirm.mock.calls.length === 1);
    expect(mocks.confirm).toHaveBeenCalledWith({
      jobId: "job-1",
      callId: "call-9",
      nonce: "nonce-call-9",
      decision: "allow",
    });
    expect(mocks.confirm.mock.calls[0][0]).not.toHaveProperty("environmentNames");
  });
});

describe("AI HITL restored from a snapshot", () => {
  function interruptOf(overrides: Record<string, unknown> = {}) {
    return {
      id: "req-snap-1",
      runId: "run-needed",
      checkpointId: "run-needed",
      checkpointHash: "hash-1",
      targetId: "target-1",
      callId: "call-snap-1",
      tool: "exec_commands",
      kind: "confirm",
      parameters: { commands: ["systemctl restart nginx"] },
      parameterHash: "phash-1",
      nonce: "nonce-snap-1",
      createdAt: "2026-10-03T10:00:00Z",
      expiresAt: "2026-10-03T10:05:00Z",
      attempt: 1,
      seq: 1,
      ...overrides,
    };
  }

  async function showRestored(pending: Record<string, unknown>[]): Promise<void> {
    const workspaceId = useUi.getState().ensureWorkspace({ kind: "session", sessionId: "s1", title: "ws" });
    useUi.setState({
      activeWorkspaceId: workspaceId,
      aiBoards: {
        [workspaceId]: { tabs: [{ id: "t1", title: "Old", conversationId: "conv-old" }], activeTabId: "t1" },
      },
    });
    mocks.conversationList.mockResolvedValue([{ id: "conv-old", title: "Old", createdAt: 0, updatedAt: 0, scope: {} }]);
    mocks.messages.mockResolvedValue([]);
    mocks.runs.mockResolvedValue([
      {
        id: "run-needed", conversationId: "conv-old", status: "interrupted", attempt: 1, seq: 1,
        planMode: false, source: "chat", answer: "", turns: 0, tokensIn: 0, tokensOut: 0,
        cacheCreationTokens: 0, latencyMs: 0, retries: 0, failures: 0, createdAt: 1, updatedAt: 1,
      },
    ]);
    mocks.hitlSnapshot.mockResolvedValue({
      runId: "run-needed",
      checkpointId: "run-needed",
      status: "interrupted",
      attempt: 1,
      seq: 1,
      pending,
    });
    mounted = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flush();
    await flushUntil(() => mounted!.container.querySelector('[aria-label="生产环境名列表"]') !== null
      || mounted!.container.textContent?.includes("需要你确认") === true);
  }

  it("restores a single-target production challenge and sends only the typed names", async () => {
    await showRestored([interruptOf({ requiredEnvironmentNames: ["production"] })]);

    expect(mounted!.container.querySelector('[aria-label="生产环境名列表"]')).not.toBeNull();
    expect(decisionButton("允许一次").disabled).toBe(true);
    setInputValue(environmentInput("production"), "production");
    expect(decisionButton("允许一次").disabled).toBe(false);
    await clickAndFlush(decisionButton("允许一次"));

    await flushUntil(() => mocks.confirm.mock.calls.length === 1);
    expect(mocks.confirm.mock.calls[0][0]).toEqual({
      jobId: "run-needed",
      callId: "call-snap-1",
      nonce: "nonce-snap-1",
      decision: "allow",
      environmentNames: ["production"],
    });
  });

  it("restores a multi-target production challenge and requires every name", async () => {
    await showRestored([interruptOf({ requiredEnvironmentNames: ["live", "production"] })]);

    expect(decisionButton("允许一次").disabled).toBe(true);
    setInputValue(environmentInput("live"), "live");
    expect(decisionButton("允许一次").disabled).toBe(true);
    setInputValue(environmentInput("production"), "production");
    expect(decisionButton("允许一次").disabled).toBe(false);
    await clickAndFlush(decisionButton("允许一次"));

    await flushUntil(() => mocks.confirm.mock.calls.length === 1);
    expect(mocks.confirm.mock.calls[0][0].environmentNames).toEqual(["live", "production"]);
  });

  it("does not send when a restored challenge is missing a name", async () => {
    await showRestored([interruptOf({ requiredEnvironmentNames: ["live", "production"] })]);

    setInputValue(environmentInput("live"), "live");
    expect(decisionButton("允许一次").disabled).toBe(true);
    await clickAndFlush(decisionButton("允许一次"));
    await flush();
    expect(mocks.confirm).not.toHaveBeenCalled();
  });

  it("does not send when a restored challenge has a wrong name", async () => {
    await showRestored([interruptOf({ requiredEnvironmentNames: ["production"] })]);

    setInputValue(environmentInput("production"), "staging");
    expect(decisionButton("允许一次").disabled).toBe(true);
    await clickAndFlush(decisionButton("允许一次"));
    await flush();
    expect(mocks.confirm).not.toHaveBeenCalled();
  });

  it("keeps a legacy snapshot interrupt on the fieldless flow", async () => {
    await showRestored([interruptOf()]);

    expect(mounted!.container.querySelector('[aria-label="生产环境名列表"]')).toBeNull();
    expect(decisionButton("允许一次").disabled).toBe(false);
    await clickAndFlush(decisionButton("允许一次"));
    await flushUntil(() => mocks.confirm.mock.calls.length === 1);
    expect(mocks.confirm.mock.calls[0][0]).toEqual({
      jobId: "run-needed",
      callId: "call-snap-1",
      nonce: "nonce-snap-1",
      decision: "allow",
    });
    expect(mocks.confirm.mock.calls[0][0]).not.toHaveProperty("environmentNames");
  });
});
