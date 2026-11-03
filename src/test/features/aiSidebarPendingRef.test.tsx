/** @vitest-environment jsdom */

// A queued command-block reference is picked up by the visible AI panel and sent as data, not as message text.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, mount, setInputValue, type MountedView } from "./reactTestUtils";

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


describe("AiSidebar pending references", () => {
  let view: MountedView | null = null;

  beforeEach(async () => {
    convSeq = 0;
    mocks.channels.length = 0;
    mocks.chat.mockImplementation(() => {
      convSeq += 1;
      return Promise.resolve({ jobId: `job-${convSeq}`, conversationId: `conv-${convSeq}` });
    });
    mocks.hitlSnapshot.mockResolvedValue({ runId: "job-1", checkpointId: "job-1", status: "running", attempt: 1, seq: 0, pending: [] });
    mocks.hitlEvents.mockResolvedValue([]);
    mocks.runs.mockResolvedValue([]);
    mocks.runEvents.mockResolvedValue([]);
    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    mocks.conversationList.mockResolvedValue([]);
    mocks.messages.mockResolvedValue([]);
    mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
    mocks.cronList.mockResolvedValue([]);
    mocks.assetList.mockResolvedValue([]);
    mocks.listLive.mockResolvedValue([]);
    mocks.listDurable.mockResolvedValue([]);
    mocks.listenEvent.mockImplementation(() => Promise.resolve(vi.fn()));
    mocks.call.mockImplementation((cmd: string) => {
      if (cmd === "ai_roster_list") return Promise.resolve([]);
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      if (cmd === "ai_watch_list") return Promise.resolve([]);
      if (cmd === "ai_watch_targets") return Promise.resolve([]);
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    localStorage.clear();
    useUi.setState({
      rightOpen: true, aiBusy: false, takeover: null, pushToast: mocks.toast,
      workspaces: [], aiBoards: {}, sessions: [], activeWorkspaceId: null, aiPendingRefs: [],
    });
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flush();
  });

  afterEach(async () => {
    await flush();
    view?.unmount();
    view = null;
  });

  it("takes a queued text reference, prefills the input, and sends it as a ref rather than message text", async () => {
    act(() => {
      useUi.getState().queueAiRef({
        kind: "text", id: "block:1", label: "命令块 #3 · ls", detail: "web-01", refId: "block:1", text: "SECRET-OUTPUT-LINE",
      });
    });
    await flush();
    expect(useUi.getState().aiPendingRefs).toEqual([]);
    const textarea = view!.container.querySelector("textarea")!;
    expect(textarea.value).not.toBe("");
    expect(view!.container.textContent).toContain("命令块 #3");
    click(view!.container.querySelector('button[title="发送 (Enter)"]')!);
    await flush();
    expect(mocks.chat).toHaveBeenCalledTimes(1);
    const args = mocks.chat.mock.calls[0][0];
    expect(args.refs).toEqual([{ kind: "text", id: "block:1", label: "命令块 #3 · ls", text: "SECRET-OUTPUT-LINE" }]);
    expect(args.message).toBe("[引用]\n- 命令块 #3 · ls（web-01）\n\n解释这段命令输出");
  });

  it("keeps text the user already typed", async () => {
    const textarea = view!.container.querySelector("textarea")!;
    setInputValue(textarea, "my own question");
    act(() => {
      useUi.getState().queueAiRef({ kind: "text", id: "block:2", label: "L", detail: "d", refId: "block:2", text: "x" });
    });
    await flush();
    expect(view!.container.querySelector("textarea")!.value).toBe("my own question");
  });
});
