/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  listSessions: vi.fn(),
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    sessionApi: { list: mocks.listSessions },
    terminalApi: { listLive: vi.fn().mockResolvedValue([]) },
  });
});

import {
  takeOverAggregateSession,
  takeOverBackgroundTab,
  type BackgroundTab,
} from "../../features/terminal/backgroundSessionsAccess";
import { useUi, type SessionRuntimeInfo, type Workspace } from "../../app/store";
import type { AggregateHost } from "../../ipc/generated/types";

const KINDS = ["local", "ssh", "device"] as const;

function seed(kind: string): SessionRuntimeInfo {
  const session = {
    id: "s1", assetId: kind === "local" ? null : "a1", name: "host-a", kind, status: "connected", tabs: [], createdAt: 1,
  } as SessionRuntimeInfo;
  const ws: Workspace = {
    id: "ws", kind: "session", title: "host-a", sessionId: "s1", assetId: session.assetId ?? undefined, assetKind: kind,
    panes: [{ id: "p", tabs: [], activeTabId: null }], activePaneId: "p", closable: true,
  };
  useUi.setState({ workspaces: [ws], activeWorkspaceId: "ws", sessions: [session], toasts: [] });
  mocks.listSessions.mockResolvedValue([session]);
  return session;
}

function row(kind: string, extra: Partial<BackgroundTab>): BackgroundTab {
  return {
    tabId: "durable-abc123", sessionId: "s1", sessionName: "host-a", sessionKind: kind,
    cols: 80, rows: 24, sizeOwner: null, subscribers: 0, viewers: 0, exited: false, ...extra,
  };
}

const terminals = () =>
  useUi.getState().workspaces.flatMap((ws) => ws.panes.flatMap((pane) => pane.tabs)).filter((tab) => tab.kind === "terminal");

beforeEach(() => {
  vi.clearAllMocks();
});

describe.each(KINDS)("taking over a %s background terminal", (kind) => {
  it("names the tab by the program's window title, not the host", async () => {
    seed(kind);
    const result = await takeOverBackgroundTab(row(kind, { windowTitle: "vim notes.txt" }));
    expect(result).toEqual({ ok: true, sessionName: "host-a" });
    const [tab] = terminals();
    expect(tab).toMatchObject({ title: "vim notes.txt", tabId: "durable-abc123", sessionId: "s1" });
    expect(tab.renamedByUser).toBeUndefined();
  });

  it("keeps the background list's fallback name without a title", async () => {
    seed(kind);
    await takeOverBackgroundTab(row(kind, {}));
    expect(terminals()[0].title).toBe("终端 abc123");
  });

  it("locks only a custom title with its decision time", async () => {
    seed(kind);
    await takeOverBackgroundTab(row(kind, { title: "deploy", titleCustom: true, titleChangedAt: 42, windowTitle: "vim" }));
    expect(terminals()[0]).toMatchObject({ title: "deploy", renamedByUser: true, customTitleAt: 42 });
  });
});

describe("taking over from the cross-host listing", () => {
  it("names a device terminal by its window title, not the device", async () => {
    const session = seed("device");
    const host = { kind: "device", name: "host-a", sessionId: session.id, deviceId: "d1" } as unknown as AggregateHost;
    useUi.setState({ sessions: [{ ...session, assetId: "device:d1" } as SessionRuntimeInfo] });
    mocks.listSessions.mockResolvedValue([{ ...session, assetId: "device:d1" }]);
    const result = await takeOverAggregateSession(host, "durable-abc123", undefined, "htop");
    expect(result).toEqual({ ok: true, sessionName: "host-a" });
    const [tab] = terminals();
    expect(tab.title).toBe("htop");
    expect(tab.renamedByUser).toBeUndefined();
  });
});
