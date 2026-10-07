/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, mount, type MountedView } from "./reactTestUtils";

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
  dispose: vi.fn(),
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
  onChannelReopen: (channel: unknown, cb: () => void) => {
    mocks.reopens.set(channel, cb);
    return () => mocks.reopens.delete(channel);
  },
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, promptText: mocks.promptText }));

import { AiSidebar } from "../../features/ai/AiSidebar";
import { useUi } from "../../app/store";
import { getKeybinding, resetAllKeybindings, setKeybinding } from "../../app/keybindings";

let mounted: MountedView | undefined;

function collapseButton(): HTMLButtonElement | null {
  return (
    [...(mounted?.container.querySelectorAll("button") ?? [])].find((b) =>
      b.getAttribute("title")?.startsWith("收起 AI 侧栏"),
    ) ?? null
  );
}

function takeoverBadge(): HTMLElement | null {
  return mounted?.container.querySelector<HTMLElement>(".nx-badge-red") ?? null;
}

async function show(): Promise<void> {
  mounted = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
  await flush();
}

beforeEach(async () => {
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
  mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
  mocks.conversationList.mockResolvedValue([]);
  mocks.conversationDelete.mockResolvedValue(undefined);
  mocks.messages.mockResolvedValue([]);
  mocks.takeoverEnter.mockResolvedValue({ token: "tok-1" });
  mocks.takeoverRun.mockResolvedValue({ jobId: "job-t", token: "tok-2" });
  mocks.takeoverExit.mockResolvedValue(undefined);
  useUi.setState({
    rightOpen: true,
    rightWidth: 352,
    aiBusy: false,
    takeover: null,
    pushToast: mocks.toast,
    workspaces: [],
    sessions: [],
  });
  await show();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  resetAllKeybindings();
});

describe("AiSidebar shortcut hints follow keybindings", () => {
  it("shows the default Ctrl+J hint on the collapse button", () => {
    const button = collapseButton();
    expect(button?.getAttribute("title")).toBe("收起 AI 侧栏 (Ctrl+J)");
    expect(button?.getAttribute("aria-keyshortcuts")).toBe("Control+J");
  });

  it("reflects a rebound toggleAiSidebar immediately and after remount", async () => {
    act(() => setKeybinding("toggleAiSidebar", "Alt+Shift+j"));
    expect(collapseButton()?.getAttribute("title")).toBe("收起 AI 侧栏 (Shift+Alt+J)");
    expect(collapseButton()?.getAttribute("aria-keyshortcuts")).toBe("Shift+Alt+J");

    mounted?.unmount();
    await show();
    expect(collapseButton()?.getAttribute("title")).toBe("收起 AI 侧栏 (Shift+Alt+J)");
    expect(getKeybinding("toggleAiSidebar")).toBe("Shift+Alt+j");
  });

  it("drops the hint when the binding is cleared", () => {
    act(() => setKeybinding("toggleAiSidebar", null));
    const button = collapseButton();
    expect(button?.getAttribute("title")).toBe("收起 AI 侧栏");
    expect(button?.getAttribute("aria-keyshortcuts")).toBeNull();
  });

  it("keeps the collapse behaviour itself unchanged", async () => {
    const button = collapseButton();
    expect(button).not.toBeNull();
    click(button as HTMLButtonElement);
    await flush();
    expect(useUi.getState().rightOpen).toBe(false);
  });

  it("shows the reclaim hint on the takeover badge and follows rebinds", () => {
    act(() => {
      useUi.setState({
        takeover: {
          tabId: "t1",
          jobId: "job-t",
          token: "tok-2",
          task: "安装 nginx",
          allowWrite: true,
          startedAt: Date.now(),
        },
      });
    });
    expect(takeoverBadge()?.getAttribute("title")).toBe("终端接管进行中：按 Esc 随时夺回");

    act(() => setKeybinding("reclaimTakeover", "Ctrl+Shift+r"));
    expect(takeoverBadge()?.getAttribute("title")).toBe("终端接管进行中：按 Ctrl+Shift+R 随时夺回");

    act(() => setKeybinding("reclaimTakeover", null));
    expect(takeoverBadge()?.getAttribute("title")).toBe("终端接管进行中");
  });

  it("shows the reclaim hint on the takeover launch button and follows rebinds", () => {
    const launch = [...(mounted?.container.querySelectorAll("button") ?? [])].find((b) =>
      b.getAttribute("title")?.startsWith("终端接管（实验性功能）"),
    );
    expect(launch?.getAttribute("title")).toBe(
      "终端接管（实验性功能）：AI 直接接手当前终端，随时按 Esc 夺回",
    );
    act(() => setKeybinding("reclaimTakeover", "Ctrl+Shift+r"));
    const rebound = [...(mounted?.container.querySelectorAll("button") ?? [])].find((b) =>
      b.getAttribute("title")?.startsWith("终端接管（实验性功能）"),
    );
    expect(rebound?.getAttribute("title")).toBe(
      "终端接管（实验性功能）：AI 直接接手当前终端，随时按 Ctrl+Shift+R 夺回",
    );
  });
});
