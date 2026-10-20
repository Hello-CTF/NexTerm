/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ALL_PROJECTS, projectFocusForGroup, type ProjectInventory } from "../../app/projectFocus";
import type { AiBoard, AppTab, Pane, Workspace } from "../../app/store";
import type { Asset, AssetGroup } from "../../ipc/commands";

const server = vi.hoisted(() => ({
  revision: 0,
  data: null as string | null,
  putCalls: 0,
}));

vi.mock("../../ipc/commands", () => ({
  layoutApi: {
    get: vi.fn(async () => ({
      revision: server.revision,
      updatedAt: server.revision,
      data: server.data === null ? null : JSON.parse(server.data),
    })),
    put: vi.fn(async (data: string, revision: number) => {
      server.putCalls += 1;
      if (revision !== server.revision) return { saved: false, revision: server.revision, conflict: true };
      server.revision = revision + 1;
      server.data = data;
      return { saved: true, revision: server.revision, conflict: false };
    }),
  },
  assetApi: { list: vi.fn(async () => []) },
  terminalApi: { listLive: vi.fn(async () => []) },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
}));
vi.mock("../../ipc/events", () => ({ listenEvent: vi.fn(async () => () => {}) }));

function group(id: string, parentId: string | null = null): AssetGroup {
  return { id, parentId, name: id, sort: 0, createdAt: 1, updatedAt: 1 };
}

function asset(id: string, groupId: string | null): Asset {
  return {
    id,
    groupId,
    kind: "ssh",
    name: id,
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

function terminal(id: string): AppTab {
  return { id, kind: "terminal", title: id, sessionId: `session-${id}`, tabId: `kernel-${id}`, closable: true };
}

function pane(id: string, tabs: AppTab[]): Pane {
  return { id, tabs, activeTabId: tabs[0]?.id ?? null };
}

function workspace(id: string, assetId: string): Workspace {
  return {
    id,
    kind: "session",
    title: id,
    sessionId: `session-${id}`,
    assetId,
    assetKind: "ssh",
    panes: [pane(`${id}-pane`, [terminal(`${id}-tab`)])],
    activePaneId: `${id}-pane`,
    splitRatio: 0.5,
    closable: true,
  };
}

function layout(workspaces: Workspace[], aiBoards: Record<string, AiBoard> = {}) {
  return {
    v: 3,
    leftOpen: true,
    leftMode: "assets" as const,
    rightOpen: true,
    leftWidth: 248,
    rightWidth: 352,
    workspaces,
    activeWorkspaceId: workspaces[0]?.id ?? null,
    aiBoards,
  };
}

const inventory: ProjectInventory = {
  assets: [asset("root-asset", "p1"), asset("other-asset", "p2")],
  groups: [group("p1"), group("p2")],
};
const projectOne = projectFocusForGroup("p1");

interface Client {
  layout: typeof import("../../app/layout");
  store: typeof import("../../app/store").useUi;
}

const clients: Client[] = [];

async function openClient(clearDeviceStorage = false): Promise<Client> {
  if (clearDeviceStorage) window.localStorage.clear();
  vi.resetModules();
  const layoutModule = await import("../../app/layout");
  const { useUi } = await import("../../app/store");
  const client = { layout: layoutModule, store: useUi };
  clients.push(client);
  layoutModule.startLayoutSync();
  await layoutModule.layoutBootstrapped;
  return client;
}

beforeEach(() => {
  server.revision = 1;
  server.data = JSON.stringify(layout([
    workspace("ws-root", "root-asset"),
    workspace("ws-other", "other-asset"),
  ]));
  server.putCalls = 0;
  window.localStorage.clear();
});

afterEach(() => {
  for (const client of clients) client.layout.resetLayoutSyncForTest();
  clients.length = 0;
  vi.clearAllMocks();
  window.localStorage.clear();
});

describe("project focus device layout", () => {
  it("does not write project selection to shared layout and restores it after reopening", async () => {
    const first = await openClient();
    first.store.getState().setProjectInventory(inventory);
    first.store.getState().setActiveProject(projectOne);
    await first.layout.flushLayout();

    expect(first.store.getState().activeProject).toBe(projectOne);
    expect(first.store.getState().activeWorkspaceId).toBe("ws-root");
    expect(server.putCalls).toBe(0);
    expect(server.revision).toBe(1);
    expect(first.layout.serializeLayout(first.store.getState())).not.toHaveProperty("activeProject");
    expect(server.data).not.toContain('"activeProject"');
    const deviceValues = Object.keys(window.localStorage)
      .filter((key) => key.startsWith("nexterm:layout-device-v1:"))
      .map((key) => window.localStorage.getItem(key) ?? "");
    expect(deviceValues.some((value) => value.includes('"activeProject":"project:p1"'))).toBe(true);

    const reopened = await openClient();
    expect(reopened.store.getState().activeProject).toBe(projectOne);
    expect(reopened.store.getState().activeWorkspaceId).toBe("ws-root");
    reopened.store.getState().setProjectInventory(inventory);
    expect(reopened.store.getState().activeWorkspaceId).toBe("ws-root");

    const remote = layout([
      { ...workspace("ws-root", "root-asset"), title: "remote rename" },
      workspace("ws-other", "other-asset"),
    ]);
    server.revision = 2;
    server.data = JSON.stringify(remote);
    reopened.layout.onRemoteChange({ revision: 2 });
    await vi.waitFor(() => expect(reopened.store.getState().workspaces[0]?.title).toBe("remote rename"));
    expect(reopened.store.getState().activeProject).toBe(projectOne);
    expect(reopened.store.getState().activeWorkspaceId).toBe("ws-root");
  });

  it("keeps another device's selection at its own default", async () => {
    const first = await openClient();
    first.store.getState().setProjectInventory(inventory);
    first.store.getState().setActiveProject(projectOne);
    await first.layout.flushLayout();

    const second = await openClient(true);
    expect(second.store.getState().activeProject).toBe(ALL_PROJECTS);
    expect(second.store.getState().workspaces.map((item) => item.id)).toEqual(["ws-root", "ws-other"]);
    expect(first.store.getState().activeProject).toBe(projectOne);
    expect(server.putCalls).toBe(0);
  });
});
