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
import {
  CONVERSATION_SCROLL_SETTLE_MS,
  VIRTUAL_ITEM_ESTIMATE_PX,
  VIRTUAL_ITEM_GAP_PX,
} from "../features/ai/conversationVirtual";

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
import { GLOBAL_AI_BOARD_KEY, useUi } from "../app/store";

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

const STEP = VIRTUAL_ITEM_ESTIMATE_PX + VIRTUAL_ITEM_GAP_PX;
const VIEWPORT = 400;

let currentScrollTop = 0;
let topOf: (index: number) => number = (i) => 200 + i * 150;
let heightOf: (index: number) => number = (i) => (i === 2 || i === 70 ? 300 : 30);

function stubGeometry(view: MountedView, scrollHeight: number | (() => number) = Number.MAX_SAFE_INTEGER): HTMLDivElement {
  const log = view.container.querySelector<HTMLDivElement>('div[role="log"]');
  if (!log) throw new Error("log container not found");
  Object.defineProperty(log, "clientHeight", { configurable: true, get: () => VIEWPORT });
  Object.defineProperty(log, "scrollHeight", {
    configurable: true,
    get: () => (typeof scrollHeight === "function" ? scrollHeight() : scrollHeight),
  });
  Object.defineProperty(log, "scrollTop", {
    configurable: true,
    get: () => currentScrollTop,
    set: (value: number) => {
      const max = Math.max(0, log.scrollHeight - log.clientHeight);
      currentScrollTop = Math.min(Math.max(0, value), max);
    },
  });
  log.scrollTo = (options?: ScrollToOptions | number) => {
    if (typeof options === "object" && options) log.scrollTop = options.top ?? 0;
  };
  log.getBoundingClientRect = () => rect(0, 0);
  return log;
}

function wrapperOf(view: MountedView, index: number): HTMLElement | null {
  return view.container.querySelector<HTMLElement>(`[data-conversation-index="${index}"]`);
}

function dispatchScroll(log: HTMLDivElement) {
  act(() => {
    log.dispatchEvent(new Event("scroll"));
  });
}

