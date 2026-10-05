/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from "vitest";
import { act, createElement } from "react";
import {
  click,
  flush,
  flushUntil,
  mount,
  setInputValue,
  type MountedView,
} from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  chat: vi.fn(),
  cancel: vi.fn(),
  confirm: vi.fn(),
  answer: vi.fn(),
  editResend: vi.fn(),
  hitlSnapshot: vi.fn(),
  hitlEvents: vi.fn(),
  runs: vi.fn(),
  runEvents: vi.fn(),
  getPermission: vi.fn(),
  conversationList: vi.fn(),
  conversationDelete: vi.fn(),
  messages: vi.fn(),
  overview: vi.fn(),
  usageSummary: vi.fn(),
  toast: vi.fn(),
  dispose: vi.fn(),
  channels: [] as { onEvent: (ev: Record<string, unknown>) => void }[],
}));

vi.mock("../ipc/commands", () => ({
  aiApi: {
    chat: mocks.chat,
    cancel: mocks.cancel,
    confirm: mocks.confirm,
    answer: mocks.answer,
    editResend: mocks.editResend,
    hitlSnapshot: mocks.hitlSnapshot,
    hitlEvents: mocks.hitlEvents,
    runs: mocks.runs,
    runEvents: mocks.runEvents,
    getPermission: mocks.getPermission,
    conversationList: mocks.conversationList,
    conversationDelete: mocks.conversationDelete,
    messages: mocks.messages,
  },
  modelApi: { overview: mocks.overview, activate: vi.fn(), usageSummary: mocks.usageSummary },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: {},
}));
vi.mock("../ipc/events", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../ipc/events")>()),
  createAiChannel: (onEvent: (ev: Record<string, unknown>) => void) => {
    const channel = { onEvent };
    mocks.channels.push(channel);
    return channel;
  },
  disposeChannel: mocks.dispose,
  onChannelReopen: () => () => undefined,
}));

import { AiSidebar } from "../features/ai/AiSidebar";
import { useUi } from "../app/store";

function message(id: string, role: string, content: string) {
  return {
    id,
    conversationId: "conv-1",
    role,
    content: { role, content },
    tokensIn: null,
    tokensOut: null,
    createdAt: 1,
  };
}

function rect(top: number, height: number): DOMRect {
  return {
    top,
    height,
    bottom: top + height,
    left: 0,
    right: 0,
    width: 0,
    x: 0,
    y: top,
    toJSON: () => ({}),
  } as DOMRect;
}

let currentScrollTop = 0;
let topOf: (index: number) => number = (i) => 200 + i * 150;
let heightOf: (index: number) => number = (i) => (i === 2 || i === 70 ? 300 : 30);

function stubGeometry(view: MountedView, scrollHeight = Number.MAX_SAFE_INTEGER): HTMLDivElement {
  const log = view.container.querySelector<HTMLDivElement>('div[role="log"]');
  if (!log) throw new Error("log container not found");
  Object.defineProperty(log, "clientHeight", { configurable: true, get: () => 400 });
  Object.defineProperty(log, "scrollHeight", { configurable: true, get: () => scrollHeight });
  Object.defineProperty(log, "scrollTop", {
    configurable: true,
    get: () => currentScrollTop,
    set: (value: number) => {
      const max = Math.max(0, log.scrollHeight - log.clientHeight);
      currentScrollTop = Math.min(Math.max(0, value), max);
    },
  });
  log.getBoundingClientRect = () => rect(0, 0);
  return log;
}

function wrapperOf(view: MountedView, index: number): HTMLElement | null {
  return view.container.querySelector<HTMLElement>(`[data-conversation-index="${index}"]`);
}

