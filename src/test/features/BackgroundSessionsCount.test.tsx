/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { createElement, Fragment } from "react";

const mocks = vi.hoisted(() => ({ listLive: vi.fn(), listDurable: vi.fn() }));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    terminalApi: { listLive: mocks.listLive, listDurable: mocks.listDurable },
  });
});

import { BackgroundSessions } from "../../features/terminal/BackgroundSessions";
import { useBackgroundSessionCount } from "../../features/terminal/backgroundSessionsAccess";
import { useUi, type Workspace } from "../../app/store";
import type { LiveTabInfo, SessionInfo } from "../../ipc/commands";
import { flush, mount, waitForRendered } from "./reactTestUtils";

function session(id: string, kind: string, name: string, assetId: string | null): SessionInfo {
  return { id, assetId, name, kind, status: "connected", tabs: [], createdAt: 0 };
}

function workspace(id: string, sessionId: string, assetKind: string, assetId?: string): Workspace {
  return {
    id,
    kind: "session",
    title: id,
    sessionId,
    assetKind,
    assetId,
    panes: [{ id: `${id}-pane`, tabs: [], activeTabId: null }],
    activePaneId: `${id}-pane`,
    closable: true,
  } as Workspace;
}

function live(tabId: string, sessionId: string, kind: string, title: string): LiveTabInfo {
  return {
    tabId, sessionId, sessionName: sessionId, sessionKind: kind, cols: 80, rows: 24, sizeOwner: null,
    subscribers: 0, viewers: 0, exited: false, lastOutputMsAgo: 1000, title,
  };
}

function Probe({ id }: { id: string }) {
  const ws = useUi((s) => s.workspaces.find((w) => w.id === id));
  const count = useBackgroundSessionCount(ws, true);
  return createElement(Fragment, null,
    createElement("span", { "data-testid": "badge" }, count === null ? "?" : String(count)),
    createElement(BackgroundSessions, { workspaceId: id, sessionId: ws?.sessionId }));
}

describe("background session badge and page share one rule", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    useUi.setState({
      sessions: [
        session("sA", "ssh", "主机甲", "asset-a"),
        session("sB", "ssh", "主机乙", "asset-b"),
        session("sL", "local", "本机", null),
      ],
      workspaces: [workspace("wA", "sA", "ssh", "asset-a"), workspace("wB", "sB", "ssh", "asset-b"), workspace("wL", "sL", "local")],
      activeWorkspaceId: "wA",
      toasts: [],
    });
    mocks.listLive.mockResolvedValue([
      live("l1", "sL", "local", "本机构建"),
      live("a1", "sA", "ssh", "甲-实时"),
    ]);
    mocks.listDurable.mockImplementation(async (id: string) => id === "sA"
      ? [{ id: "d1", title: "甲-持久", cols: 80, rows: 24, dead: false }, { id: "d2", title: "甲-已死", cols: 80, rows: 24, dead: true }]
      : id === "sB" ? [] : []);
  });

  const badge = (c: HTMLElement) => c.querySelector('[data-testid="badge"]')?.textContent;
  const rows = (c: HTMLElement) =>
    [...c.querySelectorAll("tbody button")].filter((b) => b.textContent?.trim() === "接管").length;

  it("matches rows on the page, excludes local terminals from a remote host and follows host switches", async () => {
    const view = mount(createElement(Probe, { id: "wA" }));
    try {
      await flush();
      await waitForRendered(() => expect(badge(view.container)).toBe("2"));
      await waitForRendered(() => expect(rows(view.container)).toBe(2));
      expect(view.container.textContent).toContain("甲-持久");
      expect(view.container.textContent).not.toContain("本机构建");
      expect(view.container.querySelector('[data-testid="background-count"]')?.textContent).toBe("2 个可恢复");

      view.unmount();
      const other = mount(createElement(Probe, { id: "wB" }));
      try {
        await flush();
        await waitForRendered(() => expect(badge(other.container)).toBe("0"));
        await waitForRendered(() => expect(other.container.textContent).toContain("这台主机上没有在后台运行的终端"));
        expect(rows(other.container)).toBe(0);
        expect(other.container.querySelector('[data-testid="background-host"]')?.textContent).toBe("主机乙");
      } finally {
        other.unmount();
      }

      const local = mount(createElement(Probe, { id: "wL" }));
      try {
        await flush();
        await waitForRendered(() => expect(badge(local.container)).toBe("1"));
        await waitForRendered(() => expect(local.container.textContent).toContain("本机构建"));
        expect(local.container.textContent).not.toContain("甲-");
      } finally {
        local.unmount();
      }
    } finally {
      view.unmount();
    }
  });

  it("does not count a terminal that is already open in a tab", async () => {
    useUi.setState((state) => ({
      workspaces: state.workspaces.map((w) => w.id === "wA"
        ? { ...w, panes: [{ id: "wA-pane", activeTabId: "t", tabs: [{ id: "t", kind: "terminal", title: "t", sessionId: "sA", tabId: "d1", closable: true }] }] }
        : w) as Workspace[],
    }));
    const view = mount(createElement(Probe, { id: "wA" }));
    try {
      await flush();
      await waitForRendered(() => expect(badge(view.container)).toBe("1"));
      await waitForRendered(() => expect(rows(view.container)).toBe(1));
      expect(view.container.textContent).not.toContain("甲-持久");
    } finally {
      view.unmount();
    }
  });
});
