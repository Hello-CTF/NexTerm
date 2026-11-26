/** @vitest-environment jsdom */

// AiSidebar background tasks and approval wording: the watch panel runs inside
// the real sidebar against the real production adapter (typed IPC only), with
// the transport mocked at the commands seam.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

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
const watchButton = tasksButton;

async function send(view: MountedView, text: string) {
  const textarea = view.container.querySelector("textarea");
  if (!textarea) throw new Error("input textarea not found");
  setInputValue(textarea, text);
  click(view.container.querySelector('button[title="发送（Enter）"]')!);
  await flush();
}

function emit(ev: Record<string, unknown>) {
  const channel = mocks.channels[mocks.channels.length - 1];
  if (!channel) throw new Error("no fake channel");
  act(() => channel.onEvent(ev));
}

describe("AiSidebar background tasks", () => {
  let view: MountedView | null = null;

  beforeEach(async () => {
    convSeq = 0;
    mocks.channels.length = 0;
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

  it("keeps the background tasks button without a count while there is nothing to manage", async () => {
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_watch_list") return Promise.resolve([]);
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    expect(tasksButton(view!).getAttribute("aria-label")).toBe("后台任务");
    await send(view!, "Question A");
    emit({ type: "done", answer: "Answer A" });
    await flushUntil(() => mocks.call.mock.calls.some(([cmd]) => cmd === "ai_watch_list"));
    await flush();
    expect(tasksButton(view!).getAttribute("aria-label")).toBe("后台任务");
    expect(tasksButton(view!).querySelector(".nx-badge")).toBeNull();
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
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });

    await send(view!, "Question A");
    emit({ type: "done", answer: "Answer A" });
    await flushUntil(() => /共 \d+ 项/.test(tasksButton(view!).getAttribute("aria-label") ?? ""));
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
    expect(text).toContain("请在对应输入框输入 managed-device");
    const envInput = view!.container.querySelector<HTMLInputElement>('input[aria-label="输入环境名 managed-device"]');
    if (!envInput) throw new Error("environment input not found");
    setInputValue(envInput, "managed-device");
    await flush();
    expect(view!.container.textContent).not.toContain("环境名不匹配");
  });
});
