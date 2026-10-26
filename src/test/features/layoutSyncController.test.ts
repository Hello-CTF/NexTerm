/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { paneIdsInOrder } from "../../app/splitTree";
import { sharedLayoutKey, type PersistedLayout } from "../../app/layoutMerge";
import type { AiBoard, AppTab, Pane, SqlDraft, Workspace } from "../../app/store";
import { resetUnreadTabs, setTabUnread } from "../../app/unreadStore";

const server = vi.hoisted(() => ({
  revision: 0,
  data: null as string | null,
  putCalls: 0,
  getCalls: 0,
  putAlwaysConflicts: false,
  holdNextPut: false,
  heldPutRelease: null as (() => void) | null,
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    layoutApi: {
      get: vi.fn(async () => {
        server.getCalls += 1;
        return {
          revision: server.revision,
          updatedAt: server.revision,
          data: server.data === null ? null : JSON.parse(server.data),
        };
      }),
      put: vi.fn(async (data: string, revision: number) => {
        server.putCalls += 1;
        if (server.holdNextPut) {
          server.holdNextPut = false;
          await new Promise<void>((resolve) => {
            server.heldPutRelease = resolve;
          });
          server.heldPutRelease = null;
        }
        if (server.putAlwaysConflicts || revision !== server.revision) {
          return { saved: false, revision: server.revision, conflict: true };
        }
        server.revision = revision + 1;
        server.data = data;
        return { saved: true, revision: server.revision, conflict: false };
      }),
    },
    assetApi: { list: vi.fn(async () => []) },
    terminalApi: { listLive: vi.fn(async () => []) },
  });
});
vi.mock("../../ipc/events", () => ({ listenEvent: vi.fn(async () => () => {}) }));

interface Client {
  layout: typeof import("../../app/layout");
  store: typeof import("../../app/store").useUi;
  editor: typeof import("../../features/files/editorGuards");
}

const clients: Client[] = [];

function terminal(id: string, sessionId = "session"): AppTab {
  return { id, kind: "terminal", title: id, sessionId, tabId: `kernel-${id}`, closable: true };
}

function file(id: string, path: string): AppTab {
  return { id, kind: "files", title: id, sessionId: "session", path, closable: true };
}

function pane(id: string, tabs: AppTab[], activeTabId = tabs[0]?.id ?? null): Pane {
  return { id, tabs, activeTabId };
}

function workspace(id: string, assetId: string, panes: Pane[]): Workspace {
  return {
    id,
    kind: "session",
    title: id,
    sessionId: "session",
    assetId,
    panes,
    activePaneId: panes[0]?.id ?? id,
    closable: true,
  };
}

function database(id: string): Workspace {
  return {
    id,
    kind: "db",
    title: id,
    connId: `conn-${id}`,
    dbKind: "mysql",
    assetId: `asset-${id}`,
    panes: [pane(`${id}-pane`, [])],
    activePaneId: `${id}-pane`,
    closable: true,
  };
}

function layout(workspaces: Workspace[], aiBoards: Record<string, AiBoard> = {}): PersistedLayout {
  return {
    v: 4,
    leftOpen: true,
    leftMode: "assets",
    rightOpen: true,
    leftWidth: 248,
    rightWidth: 352,
    workspaces,
    activeWorkspaceId: workspaces[0]?.id ?? null,
    aiBoards,
  };
}

function serverLayout(): PersistedLayout {
  if (server.data === null) throw new Error("expected a server layout");
  return JSON.parse(server.data) as PersistedLayout;
}

async function openClient(): Promise<Client> {
  window.localStorage.clear();
  vi.resetModules();
  const layoutModule = await import("../../app/layout");
  const { useUi } = await import("../../app/store");
  const editor = await import("../../features/files/editorGuards");
  const client = { layout: layoutModule, store: useUi, editor };
  clients.push(client);
  layoutModule.startLayoutSync();
  await layoutModule.layoutBootstrapped;
  return client;
}

