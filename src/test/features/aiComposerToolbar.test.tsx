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
import { resetAllKeybindings } from "../../app/keybindings";
import { ComposerMoreMenu } from "../../features/ai/panel/ComposerMoreMenu";

let mounted: MountedView | undefined;

async function show(props: { sessionId?: string; tabId?: string } = { sessionId: "s1", tabId: "t1" }): Promise<void> {
  mounted?.unmount();
  mounted = mount(createElement(AiSidebar, props));
  await flush();
}

const q = (sel: string) => mounted!.container.querySelector<HTMLButtonElement>(sel);

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
    aiBoards: {},
    sessions: [],
  });
  await show();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  resetAllKeybindings();
});

describe("AI composer toolbar", () => {
  it("shows the permission mode as a labeled control", async () => {
    await flush();
    const perm = q('button[title^="AI 权限与模式"]')!;
    expect(perm.textContent).toBe("读写");
    expect(perm.getAttribute("aria-label")).toContain("读写");
    expect(perm.getAttribute("aria-expanded")).toBe("false");
    click(perm);
    expect(perm.getAttribute("aria-expanded")).toBe("true");
  });

  it("keeps the experimental takeover entry inside the more menu", () => {
    expect(q('button[title^="终端接管（实验性功能）"]')).toBeNull();
    const more = q('button[aria-label="更多"]')!;
    expect(more.getAttribute("aria-expanded")).toBe("false");
    click(more);
    const item = q('[role="menuitem"]')!;
    expect(item.textContent).toContain("终端接管");
    expect(item.textContent).toContain("实验性");
    expect(item.className).not.toContain("red");
  });

  it("closes the more menu on Escape and returns focus to its trigger", () => {
    const more = q('button[aria-label="更多"]')!;
    click(more);
    const menu = q('[role="menu"]')!;
    act(() => {
      menu.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(q('[role="menu"]')).toBeNull();
    expect(document.activeElement).toBe(more);
  });

  it("hides the more menu when no terminal tab is open", async () => {
    await show({ sessionId: undefined, tabId: undefined });
    expect(q('button[aria-label="更多"]')).toBeNull();
    expect(q('button[title^="终端接管"]')).toBeNull();
  });

  it("disables send until there is content, then swaps to stop while running", async () => {
    const send = () => q('button[aria-label="发送"]');
    expect(send()?.disabled).toBe(true);
    expect(send()?.getAttribute("title")).toBe("发送 (Enter)");
    setInputValue(mounted!.container.querySelector("textarea")!, "hello");
    await flush();
    expect(send()?.disabled).toBe(false);
    click(send()!);
    await flush();
    expect(q('button[aria-label="停止"]')).not.toBeNull();
    expect(send()).toBeNull();
  });

  it("uses the same toolbar height class family for every control", () => {
    const bar = mounted!.container.querySelector(".nx-ai-composer-actions")!;
    const buttons = [...bar.querySelectorAll("button")];
    expect(buttons.length).toBeGreaterThanOrEqual(4);
    for (const b of buttons) {
      expect(b.className).toMatch(/nx-cbar-icon|nx-cbar-btn|nx-chip|nx-send-btn/);
      expect(b.getAttribute("aria-label") || b.textContent?.trim() || b.getAttribute("title")).toBeTruthy();
    }
  });
});

describe("ComposerMoreMenu", () => {
  it("renders nothing without applicable items", () => {
    const view = mount(createElement(ComposerMoreMenu, { items: [] }));
    expect(view.container.querySelector("button")).toBeNull();
    view.unmount();
  });

  it("explains a disabled item and keyboard-navigates enabled ones", () => {
    const run = vi.fn();
    const view = mount(
      createElement(ComposerMoreMenu, {
        items: [
          { key: "a", label: "甲", title: "甲", icon: null, run },
          { key: "b", label: "乙", title: "乙", icon: null, disabled: true, disabledReason: "运行中不可用", run },
          { key: "c", label: "丙", title: "丙", icon: null, run },
        ],
      }),
    );
    click(view.container.querySelector('button[aria-label="更多"]')!);
    const items = [...view.container.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')];
    expect(items[1].disabled).toBe(true);
    expect(items[1].textContent).toContain("运行中不可用");
    expect(document.activeElement).toBe(items[0]);
    act(() => {
      items[0].dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
    });
    expect(document.activeElement).toBe(items[2]);
    click(items[2]);
    expect(run).toHaveBeenCalledTimes(1);
    expect(view.container.querySelector('[role="menu"]')).toBeNull();
    view.unmount();
  });
});
