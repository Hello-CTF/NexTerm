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
});
