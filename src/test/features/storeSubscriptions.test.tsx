/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, Profiler, type ReactElement } from "react";

const mocks = vi.hoisted(() => ({
  listLive: vi.fn(),
  listDurable: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    terminalApi: { listLive: mocks.listLive, listDurable: mocks.listDurable },
  });
});

import { BackgroundSessions } from "../../features/terminal/BackgroundSessions";
import { ProjectWorkspaceStrip } from "../../app/ProjectWorkspaceStrip";
import { UNGROUPED_PROJECT, type ProjectInventory } from "../../app/projectFocus";
import { AppHeader } from "../../app/shell/AppHeader";
import { StatusBar } from "../../app/shell/StatusBar";
import {
  sessionStatusText,
  useSessionById,
  useSessionList,
  useUi,
  type SessionRuntimeInfo,
  type Workspace,
} from "../../app/store";
import type { Asset } from "../../ipc/commands";
import { flush, mount, type MountedView } from "./reactTestUtils";

// The fixture mirrors a busy account: 20 host workspaces and 40 sessions, half of them owned by a workspace.
const WORKSPACES = 20;
const SESSIONS = 40;

function session(index: number, status: SessionRuntimeInfo["status"] = "connected"): SessionRuntimeInfo {
  return {
    id: `s-${index}`,
    assetId: `asset-${index}`,
    name: `host-${index}`,
    kind: "ssh",
    status,
    tabs: [`kernel-${index}`],
    createdAt: 1,
  };
}

function workspace(index: number): Workspace {
  return {
    id: `ws-${index}`,
    kind: "session",
    title: `host-${index}`,
    sessionId: `s-${index}`,
    assetId: `asset-${index}`,
    assetKind: "ssh",
    panes: [{
      id: `pane-${index}`,
      activeTabId: `tab-${index}`,
      tabs: [{ id: `tab-${index}`, kind: "terminal", title: "bash", sessionId: `s-${index}`, tabId: `kernel-${index}`, closable: true }],
    }],
    activePaneId: `pane-${index}`,
    closable: true,
  };
}

function asset(index: number): Asset {
  return {
    id: `asset-${index}`,
    groupId: null,
    kind: "ssh",
    name: `host-${index}`,
    host: null,
    port: null,
    username: null,
    authKind: null,
    keyPath: null,
    credId: null,
    options: {},
    tags: "",
    note: "",
    sort: 0,
    createdAt: 1,
    updatedAt: 1,
    deletedAt: null,
    builtin: false,
  };
}

const workspaces = Array.from({ length: WORKSPACES }, (_, index) => workspace(index));
const inventory: ProjectInventory = { groups: [], assets: Array.from({ length: WORKSPACES }, (_, index) => asset(index)) };

function patchSession(id: string, patch: Partial<SessionRuntimeInfo>): void {
  act(() => {
    const state = useUi.getState();
    state.setSessions(state.sessions.map((item) => (item.id === id ? { ...item, ...patch } : item)));
  });
}

// profiled mounts a view under a React Profiler; commits counts every commit in which the view rendered.
function profiled(node: ReactElement): { view: MountedView; commits: () => number; reset: () => void } {
  let count = 0;
  const view = mount(createElement(Profiler, { id: "probe", onRender: () => { count += 1; } }, node));
  return { view, commits: () => count, reset: () => { count = 0; } };
}

let mounted: MountedView | undefined;

