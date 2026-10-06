/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, flushUntil, mount, type MountedView } from "./features/reactTestUtils";
import type { AiRunEventDto } from "../ipc/types";
import { RUN_EVENTS_PAGE_SIZE } from "../features/ai/runRestore";

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
  usageSummary: vi.fn(),
  toast: vi.fn(),
  dispose: vi.fn(),
  channels: [] as { onEvent: (ev: Record<string, unknown>) => void }[],
  reopens: [] as (() => void)[],
  runEventCalls: [] as { jobId: string; afterSeq: number; limit: number }[],
}));

vi.mock("../ipc/commands", () => ({
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
  onChannelReopen: (_channel: unknown, cb: () => void) => {
    mocks.reopens.push(cb);
    return () => undefined;
  },
}));

import { AiSidebar } from "../features/ai/AiSidebar";
import { useUi } from "../app/store";

const runRows = [
  {
    id: "job-2",
    conversationId: "conv-1",
    status: "interrupted",
    attempt: 1,
    seq: 2,
    planMode: false,
    source: "chat",
    answer: "",
    turns: 1,
    tokensIn: 5,
    tokensOut: 0,
    cacheCreationTokens: 0,
    latencyMs: 50,
    retries: 0,
    failures: 0,
    createdAt: 3,
    updatedAt: 3,
  },
];

const pendingSnapshot = {
  runId: "job-2",
  checkpointId: "job-2",
  status: "running",
  attempt: 1,
  seq: 0,
  pending: [
    {
      id: "req-1",
      runId: "job-2",
      checkpointId: "job-2",
      checkpointHash: "hash",
      targetId: "target",
      callId: "call-1",
      tool: "exec_commands",
      kind: "confirm",
      parameters: {},
      parameterHash: "phash",
      nonce: "nonce-1",
      createdAt: "2026-10-06T00:00:00Z",
      expiresAt: "2999-01-01T00:00:00Z",
      attempt: 1,
      seq: 1,
    },
  ],
};

function marker(seq: number): string {
  return `mk${String(seq).padStart(4, "0")}`;
}

function deltaEvents(from: number, to: number): AiRunEventDto[] {
  const events: AiRunEventDto[] = [];
  for (let seq = from; seq <= to; seq += 1) {
    events.push({ type: "delta", text: `${marker(seq)} `, seq });
  }
  return events;
}

function markersIn(text: string): string[] {
  return text.match(/mk\d{4}/g) ?? [];
}

function expectedMarkers(from: number, to: number): string[] {
  const out: string[] = [];
  for (let seq = from; seq <= to; seq += 1) out.push(marker(seq));
  return out;
}

describe("AiSidebar run event paging", () => {
  let view: MountedView | null = null;
  let dbEvents: AiRunEventDto[] = [];

  const job2Calls = () => mocks.runEventCalls.filter((call) => call.jobId === "job-2");

  beforeEach(async () => {
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      setTimeout(cb, 0);
      return 1;
    });
    vi.stubGlobal("cancelAnimationFrame", () => undefined);
    mocks.channels.length = 0;
    mocks.reopens.length = 0;
    mocks.runEventCalls.length = 0;
    dbEvents = deltaEvents(1, 450);
    mocks.runEvents.mockImplementation((jobId: string, afterSeq: number, limit?: number) => {
      mocks.runEventCalls.push({ jobId, afterSeq, limit: limit ?? 0 });
      if (jobId !== "job-2") return Promise.resolve([]);
      return Promise.resolve(
        dbEvents.filter((event) => (event.seq as number) > afterSeq).slice(0, limit),
      );
    });
    mocks.hitlSnapshot.mockResolvedValue(pendingSnapshot);
    mocks.hitlEvents.mockResolvedValue([]);
    mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
    mocks.conversationList.mockResolvedValue([{ id: "conv-1", title: "演示", createdAt: 0, updatedAt: 0, scope: {} }]);
    mocks.messages.mockResolvedValue([
      { id: "m1", conversationId: "conv-1", role: "user", content: { role: "user", content: "你好", imageCount: 0 }, tokensIn: null, tokensOut: null, createdAt: 1 },
    ]);
    mocks.runs.mockResolvedValue(runRows);
    mocks.overview.mockResolvedValue({ profiles: [], activeId: null });
    mocks.usageSummary.mockResolvedValue([]);
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
    localStorage.removeItem("nexterm.ai.conversation.v1");
    vi.unstubAllGlobals();
  });

  it("reloads an interrupted run through multiple pages without losing, duplicating or reordering seq", async () => {
    const text = () => view!.container.textContent ?? "";
    await flushUntil(() => text().includes(marker(450)));

    expect(job2Calls()).toEqual([
      { jobId: "job-2", afterSeq: 0, limit: RUN_EVENTS_PAGE_SIZE },
      { jobId: "job-2", afterSeq: 200, limit: RUN_EVENTS_PAGE_SIZE },
      { jobId: "job-2", afterSeq: 400, limit: RUN_EVENTS_PAGE_SIZE },
    ]);
    expect(markersIn(text())).toEqual(expectedMarkers(1, 450));
  });

  it("resume catch-up pages from the live cursor and stays equivalent without duplicates", async () => {
    const text = () => view!.container.textContent ?? "";
    await flushUntil(() => text().includes(marker(450)));

    const channel = mocks.channels.at(-1);
    expect(channel).toBeDefined();
    act(() => {
      channel!.onEvent({ type: "delta", text: `${marker(451)} `, seq: 451 });
    });
    await flushUntil(() => text().includes(marker(451)));

    dbEvents = [...dbEvents, ...deltaEvents(452, 700)];
    const reopen = mocks.reopens.at(-1);
    expect(reopen).toBeDefined();
    act(() => {
      reopen!();
    });
    await flushUntil(() => text().includes(marker(700)));

    expect(job2Calls().slice(3)).toEqual([
      { jobId: "job-2", afterSeq: 451, limit: RUN_EVENTS_PAGE_SIZE },
      { jobId: "job-2", afterSeq: 651, limit: RUN_EVENTS_PAGE_SIZE },
    ]);
    expect(markersIn(text())).toEqual(expectedMarkers(1, 700));
  });
});