describe("AiSidebar search jump", () => {
  let view: MountedView | null = null;
  let rectSpy: MockInstance | null = null;

  beforeEach(async () => {
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      setTimeout(cb, 0);
      return 1;
    });
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
    currentScrollTop = 0;
    topOf = (i) => 200 + i * 150;
    heightOf = (i) => (i === 2 || i === 70 ? 300 : 30);
    mocks.channels.length = 0;
    mocks.runs.mockResolvedValue([]);
    mocks.runEvents.mockResolvedValue([]);
    mocks.hitlSnapshot.mockResolvedValue({
      runId: "job-x",
      checkpointId: "job-x",
      status: "running",
      attempt: 1,
      seq: 0,
      pending: [],
    });
    mocks.hitlEvents.mockResolvedValue([]);
    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "演示", createdAt: 0, updatedAt: 0, scope: {} }]);
    mocks.messages.mockResolvedValue([]);
    mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
    mocks.usageSummary.mockResolvedValue([]);
    rectSpy = vi
      .spyOn(HTMLElement.prototype, "getBoundingClientRect")
      .mockImplementation(function (this: HTMLElement) {
        const index = this.getAttribute("data-conversation-index");
        if (index !== null) {
          const i = Number(index);
          return rect(topOf(i) - currentScrollTop, heightOf(i));
        }
        return rect(0, 0);
      });
    localStorage.setItem("nexterm.ai.conversation.v1", "conv-1");
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
    rectSpy?.mockRestore();
    rectSpy = null;
    localStorage.removeItem("nexterm.ai.conversation.v1");
    vi.unstubAllGlobals();
  });

  it("按真实几何居中命中项：短气泡与高气泡都不用 index*88 估算", async () => {
    const text = () => view!.container.textContent ?? "";
    mocks.messages.mockResolvedValue([
      message("m1", "user", "alpha"),
      message("m2", "assistant", "beta 目标短语"),
      message("m3", "user", "gamma 目标短语"),
      message("m4", "assistant", "delta"),
    ]);
    view?.unmount();
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flushUntil(() => text().includes("delta"));
    const log = stubGeometry(view!);

    click(view!.container.querySelector('button[title="搜索对话内容"]')!);
    await flush();
    setInputValue(
      view!.container.querySelector('input[aria-label="搜索对话内容"]') as HTMLInputElement,
      "目标短语",
    );
    await flush();

    click(view!.container.querySelector('button[aria-label="下一个匹配"]')!);
    await flush();
    expect(log.scrollTop).toBe(topOf(2) - (400 - heightOf(2)) / 2);
    expect(log.scrollTop).not.toBe(2 * 88);
    expect(wrapperOf(view!, 2)?.className).toContain("ring-amber");

    click(view!.container.querySelector('button[aria-label="上一个匹配"]')!);
    await flush();
    expect(log.scrollTop).toBe(topOf(1) - (400 - heightOf(1)) / 2);
    expect(log.scrollTop).not.toBe(88);
    expect(wrapperOf(view!, 1)?.className).toContain("ring-amber");
  });

  it("虚拟化窗口外命中：先估算挂载窗口再按真实位置校正", async () => {
    const text = () => view!.container.textContent ?? "";
    const rows = Array.from({ length: 80 }, (_, i) =>
      message(`m${i}`, i % 2 === 0 ? "user" : "assistant", `词${i} ${i % 2 === 0 ? "问题" : "回答"}`),
    );
    mocks.messages.mockResolvedValue(rows);
    view?.unmount();
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flushUntil(() => text().includes("词1 回答"));
    const log = stubGeometry(view!);
    expect(wrapperOf(view!, 70)).toBeNull();

    click(view!.container.querySelector('button[title="搜索对话内容"]')!);
    await flush();
    setInputValue(
      view!.container.querySelector('input[aria-label="搜索对话内容"]') as HTMLInputElement,
      "词70",
    );
    await flush();

    click(view!.container.querySelector('button[aria-label="下一个匹配"]')!);
    await flush();
    expect(log.scrollTop).toBe(70 * 88);
    expect(wrapperOf(view!, 70)).toBeNull();

    act(() => {
      log.dispatchEvent(new Event("scroll"));
    });
    await flush();
    expect(wrapperOf(view!, 70)).not.toBeNull();
    const expected = topOf(70) - (400 - heightOf(70)) / 2;
    expect(log.scrollTop).toBe(expected);
    expect(log.scrollTop).not.toBe(70 * 88);
    expect(wrapperOf(view!, 70)?.className).toContain("ring-amber");

    act(() => {
      log.dispatchEvent(new Event("scroll"));
    });
    await flush();
    expect(wrapperOf(view!, 70)).not.toBeNull();
    expect(log.scrollTop).toBe(expected);
    expect(wrapperOf(view!, 70)?.className).toContain("ring-amber");
  });

  it("reviewer 场景：100 条中命中 50，前置 6 个 300px 气泡，校正后的第二次 scroll 不再卸载目标", async () => {
    const text = () => view!.container.textContent ?? "";
    topOf = (i) => (i <= 44 ? i * 88 : 3872 + (i - 44) * 300);
    heightOf = (i) => (i >= 44 && i <= 49 ? 300 : 30);
    const rows = Array.from({ length: 100 }, (_, i) =>
      message(`m${i}`, i % 2 === 0 ? "user" : "assistant", `词${i} ${i % 2 === 0 ? "问题" : "回答"}`),
    );
    mocks.messages.mockResolvedValue(rows);
    view?.unmount();
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flushUntil(() => text().includes("词1 回答"));
    const log = stubGeometry(view!);

    click(view!.container.querySelector('button[title="搜索对话内容"]')!);
    await flush();
    setInputValue(
      view!.container.querySelector('input[aria-label="搜索对话内容"]') as HTMLInputElement,
      "词50",
    );
    await flush();

    click(view!.container.querySelector('button[aria-label="下一个匹配"]')!);
    await flush();
    expect(log.scrollTop).toBe(50 * 88);

    act(() => {
      log.dispatchEvent(new Event("scroll"));
    });
    await flush();
    expect(wrapperOf(view!, 50)).not.toBeNull();
    const centered = 5672 - (400 - 30) / 2;
    expect(log.scrollTop).toBe(centered);

    act(() => {
      log.dispatchEvent(new Event("scroll"));
    });
    await flush();
    expect(wrapperOf(view!, 50)).not.toBeNull();
    expect(log.scrollTop).toBe(centered);
    expect(wrapperOf(view!, 50)?.className).toContain("ring-amber");

    await flush();
    expect(log.scrollTop).toBe(centered);
  });

  it("底部边界：短最后项按真实最大偏移 clamp 并判定 settled，手滚离开无 snapback、无 frozen 残留", async () => {
    const text = () => view!.container.textContent ?? "";
    topOf = (i) => i * 88;
    heightOf = (i) => (i === 99 ? 30 : 88);
    const rows = Array.from({ length: 100 }, (_, i) =>
      message(`m${i}`, i % 2 === 0 ? "user" : "assistant", `词${i} ${i % 2 === 0 ? "问题" : "回答"}`),
    );
    mocks.messages.mockResolvedValue(rows);
    view?.unmount();
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    await flushUntil(() => text().includes("词1 回答"));
    const maxScroll = 99 * 88 + 30 - 400;
    const log = stubGeometry(view!, 99 * 88 + 30);

    click(view!.container.querySelector('button[title="搜索对话内容"]')!);
    await flush();
    setInputValue(
      view!.container.querySelector('input[aria-label="搜索对话内容"]') as HTMLInputElement,
      "词99",
    );
    await flush();

    click(view!.container.querySelector('button[aria-label="下一个匹配"]')!);
    await flush();
    expect(log.scrollTop).toBe(maxScroll);

    act(() => {
      log.dispatchEvent(new Event("scroll"));
    });
    await flush();
    expect(wrapperOf(view!, 99)).not.toBeNull();
    expect(log.scrollTop).toBe(maxScroll);
    expect(log.scrollTop).not.toBe(99 * 88 - (400 - 30) / 2);
    expect(wrapperOf(view!, 99)?.className).toContain("ring-amber");

    log.scrollTop = 100;
    act(() => {
      log.dispatchEvent(new Event("scroll"));
    });
    await flush();
    expect(log.scrollTop).toBe(100);
    expect(wrapperOf(view!, 0)).not.toBeNull();
    expect(wrapperOf(view!, 99)).toBeNull();
  });
});
