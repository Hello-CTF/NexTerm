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
import { UsageRing } from "../../features/ai/UsageRing";
import { useUi } from "../../app/store";

function keyDown(target: EventTarget, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

function keyDown229(target: EventTarget, key: string): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true });
  Object.defineProperty(event, "keyCode", { value: 229 });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

function emit(ev: Record<string, unknown>) {
  const channel = mocks.channels.at(-1);
  if (!channel) throw new Error("no fake channel");
  act(() => channel.onEvent(ev));
}

describe("M119 AI responsive fixes", () => {
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
      aiBoards: {},
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

  it("does not send on Enter while IME composition is active", async () => {
    setInputValue(composer(), "ni'h");
    keyDown(composer(), "Enter", { isComposing: true });
    await flush();
    expect(mocks.chat).not.toHaveBeenCalled();
    expect(composer().value).toBe("ni'h");

    keyDown229(composer(), "Enter");
    await flush();
    expect(mocks.chat).not.toHaveBeenCalled();
    expect(composer().value).toBe("ni'h");

    keyDown(composer(), "Enter", { shiftKey: true });
    await flush();
    expect(mocks.chat).not.toHaveBeenCalled();

    keyDown(composer(), "Enter");
    await flush();
    expect(mocks.chat).toHaveBeenCalledOnce();
  });

  it("keeps long uninterrupted user content wrapped inside the bubble", async () => {
    const token = "a1b2c3d4e5".repeat(22);
    setInputValue(composer(), token);
    click(view!.container.querySelector('button[title="发送 (Enter)"]')!);
    await flush();
    const pre = view!.container.querySelector('div[role="log"] pre.break-words');
    expect(pre).not.toBeNull();
    expect(pre!.textContent).toBe(token);
    const bubble = pre!.closest("div.rounded-\\[10px\\]");
    expect(bubble?.className).toContain("--nx-fg-on-tint");
  });

  it("renders the confirmation card inside the log scroll flow with a readable body", async () => {
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

    const log = view!.container.querySelector('div[role="log"]');
    expect(log).not.toBeNull();
    const allow = [...view!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "允许一次",
    );
    expect(allow).not.toBeNull();
    expect(log!.contains(allow!)).toBe(true);
    const alert = allow!.closest(".nx-alert");
    expect(alert).not.toBeNull();
    expect(alert!.parentElement).toBe(log);
    const body = alert!.querySelector("pre.overflow-auto");
    expect(body).not.toBeNull();
    expect(body!.textContent).toContain("rm -rf");
    const submit = [...alert!.querySelectorAll("button")];
    expect(submit.length).toBeGreaterThanOrEqual(4);
  });

  it("makes the UsageRing details reachable by keyboard focus, not hover only", () => {
    const ring = mount(
      createElement(UsageRing, {
        usage: { promptTokens: 1200, completionTokens: 300, cachedTokens: 600, contextWindow: 8192 },
      }),
    );
    const img = ring.container.querySelector<HTMLElement>('span[role="img"]');
    expect(img).not.toBeNull();
    expect(img!.tabIndex).toBe(0);
    const tooltip = ring.container.querySelector(".group-focus-within\\:block");
    expect(tooltip).not.toBeNull();
    ring.unmount();
  });

  it("keeps the interactive UsageRing button as the only focus stop with a coarse touch target", () => {
    const ring = mount(createElement(UsageRing, { loadSummary: () => Promise.resolve([]) }));
    const button = ring.container.querySelector<HTMLButtonElement>("button");
    expect(button).not.toBeNull();
    expect(button!.tabIndex).toBe(0);
    expect(button!.className).toContain("pointer-coarse:min-h-6");
    expect(button!.className).toContain("pointer-coarse:min-w-6");
    const img = ring.container.querySelector<HTMLElement>('span[role="img"]');
    expect(img!.tabIndex).toBe(-1);
    expect(ring.container.querySelector(".group-focus-within\\:block")).not.toBeNull();
    ring.unmount();
  });

  it("clamps the model dropdown width to the viewport", async () => {
    const selector = mount(createElement(ModelSelector, { onManage: () => undefined }));
    await flush();
    const chip = selector.container.querySelector("button.nx-chip") as HTMLButtonElement;
    expect(chip.className).toContain("min-w-0");
    const openAt = (left: number, innerWidth: number) => {
      chip.getBoundingClientRect = () =>
        ({ left, right: left + 80, top: 0, bottom: 0, width: 80, height: 21, x: left, y: 0, toJSON: () => ({}) }) as DOMRect;
      Object.defineProperty(window, "innerWidth", { value: innerWidth, configurable: true });
      click(chip);
    };
    openAt(300, 320);
    await flush();
    const dropdown = selector.container.querySelector(".nx-menu-title")?.parentElement as HTMLElement;
    expect(dropdown).not.toBeNull();
    const width = parseFloat(dropdown.style.width);
    expect(300 + width).toBeLessThanOrEqual(320);
    expect(width).toBeLessThan(248);
    click(chip);
    await flush();
    openAt(103, 320);
    await flush();
    const reopened = selector.container.querySelector(".nx-menu-title")?.parentElement as HTMLElement;
    const width2 = parseFloat(reopened.style.width);
    expect(103 + width2).toBeLessThanOrEqual(320);
    expect(width2).toBe(209);
    selector.unmount();
    Object.defineProperty(window, "innerWidth", { value: 1024, configurable: true });
  });

  it("stacks the model manager into a single column below 560px", async () => {
    const manager = mount(createElement(ModelManager, {}));
    await flush();
    const listColumn = [...manager.container.querySelectorAll("div")].find((d) =>
      d.className.includes("min-[560px]:w-[30%]"),
    );
    expect(listColumn).not.toBeNull();
    expect(listColumn!.className).toContain("w-full");
    const row = listColumn!.parentElement!;
    expect(row.className).toContain("min-[560px]:flex-row");
    expect(row.className).toContain("flex-col");
    const listBox = listColumn!.querySelector(".overflow-y-auto");
    expect(listBox!.className).toContain("max-h-56");
    expect(listBox!.className).toContain("min-[560px]:max-h-[420px]");
    manager.unmount();
  });
});
