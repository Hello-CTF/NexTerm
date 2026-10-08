/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const server = vi.hoisted(() => ({
  revision: 0,
  data: null as string | null,
  putAlwaysConflicts: false,
  handlers: [] as Array<(payload: { revision?: number } | null) => void>,
}));

vi.mock("../../ipc/commands", () => ({
  layoutApi: {
    get: vi.fn(async () => ({
      revision: server.revision,
      updatedAt: 0,
      data: server.data === null ? null : JSON.parse(server.data),
    })),
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
  toasts: { text: string }[];
}

interface LayoutModule {
  startLayoutSync: () => void;
  flushLayout: () => Promise<void>;
  onRemoteChange: (payload: { revision?: number } | null) => void;
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

function panelTab(id: string, kind: AppTab["kind"], title: string): AppTab {
  return { id, kind, title, closable: true };
}

function workspace(tabs: AppTab[], activeTabId: string | null = tabs[tabs.length - 1]?.id ?? null): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "workspace",
    panes: [{ id: "pane", tabs, activeTabId }],
    activePaneId: "pane",
    splitRatio: 0.5,
    closable: true,
  };
}

function toolTabs(): AppTab[] {
  return [
    panelTab("settings", "settings", "设置"),
    panelTab("history", "history", "终端历史"),
    panelTab("background", "background", "后台会话"),
    panelTab("audit", "audit", "审计日志"),
  ];
}

function persistedLayout(workspaces: Workspace[]): PersistedLayout {
  return {
    v: 1,
    leftOpen: true,
    leftMode: "assets",
    rightOpen: true,
    leftWidth: 248,
    rightWidth: 352,
    workspaces,
    activeWorkspaceId: workspaces[0]?.id ?? null,
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
  layout.startLayoutSync();
  await layout.layoutBootstrapped;
  return { layout, store: store.useUi };
}

beforeEach(() => {
  server.revision = 0;
  server.data = null;
  server.putAlwaysConflicts = false;
  server.handlers.length = 0;
});

afterEach(() => {
  vi.resetModules();
  vi.clearAllMocks();
});

describe("history tab in the layout pane contract", () => {
  it("serialize → JSON → sanitize round-trip keeps history alongside background, audit and settings", async () => {
    const { layout, store } = await openTab();
    store.setState({ workspaces: [workspace(toolTabs(), "history")], activeWorkspaceId: "ws" });

    const dto = layout.serializeLayout(store.getState());
    expect(dto).not.toBeNull();
    const parsed = layout.sanitizeLayout(JSON.parse(JSON.stringify(dto)));
    expect(parsed).not.toBeNull();

    // 全局工具标签统一迁入「工具」工作区, 搬空的原工作区不再保留
    expect(parsed?.workspaces).toHaveLength(1);
    expect(parsed?.workspaces[0].kind).toBe("tools");
    const pane = parsed?.workspaces[0].panes[0];
    expect(pane?.tabs.map((t) => t.kind)).toEqual(["settings", "history", "background", "audit"]);
    expect(pane?.activeTabId).toBe("history");
    const history = pane?.tabs.find((t) => t.id === "history");
    expect(history?.title).toBe("终端历史");
    expect(history?.closable).toBe(true);
  });

  it("sanitizeLayout keeps a pane whose only tab is history", async () => {
    const { layout } = await openTab();
    const parsed = layout.sanitizeLayout(
      persistedLayout([workspace([panelTab("history", "history", "终端历史")], "history")]),
    );
    expect(parsed?.workspaces).toHaveLength(1);
    expect(parsed?.workspaces[0].kind).toBe("tools");
    expect(parsed?.workspaces[0].panes[0].tabs.map((t) => t.kind)).toEqual(["history"]);
    expect(parsed?.workspaces[0].panes[0].activeTabId).toBe("history");
    expect(parsed?.activeWorkspaceId).toBe("ws-tools");
  });

  it("sanitizeLayout is idempotent for already-migrated layouts", async () => {
    const { layout } = await openTab();
    const once = layout.sanitizeLayout(persistedLayout([workspace(toolTabs(), "history")]));
    const twice = layout.sanitizeLayout(JSON.parse(JSON.stringify(once)));
    expect(twice).toEqual(once);
  });

  it("boot application preserves the history tab and its active position", async () => {
    server.data = JSON.stringify(persistedLayout([workspace(toolTabs(), "history")]));
    const { store } = await openTab();

    const pane = store.getState().workspaces.find((w) => w.kind === "tools")?.panes[0];
    expect(pane?.tabs.map((t) => t.kind)).toEqual(["settings", "history", "background", "audit"]);
    expect(pane?.activeTabId).toBe("history");
    expect(findTab(store.getState(), "history")?.title).toBe("终端历史");
  });

  it("remote LAYOUT_CHANGED application preserves the history tab", async () => {
    server.data = JSON.stringify(persistedLayout([workspace([panelTab("settings", "settings", "设置")])]));
    const { layout, store } = await openTab();
    expect(findTab(store.getState(), "history")).toBeUndefined();

    server.revision = 1;
    server.data = JSON.stringify(persistedLayout([workspace(toolTabs(), "history")]));
    layout.onRemoteChange({ revision: 1 });

    await vi.waitFor(() => {
      expect(findTab(store.getState(), "history")?.title).toBe("终端历史");
    });
    const pane = store.getState().workspaces.find((w) => w.kind === "tools")?.panes[0];
    expect(pane?.tabs.map((t) => t.kind)).toEqual(["settings", "history", "background", "audit"]);
    expect(pane?.activeTabId).toBe("history");
  });

  it("conflict layout application preserves the history tab and refreshes to the server copy", async () => {
    server.data = JSON.stringify(persistedLayout([workspace([panelTab("settings", "settings", "设置")])]));
    const { layout, store } = await openTab();

    server.revision = 7;
    server.data = JSON.stringify(persistedLayout([workspace(toolTabs(), "history")]));
    server.putAlwaysConflicts = true;
    store.setState({ leftWidth: 300 });
    await layout.flushLayout();

    const state = store.getState();
    const pane = state.workspaces.find((w) => w.kind === "tools")?.panes[0];
    expect(pane?.tabs.map((t) => t.kind)).toEqual(["settings", "history", "background", "audit"]);
    expect(pane?.activeTabId).toBe("history");
    expect(state.leftWidth).toBe(248);
    expect(state.toasts.some((t) => t.text.includes("布局已在其他设备上更新"))).toBe(true);
  });
});
