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
import { askAiAboutSelection } from "../../features/terminal/selectionRef";
import { MAX_MESSAGE_REFS } from "../../features/ai/userRefs";
import { askAiAboutEditorSelection, askAiAboutFile } from "../../features/files/fileAiRef";
import { takeFileReveal } from "../../features/files/fileReveal";

let convSeq = 0;
const realSetActiveTab = useUi.getState().setActiveTab;


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
      if (cmd === "production_settings_get") return Promise.resolve({ productionEnvironments: [] });
      if (cmd === "ai_watch_list") return Promise.resolve([]);
      if (cmd === "ai_watch_targets") return Promise.resolve([]);
      return Promise.reject(new Error(`unexpected call: ${cmd}`));
    });
    localStorage.clear();
    useUi.setState({
      rightOpen: true, aiBusy: false, takeover: null, pushToast: mocks.toast, setActiveTab: realSetActiveTab,
      workspaces: [], aiBoards: {}, sessions: [], activeWorkspaceId: null, aiPendingRefs: [], aiPendingPrompt: null,
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
    click(view!.container.querySelector('button[title="发送（Enter）"]')!);
    await flush();
    expect(mocks.chat).toHaveBeenCalledTimes(1);
    const args = mocks.chat.mock.calls[0][0];
    expect(args.refs).toEqual([{ kind: "text", id: "block:1", label: "命令块 #3 · ls", text: "SECRET-OUTPUT-LINE" }]);
    expect(args.message).toBe("[引用]\n- 命令块 #3 · ls（web-01）\n\n解释这段命令输出");
  });

  it("does not prefill the input for a non-text reference", async () => {
    act(() => {
      useUi.getState().queueAiRef({ kind: "tab", id: "tab:t9", label: "term", detail: "web-01 的终端", refId: "kernel-9" });
    });
    await flush();
    expect(useUi.getState().aiPendingRefs).toEqual([]);
    expect(view!.container.querySelector("textarea")!.value).toBe("");
    expect(view!.container.textContent).toContain("term");
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
  it("adds a terminal selection as a previewed chip without prefilling and sends only its text", async () => {
    act(() => {
      askAiAboutSelection("Permission denied (publickey)\nsecond line", { storeTabId: "st-1", title: "deploy", host: "web-01" });
    });
    await flush();
    expect(useUi.getState().aiPendingRefs).toEqual([]);
    const chip = view!.container.querySelector<HTMLElement>("[data-ref-source]");
    expect(chip?.textContent).toBe("终端选区 · Permission denied (publi…");
    expect(view!.container.querySelector("textarea")!.value).toBe("");
    setInputValue(view!.container.querySelector("textarea")!, "why?");
    click(view!.container.querySelector('button[title="发送（Enter）"]')!);
    await flush();
    expect(mocks.chat).toHaveBeenCalledTimes(1);
    const args = mocks.chat.mock.calls[0][0];
    expect(args.refs).toHaveLength(1);
    expect(args.refs[0]).toEqual({
      kind: "text",
      id: expect.stringMatching(/^selection:/),
      label: "终端选区 · Permission denied (publi…",
      text: "Permission denied (publickey)\nsecond line",
    });
    expect(args.message).toBe("[引用]\n- 终端选区 · Permission denied (publi…（deploy · web-01）\n\nwhy?");
  });

  it("removes a selection chip so it is not sent", async () => {
    act(() => {
      askAiAboutSelection("df -h output", { storeTabId: "st-1", title: "deploy", host: "web-01" });
    });
    await flush();
    const chip = view!.container.querySelector<HTMLElement>("[data-ref-source]")!.closest<HTMLElement>("[data-ref-id]")!;
    click(chip.querySelector('button[title="移除引用"]')!);
    await flush();
    expect(view!.container.querySelector("[data-ref-source]")).toBeNull();
    setInputValue(view!.container.querySelector("textarea")!, "hi");
    click(view!.container.querySelector('button[title="发送（Enter）"]')!);
    await flush();
    expect(mocks.chat.mock.calls[0][0].refs).toBeUndefined();
  });

  it("says so when the chip's source terminal tab is gone", async () => {
    const setActiveTab = vi.fn();
    act(() => useUi.setState({ setActiveTab }));
    act(() => {
      askAiAboutSelection("error: x", { storeTabId: "st-1", title: "deploy", host: "web-01" });
    });
    await flush();
    click(view!.container.querySelector("[data-ref-source]")!);
    expect(setActiveTab).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("info", "来源终端已关闭");
  });

  it("jumps back to the source tab of its workspace and keeps the chip", async () => {
    const tab = (id: string) => ({ id, kind: "terminal", title: id, sessionId: "s1", tabId: `kernel-${id}`, closable: true });
    act(() => {
      useUi.setState({
        workspaces: [
          {
            id: "ws1",
            kind: "session",
            title: "ws-1",
            closable: true,
            sessionId: "s1",
            panes: [{ id: "p1", activeTabId: "t2", tabs: [tab("t1"), tab("t2")] }],
            activePaneId: "p1",
          },
        ] as never,
        activeWorkspaceId: "ws1",
      });
    });
    await flush();
    act(() => {
      askAiAboutSelection("error: x", { storeTabId: "t1", title: "t1", host: "web-01" });
    });
    await flush();
    click(view!.container.querySelector("[data-ref-source]")!);
    await flush();
    expect(useUi.getState().workspaces[0].panes[0].activeTabId).toBe("t1");
    expect(view!.container.querySelector("[data-ref-source]")?.textContent).toBe("终端选区 · error: x");
  });

  it("refuses a queued selection beyond the reference limit visibly", async () => {
    act(() => {
      for (let i = 0; i < MAX_MESSAGE_REFS + 1; i++) {
        askAiAboutSelection(`line ${i}`, { storeTabId: "st-1", title: "deploy", host: "web-01" });
      }
    });
    await flush();
    expect(view!.container.querySelectorAll("[data-ref-source]")).toHaveLength(MAX_MESSAGE_REFS);
    expect(mocks.toast).toHaveBeenCalledWith("info", `每条消息最多引用 ${MAX_MESSAGE_REFS} 项，请先移除一个引用`);
  });

  it("prefills a queued prompt without sending it", async () => {
    act(() => useUi.getState().queueAiPrompt("why is nginx slow"));
    await flush();
    expect(useUi.getState().aiPendingPrompt).toBeNull();
    expect(view!.container.querySelector("textarea")!.value).toBe("why is nginx slow");
    expect(mocks.chat).not.toHaveBeenCalled();
  });

  it("appends a queued prompt below a draft instead of replacing it", async () => {
    setInputValue(view!.container.querySelector("textarea")!, "my draft");
    act(() => useUi.getState().queueAiPrompt("disk usage"));
    await flush();
    expect(view!.container.querySelector("textarea")!.value).toBe("my draft\ndisk usage");
    expect(mocks.chat).not.toHaveBeenCalled();
  });
  it("sends an editor selection as a file excerpt without prefilling and jumps back to its lines", async () => {
    const addTab = vi.fn();
    act(() => useUi.setState({ addTab, sessions: [{ id: "s1", name: "web-01", kind: "ssh" }] as never }));
    act(() => {
      askAiAboutEditorSelection({ sessionId: "s1", path: "/srv/app/main.go", text: "func main() {\n}", line: 12, endLine: 13 });
    });
    await flush();
    const chip = view!.container.querySelector<HTMLElement>("[data-ref-source]")!;
    expect(chip.textContent).toBe("main.go:12-13");
    expect(view!.container.querySelector("textarea")!.value).toBe("");
    click(chip);
    expect(takeFileReveal("s1", "/srv/app/main.go")).toEqual({ line: 12, endLine: 13 });
    expect(addTab).toHaveBeenCalledWith(expect.objectContaining({ kind: "files", sessionId: "s1", path: "/srv/app/main.go" }));
    setInputValue(view!.container.querySelector("textarea")!, "is this right?");
    click(view!.container.querySelector('button[title="发送（Enter）"]')!);
    await flush();
    expect(mocks.chat.mock.calls[0][0].refs).toEqual([
      {
        kind: "text",
        id: expect.stringMatching(/^editor:/),
        label: "main.go:12-13",
        path: "/srv/app/main.go",
        sessionId: "s1",
        text: "func main() {\n}",
      },
    ]);
  });

  it("sends a whole file as a path reference the backend reads", async () => {
    act(() => useUi.setState({ sessions: [{ id: "s1", name: "web-01", kind: "ssh" }] as never }));
    act(() => {
      askAiAboutFile("s1", "/etc/nginx/nginx.conf");
      askAiAboutFile("s1", "/etc/nginx/nginx.conf");
    });
    await flush();
    expect(view!.container.querySelectorAll("[data-ref-source]")).toHaveLength(1);
    expect(view!.container.querySelector("textarea")!.value).toBe("");
    setInputValue(view!.container.querySelector("textarea")!, "review");
    click(view!.container.querySelector('button[title="发送（Enter）"]')!);
    await flush();
    expect(mocks.chat.mock.calls[0][0].refs).toEqual([
      { kind: "file", id: "path:s1:/etc/nginx/nginx.conf", label: "nginx.conf", path: "/etc/nginx/nginx.conf", sessionId: "s1" },
    ]);
  });
  it("sends a problem reference as data without prefilling the command-output prompt", async () => {
    act(() => {
      useUi.getState().queueAiRef({ kind: "problem", id: "problem:1", label: "问题 · timeout", detail: "执行 SQL · db", refId: "problem:1", text: "摘要：timeout" });
    });
    await flush();
    expect(view!.container.querySelector("textarea")!.value).toBe("");
    setInputValue(view!.container.querySelector("textarea")!, "why?");
    click(view!.container.querySelector('button[title="发送（Enter）"]')!);
    await flush();
    expect(mocks.chat.mock.calls[0][0].refs).toEqual([{ kind: "problem", id: "problem:1", label: "问题 · timeout", text: "摘要：timeout" }]);
  });
});