function addTab(client: Client, tab: AppTab, workspaceId: string, paneId: string): void {
  client.store.setState((state) => ({
    workspaces: state.workspaces.map((item) =>
      item.id !== workspaceId
        ? item
        : {
            ...item,
            panes: item.panes.map((target) =>
              target.id === paneId ? { ...target, tabs: [...target.tabs, tab] } : target,
            ),
          },
    ),
  }));
}

function removeTab(client: Client, tabId: string): void {
  client.store.setState((state) => ({
    workspaces: state.workspaces.map((item) => ({
      ...item,
      panes: item.panes.map((target) => ({
        ...target,
        tabs: target.tabs.filter((tab) => tab.id !== tabId),
      })),
    })),
  }));
}

function renameTab(client: Client, tabId: string, title: string): void {
  client.store.setState((state) => ({
    workspaces: state.workspaces.map((item) => ({
      ...item,
      panes: item.panes.map((target) => ({
        ...target,
        tabs: target.tabs.map((tab) => (tab.id === tabId ? { ...tab, title } : tab)),
      })),
    })),
  }));
}

function allTabIds(value: PersistedLayout): string[] {
  return value.workspaces.flatMap((item) => item.panes.flatMap((target) => target.tabs.map((tab) => tab.id)));
}

function sqlDraft(): SqlDraft {
  return {
    text: "SELECT 1",
    selection: { from: 0, to: 8 },
    schema: "public",
    connId: "conn-db",
    dbKind: "mysql",
  };
}

beforeEach(() => {
  server.revision = 0;
  server.data = null;
  server.putCalls = 0;
  server.getCalls = 0;
  server.putAlwaysConflicts = false;
  server.holdNextPut = false;
  server.heldPutRelease = null;
  window.localStorage.clear();
});

afterEach(() => {
  server.heldPutRelease?.();
  server.heldPutRelease = null;
  for (const client of clients) {
    client.editor.setFileEditorDirty("session", "/dirty.txt", false);
    client.layout.resetLayoutSyncForTest();
  }
  clients.length = 0;
  vi.clearAllMocks();
  window.localStorage.clear();
});

