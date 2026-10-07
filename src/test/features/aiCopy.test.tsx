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
import { useUi } from "../../app/store";

function emit(ev: Record<string, unknown>) {
  const channel = mocks.channels.at(-1);
  if (!channel) throw new Error("no fake channel");
  act(() => channel.onEvent(ev));
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
    mocks.promptText.mockResolvedValue("安装 nginx");
    mocks.ask.mockResolvedValue(true);
    useUi.setState({
      rightOpen: true,
      aiBusy: false,
      takeover: null,
      pushToast: mocks.toast,
      workspaces: [],
      sessions: [],
    });
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
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
    setInputValue(composer(), "跑个命令");
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
    expect(alert!.textContent).toContain("把它加为拦截规则");
    expect(alert!.textContent).not.toContain("自定义危险操作");
  });

  it("states the read-only hint with the device-grant exception", async () => {
    click(view!.container.querySelector('button[title^="AI 权限"]')!);
    await flush();
    const text = view!.container.textContent ?? "";
    expect(text).toContain("设备长期授权允许的终端写入或命令执行除外");
    expect(text).toContain("拦截规则命中或拿不准的仍会问你");
  });

  it("explains the model selector without BYOK jargon", async () => {
    const selector = mount(createElement(ModelSelector, { onManage: () => undefined }));
    await flush();
    const chip = selector.container.querySelector("button.nx-chip");
    expect(chip?.getAttribute("title")).toBe("模型配置（可保存多份模型档案）");
    expect(selector.container.textContent).not.toContain("BYOK");
    selector.unmount();
  });

  it("labels the active profile 当前 and keeps the key-storage hint free of internal cross-references", async () => {
    const manager = mount(createElement(ModelManager, {}));
    await flush();
    const label = manager.container.querySelector("span.nx-hint.mr-auto");
    expect(label?.textContent).toBe("当前");
    const text = manager.container.textContent ?? "";
    expect(text).not.toContain("当前激活");
    expect(text).toContain("API Key 加密后保存在本机，不会上传到任何服务器。");
    expect(text).not.toContain("明文存进本机 sqlite");
    expect(text).not.toContain("同一约定");
    manager.unmount();
  });

  it("labels the read_screen tool item 读取屏幕", () => {
    const s = createConversationStream(() => () => undefined);
    s.beginRun(1, "takeover");
    s.pushEvent(1, { type: "screen", text: "nginx 安装日志" });
    const item = s
      .getState()
      .items.find((i) => i.role === "tool" && i.name === "read_screen");
    expect(item).toMatchObject({ display: "读取屏幕" });
  });
});
