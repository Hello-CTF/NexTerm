/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../ipc/commands", () => ({
  sessionApi: { list: vi.fn() },
  assetApi: {},
  dbApi: {},
  terminalApi: { recordingStatus: vi.fn() },
  vaultApi: {},
}));

import { sessionApi, terminalApi, type SessionInfo } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { deferred } from "./reactTestUtils";

function session(id: string, status: SessionInfo["status"]): SessionInfo {
  return { id, assetId: null, name: id, kind: "ssh", status, tabs: [], createdAt: 1 };
}

describe("resyncSessions", () => {
  beforeEach(() => {
    vi.mocked(sessionApi.list).mockReset();
    vi.mocked(terminalApi.recordingStatus).mockReset();
    vi.spyOn(console, "error").mockImplementation(() => {});
    useUi.setState({ sessions: [], toasts: [], sessionResyncFailed: false, recordings: {} });
  });

  it("replaces local sessions with the authoritative server list", async () => {
    const authoritative = [session("s1", "failed"), session("s2", "connected")];
    vi.mocked(sessionApi.list).mockResolvedValue(authoritative);
    useUi.setState({ sessions: [session("stale", "connected")] });

    await useUi.getState().resyncSessions();

    expect(useUi.getState().sessions).toEqual(authoritative);
  });

  it("preserves existing sessions when fetching fails", async () => {
    vi.mocked(sessionApi.list).mockRejectedValue(new Error("down"));
    const existing = [session("keep", "reconnecting")];
    useUi.setState({ sessions: existing });

    await useUi.getState().resyncSessions();

    expect(useUi.getState().sessions).toEqual(existing);
  });

  it("shows an error toast when fetching fails", async () => {
    vi.mocked(sessionApi.list).mockRejectedValue(new Error("down"));

    await useUi.getState().resyncSessions();

    const toasts = useUi.getState().toasts;
    expect(toasts).toHaveLength(1);
    expect(toasts[0].kind).toBe("error");
    expect(toasts[0].text).toContain("刷新会话列表失败");
    expect(toasts[0].text).toContain("down");
  });

  it("shows only one toast for consecutive failures", async () => {
    vi.mocked(sessionApi.list).mockRejectedValue(new Error("down"));

    await useUi.getState().resyncSessions();
    await useUi.getState().resyncSessions();
    await useUi.getState().resyncSessions();

    expect(useUi.getState().toasts).toHaveLength(1);
  });

  it("shows a new toast when a failure follows a successful recovery", async () => {
    vi.mocked(sessionApi.list).mockRejectedValueOnce(new Error("down"));
    await useUi.getState().resyncSessions();
    const firstId = useUi.getState().toasts[0]?.id;
    vi.mocked(sessionApi.list).mockResolvedValueOnce([]);
    await useUi.getState().resyncSessions();
    vi.mocked(sessionApi.list).mockRejectedValueOnce(new Error("down"));

    await useUi.getState().resyncSessions();

    const toasts = useUi.getState().toasts;
    expect(toasts).toHaveLength(1);
    expect(toasts[0].id).not.toBe(firstId);
    expect(toasts[0].text).toContain("刷新会话列表失败");
  });
});

describe("recording resync", () => {
  beforeEach(() => {
    vi.mocked(terminalApi.recordingStatus).mockReset();
    useUi.setState({ recordings: {} });
  });

  it("keeps the newest recording generation", () => {
    useUi.getState().setRecordingStatus({
      tabId: "tab-1",
      state: "failed",
      bytes: 8,
      path: "/tmp/new.log",
      safeError: "disk full",
      generation: 2,
    });
    useUi.getState().setRecordingStatus({
      tabId: "tab-1",
      state: "running",
      bytes: 2,
      path: "/tmp/old.log",
      generation: 1,
    });

    expect(useUi.getState().recordings["tab-1"]?.generation).toBe(2);
    expect(useUi.getState().recordings["tab-1"]?.state).toBe("failed");
  });

  it("drops a stale status response after a newer resync completes", async () => {
    const stale = deferred<{ tabId: string; state: "running"; bytes: number; generation: number }>();
    vi.mocked(terminalApi.recordingStatus)
      .mockReturnValueOnce(stale.promise)
      .mockResolvedValueOnce({
        tabId: "tab-1",
        state: "failed",
        bytes: 8,
        path: "/tmp/failed.log",
        safeError: "disk full",
        generation: 1,
      });
    const first = useUi.getState().resyncRecording("tab-1");
    await useUi.getState().resyncRecording("tab-1");

    stale.resolve({ tabId: "tab-1", state: "running", bytes: 2, generation: 1 });
    await first;

    expect(useUi.getState().recordings["tab-1"]?.state).toBe("failed");
    expect(useUi.getState().recordings["tab-1"]?.safeError).toBe("disk full");
  });

  it("clears recording state when sessions reset", () => {
    useUi.getState().setRecordingStatus({
      tabId: "tab-1",
      state: "failed",
      bytes: 8,
      safeError: "disk full",
      generation: 1,
    });

    useUi.getState().resetSessions();

    expect(useUi.getState().recordings).toEqual({});
  });
});
