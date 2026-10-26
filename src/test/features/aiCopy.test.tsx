/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  chat: vi.fn(),
  cancel: vi.fn(),
  confirm: vi.fn(),
  answer: vi.fn(),
  hitlSnapshot: vi.fn(),
  hitlEvents: vi.fn(),
  getPermission: vi.fn(),
  conversationList: vi.fn(),
  conversationDelete: vi.fn(),
  messages: vi.fn(),
  overview: vi.fn(),
  presets: vi.fn(),
  activate: vi.fn(),
  promptText: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
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
    getPermission: mocks.getPermission,
    conversationList: mocks.conversationList,
    conversationDelete: mocks.conversationDelete,
    messages: mocks.messages,
  },
  modelApi: {
    overview: mocks.overview,
    presets: mocks.presets,
    activate: mocks.activate,
  },
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
  onChannelReopen: () => () => undefined,
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, promptText: mocks.promptText }));

import { AiSidebar } from "../../features/ai/AiSidebar";
import { ModelManager } from "../../features/ai/ModelPanel";
import { ModelSelector } from "../../features/ai/ModelSelector";
import { createConversationStream } from "../../features/ai/conversationStream";
import { aiTabBusyIds, aiTabRunControls } from "../../features/ai/runOwnership";
import { useUi } from "../../app/store";

function emit(ev: Record<string, unknown>) {
  const channel = mocks.channels.at(-1);
  if (!channel) throw new Error("no fake channel");
  act(() => channel.onEvent(ev));
}

function AccountAiSidebar(props: { sessionId: string; tabId: string }) {
  const epoch = useUi((s) => s.aiAccountEpoch);
  return createElement(AiSidebar, { ...props, key: epoch });
}

