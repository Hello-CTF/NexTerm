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
  skillsList: vi.fn(),
  compact: vi.fn(),
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
    compactConversation: mocks.compact,
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
vi.mock("../../features/settings/skillsApi", () => ({ productionSkillsApi: { list: mocks.skillsList } }));

import { AiSidebar } from "../../features/ai/AiSidebar";
import { useUi } from "../../app/store";

function AccountAiSidebar(props: { sessionId: string; tabId: string }) {
  const epoch = useUi((s) => s.aiAccountEpoch);
  return createElement(AiSidebar, { ...props, key: epoch });
}

describe("slash command menu", () => {
  let view: MountedView | null = null;

  beforeEach(async () => {
    vi.clearAllMocks();
    mocks.channels.length = 0;
    mocks.chat.mockResolvedValue({ jobId: "job-1", conversationId: "conv-1" });
    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    mocks.overview.mockResolvedValue({
      profiles: [
        { id: "p1", name: "DeepSeek", model: "deepseek-chat" },
        { id: "p2", name: "Kimi", model: "kimi-k2" },
      ],
      activeId: "p1",
    });
    mocks.activate.mockResolvedValue(undefined);
    mocks.presets.mockResolvedValue([]);
    mocks.conversationList.mockResolvedValue([]);
    mocks.messages.mockResolvedValue([]);
    mocks.hitlEvents.mockResolvedValue([]);
    mocks.skillsList.mockResolvedValue([
      { name: "nginx-check", description: "检查 nginx 配置", disabled: false },
      { name: "disk-report", description: "整理磁盘占用", disabled: false },
      { name: "old-skill", description: "已停用", disabled: true },
    ]);
    useUi.setState({ rightOpen: true, aiBusy: false, takeover: null, pushToast: mocks.toast, workspaces: [], aiBoards: {}, sessions: [] });
    view = mount(createElement(AccountAiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flush();
  });

  afterEach(() => {
    view?.unmount();
    view = null;
  });

  function composer(): HTMLTextAreaElement {
    return view!.container.querySelector<HTMLTextAreaElement>('textarea[aria-label="消息输入"]')!;
  }

  function labels(): string[] {
    return [...view!.container.querySelectorAll('[aria-label="命令与技能"] [role="option"] .nx-menu-label')].map((option) => option.textContent ?? "");
  }

  function press(key: string): void {
    act(() => {
      composer().dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
    });
  }

  it("lists built-in commands before enabled skills and filters as the user types", async () => {
    setInputValue(composer(), "/");
    await flush();
    await flush();
    expect(labels()).toEqual(["/new", "/plan", "/model", "/compact", "/permissions", "/nginx-check", "/disk-report"]);
    setInputValue(composer(), "/disk");
    await flush();
    expect(labels()).toEqual(["/disk-report"]);
  });

  it("fills a skill on click and runs the highlighted item on Enter without sending", async () => {
    setInputValue(composer(), "/ngi");
    await flush();
    await flush();
    click(view!.container.querySelector('[aria-label="命令与技能"] [role="option"]')!);
    await flush();
    expect(composer().value).toBe("/nginx-check ");

    setInputValue(composer(), "/d");
    await flush();
    press("Enter");
    await flush();
    expect(composer().value).toBe("/disk-report ");
    expect(mocks.chat).not.toHaveBeenCalled();
  });

  it("toggles plan mode with /plan", async () => {
    setInputValue(composer(), "/pl");
    await flush();
    press("Enter");
    await flush();
    expect(composer().value).toBe("");
    expect(mocks.toast).toHaveBeenCalledWith("info", "已开启计划模式");
  });

  it("switches the model through /model with arrow keys", async () => {
    setInputValue(composer(), "/model ");
    await flush();
    await flush();
    expect(labels()).toEqual(["DeepSeek", "Kimi"]);
    press("ArrowDown");
    press("Enter");
    await flush();
    expect(mocks.activate).toHaveBeenCalledWith("p2");
    expect(mocks.toast).toHaveBeenCalledWith("success", "已切换模型：Kimi");
  });

  it("explains that /compact needs a conversation first", async () => {
    setInputValue(composer(), "/compact");
    await flush();
    press("Enter");
    await flush();
    expect(mocks.compact).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("info", "当前没有对话内容，无需压缩");
  });

  it("does not open for a slash inside the message", async () => {
    setInputValue(composer(), "check /etc/hosts");
    await flush();
    expect(view!.container.querySelector('[aria-label="命令与技能"]')).toBeNull();
    expect(mocks.skillsList).not.toHaveBeenCalled();
  });
});
