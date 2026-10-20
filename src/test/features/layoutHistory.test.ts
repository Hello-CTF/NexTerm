/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { deferred, flushMacrotask } from "./reactTestUtils";
import { layoutApi, type LayoutDto, type LayoutSaveResult } from "../../ipc/commands";

const server = vi.hoisted(() => ({
  revision: 0,
  data: null as string | null,
  getCalls: 0,
  putAlwaysConflicts: false,
  handlers: [] as Array<(payload: { revision?: number } | null) => void>,
}));

vi.mock("../../ipc/commands", () => ({
  layoutApi: {
    get: vi.fn(async () => {
      server.getCalls += 1;
      return {
        revision: server.revision,
        updatedAt: 0,
        data: server.data === null ? null : JSON.parse(server.data),
      };
    }),
    put: vi.fn(async (data: string, revision: number) => {
      if (server.putAlwaysConflicts || revision !== server.revision) {
        return { saved: false, revision: server.revision, conflict: true };
      }
      server.revision = revision + 1;
      server.data = data;
      return { saved: true, revision: server.revision, conflict: false };
    }),
  },
  terminalApi: { listLive: vi.fn(async () => []) },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
}));

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    listenEvent: vi.fn((topic: string, handler: (payload: never) => void) => {
      if (topic === "layout://changed") {
        server.handlers.push(handler as (payload: { revision?: number } | null) => void);
      }
      return Promise.resolve(() => {});
    }),
  };
});

import type { PersistedLayout } from "../../app/layout";
import type { AppTab, Workspace } from "../../app/store";

interface UiState {
  leftOpen: boolean;
  leftMode: string;
  rightOpen: boolean;
  leftWidth: number;
  rightWidth: number;
  workspaces: Workspace[];
  activeWorkspaceId: string | null;
  aiBoards: PersistedLayout["aiBoards"];
  aiAccountEpoch: number;
  resetAiAccount: () => void;
  addTab: (tab: AppTab) => void;
  toasts: { id: number; text: string }[];
}

interface LayoutModule {
  startLayoutSync: () => void;
  flushLayout: () => Promise<void>;
  onRemoteChange: (payload: { revision?: number } | null) => void;
  advanceLayoutAccount: () => void;
  resetLayoutSyncForTest: () => void;
  LAYOUT_RETRY_MS: number;
  LAYOUT_MAX_RETRY: number;
  layoutBootstrapped: Promise<void>;
  sanitizeLayout: (raw: unknown) => PersistedLayout | null;
  serializeLayout: (s: UiState) => PersistedLayout | null;
}

interface StoreModule {
  useUi: {
    getState: () => UiState;
    setState: (partial: Partial<UiState>) => void;
  };
}

const openedLayouts: LayoutModule[] = [];

function panelTab(id: string, kind: AppTab["kind"], title: string): AppTab {
  return { id, kind, title, closable: true };
}

function workspace(tabs: AppTab[], activeTabId: string | null = tabs[tabs.length - 1]?.id ?? null): Workspace {
  return {
    id: "ws",
    kind: "tools",
    title: "工具",
    panes: [{ id: "pane", tabs, activeTabId }],
    activePaneId: "pane",
    splitRatio: 0.5,
    closable: true,
  };
}

function toolTabs(): AppTab[] {
  return [
    panelTab("settings", "settings", "Settings"),
    { ...panelTab("history", "history", "终端历史"), sessionId: "s1", assetId: "asset-1" },
    panelTab("audit", "audit", "Audit log"),
  ];
}

function background(id: string, scope: { sessionId?: string; assetId?: string } = {}): AppTab {
  return { ...panelTab(id, "background", "Background sessions"), ...scope };
}

function terminal(id: string, sessionId: string): AppTab {
  return {
    id,
    kind: "terminal",
    title: id,
    sessionId,
    tabId: `kernel-${id}`,
    closable: true,
  };
}

function hostWorkspace(
  id: string,
  sessionId: string,
  assetId: string,
  tabs: AppTab[],
  activeTabId: string | null = tabs[tabs.length - 1]?.id ?? null,
): Workspace {
  return {
    id,
    kind: "session",
    title: id,
    sessionId,
    assetId,
    panes: [{ id: `${id}-pane`, tabs, activeTabId }],
    activePaneId: `${id}-pane`,
    splitRatio: 0.5,
    closable: true,
  };
}

function legacyWorkspaces(activeTabId: string): Workspace[] {
  return [
    workspace(
      [
        panelTab("settings", "settings", "Settings"),
        { ...panelTab("history", "history", "终端历史"), sessionId: "s1", assetId: "asset-1" },
        background("background", { sessionId: "s1", assetId: "asset-1" }),
        panelTab("audit", "audit", "Audit log"),
      ],
      activeTabId,
    ),
    hostWorkspace("host", "s1", "asset-1", [terminal("terminal", "s1")]),
  ];
}

function persistedLayout(workspaces: Workspace[]): PersistedLayout {
  return {
    v: 2,
    leftOpen: true,
    leftMode: "assets",
    rightOpen: true,
    leftWidth: 248,
    rightWidth: 352,
    workspaces,
    activeWorkspaceId: workspaces[0]?.id ?? null,
    aiBoards: {},
  };
}