describe("M134 AI copy", () => {
  let view: MountedView | null = null;

  beforeEach(async () => {
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
    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    mocks.overview.mockResolvedValue({
      profiles: [{ id: "p1", name: "公司 DeepSeek", model: "deepseek-chat" }],
      activeId: "p1",
    });
    mocks.presets.mockResolvedValue(["deepseek"]);
    mocks.activate.mockResolvedValue(undefined);
    mocks.conversationList.mockResolvedValue([]);
    mocks.conversationDelete.mockResolvedValue(undefined);
    mocks.messages.mockResolvedValue([]);
    mocks.promptText.mockResolvedValue("Install nginx");
    mocks.ask.mockResolvedValue(true);
    useUi.setState({
      rightOpen: true,
      aiBusy: false,
      takeover: null,
      pushToast: mocks.toast,
      workspaces: [],
      aiBoards: {},
      sessions: [],
    });
    view = mount(createElement(AccountAiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flush();
  });

  afterEach(() => {
    view?.unmount();
    view = null;
  });

  function composer(): HTMLTextAreaElement {
    const textarea = view!.container.querySelector<HTMLTextAreaElement>('textarea[aria-label="消息输入"]');
    if (!textarea) throw new Error("composer textarea not found");
    return textarea;
  }

  it("points the confirm-card guidance at the real interception-rule setting", async () => {
    setInputValue(composer(), "Run a command");
    click(view!.container.querySelector('button[title="发送 (Enter)"]')!);
    await flush();
    emit({
      type: "confirmRequired",
      id: "call-1",
      tool: "exec_commands",
      rendered: "$ rm -rf /tmp/x",
    });
    await flush();

    const allow = [...view!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "允许一次",
    );
    expect(allow).not.toBeNull();
    const alert = allow!.closest(".nx-alert");
    expect(alert).not.toBeNull();
    expect(alert!.querySelector('[role="img"]')?.getAttribute("title")).toContain("「设置 → AI 拦截规则」");
    expect(alert!.textContent).not.toContain("自定义危险操作");
  });

  it("states the read-only hint with the device-grant exception", async () => {
    click(view!.container.querySelector('button[title^="AI 权限"]')!);
    await flush();
    const text = view!.container.textContent ?? "";
    const readOnly = [...view!.container.querySelectorAll('[role="radio"]')].find((radio) => radio.textContent === "只读");
    expect(readOnly?.getAttribute("title")).toContain("已开启免确认授权的操作除外");
    expect(text).toContain("高危和无法判断的操作始终询问");
  });

  it("explains the model selector without BYOK jargon", async () => {
    const selector = mount(createElement(ModelSelector, { onManage: () => undefined }));
    await flush();
    const chip = selector.container.querySelector("button.nx-chip");
    expect(chip?.getAttribute("title")).toBe("模型配置（可保存多份模型档案）");
    expect(selector.container.textContent).not.toContain("BYOK");
    selector.unmount();
  });

  it("labels the active profile as current and keeps the key-storage hint free of internal cross-references", async () => {
    const manager = mount(createElement(ModelManager, {}));
    await flush();
    const label = manager.container.querySelector("span.nx-hint.mr-auto");
    expect(label?.textContent).toBe("当前");
    const text = manager.container.textContent ?? "";
    expect(text).not.toContain("当前激活");
    expect(text).toContain("API Key 由当前 NexTerm 实例加密保存；模型请求仅发往你配置的端点。");
    expect(text).not.toContain("明文存进本机 sqlite");
    expect(text).not.toContain("同一约定");
    manager.unmount();
  });

  it("uses the localized read_screen tool label", () => {
    const s = createConversationStream(() => () => undefined);
    s.beginRun(1, "takeover");
    s.pushEvent(1, { type: "screen", text: "nginx installation log" });
    const item = s
      .getState()
      .items.find((i) => i.role === "tool" && i.name === "read_screen");
    expect(item).toMatchObject({ display: "读取屏幕" });
  });

  it("shows an error and retry when model profiles fail to load instead of presenting an empty configuration", async () => {
    mocks.overview.mockRejectedValue(new Error("档案服务不可用"));
    const selector = mount(createElement(ModelSelector, { onManage: () => undefined }));
    await flush();
    click(selector.container.querySelector("button.nx-chip")!);
    await flush();

    expect(selector.container.textContent).toContain("模型档案读取失败 · 档案服务不可用");
    expect(selector.container.textContent).not.toContain("还没有模型档案");

    mocks.overview.mockResolvedValue({
      profiles: [{ id: "p1", name: "公司 DeepSeek", model: "deepseek-chat" }],
      activeId: "p1",
    });
    const retry = [...selector.container.querySelectorAll("button")].find(
      (button) => button.textContent === "重试",
    );
    click(retry!);
    await flush();
    expect(selector.container.textContent).toContain("公司 DeepSeek");
    selector.unmount();
  });

  it("keeps the composer draft when switching away and back to a workspace", async () => {
    const wsA = {
      id: "ws-a",
      kind: "session" as const,
      title: "A",
      sessionId: "s1",
      panes: [],
      activePaneId: "p",
      closable: true,
    };
    const wsB = { ...wsA, id: "ws-b", title: "B", sessionId: "s2" };
    act(() => {
      useUi.setState({ workspaces: [wsA, wsB], activeWorkspaceId: "ws-a" });
    });
    await flush();

    setInputValue(composer(), "帮我看看 nginx 日志");
    await flush();

    act(() => {
      useUi.setState({ activeWorkspaceId: "ws-b" });
    });
    await flush();
    expect(composer().value).toBe("");

    act(() => {
      useUi.setState({ activeWorkspaceId: "ws-a" });
    });
    await flush();
    expect(composer().value).toBe("帮我看看 nginx 日志");
  });

  it("clears drafts and hides running work across account reset without cancelling the backend run", async () => {
    const wsA = {
      id: "ws-a",
      kind: "session" as const,
      title: "A",
      sessionId: "s1",
      panes: [],
      activePaneId: "p",
      closable: true,
    };
    const wsB = { ...wsA, id: "ws-b", title: "B", sessionId: "s2" };
    act(() => {
      useUi.setState({ workspaces: [wsA, wsB], activeWorkspaceId: "ws-a" });
    });
    await flush();
    setInputValue(composer(), "A 运行任务");
    click(view!.container.querySelector('button[title="发送 (Enter)"]')!);
    await flush();
    setInputValue(composer(), "A 未发送秘密");
    await flush();
    const oldTabId = useUi.getState().aiBoards["ws-a"].tabs[0].id;
    const oldEpoch = useUi.getState().aiAccountEpoch;
    const oldRunControl = aiTabRunControls.get(oldTabId);
    expect(aiTabBusyIds.has(oldTabId)).toBe(true);
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "A", updatedAt: 0 }]);

    act(() => {
      useUi.getState().resetAiAccount();
    });
    act(() => {
      useUi.setState({
        workspaces: [wsA, wsB],
        activeWorkspaceId: "ws-a",
        aiBoards: {
          "ws-a": {
            tabs: [{ id: oldTabId, title: "A", conversationId: "conv-1" }],
            activeTabId: oldTabId,
          },
        },
      });
    });
    await flush();

    expect(composer().value).toBe("");
    expect(useUi.getState().aiBoards["ws-a"].tabs[0].draft).toBeFalsy();
    expect(useUi.getState().aiBoards["ws-a"].tabs[0].conversationId).toBe("conv-1");
    expect(aiTabRunControls.get(oldTabId)).not.toBe(oldRunControl);
    expect(aiTabBusyIds.has(oldTabId)).toBe(false);
    expect(useUi.getState().aiBusy).toBe(false);
    expect(mocks.cancel).not.toHaveBeenCalled();

    useUi.getState().updateAiTab("ws-a", oldTabId, { draft: "late old draft" }, oldEpoch);
    emit({ type: "delta", text: "旧账号增量" });
    await flush();
    expect(aiTabBusyIds.has(oldTabId)).toBe(false);
    expect(useUi.getState().aiBusy).toBe(false);
    expect(view!.container.textContent).not.toContain("旧账号增量");
    emit({ type: "done", answer: "旧账号任务结果" });
    await flush();
    expect(useUi.getState().aiBoards["ws-a"].tabs[0].draft).toBeFalsy();
    expect(view!.container.textContent).not.toContain("旧账号任务结果");
  });
});
