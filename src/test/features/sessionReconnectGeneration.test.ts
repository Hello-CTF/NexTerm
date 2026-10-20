/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  sessionList: vi.fn(),
  sessionReconnect: vi.fn(),
  probeHostKey: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  vaultApi: {},
  terminalApi: { recordingStatus: vi.fn() },
  sessionApi: {
    list: mocks.sessionList,
    reconnect: mocks.sessionReconnect,
    probeHostKey: mocks.probeHostKey,
  },
}));

import {
  applySessionStatusEvent,
  reconnectSessionAndWait,
  useUi,
  type SessionRuntimeInfo,
} from "../../app/store";
import { deferred } from "./reactTestUtils";

function session(
  status: SessionRuntimeInfo["status"],
  extra: Partial<SessionRuntimeInfo> = {},
): SessionRuntimeInfo {
  return {
    id: "s1",
    assetId: "a1",
    name: "web-01",
    kind: "ssh",
    status,
    tabs: [],
    createdAt: 1,
    ...extra,
  };
}

async function flushPolls(rounds = 4): Promise<void> {
  for (let i = 0; i < rounds; i++) {
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
}

beforeEach(() => {
  vi.clearAllMocks();
  useUi.getState().resetSessions();
  useUi.setState({
    sessions: [session("connected")],
    workspaces: [],
    activeWorkspaceId: null,
    toasts: [],
    connectProgress: {},
  });
  mocks.sessionReconnect.mockResolvedValue(true);
  mocks.probeHostKey.mockResolvedValue({ state: "known" });
});

describe("manual reconnect session generation", () => {
  it("ignores a stale connected first frame until the reconnect engages", async () => {
    applySessionStatusEvent({ sessionId: "s1", status: "connected", error: null, version: 1 });
    // Hold the first two list responses so the stale frame is processed in isolation: poll 1 is the
    // pre-reconnect connected snapshot, poll 2 proves the reconnect engaged.
    const firstList = deferred<SessionRuntimeInfo[]>();
    const secondList = deferred<SessionRuntimeInfo[]>();
    mocks.sessionList
      .mockReturnValueOnce(firstList.promise)
      .mockReturnValueOnce(secondList.promise)
      .mockResolvedValue([session("connected")]);

    let settled = false;
    const waiting = reconnectSessionAndWait(session("connected"), { timeoutMs: 2_000, intervalMs: 1 })
      .then((fresh) => {
        settled = true;
        return fresh;
      });

    await flushPolls(1);
    expect(mocks.sessionList).toHaveBeenCalledTimes(1);
    firstList.resolve([session("connected")]);
    await flushPolls(1);
    expect(mocks.sessionList).toHaveBeenCalledTimes(2);
    expect(settled).toBe(false);

    secondList.resolve([session("reconnecting")]);
    await expect(waiting).resolves.toMatchObject({ id: "s1", status: "connected" });
    expect(settled).toBe(true);
  });

  it("ignores a stale failed first frame until the reconnect engages", async () => {
    applySessionStatusEvent({ sessionId: "s1", status: "disconnected", error: null, version: 1 });
    mocks.sessionList
      .mockResolvedValueOnce([session("failed", { error: "stale failure", errorCode: "ssh" })])
      .mockResolvedValueOnce([session("reconnecting")])
      .mockResolvedValue([session("connected")]);

    await expect(
      reconnectSessionAndWait(session("disconnected"), { timeoutMs: 2_000, intervalMs: 1 }),
    ).resolves.toMatchObject({ status: "connected" });
  });

  it("accepts same-generation success when joining an in-flight reconnect without a fresh reconnecting frame", async () => {
    // The session is already auto-reconnecting (generation N, reconnecting event version 2).
    useUi.setState({ sessions: [session("reconnecting")] });
    applySessionStatusEvent({ sessionId: "s1", status: "reconnecting", error: null, version: 2 });

    // Hold the only list response until the fresh connected event (version 3, same generation)
    // has landed, so acceptance must come from the generation evidence, not from a later poll.
    const firstList = deferred<SessionRuntimeInfo[]>();
    mocks.sessionList.mockReturnValue(firstList.promise);
    const waiting = reconnectSessionAndWait(session("reconnecting"), { timeoutMs: 2_000, intervalMs: 1 });
    await flushPolls(1);
    expect(mocks.sessionList).toHaveBeenCalledTimes(1);
    applySessionStatusEvent({ sessionId: "s1", status: "connected", error: null, version: 3 });
    firstList.resolve([session("connected")]);

    await expect(waiting).resolves.toMatchObject({ status: "connected" });
  });

  it("accepts a fast reconnect on the first poll when the connected event is fresh", async () => {
    applySessionStatusEvent({ sessionId: "s1", status: "connected", error: null, version: 1 });

    const firstList = deferred<SessionRuntimeInfo[]>();
    mocks.sessionList.mockReturnValue(firstList.promise);
    const waiting = reconnectSessionAndWait(session("connected"), { timeoutMs: 2_000, intervalMs: 1 });
    await flushPolls(1);
    applySessionStatusEvent({ sessionId: "s1", status: "connected", error: null, version: 2 });
    firstList.resolve([session("connected")]);

    await expect(waiting).resolves.toMatchObject({ status: "connected" });
  });

  it("fails with the typed error when the fresh generation reaches a terminal failure", async () => {
    applySessionStatusEvent({ sessionId: "s1", status: "disconnected", error: null, version: 1 });
    mocks.sessionList
      .mockResolvedValueOnce([session("reconnecting")])
      .mockResolvedValue([session("failed", {
        error: "认证失败",
        errorCode: "auth",
        attempt: 1,
        nextRetryAt: null,
      })]);

    await expect(
      reconnectSessionAndWait(session("disconnected"), { timeoutMs: 2_000, intervalMs: 1 }),
    ).rejects.toMatchObject({ message: "认证失败", code: "auth" });
  });

  it("accepts a first-poll connected frame when the session was already disconnected at start", async () => {
    useUi.setState({ sessions: [session("disconnected")] });
    mocks.sessionList.mockResolvedValue([session("connected")]);

    await expect(
      reconnectSessionAndWait(session("disconnected"), { timeoutMs: 2_000, intervalMs: 1 }),
    ).resolves.toMatchObject({ status: "connected" });
    expect(mocks.sessionList).toHaveBeenCalledTimes(1);
  });

  it("fails with the typed error when fresh reconnecting and failed events arrive without an engaged list frame", async () => {
    applySessionStatusEvent({ sessionId: "s1", status: "disconnected", error: null, version: 1 });
    // The list keeps returning the pre-start frame; only the events carry the fresh failure,
    // so acceptance must come from the event evidence rather than an engaged list frame.
    mocks.sessionList.mockResolvedValue([session("failed", { error: "stale frame", errorCode: "ssh" })]);
    const waiting = reconnectSessionAndWait(session("disconnected"), { timeoutMs: 2_000, intervalMs: 1 });
    // Attach the rejection assertion before the poll loop can reject: a handler added after the
    // rejection is an unhandled rejection even when the assertion later passes.
    const rejection = expect(waiting).rejects.toMatchObject({ message: "认证失败", code: "auth", attempt: 1 });
    await flushPolls(1);
    applySessionStatusEvent({ sessionId: "s1", status: "reconnecting", error: null, version: 2 });
    applySessionStatusEvent({
      sessionId: "s1",
      status: "failed",
      error: "认证失败",
      errorCode: "auth",
      attempt: 1,
      nextRetryAt: null,
      version: 3,
    });

    await rejection;
  });

  it("keeps list-driven failure when a fresh failed event lands after an engaged list frame", async () => {
    applySessionStatusEvent({ sessionId: "s1", status: "disconnected", error: null, version: 1 });
    mocks.sessionList
      .mockResolvedValueOnce([session("reconnecting")])
      .mockResolvedValue([session("failed", { error: "认证失败", errorCode: "auth", attempt: 1 })]);
    const waiting = reconnectSessionAndWait(session("disconnected"), { timeoutMs: 2_000, intervalMs: 1 });
    const rejection = expect(waiting).rejects.toMatchObject({ message: "认证失败", code: "auth" });
    await flushPolls(1);
    applySessionStatusEvent({ sessionId: "s1", status: "reconnecting", error: null, version: 2 });

    await rejection;
  });

  it("falls back to post-first-poll lists when no generation evidence arrives", async () => {
    applySessionStatusEvent({ sessionId: "s1", status: "connected", error: null, version: 7 });
    useUi.getState().resetSessions();
    useUi.setState({ sessions: [session("connected")] });

    // The reset dropped the tracked generations; with a dead event stream only the lists remain,
    // so the wait must still settle from the second poll on instead of timing out.
    mocks.sessionList.mockResolvedValue([session("connected")]);
    await expect(
      reconnectSessionAndWait(session("connected"), { timeoutMs: 2_000, intervalMs: 1 }),
    ).resolves.toMatchObject({ status: "connected" });
  });
});