beforeEach(() => {
  document.body.replaceChildren();
  mocks.listLive.mockResolvedValue([]);
  mocks.listDurable.mockResolvedValue([]);
  useUi.setState({
    sessions: Array.from({ length: SESSIONS }, (_, index) => session(index)),
    workspaces,
    activeWorkspaceId: "ws-0",
    toasts: [],
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("session selector hooks", () => {
  it("useSessionById ignores other sessions and resyncs that return equal data", () => {
    const seen: (SessionRuntimeInfo | undefined)[] = [];
    function Probe() {
      seen.push(useSessionById("s-3"));
      return null;
    }
    mounted = mount(createElement(Probe));
    expect(seen).toHaveLength(1);

    patchSession("s-7", { status: "reconnecting" });
    act(() => useUi.getState().setSessions(useUi.getState().sessions.map((item) => ({ ...item }))));
    expect(seen).toHaveLength(1);

    patchSession("s-3", { status: "failed" });
    expect(seen).toHaveLength(2);
    expect(seen[1]?.status).toBe("failed");
  });

  it("useSessionList follows only the requested sessions", () => {
    const seen: string[][] = [];
    function Probe() {
      seen.push(useSessionList(["s-1", "s-2"]).map((item) => `${item.id}:${item.status}`));
      return null;
    }
    mounted = mount(createElement(Probe));
    patchSession("s-9", { status: "failed" });
    expect(seen).toHaveLength(1);
    patchSession("s-2", { status: "failed" });
    expect(seen).toEqual([["s-1:connected", "s-2:connected"], ["s-1:connected", "s-2:failed"]]);
  });
});

describe("render counts for session-bound views", () => {
  it("the status bar re-renders only when a session count changes", () => {
    const probe = profiled(createElement(StatusBar, {
      vaultStatus: { attention: null, uninitialized: false, passwordless: false, text: "凭据库已解锁" },
      sync: { text: "同步：本地模式", title: undefined, className: "", warnings: [], showWarnings: false, failed: false },
      onOpenVaultProtection: () => {},
      onOpenSyncSettings: () => {},
    }));
    mounted = probe.view;
    probe.reset();

    patchSession("s-7", { name: "renamed" });
    act(() => useUi.getState().setSessions(useUi.getState().sessions.map((item) => ({ ...item }))));
    expect(probe.commits()).toBe(0);

    patchSession("s-7", { status: "disconnected" });
    expect(probe.commits()).toBe(1);
    expect(mounted.container.textContent).toContain(`${SESSIONS - 1} 已连接`);
  });

  it("the header follows only the active workspace session", () => {
    const probe = profiled(createElement(AppHeader, {
      ws: workspaces[0],
      activeSessionId: "s-0",
      activeTab: null,
      rightDockOpen: false,
      commandPaletteLabel: "Ctrl+K",
      onOpenWorkspaceSwitcher: () => {},
      onToggleRight: () => {},
      onOpenPalette: () => {},
    }));
    mounted = probe.view;
    probe.reset();

    for (let index = 1; index < SESSIONS; index++) patchSession(`s-${index}`, { status: "reconnecting" });
    expect(probe.commits()).toBe(0);

    patchSession("s-0", { status: "reconnecting" });
    expect(probe.commits()).toBe(1);
    expect(mounted.container.querySelector(".nx-ws-switch .nx-ws-status")?.getAttribute("aria-label"))
      .toBe(sessionStatusText("reconnecting"));
  });

  it("the workspace strip re-renders only for sessions of its workspaces", () => {
    const probe = profiled(createElement(ProjectWorkspaceStrip, {
      workspaces,
      activeWorkspaceId: "ws-0",
      activeProject: UNGROUPED_PROJECT,
      projectInventory: inventory,
      groups: [],
      onSelect: () => {},
    }));
    mounted = probe.view;
    expect(mounted.container.querySelectorAll("[data-project-workspace-id]")).toHaveLength(WORKSPACES);
    probe.reset();

    patchSession("s-30", { status: "failed" });
    expect(probe.commits()).toBe(0);

    patchSession("s-7", { status: "failed" });
    expect(probe.commits()).toBe(1);
    const tab = mounted.container.querySelector('[data-project-workspace-id="ws-7"]');
    expect(tab?.getAttribute("title")).toContain(sessionStatusText("failed"));
  });

  it("a host's background session view ignores other hosts' sessions", async () => {
    const probe = profiled(createElement(BackgroundSessions, { workspaceId: "ws-3" }));
    mounted = probe.view;
    await flush();
    probe.reset();
    const fetches = mocks.listDurable.mock.calls.length;

    for (const index of [0, 7, 19, 30]) patchSession(`s-${index}`, { status: "reconnecting" });
    await flush();
    expect(probe.commits()).toBe(0);
    expect(mocks.listDurable.mock.calls.length).toBe(fetches);

    patchSession("s-3", { status: "reconnecting" });
    await flush();
    expect(probe.commits()).toBeGreaterThan(0);
  });
});
