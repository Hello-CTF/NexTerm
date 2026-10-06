/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../ipc/commands", () => ({
  sessionApi: { list: vi.fn() },
  assetApi: {},
  dbApi: {},
  terminalApi: {},
  vaultApi: {},
}));

import { sessionApi, type SessionInfo } from "../../ipc/commands";
import { useUi } from "../../app/store";

function session(id: string, status: SessionInfo["status"]): SessionInfo {
  return { id, assetId: null, name: id, kind: "ssh", status, tabs: [], createdAt: 1 };
}

describe("resyncSessions", () => {
  beforeEach(() => {
    vi.mocked(sessionApi.list).mockReset();
    vi.spyOn(console, "error").mockImplementation(() => {});
    useUi.setState({ sessions: [], toasts: [], sessionResyncFailed: false });
  });

  it("用服务端权威列表替换本地 sessions", async () => {
    const authoritative = [session("s1", "failed"), session("s2", "connected")];
    vi.mocked(sessionApi.list).mockResolvedValue(authoritative);
    useUi.setState({ sessions: [session("stale", "connected")] });

    await useUi.getState().resyncSessions();

    expect(useUi.getState().sessions).toEqual(authoritative);
  });

  it("拉取失败时保留现有 sessions", async () => {
    vi.mocked(sessionApi.list).mockRejectedValue(new Error("down"));
    const existing = [session("keep", "reconnecting")];
    useUi.setState({ sessions: existing });

    await useUi.getState().resyncSessions();

    expect(useUi.getState().sessions).toEqual(existing);
  });

  it("拉取失败时给出错误 toast 反馈", async () => {
    vi.mocked(sessionApi.list).mockRejectedValue(new Error("down"));

    await useUi.getState().resyncSessions();

    const toasts = useUi.getState().toasts;
    expect(toasts).toHaveLength(1);
    expect(toasts[0].kind).toBe("error");
    expect(toasts[0].text).toContain("刷新会话列表失败");
    expect(toasts[0].text).toContain("down");
  });

  it("连续失败只提示一次，不重复弹 toast", async () => {
    vi.mocked(sessionApi.list).mockRejectedValue(new Error("down"));

    await useUi.getState().resyncSessions();
    await useUi.getState().resyncSessions();
    await useUi.getState().resyncSessions();

    expect(useUi.getState().toasts).toHaveLength(1);
  });

  it("恢复成功后再次失败会重新提示", async () => {
    vi.mocked(sessionApi.list).mockRejectedValueOnce(new Error("down"));
    await useUi.getState().resyncSessions();
    vi.mocked(sessionApi.list).mockResolvedValueOnce([]);
    await useUi.getState().resyncSessions();
    vi.mocked(sessionApi.list).mockRejectedValueOnce(new Error("down"));

    await useUi.getState().resyncSessions();

    expect(useUi.getState().toasts).toHaveLength(2);
  });
});