describe("AiSidebar search jump", () => {
  let view: MountedView | null = null;
  let rectSpy: MockInstance | null = null;
  let offsetSpy: MockInstance | null = null;

  beforeEach(async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"] });
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
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "Demo", createdAt: 0, updatedAt: 0, scope: {} }]);
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
    offsetSpy = vi
      .spyOn(HTMLElement.prototype, "offsetHeight", "get")
      .mockImplementation(function (this: HTMLElement) {
        const index = this.getAttribute("data-conversation-index");
        if (index !== null) return heightOf(Number(index));
        if (this.getAttribute("role") === "log") return VIEWPORT;
        return 0;
      });
    useUi.setState({
      rightOpen: true,
      aiBusy: false,
      takeover: null,
      pushToast: mocks.toast,
      workspaces: [],
      aiBoards: {
        [GLOBAL_AI_BOARD_KEY]: {
          tabs: [{ id: "tab-bound", title: "Demo", conversationId: "conv-1" }],
          activeTabId: "tab-bound",
        },
      },
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
    offsetSpy?.mockRestore();
    offsetSpy = null;
    vi.unstubAllGlobals();
  });

  function conversationWith(rows: ReturnType<typeof message>[]) {
    mocks.messages.mockResolvedValue(rows);
    view?.unmount();
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
  }

  async function search(query: string) {
    click(view!.container.querySelector('button[title="搜索对话内容"]')!);
    await flush();
    setInputValue(
      view!.container.querySelector('input[aria-label="搜索对话内容"]') as HTMLInputElement,
      query,
    );
    await flush();
  }

  function clickNext() {
    click(view!.container.querySelector('button[aria-label="下一个匹配"]')!);
  }

  it("centers matches using real geometry: short and tall bubbles never use an index*88 estimate", async () => {
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

    await search("目标短语");

    clickNext();
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

  it("match outside the rendered window: jump centers the virtual index, then the target mounts", async () => {
    const text = () => view!.container.textContent ?? "";
    heightOf = () => VIRTUAL_ITEM_ESTIMATE_PX;
    topOf = (i) => VIEWPORT + i * STEP;
    conversationWith(
      Array.from({ length: 80 }, (_, i) =>
        message(`m${i}`, i % 2 === 0 ? "user" : "assistant", `词${i} ${i % 2 === 0 ? "问题" : "回答"}`),
      ),
    );
    await flushUntil(() => text().includes("词1 回答"));
    const log = stubGeometry(view!);
    expect(wrapperOf(view!, 70)).toBeNull();

    await search("词70");
    clickNext();
    await flush();
    const estimated = VIEWPORT + 70 * STEP - (VIEWPORT - VIRTUAL_ITEM_ESTIMATE_PX) / 2;
    expect(log.scrollTop).toBe(estimated);
    expect(log.scrollTop).not.toBe(70 * VIRTUAL_ITEM_ESTIMATE_PX);
    expect(wrapperOf(view!, 70)).toBeNull();

    dispatchScroll(log);
    await flush();
    const centered = topOf(70) - (VIEWPORT - heightOf(70)) / 2;
    expect(wrapperOf(view!, 70)).not.toBeNull();
    expect(wrapperOf(view!, 70)?.className).toContain("ring-amber");
    expect(log.scrollTop).toBe(centered);

    dispatchScroll(log);
    await flush();
    expect(wrapperOf(view!, 70)).not.toBeNull();
    expect(log.scrollTop).toBe(centered);
  });

  it("keeps following the real bottom as measured heights replace estimates", async () => {
    let resolveMessages!: (rows: ReturnType<typeof message>[]) => void;
    mocks.messages.mockReturnValue(new Promise((resolve) => { resolveMessages = resolve; }));
    view?.unmount();
    view = mount(createElement(AiSidebar, { sessionId: "s1", tabId: "t1" }));
    heightOf = (i) => (i % 12 === 2 ? 300 : VIRTUAL_ITEM_ESTIMATE_PX);
    const log = stubGeometry(view!, () =>
      24 + [...log.children].reduce((total, child) => {
        const element = child as HTMLElement;
        const index = element.getAttribute("data-conversation-index");
        const height = index === null ? Number.parseFloat(element.style.height) || 0 : heightOf(Number(index));
        return total + (Number.parseFloat(element.style.marginTop) || 0) + height;
      }, 0),
    );

    resolveMessages(Array.from({ length: 100 }, (_, i) =>
      message(`m${i}`, i % 2 === 0 ? "user" : "assistant", `词${i}`),
    ));
    await flushUntil(() => view!.container.querySelector("[data-conversation-index]") !== null);
    dispatchScroll(log);
    for (let i = 0; i < 8; i++) await flush();

    expect(wrapperOf(view!, 99)).not.toBeNull();
    expect(log.scrollTop).toBe(log.scrollHeight - VIEWPORT);
  });

  it("centers a virtual match again with real container geometry after it mounts", async () => {
    const text = () => view!.container.textContent ?? "";
    heightOf = () => VIRTUAL_ITEM_ESTIMATE_PX;
    topOf = (i) => VIEWPORT + 12 + i * STEP;
    conversationWith(
      Array.from({ length: 80 }, (_, i) =>
        message(`m${i}`, i % 2 === 0 ? "user" : "assistant", `词${i} ${i % 2 === 0 ? "问题" : "回答"}`),
      ),
    );
    await flushUntil(() => text().includes("词1 回答"));
    const log = stubGeometry(view!);
    const estimated = VIEWPORT + 70 * STEP - (VIEWPORT - VIRTUAL_ITEM_ESTIMATE_PX) / 2;

    await search("词70");
    clickNext();
    await flush();
    expect(log.scrollTop).toBe(estimated);

    dispatchScroll(log);
    await flush();
    expect(wrapperOf(view!, 70)).not.toBeNull();
    expect(log.scrollTop).toBe(estimated + 12);

    dispatchScroll(log);
    await flush();
    expect(log.scrollTop).toBe(estimated + 12);
  });

  it("tall bubbles ahead of the match: the window corrects to measured heights and does not rebound", async () => {
    const text = () => view!.container.textContent ?? "";
    heightOf = (i) => (i >= 44 && i <= 49 ? 300 : VIRTUAL_ITEM_ESTIMATE_PX);
    topOf = (i) => {
      let start = VIEWPORT;
      for (let j = 0; j < i; j++) start += heightOf(j) + VIRTUAL_ITEM_GAP_PX;
      return start;
    };
    conversationWith(
      Array.from({ length: 100 }, (_, i) =>
        message(`m${i}`, i % 2 === 0 ? "user" : "assistant", `词${i} ${i % 2 === 0 ? "问题" : "回答"}`),
      ),
    );
    await flushUntil(() => text().includes("词1 回答"));
    const log = stubGeometry(view!);
    expect(wrapperOf(view!, 50)).toBeNull();

    await search("词50");
    clickNext();
    await flush();
    const estimatedJump = VIEWPORT + 50 * STEP - (VIEWPORT - VIRTUAL_ITEM_ESTIMATE_PX) / 2;
    expect(log.scrollTop).toBe(estimatedJump);

    for (let i = 0; i < 5; i++) {
      dispatchScroll(log);
      await flush();
    }
    expect(wrapperOf(view!, 50)).not.toBeNull();
    expect(wrapperOf(view!, 50)?.className).toContain("ring-amber");
    const startOfMatch = VIEWPORT + 44 * STEP + 6 * (300 + VIRTUAL_ITEM_GAP_PX);
    const settledCenter = startOfMatch - (VIEWPORT - VIRTUAL_ITEM_ESTIMATE_PX) / 2;
    const settled = log.scrollTop;
    expect(settled).toBe(settledCenter);
    expect(settled).toBeGreaterThan(estimatedJump);

    dispatchScroll(log);
    await flush();
    expect(log.scrollTop).toBe(settled);
    expect(wrapperOf(view!, 50)).not.toBeNull();
  });

  it("bottom boundary: a short last item clamps to the real maximum offset; manual scrolling away does not snap back", async () => {
    const text = () => view!.container.textContent ?? "";
    heightOf = (i) => (i === 99 ? 30 : VIRTUAL_ITEM_ESTIMATE_PX);
    conversationWith(
      Array.from({ length: 100 }, (_, i) =>
        message(`m${i}`, i % 2 === 0 ? "user" : "assistant", `词${i} ${i % 2 === 0 ? "问题" : "回答"}`),
      ),
    );
    await flushUntil(() => text().includes("词1 回答"));
    const totalSize = 99 * STEP + 30;
    const maxScroll = totalSize - VIEWPORT;
    const log = stubGeometry(view!, totalSize);

    await search("词99");
    clickNext();
    await flush();
    expect(log.scrollTop).toBe(maxScroll);

    dispatchScroll(log);
    await flush();
    expect(wrapperOf(view!, 99)).not.toBeNull();
    expect(log.scrollTop).toBe(maxScroll);
    expect(wrapperOf(view!, 99)?.className).toContain("ring-amber");

    log.dispatchEvent(new WheelEvent("wheel", { deltaY: -240 }));
    log.scrollTop = 100;
    dispatchScroll(log);
    await flush();
    expect(log.scrollTop).toBe(100);
    expect(wrapperOf(view!, 0)).not.toBeNull();
    expect(wrapperOf(view!, 99)).toBeNull();
  });

  it("reveals the first match immediately after entering a query, without clicking next first", async () => {
    const text = () => view!.container.textContent ?? "";
    heightOf = () => VIRTUAL_ITEM_ESTIMATE_PX;
    topOf = (i) => VIEWPORT + i * STEP;
    conversationWith(
      Array.from({ length: 80 }, (_, i) =>
        message(`m${i}`, i % 2 === 0 ? "user" : "assistant", `词${i} ${i % 2 === 0 ? "问题" : "回答"}`),
      ),
    );
    await flushUntil(() => text().includes("词1 回答"));
    const log = stubGeometry(view!);

    await search("词70");
    expect(log.scrollTop).toBe(VIEWPORT + 70 * STEP - (VIEWPORT - VIRTUAL_ITEM_ESTIMATE_PX) / 2);

    dispatchScroll(log);
    await flush();
    expect(wrapperOf(view!, 70)).not.toBeNull();
    expect(wrapperOf(view!, 70)?.className).toContain("ring-amber");
  });

  it("keeps index 99 and wrapped middle centered after font changes and streaming", async () => {
    const text = () => view!.container.textContent ?? "";
    const observers: { callback: ResizeObserverCallback; targets: Set<Element> }[] = [];
    vi.stubGlobal("ResizeObserver", class {
      readonly targets = new Set<Element>();
      constructor(readonly callback: ResizeObserverCallback) {
        observers.push(this);
      }
      observe(target: Element) {
        this.targets.add(target);
      }
      unobserve(target: Element) {
        this.targets.delete(target);
      }
      disconnect() {
        this.targets.clear();
      }
    });
    heightOf = (i) => (i === 99 ? 30 : VIRTUAL_ITEM_ESTIMATE_PX);
    topOf = (i) => {
      let start = VIEWPORT;
      for (let j = 0; j < i; j++) start += heightOf(j) + VIRTUAL_ITEM_GAP_PX;
      return start;
    };
    conversationWith(
      Array.from({ length: 100 }, (_, i) =>
        message(`m${i}`, i % 2 === 0 ? "user" : "assistant", i === 0 || i === 50 || i === 99 ? `needle ${i}` : `其他 ${i}`),
      ),
    );
    await flushUntil(() => text().includes("其他 1"));
    let conversationCount = 100;
    const log = stubGeometry(
      view!,
      () => 24 + 2 * VIEWPORT + Array.from({ length: conversationCount }, (_, i) => heightOf(i)).reduce((sum, height) => sum + height + VIRTUAL_ITEM_GAP_PX, 0),
    );

    mocks.chat.mockResolvedValue({ jobId: "job-live", conversationId: "conv-1" });
    setInputValue(view!.container.querySelector('textarea[aria-label="消息输入"]')!, "stream");
    click(view!.container.querySelector('button[title="发送 (Enter)"]')!);
    await flush();
    act(() => mocks.channels.at(-1)!.onEvent({ type: "delta", text: "more" }));
    await flush();
    act(() => mocks.channels.at(-1)!.onEvent({ type: "done", answer: "finished" }));
    conversationCount = 102;
    await flush();

    document.documentElement.style.fontSize = "20px";
    heightOf = (i) => (i === 50 || i === 90 ? 98 : i === 99 ? 30 : VIRTUAL_ITEM_ESTIMATE_PX);
    dispatchScroll(log);
    await flush();
    const centered = (index: number) => topOf(index) - (VIEWPORT - heightOf(index)) / 2;

    await search("needle");
    await flush();
    expect(log.scrollTop).toBe(centered(0));
    clickNext();
    dispatchScroll(log);
    await flush();
    expect(wrapperOf(view!, 50)).not.toBeNull();
    expect(log.scrollTop).toBe(centered(50));
    clickNext();
    dispatchScroll(log);
    await flush();
    expect(wrapperOf(view!, 99)).not.toBeNull();
    expect(log.scrollTop).toBe(centered(99));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(CONVERSATION_SCROLL_SETTLE_MS);
    });
    heightOf = (i) => (i === 50 || i === 90 ? 98 : i === 95 ? 108 : i === 99 ? 30 : VIRTUAL_ITEM_ESTIMATE_PX);
    const above = wrapperOf(view!, 95)!;
    const observing = observers.filter((observer) => observer.targets.has(above));
    expect(observing).not.toHaveLength(0);
    for (const observer of observing) {
      act(() => observer.callback([{ target: above } as unknown as ResizeObserverEntry], {} as ResizeObserver));
    }
    await flush();
    expect(log.scrollTop).toBe(centered(99));
    const correctedLast = log.scrollTop;
    dispatchScroll(log);
    await flush();
    expect(log.scrollTop).toBe(correctedLast);

    click(view!.container.querySelector('button[aria-label="上一个匹配"]')!);
    dispatchScroll(log);
    await flush();
    expect(log.scrollTop).toBe(centered(50));
    const settledMiddle = log.scrollTop;
    dispatchScroll(log);
    await flush();
    expect(log.scrollTop).toBe(settledMiddle);
    expect(log.scrollHeight - log.scrollTop - log.clientHeight).toBeGreaterThan(0);

    click(view!.container.querySelector('button[title="搜索对话内容"]')!);
    await flush();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(CONVERSATION_SCROLL_SETTLE_MS);
    });
    const restoredAbove = [...view!.container.querySelectorAll<HTMLElement>("[data-conversation-index]")].find(
      (element) =>
        topOf(Number(element.dataset.conversationIndex)) + heightOf(Number(element.dataset.conversationIndex)) <= log.scrollTop &&
        observers.some((observer) => observer.targets.has(element)),
    );
    if (!restoredAbove) throw new Error("no observed item above the viewport");
    const restoredIndex = Number(restoredAbove.dataset.conversationIndex);
    const beforeRestore = log.scrollTop;
    heightOf = (i) =>
      i === restoredIndex || i === 95 ? 108 : i === 50 || i === 90 ? 98 : i === 99 ? 30 : VIRTUAL_ITEM_ESTIMATE_PX;
    for (const observer of observers) {
      if (observer.targets.has(restoredAbove)) {
        act(() => observer.callback([{ target: restoredAbove } as unknown as ResizeObserverEntry], {} as ResizeObserver));
      }
    }
    await flush();
    expect(log.scrollTop).toBe(beforeRestore + 20);
  });

  it("keeps wrapped index 73 centered when late font measurements move its spacer", async () => {
    const text = () => view!.container.textContent ?? "";
    const observers: { callback: ResizeObserverCallback; targets: Set<Element> }[] = [];
    vi.stubGlobal("ResizeObserver", class {
      readonly targets = new Set<Element>();
      constructor(readonly callback: ResizeObserverCallback) {
        observers.push(this);
      }
      observe(target: Element) {
        this.targets.add(target);
      }
      unobserve(target: Element) {
        this.targets.delete(target);
      }
      disconnect() {
        this.targets.clear();
      }
    });
    heightOf = (i) => (i === 99 ? 30 : VIRTUAL_ITEM_ESTIMATE_PX);
    topOf = (i) => {
      let start = VIEWPORT;
      for (let j = 0; j < i; j++) start += heightOf(j) + VIRTUAL_ITEM_GAP_PX;
      return start;
    };
    conversationWith(
      Array.from({ length: 100 }, (_, i) =>
        message(`m${i}`, i % 2 === 0 ? "user" : "assistant", i === 73 || i === 99 ? `needle ${i}` : `其他 ${i}`),
      ),
    );
    await flushUntil(() => text().includes("其他 1"));
    const log = stubGeometry(
      view!,
      () => 24 + 2 * VIEWPORT + Array.from({ length: 100 }, (_, i) => heightOf(i)).reduce((sum, height) => sum + height + VIRTUAL_ITEM_GAP_PX, 0),
    );
    const centered = (index: number) => topOf(index) - (VIEWPORT - heightOf(index)) / 2;

    await search("needle");
    dispatchScroll(log);
    await flush();
    expect(log.scrollTop).toBe(centered(73));
    clickNext();
    dispatchScroll(log);
    await flush();
    expect(log.scrollTop).toBe(centered(99));

    log.scrollTop = log.scrollHeight;
    dispatchScroll(log);
    await flush();
    document.documentElement.style.fontSize = "22px";
    heightOf = (i) => (i === 72 ? 98 : i === 99 ? 30 : VIRTUAL_ITEM_ESTIMATE_PX);
    dispatchScroll(log);
    await flush();
    clickNext();
    dispatchScroll(log);
    await flush();
    expect(log.scrollTop).toBe(centered(73));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(CONVERSATION_SCROLL_SETTLE_MS);
    });
    heightOf = (i) => (i === 72 ? 108 : i === 99 ? 30 : VIRTUAL_ITEM_ESTIMATE_PX);
    const above = wrapperOf(view!, 72)!;
    const observing = observers.filter((observer) => observer.targets.has(above));
    expect(observing).not.toHaveLength(0);
    for (const observer of observing) {
      act(() => observer.callback([{ target: above } as unknown as ResizeObserverEntry], {} as ResizeObserver));
    }
    await flush();
    expect(log.scrollTop).toBe(centered(73));
    const corrected = log.scrollTop;
    for (let i = 0; i < 3; i++) {
      dispatchScroll(log);
      await flush();
      expect(log.scrollTop).toBe(corrected);
    }

    clickNext();
    dispatchScroll(log);
    await flush();
    expect(log.scrollTop).toBe(centered(99));
    log.scrollTop = log.scrollHeight;
    dispatchScroll(log);
    await flush();
    document.documentElement.style.fontSize = "16px";
    heightOf = (i) => (i === 99 ? 30 : VIRTUAL_ITEM_ESTIMATE_PX);
    dispatchScroll(log);
    await flush();
    clickNext();
    dispatchScroll(log);
    await flush();
    expect(log.scrollTop).toBe(centered(73));
  });
});