describe("two-client layout controller", () => {
  it("keeps focus and sidebars device-local without writing a focus-only revision", async () => {
    const base = layout([
      workspace("ws-a", "asset-a", [pane("pane-a", [terminal("a1"), terminal("a2")], "a1")]),
      workspace("ws-b", "asset-b", [pane("pane-b", [terminal("b1")])]),
    ]);
    server.revision = 1;
    server.data = JSON.stringify(base);
    const a = await openClient();
    const b = await openClient();
    const puts = server.putCalls;

    a.store.getState().setActiveWorkspace("ws-b");
    a.store.getState().setActivePane("pane-a", "ws-a");
    a.store.setState((state) => ({
      leftOpen: false,
      leftMode: "files",
      rightOpen: false,
      leftWidth: 400,
      rightWidth: 600,
      workspaces: state.workspaces.map((item) =>
        item.id === "ws-a"
          ? { ...item, panes: item.panes.map((target) => ({ ...target, activeTabId: "a2" })) }
          : item,
      ),
    }));
    await a.layout.flushLayout();
    expect(server.putCalls).toBe(puts);

    b.store.setState((state) => ({
      workspaces: state.workspaces.map((item) => (item.id === "ws-a" ? { ...item, title: "shared edit" } : item)),
    }));
    await b.layout.flushLayout();
    a.layout.onRemoteChange({ revision: server.revision });
    await vi.waitFor(() => expect(a.store.getState().workspaces[0].title).toBe("shared edit"));

    expect(a.store.getState().activeWorkspaceId).toBe("ws-b");
    expect(a.store.getState().workspaces[0].panes[0].activeTabId).toBe("a2");
    expect(a.store.getState()).toMatchObject({
      leftOpen: false,
      leftMode: "files",
      rightOpen: false,
      leftWidth: 400,
      rightWidth: 600,
    });
    expect(server.revision).toBe(2);
  });

  it("preserves a local debounce addition when a remote event arrives first", async () => {
    const base = layout([workspace("ws", "asset", [pane("pane", [terminal("base")])])]);
    server.revision = 1;
    server.data = JSON.stringify(base);
    const a = await openClient();
    const b = await openClient();

    addTab(a, terminal("local-a"), "ws", "pane");
    addTab(b, terminal("remote-b"), "ws", "pane");
    await b.layout.flushLayout();
    a.layout.onRemoteChange({ revision: server.revision });
    await vi.waitFor(() => expect(allTabIds(a.layout.serializeLayout(a.store.getState()) as PersistedLayout)).toContain("remote-b"));
    await a.layout.flushLayout();

    expect(allTabIds(serverLayout()).sort()).toEqual(["base", "local-a", "remote-b"]);
  });

  it("merges a remote event received while the local PUT is in flight", async () => {
    const base = layout([workspace("ws", "asset", [pane("pane", [terminal("base")])])]);
    server.revision = 1;
    server.data = JSON.stringify(base);
    const a = await openClient();
    const b = await openClient();
    addTab(a, terminal("tab-a"), "ws", "pane");
    addTab(b, terminal("tab-b"), "ws", "pane");
    server.holdNextPut = true;

    const writing = a.layout.flushLayout();
    await vi.waitFor(() => expect(server.heldPutRelease).not.toBeNull());
    await b.layout.flushLayout();
    a.layout.onRemoteChange({ revision: server.revision });
    server.heldPutRelease?.();
    await writing;

    expect(allTabIds(serverLayout()).sort()).toEqual(["base", "tab-a", "tab-b"]);
  });

  it("retains concurrent additions through a real PUT conflict and converges both clients", async () => {
    const base = layout([workspace("ws", "asset", [pane("pane", [terminal("base")])])]);
    server.revision = 1;
    server.data = JSON.stringify(base);
    const a = await openClient();
    const b = await openClient();
    addTab(a, terminal("tab-a"), "ws", "pane");
    addTab(b, terminal("tab-b"), "ws", "pane");

    await Promise.all([a.layout.flushLayout(), b.layout.flushLayout()]);
    expect(allTabIds(serverLayout()).sort()).toEqual(["base", "tab-a", "tab-b"]);

    const loser = allTabIds(a.layout.serializeLayout(a.store.getState()) as PersistedLayout).includes("tab-b") ? b : a;
    loser.layout.onRemoteChange({ revision: server.revision });
    await vi.waitFor(() => {
      const current = loser.layout.serializeLayout(loser.store.getState());
      expect(current && sharedLayoutKey(current)).toBe(sharedLayoutKey(serverLayout()));
    });
  });

  it("lets close win over an edit, and lets a remote delete win over a clean local edit", async () => {
    const base = layout([workspace("ws", "asset", [pane("pane", [terminal("keep"), terminal("gone")])])]);
    server.revision = 1;
    server.data = JSON.stringify(base);
    const a = await openClient();
    const b = await openClient();

    removeTab(a, "gone");
    renameTab(b, "gone", "edited remotely");
    await b.layout.flushLayout();
    a.layout.onRemoteChange({ revision: server.revision });
    await vi.waitFor(() => expect(allTabIds(a.layout.serializeLayout(a.store.getState()) as PersistedLayout)).not.toContain("gone"));
    await a.layout.flushLayout();
    expect(allTabIds(serverLayout())).toEqual(["keep"]);

    renameTab(a, "keep", "clean local edit");
    b.store.setState({ workspaces: [] });
    await b.layout.flushLayout();
    a.layout.onRemoteChange({ revision: server.revision });
    await vi.waitFor(() => expect(a.store.getState().workspaces).toEqual([]));
    expect(serverLayout().workspaces).toEqual([]);
  });

  it("prunes broadcast targets when a remote layout deletes their tabs", async () => {
    const base = layout([workspace("ws", "asset", [pane("pane", [terminal("keep"), terminal("gone")])])]);
    server.revision = 1;
    server.data = JSON.stringify(base);
    const a = await openClient();
    const b = await openClient();
    a.store.getState().setBroadcast({
      origin: { workspaceId: "ws", storeTabId: "keep" },
      targetIds: ["keep", "gone"],
    });
    const generation = a.store.getState().broadcast?.generation ?? 0;

    removeTab(b, "gone");
    await b.layout.flushLayout();
    a.layout.onRemoteChange({ revision: server.revision });
    await vi.waitFor(() => {
      expect(allTabIds(a.layout.serializeLayout(a.store.getState()) as PersistedLayout)).toEqual(["keep"]);
      expect(a.store.getState().broadcast?.targetIds).toEqual(["keep"]);
    });
    expect(a.store.getState().broadcast?.generation ?? 0).toBeGreaterThan(generation);
  });

  it("maps identical assets with different random IDs into one workspace without losing a pane", async () => {
    const a = await openClient();
    const b = await openClient();
    const local = workspace("ws-a", "asset", [
      pane("pane-a1", [terminal("a1")]),
      pane("pane-a2", [terminal("a2")]),
    ]);
    const remote = workspace("ws-b", "asset", [
      pane("pane-b1", [terminal("b1")]),
      pane("pane-b2", [terminal("b2")]),
    ]);
    a.store.setState({
      workspaces: [local],
      activeWorkspaceId: local.id,
      aiBoards: { [local.id]: { tabs: [{ id: "ai-a", title: "A", conversationId: "conv-a" }], activeTabId: "ai-a" } },
    });
    b.store.setState({
      workspaces: [remote],
      activeWorkspaceId: remote.id,
      aiBoards: { [remote.id]: { tabs: [{ id: "ai-b", title: "B", conversationId: "conv-b" }], activeTabId: "ai-b" } },
    });

    await Promise.all([a.layout.flushLayout(), b.layout.flushLayout()]);
    const saved = serverLayout();
    expect(saved.workspaces).toHaveLength(1);
    // The first panes are the same pane opened twice and merge; the second panes are different
    // splits from each device and both stay in the tree.
    expect(saved.workspaces[0].panes).toHaveLength(3);
    expect(paneIdsInOrder(saved.workspaces[0].layoutTree!).sort()).toEqual(
      saved.workspaces[0].panes.map((item) => item.id).sort(),
    );
    expect(allTabIds(saved).sort()).toEqual(["a1", "a2", "b1", "b2"]);
    expect(Object.keys(saved.aiBoards)).toEqual([saved.workspaces[0].id]);
    expect(saved.aiBoards[saved.workspaces[0].id].tabs.map((tab) => tab.id).sort()).toEqual(["ai-a", "ai-b"]);
  });

  it("reads v2, rewrites it as v4, and protects dirty files and SQL drafts from remote close", async () => {
    const files = workspace("files", "asset-files", [pane("files-pane", [file("clean", "/clean.txt"), file("dirty", "/dirty.txt")])]);
    const db = database("db");
    const base = layout([files, db]);
    base.v = 2;
    server.revision = 1;
    server.data = JSON.stringify(base);
    const a = await openClient();
    const b = await openClient();
    a.editor.setFileEditorDirty("session", "/dirty.txt", true);
    a.store.getState().updateSqlDraft("db", sqlDraft());
    await a.layout.flushLayout();
    // A legacy document is rewritten in the current format on first read.
    expect(server.putCalls).toBe(1);
    expect(serverLayout().v).toBe(4);

    b.store.setState({ workspaces: [], aiBoards: {} });
    await b.layout.flushLayout();
    expect(serverLayout().v).toBe(4);
    a.layout.onRemoteChange({ revision: server.revision });
    await vi.waitFor(() => expect(a.store.getState().workspaces.map((item) => item.id).sort()).toEqual(["db", "files"]));

    expect(a.store.getState().workspaces.find((item) => item.id === "files")?.panes[0].tabs.map((tab) => tab.id)).toEqual(["dirty"]);
    expect(a.store.getState().getWorkspaceRuntime("db")?.sqlDraft).toEqual(sqlDraft());
    await a.layout.flushLayout();
    expect(serverLayout().workspaces.map((item) => item.id).sort()).toEqual(["db", "files"]);
  });

  it("bounds repeated conflicts without dropping the merged local changes", async () => {
    vi.useFakeTimers();
    const base = layout([workspace("ws", "asset", [pane("pane", [terminal("base")])])]);
    server.revision = 1;
    server.data = JSON.stringify(base);
    const a = await openClient();
    addTab(a, terminal("local"), "ws", "pane");
    server.revision = 2;
    server.data = JSON.stringify(layout([workspace("ws", "asset", [pane("pane", [terminal("base"), terminal("remote")])])]));
    server.putAlwaysConflicts = true;

    await a.layout.flushLayout();
    expect(server.putCalls).toBe(3);
    expect(allTabIds(a.layout.serializeLayout(a.store.getState()) as PersistedLayout).sort()).toEqual([
      "base",
      "local",
      "remote",
    ]);
    await vi.advanceTimersByTimeAsync(a.layout.LAYOUT_SAVE_DEBOUNCE_MS);
    expect(server.putCalls).toBe(3);
  });

  it("does not serialize toast, unread, session-runtime, or AI draft updates into shared writes", async () => {
    vi.useFakeTimers();
    resetUnreadTabs();
    const base = layout(
      [workspace("ws", "asset", [pane("pane", [terminal("base")])])],
      { ws: { tabs: [{ id: "ai", title: "AI" }], activeTabId: "ai" } },
    );
    server.revision = 1;
    server.data = JSON.stringify(base);
    const a = await openClient();
    a.store.getState().pushToast("info", "local only");
    setTabUnread("base", true);
    a.store.getState().updateTab("base", { shell: "posix" });
    a.store.getState().updateSqlDraft("ws", sqlDraft());
    a.store.setState({
      aiBoards: {
        ws: { tabs: [{ id: "ai", title: "AI", draft: "private" }], activeTabId: "ai" },
      },
    });
    await vi.advanceTimersByTimeAsync(a.layout.LAYOUT_SAVE_DEBOUNCE_MS);
    await a.layout.flushLayout();
    expect(server.putCalls).toBe(0);
    resetUnreadTabs();
  });
});

