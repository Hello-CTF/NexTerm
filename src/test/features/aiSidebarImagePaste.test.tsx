/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  desktop: false,
  chat: vi.fn(),
  cancel: vi.fn(),
  confirm: vi.fn(),
  answer: vi.fn(),
  hitlSnapshot: vi.fn(),
  hitlEvents: vi.fn(),
  runs: vi.fn(),
  runEvents: vi.fn(),
  getPermission: vi.fn(),
  setPermission: vi.fn(),
  conversationList: vi.fn(),
  conversationDelete: vi.fn(),
  messages: vi.fn(),
  overview: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
  dispose: vi.fn(),
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
    runs: mocks.runs,
    runEvents: mocks.runEvents,
    getPermission: mocks.getPermission,
    setPermission: mocks.setPermission,
    conversationList: mocks.conversationList,
    conversationDelete: mocks.conversationDelete,
    messages: mocks.messages,
  },
  modelApi: { overview: mocks.overview, activate: vi.fn() },
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
  disposeChannel: mocks.dispose,
  onChannelReopen: () => () => undefined,
}));
vi.mock("../../ipc/env", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/env")>();
  return {
    ...actual,
    get WEB() {
      return !mocks.desktop;
    },
    get DESKTOP() {
      return mocks.desktop;
    },
  };
});
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, promptText: vi.fn() }));

import { AiSidebar } from "../../features/ai/AiSidebar";
import { GLOBAL_AI_BOARD_KEY, useUi } from "../../app/store";

function png(name: string): File {
  return new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], name, { type: "image/png" });
}

function pasteDataTransfer(items: unknown[], files: File[]) {
  return {
    items,
    files,
    getData: () => "",
  };
}

function dispatchPaste(view: MountedView, data: ReturnType<typeof pasteDataTransfer>): Event {
  const textarea = view.container.querySelector("textarea");
  if (!textarea) throw new Error("input textarea not found");
  const event = new Event("paste", { bubbles: true, cancelable: true });
  Object.assign(event, { clipboardData: data });
  act(() => {
    textarea.dispatchEvent(event);
  });
  return event;
}

function imageItem(file: File) {
  return { kind: "file", type: file.type, getAsFile: () => file };
}

async function send(view: MountedView, text: string) {
  const textarea = view.container.querySelector("textarea");
  if (!textarea) throw new Error("input textarea not found");
  setInputValue(textarea, text);
  click(view.container.querySelector('button[title="发送 (Enter)"]')!);
  await flush();
}

describe("AiSidebar 图片粘贴与添加", () => {
  let view: MountedView | null = null;

  beforeEach(() => {
    vi.clearAllMocks();
    mocks.desktop = false;
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
    mocks.setPermission.mockResolvedValue(undefined);
    mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
    mocks.conversationList.mockResolvedValue([]);
    mocks.conversationDelete.mockResolvedValue(undefined);
    mocks.messages.mockResolvedValue([]);
    mocks.ask.mockResolvedValue(true);
    localStorage.clear();
    useUi.setState({
      rightOpen: true,
      aiBusy: false,
      takeover: null,
      pushToast: mocks.toast,
      workspaces: [],
      aiBoards: {
        [GLOBAL_AI_BOARD_KEY]: {
          tabs: [{ id: "tab-1", title: "新会话" }],
          activeTabId: "tab-1",
        },
      },
      sessions: [],
      activeWorkspaceId: null,
    });
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
  });

  afterEach(() => {
    view?.unmount();
    view = null;
  });

  it("paste 从 items 取图片并随消息发送", async () => {
    await flush();
    const event = dispatchPaste(view!, pasteDataTransfer([imageItem(png("a.png"))], []));
    expect(event.defaultPrevented).toBe(true);
    await flushUntil(() => view!.container.querySelectorAll(".nx-attach").length === 1);

    await send(view!, "看图");
    expect(mocks.chat).toHaveBeenCalledWith(
      expect.objectContaining({
        message: "看图",
        images: [expect.stringContaining("data:image/png;base64")],
      }),
    );
  });

  it("paste 在 items 为空时回退到 files", async () => {
    await flush();
    const event = dispatchPaste(view!, pasteDataTransfer([], [png("b.png")]));
    expect(event.defaultPrevented).toBe(true);
    await flushUntil(() => view!.container.querySelectorAll(".nx-attach").length === 1);
  });

  it("items 与 files 同时给出同一图片时去重只加一张", async () => {
    await flush();
    const file = png("same.png");
    dispatchPaste(view!, pasteDataTransfer([imageItem(file)], [file]));
    await flushUntil(() => view!.container.querySelectorAll(".nx-attach").length === 1);
    await flush();
    expect(view!.container.querySelectorAll(".nx-attach")).toHaveLength(1);
  });

  it("非图片 paste 不拦截、不加附件", async () => {
    await flush();
    const event = dispatchPaste(
      view!,
      pasteDataTransfer([{ kind: "string", type: "text/plain" }], []),
    );
    expect(event.defaultPrevented).toBe(false);
    await flush();
    expect(view!.container.querySelectorAll(".nx-attach")).toHaveLength(0);
  });

  it("添加图片按钮经文件选择加入附件并随消息发送", async () => {
    await flush();
    const button = view!.container.querySelector<HTMLButtonElement>('button[aria-label="添加图片"]');
    expect(button).not.toBeNull();
    const input = view!.container.querySelector<HTMLInputElement>('input[type="file"]')!;
    expect(input).not.toBeNull();
    expect(input.accept).toBe("image/*");
    expect(input.className).toContain("hidden");

    Object.defineProperty(input, "files", { value: [png("pick.png")], configurable: true });
    act(() => {
      input.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await flushUntil(() => view!.container.querySelectorAll(".nx-attach").length === 1);

    await send(view!, "看这张图");
    expect(mocks.chat).toHaveBeenCalledWith(
      expect.objectContaining({
        message: "看这张图",
        images: [expect.stringContaining("data:image/png;base64")],
      }),
    );
  });

  it("placeholder 按平台区分：web 承诺可直接粘贴，桌面端不承诺", async () => {
    await flush();
    const placeholder = () =>
      view!.container.querySelector("textarea")?.getAttribute("placeholder") ?? "";
    expect(placeholder()).toContain("可直接粘贴图片");

    view?.unmount();
    mocks.desktop = true;
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flush();
    expect(placeholder()).not.toContain("可直接粘贴图片");
    expect(placeholder()).toContain("向 NexTerm 提问");
  });
});