function findTab(state: UiState, id: string): AppTab | undefined {
  for (const w of state.workspaces) {
    for (const p of w.panes) {
      const t = p.tabs.find((x) => x.id === id);
      if (t) return t;
    }
  }
  return undefined;
}

async function openTab(): Promise<{ layout: LayoutModule; store: StoreModule["useUi"] }> {
  vi.resetModules();
  const layout = (await import("../../app/layout")) as unknown as LayoutModule;
  const store = (await import("../../app/store")) as unknown as StoreModule;
  openedLayouts.push(layout);
  layout.startLayoutSync();
  await layout.layoutBootstrapped;
  return { layout, store: store.useUi };
}

async function waitForPull(before: number): Promise<void> {
  await vi.waitFor(() => expect(server.getCalls).toBeGreaterThan(before));
  await flushMacrotask();
}

beforeEach(() => {
  server.revision = 0;
  server.data = null;
  server.getCalls = 0;
  server.putAlwaysConflicts = false;
  server.handlers.length = 0;
  window.localStorage.clear();
});

afterEach(() => {
  for (const layout of openedLayouts) layout.resetLayoutSyncForTest();
  openedLayouts.length = 0;
  vi.resetModules();
  vi.clearAllMocks();
});

describe("history tab in the layout pane contract", () => {
  it("keeps an attaching SSH terminal through an earlier layout save and its readback", async () => {
    const { layout, store } = await openTab();
    const pending: AppTab = {
      id: "connecting-terminal", kind: "terminal", title: "终端 1", sessionId: "s1", closable: true,
    };
    const host = hostWorkspace("host", "s1", "asset-1", [pending]);
    store.setState({ workspaces: [host], activeWorkspaceId: host.id });

    const beforeAttach = layout.serializeLayout(store.getState());
    expect(beforeAttach?.workspaces[0]?.panes[0].tabs[0]).toMatchObject({
      id: pending.id, restorePending: true,
    });
    await layout.flushLayout();
    expect(JSON.parse(server.data ?? "null").workspaces[0].panes[0].tabs).toHaveLength(1);

    const attached = { ...pending, tabId: "kernel-terminal" };
    store.setState({ workspaces: [hostWorkspace("host", "s1", "asset-1", [attached])] });
    const beforeReadback = server.getCalls;
    layout.onRemoteChange(null);
    await waitForPull(beforeReadback);

    expect(findTab(store.getState(), pending.id)).toMatchObject({
      tabId: "kernel-terminal", restorePending: undefined,
    });
    await vi.waitFor(() => {
      expect(JSON.parse(server.data ?? "null").workspaces[0].panes[0].tabs[0].tabId).toBe("kernel-terminal");
    }, { timeout: 2000 });
  });

  it("serialize → JSON → sanitize keeps account tools together and background in its host", async () => {
    const { layout, store } = await openTab();
    store.setState({
      workspaces: [
        workspace(toolTabs(), "history"),
        hostWorkspace("host", "s1", "asset-1", [
          terminal("terminal", "s1"),
          background("background-host", { sessionId: "s1", assetId: "asset-1" }),
        ]),
      ],
      activeWorkspaceId: "ws",
    });

    const dto = layout.serializeLayout(store.getState());
    expect(dto).not.toBeNull();
    const parsed = layout.sanitizeLayout(JSON.parse(JSON.stringify(dto)));
    expect(parsed).not.toBeNull();

    expect(parsed?.workspaces).toHaveLength(2);
    const tools = parsed?.workspaces.find((w) => w.kind === "tools");
    const host = parsed?.workspaces.find((w) => w.id === "host");
    const pane = tools?.panes[0];
    expect(pane?.tabs.map((t) => t.kind)).toEqual(["settings", "history", "audit"]);
    expect(pane?.activeTabId).toBe("history");
    expect(pane?.tabs.find((t) => t.id === "history")).toMatchObject({
      title: "终端历史",
      closable: true,
      sessionId: "s1",
      assetId: "asset-1",
    });
    expect(host?.panes[0].tabs.map((t) => t.kind)).toEqual(["terminal", "background"]);
    expect(host?.panes[0].tabs[1]).toMatchObject({
      id: "background-host",
      sessionId: "s1",
      assetId: "asset-1",
    });
  });

  it("preserves an empty Tools split when the layout needs no migration", async () => {
    const { layout } = await openTab();
    const tools = workspace(toolTabs(), "history");
    tools.panes.push({ id: "tools-empty", tabs: [], activeTabId: null });
    tools.activePaneId = "tools-empty";

    const parsed = layout.sanitizeLayout(persistedLayout([tools]));
    const restored = parsed?.workspaces[0];
    expect(restored?.panes.map((pane) => pane.id)).toEqual(["pane", "tools-empty"]);
    expect(restored?.panes[0].activeTabId).toBe("history");
    expect(restored?.panes[1].tabs).toEqual([]);
    expect(restored?.activePaneId).toBe("tools-empty");
  });

  it("migrates legacy global tabs into one Tools workspace and repairs active pointers", async () => {
    const { layout } = await openTab();
    const legacy: Workspace[] = [
      {
        id: "host",
        kind: "session",
        title: "host",
        sessionId: "s1",
        assetId: "asset-1",
        panes: [
          {
            id: "host-p1",
            tabs: [
              { id: "terminal", kind: "terminal", title: "Terminal", sessionId: "s1", tabId: "kernel-1", closable: true },
              { id: "settings-a", kind: "settings", title: "Settings", closable: true },
            ],
            activeTabId: "settings-a",
          },
          {
            id: "host-p2",
            tabs: [
              panelTab("history", "history", "Terminal history"),
              background("background", { sessionId: "s1", assetId: "asset-1" }),
            ],
            activeTabId: "history",
          },
        ],
        activePaneId: "host-p2",
        splitRatio: 0.5,
        closable: true,
      },
      {
        id: "db",
        kind: "db",
        title: "database",
        connId: "c1",
        panes: [
          {
            id: "db-p",
            tabs: [
              { id: "db-tab", kind: "db", title: "Database", connId: "c1", dbKind: "mysql", closable: true },
              panelTab("credentials", "credentials", "Credentials"),
              panelTab("settings-b", "settings", "Settings"),
            ],
            activeTabId: "settings-b",
          },
        ],
        activePaneId: "db-p",
        splitRatio: 0.5,
        closable: true,
      },
      {
        id: "tools",
        kind: "tools",
        title: "Tools",
        panes: [
          {
            id: "tools-p",
            tabs: [
              panelTab("audit", "audit", "Audit log"),
              background("background", { assetId: "asset-1" }),
            ],
            activeTabId: "audit",
          },
        ],
        activePaneId: "tools-p",
        splitRatio: 0.5,
        closable: true,
      },
    ];

    const parsed = layout.sanitizeLayout(persistedLayout(legacy));
    expect(parsed).not.toBeNull();
    const host = parsed?.workspaces.find((w) => w.id === "host");
    expect(host?.panes.map((pane) => pane.id)).toEqual(["host-p1", "host-p2"]);
    expect(host?.panes[0].tabs.map((t) => t.id)).toEqual(["terminal"]);
    expect(host?.panes[0].activeTabId).toBe("terminal");
    expect(host?.panes[1].tabs).toEqual([
      expect.objectContaining({
        id: "background-host",
        kind: "background",
        sessionId: "s1",
        assetId: "asset-1",
      }),
    ]);
    expect(host?.panes[1].activeTabId).toBe("background-host");
    expect(host?.activePaneId).toBe("host-p2");
    const db = parsed?.workspaces.find((w) => w.id === "db");
    expect(db?.panes[0].tabs.map((t) => t.id)).toEqual(["db-tab"]);
    expect(db?.panes[0].activeTabId).toBe("db-tab");
    const tools = parsed?.workspaces.filter((w) => w.kind === "tools");
    expect(tools).toHaveLength(1);
    expect(tools?.[0].title).toBe("工具");
    expect(tools?.[0].panes[0].tabs.map((t) => t.kind)).toEqual([
      "audit",
      "settings",
      "history",
      "credentials",
    ]);
    expect(tools?.[0].panes[0].activeTabId).toBe("history");
    expect(tools?.[0].panes[0].tabs.find((t) => t.kind === "history")).toMatchObject({
      sessionId: "s1",
      assetId: "asset-1",
    });
    expect(parsed?.workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs)).filter((t) => t.kind === "settings")).toHaveLength(1);
    expect(parsed?.activeWorkspaceId).toBe("tools");
  });

  function expectLegacyLayout(state: UiState, activeTabId = "history"): void {
    const tools = state.workspaces.find((w) => w.kind === "tools");
    const host = state.workspaces.find((w) => w.id === "host");
    expect(tools?.panes[0].tabs.map((t) => t.kind)).toEqual(["settings", "history", "audit"]);
    expect(tools?.panes[0].activeTabId).toBe(activeTabId);
    expect(findTab(state, "history")?.title).toBe("终端历史");
    expect(host?.panes[0].tabs.map((t) => t.kind)).toEqual(["terminal", "background"]);
    expect(host?.panes[0].tabs[1]).toMatchObject({
      id: "background-host",
      sessionId: "s1",
      assetId: "asset-1",
    });
    expect(state.activeWorkspaceId).toBe("ws");
  }

  it("boot application applies the same legacy migration", async () => {
    server.data = JSON.stringify(persistedLayout(legacyWorkspaces("history")));
    const { store } = await openTab();

    expectLegacyLayout(store.getState());
  });

  it("remote LAYOUT_CHANGED application applies the same legacy migration", async () => {
    server.data = JSON.stringify(persistedLayout([workspace([panelTab("settings", "settings", "Settings")])]));
    const { layout, store } = await openTab();
    expect(findTab(store.getState(), "history")).toBeUndefined();

    server.revision = 1;
    server.data = JSON.stringify(persistedLayout(legacyWorkspaces("history")));
    layout.onRemoteChange({ revision: 1 });

    await vi.waitFor(() => {
      expect(findTab(store.getState(), "history")?.title).toBe("终端历史");
    });
    expectLegacyLayout(store.getState(), "settings");
  });

  it("conflict layout application applies the same legacy migration and merges the server copy", async () => {
    server.data = JSON.stringify(persistedLayout([workspace([panelTab("settings", "settings", "Settings")])]));
    const { layout, store } = await openTab();

    server.revision = 7;
    server.data = JSON.stringify(persistedLayout(legacyWorkspaces("history")));
    server.putAlwaysConflicts = true;
    store.setState({
      leftWidth: 300,
      workspaces: [
        ...store.getState().workspaces,
        hostWorkspace("local-host", "s-local", "asset-local", [terminal("local-terminal", "s-local")]),
      ],
    });
    await layout.flushLayout();

    const state = store.getState();
    expectLegacyLayout(state, "settings");
    expect(state.leftWidth).toBe(300);
    expect(state.toasts.some((t) => t.text.includes("布局已在其他设备上更新"))).toBe(true);
  });

  it("keeps same-id backgrounds with distinct owners, drops ambiguous views, and is idempotent", async () => {
    const { layout } = await openTab();
    const raw = persistedLayout([
      workspace(
        [
          background("background", { sessionId: "s1" }),
          background("background", { assetId: "asset-2" }),
          background("missing"),
          background("conflict", { sessionId: "s1", assetId: "asset-2" }),
          background("stale", { sessionId: "deleted", assetId: "asset-1" }),
        ],
        "missing",
      ),
      hostWorkspace("host-a", "s1", "asset-1", [terminal("terminal-a", "s1")]),
      hostWorkspace("host-b", "s2", "asset-2", [terminal("terminal-b", "s2")]),
    ]);
    raw.activeWorkspaceId = "host-a";

    const parsed = layout.sanitizeLayout(raw);
    const tabs = parsed?.workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs)) ?? [];
    expect(tabs.filter((t) => t.kind === "background")).toEqual([
      expect.objectContaining({ id: "background-host-a", sessionId: "s1", assetId: "asset-1" }),
      expect.objectContaining({ id: "background-host-b", sessionId: "s2", assetId: "asset-2" }),
    ]);
    expect(tabs.filter((t) => t.kind === "terminal").map((t) => t.id)).toEqual([
      "terminal-a",
      "terminal-b",
    ]);
    expect(parsed?.activeWorkspaceId).toBe("host-a");
    expect(parsed?.workspaces.find((w) => w.id === "host-a")?.panes[0].activeTabId).toBe(
      "terminal-a",
    );
    expect(parsed?.workspaces.find((w) => w.id === "host-b")?.panes[0].activeTabId).toBe(
      "terminal-b",
    );

    const twice = layout.sanitizeLayout(JSON.parse(JSON.stringify(parsed)));
    expect(twice).toEqual(parsed);
  });

  it("follows an active migrated background without letting an inactive one steal focus", async () => {
    const { layout } = await openTab();
    const rawWithActive = (activeWorkspaceId: string): PersistedLayout => {
      const raw = persistedLayout([
        workspace([background("legacy-background", { sessionId: "s2", assetId: "asset-2" })]),
        hostWorkspace("host-a", "s1", "asset-1", [terminal("terminal-a", "s1")]),
        hostWorkspace("host-b", "s2", "asset-2", [terminal("terminal-b", "s2")]),
      ]);
      raw.activeWorkspaceId = activeWorkspaceId;
      return raw;
    };

    const followed = layout.sanitizeLayout(rawWithActive("ws"));
    expect(followed?.activeWorkspaceId).toBe("host-b");
    expect(followed?.workspaces.find((w) => w.id === "host-b")?.activePaneId).toBe("host-b-pane");
    expect(followed?.workspaces.find((w) => w.id === "host-b")?.panes[0].activeTabId).toBe(
      "background-host-b",
    );

    const inactive = layout.sanitizeLayout(rawWithActive("host-a"));
    expect(inactive?.activeWorkspaceId).toBe("host-a");
    expect(inactive?.workspaces.find((w) => w.id === "host-a")?.panes[0].activeTabId).toBe(
      "terminal-a",
    );
    expect(inactive?.workspaces.find((w) => w.id === "host-b")?.panes[0].activeTabId).toBe(
      "terminal-b",
    );
  });

  it("does not guess an active owner when duplicate legacy background ids are ambiguous", async () => {
    const { layout } = await openTab();
    const raw = persistedLayout([
      workspace(
        [
          background("background", { sessionId: "s1" }),
          background("background", { assetId: "asset-2" }),
        ],
        "background",
      ),
      hostWorkspace("host-a", "s1", "asset-1", [terminal("terminal-a", "s1")]),
      hostWorkspace("host-b", "s2", "asset-2", [terminal("terminal-b", "s2")]),
    ]);

    const parsed = layout.sanitizeLayout(raw);
    expect(parsed?.activeWorkspaceId).toBe("ws");
    expect(parsed?.workspaces.find((w) => w.id === "host-a")?.panes[0].activeTabId).toBe(
      "terminal-a",
    );
    expect(parsed?.workspaces.find((w) => w.id === "host-b")?.panes[0].activeTabId).toBe(
      "terminal-b",
    );
  });

  it("preserves unrelated target pane order when a background moves in", async () => {
    const { layout } = await openTab();
    const host = hostWorkspace("host-b", "s2", "asset-2", [terminal("terminal-b", "s2")]);
    host.panes.push({
      id: "host-b-files",
      tabs: [
        {
          id: "notes",
          kind: "files",
          title: "notes",
          sessionId: "s2",
          path: "/tmp/notes",
          closable: true,
        },
      ],
      activeTabId: "notes",
    });
    const raw = persistedLayout([
      workspace([background("legacy-background", { assetId: "asset-2" })]),
      host,
    ]);
    raw.activeWorkspaceId = "host-b";

    const parsed = layout.sanitizeLayout(raw);
    const restored = parsed?.workspaces.find((w) => w.id === "host-b");
    expect(restored?.panes.map((pane) => pane.id)).toEqual(["host-b-pane", "host-b-files"]);
    expect(restored?.panes[0].activeTabId).toBe("terminal-b");
    expect(restored?.activePaneId).toBe("host-b-pane");
  });

  it("drops an unscoped legacy background on boot and asks the user to reopen it from a host", async () => {
    server.data = JSON.stringify(
      persistedLayout([
        workspace([background("missing")]),
        hostWorkspace("host-a", "s1", "asset-1", [terminal("terminal-a", "s1")]),
      ]),
    );
    const { store } = await openTab();

    const state = store.getState();
    expect(state.workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs)).some((t) => t.kind === "background")).toBe(false);
    expect(state.activeWorkspaceId).toBe("ws");
    expect(state.toasts.some((t) => t.text === "请从目标主机重新打开后台会话")).toBe(true);
  });

  it("keeps AI boards with their original workspaces when history moves to Tools", async () => {
    const { layout } = await openTab();
    const host = hostWorkspace("host", "s1", "asset-1", [
      { ...panelTab("legacy-history", "history", "Host history"), sessionId: "s1", assetId: "asset-1" },
    ]);
    const raw = persistedLayout([host, workspace([panelTab("settings", "settings", "Settings")])]);
    raw.activeWorkspaceId = "host";
    raw.aiBoards = {
      host: {
        tabs: [{ id: "ai-host", title: "Host chat", conversationId: "conv-host" }],
        activeTabId: "ai-host",
      },
      global: {
        tabs: [{ id: "ai-global", title: "Global chat", conversationId: "conv-global" }],
        activeTabId: "ai-global",
      },
    };

    const parsed = layout.sanitizeLayout(raw);
    expect(parsed?.workspaces.find((w) => w.id === "host")?.panes[0].tabs).toEqual([]);
    expect(parsed?.workspaces.find((w) => w.kind === "tools")?.panes[0].tabs.map((t) => t.id)).toEqual([
      "settings",
      "legacy-history",
    ]);
    expect(parsed?.activeWorkspaceId).toBe("ws");
    expect(parsed?.aiBoards.host).toEqual(raw.aiBoards.host);
    expect(parsed?.aiBoards.global).toEqual(raw.aiBoards.global);
  });

  it("accepts unique session owners that share an asset without rescuing invalid scopes", async () => {
    const { layout } = await openTab();
    const raw = persistedLayout([
      workspace([
        background("background-a", { sessionId: "s1", assetId: "shared" }),
        background("background-b", { sessionId: "s2", assetId: "shared" }),
        background("asset-only", { assetId: "shared" }),
        background("stale", { sessionId: "deleted", assetId: "shared" }),
      ]),
      hostWorkspace("host-a", "s1", "shared", [terminal("terminal-a", "s1")]),
      hostWorkspace("host-b", "s2", "shared", [terminal("terminal-b", "s2")]),
    ]);

    const parsed = layout.sanitizeLayout(raw);
    const backgrounds = parsed?.workspaces.flatMap((w) =>
      w.panes.flatMap((p) => p.tabs.filter((t) => t.kind === "background")),
    );
    expect(backgrounds).toEqual([
      expect.objectContaining({ id: "background-host-a", sessionId: "s1", assetId: "shared" }),
      expect.objectContaining({ id: "background-host-b", sessionId: "s2", assetId: "shared" }),
    ]);
  });

  it("preserves a boarded duplicate Tools workspace through restore, reopen, and serialize", async () => {
    const { layout, store } = await openTab();
    const secondary = workspace([panelTab("legacy-history", "history", "History")]);
    secondary.id = "tools-board";
    secondary.panes[0].id = "tools-board-pane";
    secondary.activePaneId = "tools-board-pane";
    const raw = persistedLayout([
      workspace([panelTab("settings", "settings", "Settings")]),
      secondary,
    ]);
    raw.aiBoards = {
      "tools-board": {
        tabs: [{ id: "ai-board", title: "Board", conversationId: "conv-board" }],
        activeTabId: "ai-board",
      },
    };

    const restored = layout.sanitizeLayout(raw);
    expect(restored).not.toBeNull();
    if (!restored) throw new Error("expected a sanitized layout");
    expect(restored.aiBoards["tools-board"]).toEqual(raw.aiBoards["tools-board"]);
    store.setState({
      workspaces: restored.workspaces,
      activeWorkspaceId: restored.activeWorkspaceId,
      aiBoards: restored.aiBoards,
    });

    store.getState().addTab(panelTab("audit", "audit", "Audit"));
    const serialized = layout.serializeLayout(store.getState());
    expect(serialized?.workspaces.some((w) => w.id === "tools-board")).toBe(true);
    expect(serialized?.aiBoards["tools-board"]).toEqual(raw.aiBoards["tools-board"]);
  });

  it("notifies once per invalid background scope until a clean layout resets the notice", async () => {
    const invalidLayout = (withNewScope: boolean): PersistedLayout =>
      persistedLayout([
        workspace(
          withNewScope
            ? [background("missing"), background("missing", { sessionId: "deleted" })]
            : [background("missing")],
        ),
        hostWorkspace("host-a", "s1", "asset-1", [terminal("terminal-a", "s1")]),
      ]);
    server.data = JSON.stringify(invalidLayout(false));
    const { layout, store } = await openTab();
    const noticeId = () =>
      store.getState().toasts.find((toast) => toast.text === "请从目标主机重新打开后台会话")?.id;

    const first = noticeId();
    expect(first).toBeDefined();
    let before = server.getCalls;
    layout.onRemoteChange(null);
    await waitForPull(before);
    expect(noticeId()).toBe(first);

    server.revision = 1;
    before = server.getCalls;
    layout.onRemoteChange({ revision: 1 });
    await waitForPull(before);
    expect(noticeId()).toBe(first);

    server.revision = 2;
    server.data = JSON.stringify(invalidLayout(true));
    before = server.getCalls;
    layout.onRemoteChange({ revision: 2 });
    await waitForPull(before);
    const withNewScope = noticeId();
    expect(withNewScope).not.toBe(first);

    server.revision = 3;
    server.data = JSON.stringify(
      persistedLayout([workspace([panelTab("settings", "settings", "Settings")])]),
    );
    before = server.getCalls;
    layout.onRemoteChange({ revision: 3 });
    await waitForPull(before);
    expect(noticeId()).toBe(withNewScope);

    server.revision = 4;
    server.data = JSON.stringify(invalidLayout(false));
    before = server.getCalls;
    layout.onRemoteChange({ revision: 4 });
    await waitForPull(before);
    expect(noticeId()).not.toBe(withNewScope);
  });

  it("preserves only current AI drafts for matching remote tab identities", async () => {
    const hostA = hostWorkspace("host-a", "s1", "asset-1", [terminal("terminal-a", "s1")]);
    const hostB = hostWorkspace("host-b", "s2", "asset-2", [terminal("terminal-b", "s2")]);
    const { layout, store } = await openTab();
    store.setState({
      workspaces: [hostA, hostB],
      activeWorkspaceId: "host-a",
      aiBoards: {
        "host-a": {
          tabs: [
            { id: "ai-bound", title: "Bound", conversationId: "conv-bound", draft: "local bound" },
            { id: "ai-new", title: "New", draft: "local new" },
            { id: "ai-changed", title: "Changed", conversationId: "conv-old", draft: "old account draft" },
            { id: "ai-deleted", title: "Deleted", conversationId: "conv-deleted", draft: "deleted draft" },
          ],
          activeTabId: "ai-bound",
        },
        "host-b": {
          tabs: [{ id: "ai-board-b", title: "B", conversationId: "conv-b", draft: "deleted board draft" }],
          activeTabId: "ai-board-b",
        },
      },
      toasts: [],
    });
    await layout.flushLayout();
    const remote = persistedLayout([hostA, hostB]);
    remote.aiBoards = {
      "host-a": {
        tabs: [
          { id: "ai-bound", title: "Renamed", conversationId: "conv-bound" },
          { id: "ai-new", title: "New" },
          { id: "ai-changed", title: "Changed", conversationId: "conv-other", draft: "remote draft" },
          { id: "ai-remote", title: "Remote", conversationId: "conv-remote", draft: "remote only" },
        ],
        activeTabId: "ai-bound",
      },
    };
    server.revision = 2;
    server.data = JSON.stringify(remote);
    const before = server.getCalls;

    layout.onRemoteChange({ revision: 2 });
    await waitForPull(before);

    expect(store.getState().aiBoards).toEqual({
      "host-a": {
        tabs: [
          { id: "ai-bound", title: "Renamed", conversationId: "conv-bound", draft: "local bound" },
          { id: "ai-new", title: "New", conversationId: undefined, draft: "local new" },
          { id: "ai-changed", title: "Changed", conversationId: "conv-other" },
          { id: "ai-remote", title: "Remote", conversationId: "conv-remote" },
        ],
        activeTabId: "ai-bound",
      },
    });
    const serialized = layout.serializeLayout(store.getState());
    expect(serialized?.aiBoards["host-a"].tabs.every((tab) => tab.draft === undefined)).toBe(true);
  });

  it("does not restore an old account draft after an account reset even when all remote identities match", async () => {
    const host = hostWorkspace("host-a", "s1", "asset-1", [terminal("terminal-a", "s1")]);
    const { layout, store } = await openTab();
    store.setState({
      workspaces: [host],
      activeWorkspaceId: "host-a",
      aiBoards: {
        "host-a": {
          tabs: [{ id: "ai-a", title: "A", conversationId: "conv-a", draft: "account A secret" }],
          activeTabId: "ai-a",
        },
      },
      toasts: [],
    });
    const epoch = store.getState().aiAccountEpoch;
    store.getState().resetAiAccount();
    expect(store.getState().aiAccountEpoch).toBe(epoch + 1);

    const remote = persistedLayout([host]);
    remote.aiBoards = {
      "host-a": {
        tabs: [{ id: "ai-a", title: "A", conversationId: "conv-a" }],
        activeTabId: "ai-a",
      },
    };
    server.revision = 1;
    server.data = JSON.stringify(remote);
    const before = server.getCalls;
    layout.onRemoteChange({ revision: 1 });
    await waitForPull(before);

    expect(store.getState().aiBoards["host-a"].tabs[0]).toEqual({
      id: "ai-a",
      title: "A",
      conversationId: "conv-a",
    });
  });

  it("does not apply a stale successful GET before refreshing the next account", async () => {
    const hostA = hostWorkspace("host-a", "s1", "asset-1", [terminal("terminal-a", "s1")]);
    const hostB = hostWorkspace("host-b", "s2", "asset-2", [terminal("terminal-b", "s2")]);
    const { layout, store } = await openTab();
    const stale = deferred<LayoutDto>();
    const fresh = deferred<LayoutDto>();
    const get = vi.mocked(layoutApi.get);
    get.mockImplementationOnce(() => stale.promise).mockImplementationOnce(() => fresh.promise);
    layout.onRemoteChange({ revision: 1 });
    layout.advanceLayoutAccount();
    store.setState({ workspaces: [], activeWorkspaceId: null, aiBoards: {} });
    layout.onRemoteChange(null);

    stale.resolve({ revision: 1, updatedAt: 0, data: persistedLayout([hostA]) });
    await vi.waitFor(() => expect(get).toHaveBeenCalledTimes(2));
    expect(store.getState().workspaces.some((w) => w.id === "host-a")).toBe(false);

    fresh.resolve({ revision: 2, updatedAt: 0, data: persistedLayout([hostB]) });
    await vi.waitFor(() => expect(store.getState().workspaces[0]?.id).toBe("host-b"));
  });

  it("continues the pending account refresh after a stale GET fails", async () => {
    const hostB = hostWorkspace("host-b", "s2", "asset-2", [terminal("terminal-b", "s2")]);
    const { layout, store } = await openTab();
    const stale = deferred<LayoutDto>();
    const fresh = deferred<LayoutDto>();
    const get = vi.mocked(layoutApi.get);
    get.mockImplementationOnce(() => stale.promise).mockImplementationOnce(() => fresh.promise);
    layout.onRemoteChange({ revision: 1 });
    layout.advanceLayoutAccount();
    store.setState({ workspaces: [], activeWorkspaceId: null, aiBoards: {} });
    layout.onRemoteChange(null);

    stale.reject(new Error("stale account GET failed"));
    await vi.waitFor(() => expect(get).toHaveBeenCalledTimes(2));
    fresh.resolve({ revision: 2, updatedAt: 0, data: persistedLayout([hostB]) });
    await vi.waitFor(() => expect(store.getState().workspaces[0]?.id).toBe("host-b"));
  });

  it("does not let a stale PUT success pollute the next account revision or base", async () => {
    const hostB = hostWorkspace("host-b", "s2", "asset-2", [terminal("terminal-b", "s2")]);
    const { layout, store } = await openTab();
    store.setState({ workspaces: [hostB], activeWorkspaceId: "host-b" });
    const stalePut = deferred<LayoutSaveResult>();
    const freshPut = deferred<LayoutSaveResult>();
    const freshGet = deferred<LayoutDto>();
    let freshGetStarted = false;
    const put = vi.mocked(layoutApi.put);
    put.mockImplementationOnce(() => stalePut.promise).mockImplementationOnce(() => freshPut.promise);
    vi.mocked(layoutApi.get).mockImplementationOnce(() => {
      freshGetStarted = true;
      return freshGet.promise;
    });
    const firstFlush = layout.flushLayout();
    layout.advanceLayoutAccount();
    store.setState({ workspaces: [], activeWorkspaceId: null, aiBoards: {} });
    layout.onRemoteChange(null);

    stalePut.resolve({ saved: true, revision: 1, conflict: false });
    await vi.waitFor(() => expect(freshGetStarted).toBe(true));
    freshGet.resolve({ revision: 2, updatedAt: 0, data: persistedLayout([hostB]) });
    await firstFlush;
    store.setState({ workspaces: [{ ...hostB, title: "next account" }] });
    const secondFlush = layout.flushLayout();
    await vi.waitFor(() => expect(put).toHaveBeenCalledTimes(2));
    expect(put.mock.calls[1][1]).toBe(2);

    freshPut.resolve({ saved: true, revision: 2, conflict: false });
    await secondFlush;
    expect(store.getState().workspaces[0]?.id).toBe("host-b");
  });

  it("continues the account refresh without retrying a stale failed PUT", async () => {
    vi.useFakeTimers();
    const hostB = hostWorkspace("host-b", "s2", "asset-2", [terminal("terminal-b", "s2")]);
    const { layout, store } = await openTab();
    store.setState({ workspaces: [hostB], activeWorkspaceId: "host-b" });
    const stalePut = deferred<LayoutSaveResult>();
    const freshGet = deferred<LayoutDto>();
    const put = vi.mocked(layoutApi.put);
    put.mockImplementationOnce(() => stalePut.promise);
    vi.mocked(layoutApi.get).mockImplementationOnce(() => freshGet.promise);
    const firstFlush = layout.flushLayout();
    layout.advanceLayoutAccount();
    store.setState({ workspaces: [], activeWorkspaceId: null, aiBoards: {} });
    layout.onRemoteChange(null);

    stalePut.reject(new Error("stale account PUT failed"));
    freshGet.resolve({ revision: 2, updatedAt: 0, data: persistedLayout([hostB]) });
    layout.onRemoteChange(null);
    await firstFlush;
    expect(store.getState().workspaces[0]?.id).toBe("host-b");
    await vi.advanceTimersByTimeAsync(layout.LAYOUT_RETRY_MS * layout.LAYOUT_MAX_RETRY);
    expect(vi.getTimerCount()).toBe(0);
    const staleJson = put.mock.calls[0][0];
    expect(put.mock.calls.slice(1).every(([data]) => data !== staleJson)).toBe(true);
  });

  it("stops conflict recovery when the conflict response itself is stale", async () => {
    const hostB = hostWorkspace("host-b", "s2", "asset-2", [terminal("terminal-b", "s2")]);
    const { layout, store } = await openTab();
    store.setState({ workspaces: [hostB], activeWorkspaceId: "host-b" });
    const conflict = deferred<LayoutSaveResult>();
    const put = vi.mocked(layoutApi.put);
    put.mockImplementationOnce(() => conflict.promise);
    const flushing = layout.flushLayout();
    const oldJson = put.mock.calls[0][0];

    conflict.resolve({ saved: false, revision: 0, conflict: true });
    layout.advanceLayoutAccount();
    store.setState({ workspaces: [], activeWorkspaceId: null, aiBoards: {} });
    await flushing;
    expect(put).toHaveBeenCalledTimes(1);

    server.revision = 2;
    server.data = JSON.stringify(persistedLayout([hostB]));
    layout.onRemoteChange(null);
    await vi.waitFor(() => expect(store.getState().workspaces[0]?.id).toBe("host-b"));
    store.setState({ workspaces: [{ ...hostB, title: "next account" }] });
    await layout.flushLayout();
    expect(put).toHaveBeenCalledTimes(2);
    expect(put.mock.calls[1][1]).toBe(2);
    expect(put.mock.calls[1][0]).not.toBe(oldJson);
  });

  it("does not retry old JSON after a conflict GET succeeds during account change", async () => {
    const hostA = hostWorkspace("host-a", "s1", "asset-1", [terminal("terminal-a", "s1")]);
    const hostB = hostWorkspace("host-b", "s2", "asset-2", [terminal("terminal-b", "s2")]);
    const { layout, store } = await openTab();
    store.setState({ workspaces: [hostA], activeWorkspaceId: "host-a" });
    const conflictGet = deferred<LayoutDto>();
    const put = vi.mocked(layoutApi.put);
    put.mockResolvedValueOnce({ saved: false, revision: 0, conflict: true });
    vi.mocked(layoutApi.get).mockImplementationOnce(() => conflictGet.promise);
    const flushing = layout.flushLayout();
    const oldJson = put.mock.calls[0][0];
    await vi.waitFor(() => expect(layoutApi.get).toHaveBeenCalledTimes(1));

    layout.advanceLayoutAccount();
    store.setState({ workspaces: [], activeWorkspaceId: null, aiBoards: {} });
    conflictGet.resolve({ revision: 1, updatedAt: 0, data: persistedLayout([hostA]) });
    await flushing;
    expect(put).toHaveBeenCalledTimes(1);

    server.revision = 2;
    server.data = JSON.stringify(persistedLayout([hostB]));
    layout.onRemoteChange(null);
    await vi.waitFor(() => expect(store.getState().workspaces[0]?.id).toBe("host-b"));
    store.setState({ workspaces: [{ ...hostB, title: "next account" }] });
    await layout.flushLayout();
    expect(put).toHaveBeenCalledTimes(2);
    expect(put.mock.calls[1][1]).toBe(2);
    expect(put.mock.calls[1][0]).not.toBe(oldJson);
  });

  it("does not retry old JSON or queue stale work after a conflict GET fails during account change", async () => {
    vi.useFakeTimers();
    const hostB = hostWorkspace("host-b", "s2", "asset-2", [terminal("terminal-b", "s2")]);
    const { layout, store } = await openTab();
    store.setState({ workspaces: [hostB], activeWorkspaceId: "host-b" });
    const conflictGet = deferred<LayoutDto>();
    const put = vi.mocked(layoutApi.put);
    put.mockResolvedValueOnce({ saved: false, revision: 0, conflict: true });
    vi.mocked(layoutApi.get).mockImplementationOnce(() => conflictGet.promise);
    const flushing = layout.flushLayout();
    const oldJson = put.mock.calls[0][0];
    await vi.waitFor(() => expect(layoutApi.get).toHaveBeenCalledTimes(1));

    layout.advanceLayoutAccount();
    store.setState({ workspaces: [], activeWorkspaceId: null, aiBoards: {} });
    conflictGet.reject(new Error("stale conflict GET failed"));
    await flushing;
    expect(put).toHaveBeenCalledTimes(1);

    server.revision = 2;
    server.data = JSON.stringify(persistedLayout([hostB]));
    layout.onRemoteChange(null);
    await vi.waitFor(() => expect(store.getState().workspaces[0]?.id).toBe("host-b"));
    store.setState({ workspaces: [{ ...hostB, title: "next account" }] });
    await layout.flushLayout();
    expect(put).toHaveBeenCalledTimes(2);
    expect(put.mock.calls[1][1]).toBe(2);
    expect(put.mock.calls[1][0]).not.toBe(oldJson);
    await vi.advanceTimersByTimeAsync(layout.LAYOUT_RETRY_MS * layout.LAYOUT_MAX_RETRY);
    expect(vi.getTimerCount()).toBe(0);
    expect(put.mock.calls.slice(1).every(([data]) => data !== oldJson)).toBe(true);
  });
});