describe("split trees across two clients", () => {
  function treeOf(client: Client, workspaceId = "ws") {
    const serialized = client.layout.serializeLayout(client.store.getState()) as PersistedLayout;
    return serialized.workspaces.find((item) => item.id === workspaceId)?.layoutTree;
  }

  async function pull(client: Client): Promise<void> {
    const before = server.getCalls;
    client.layout.onRemoteChange({ revision: server.revision });
    await vi.waitFor(() => expect(server.getCalls).toBeGreaterThan(before));
    await vi.waitFor(() => expect(sharedLayoutKey(client.layout.serializeLayout(client.store.getState()))).toBe(sharedLayoutKey(serverLayout())));
  }

  it("converges concurrent splits of different panes into one tree holding every pane", async () => {
    const base = layout([workspace("ws", "asset", [pane("p0", [terminal("base")])])]);
    server.revision = 1;
    server.data = JSON.stringify(base);
    const a = await openClient();
    const b = await openClient();

    const right = a.store.getState().splitPane("right", "p0", "ws")!;
    a.store.getState().addTab(terminal("from-a"), right);
    const down = b.store.getState().splitPane("down", "p0", "ws")!;
    b.store.getState().addTab(terminal("from-b"), down);

    await a.layout.flushLayout();
    await b.layout.flushLayout();
    await pull(a);

    const saved = serverLayout();
    const tree = saved.workspaces[0].layoutTree!;
    expect(paneIdsInOrder(tree).sort()).toEqual(["p0", down, right].sort());
    expect(allTabIds(saved).sort()).toEqual(["base", "from-a", "from-b"]);
    expect(treeOf(a)).toEqual(treeOf(b));
    expect(treeOf(a)).toEqual(tree);
  });

  it("carries a divider moved on one client to the other without reshaping", async () => {
    const base = layout([workspace("ws", "asset", [pane("p0", [terminal("base")])])]);
    server.revision = 1;
    server.data = JSON.stringify(base);
    const a = await openClient();
    const b = await openClient();
    const right = a.store.getState().splitPane("right", "p0", "ws")!;
    a.store.getState().addTab(terminal("side"), right);
    await a.layout.flushLayout();
    await pull(b);

    const splitId = treeOf(b)!.id;
    b.store.getState().setSplitRatio(splitId, 0.3, "ws");
    await b.layout.flushLayout();
    await pull(a);

    const tree = treeOf(a)!;
    expect(tree.type === "split" && tree.ratio).toBe(0.3);
    expect(paneIdsInOrder(tree)).toEqual(["p0", right]);
  });

  it("resets a legacy split layout to one pane per workspace, says so once, and rewrites it as v4", async () => {
    const legacy = {
      ...layout([
        { ...workspace("ws", "asset", [pane("upper", [terminal("t1")]), pane("lower", [terminal("t2")], "t2")]), title: "prod" },
        { ...workspace("solo", "asset-solo", [pane("only", [terminal("t3")])]), title: "solo" },
      ]),
      v: 3,
    };
    server.revision = 1;
    server.data = JSON.stringify(legacy);
    const a = await openClient();
    const notice = await import("../../app/layoutResetNotice");

    const ws = a.store.getState().workspaces.find((item) => item.id === "ws")!;
    expect(ws.panes.map((item) => item.id)).toEqual(["upper"]);
    expect(ws.panes[0].tabs.map((tab) => tab.id)).toEqual(["t1", "t2"]);
    expect(notice.layoutResetNotice()).toEqual(["prod"]);

    await a.layout.flushLayout();
    expect(serverLayout().v).toBe(4);
    expect(allTabIds(serverLayout()).sort()).toEqual(["t1", "t2", "t3"]);

    notice.dismissLayoutResetNotice();
    expect(notice.layoutResetNotice()).toBeNull();
    const b = await openClient();
    const freshNotice = await import("../../app/layoutResetNotice");
    expect(freshNotice.layoutResetNotice()).toBeNull();
    expect(b.store.getState().workspaces.find((item) => item.id === "ws")?.panes).toHaveLength(1);
  });

  it("neither reads nor overwrites a layout saved in a newer format", async () => {
    server.revision = 4;
    server.data = JSON.stringify({ ...layout([workspace("ws", "asset", [pane("p0", [terminal("future")])])]), v: 5 });
    const a = await openClient();
    const notice = await import("../../app/layoutResetNotice");
    expect(a.store.getState().workspaces).toEqual([]);
    expect(notice.newerLayoutNotice()).toBe(5);

    a.store.setState({ workspaces: [workspace("local", "asset-local", [pane("lp", [terminal("mine")])])] });
    await a.layout.flushLayout();
    expect(server.putCalls).toBe(0);
    expect(serverLayout().v).toBe(5);

    // Once the account layout is readable again, sync resumes and the notice clears.
    server.revision = 5;
    server.data = JSON.stringify(layout([workspace("ws", "asset", [pane("p0", [terminal("now")])])]));
    a.layout.onRemoteChange({ revision: 5 });
    await vi.waitFor(() => expect(notice.newerLayoutNotice()).toBeNull());
  });
});
