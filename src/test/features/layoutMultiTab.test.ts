/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const server = vi.hoisted(() => ({
  revision: 0,
  data: null as string | null,
  getCalls: 0,
  putCalls: 0,
  holdNextGet: null as { revision: number; updatedAt: number; data: unknown } | null,
  heldRelease: null as (() => void) | null,
  handlers: [] as Array<(payload: { revision?: number } | null) => void>,
}));

vi.mock("../../ipc/commands", () => ({
  layoutApi: {
    get: vi.fn(async () => {
      server.getCalls += 1;
      if (server.holdNextGet !== null) {
        const snapshot = server.holdNextGet;
        server.holdNextGet = null;
        await new Promise<void>((resolve) => {
          server.heldRelease = resolve;
        });
        return snapshot;
      }
      return {
        revision: server.revision,
        updatedAt: 0,
        data: server.data === null ? null : JSON.parse(server.data),
      };
    }),
    put: vi.fn(async (data: string, revision: number) => {
      server.putCalls += 1;
      if (revision !== server.revision) {
        return { saved: false, revision: server.revision, conflict: true };
      }
      server.revision = revision + 1;
      server.data = data;
      const event = { revision: server.revision };
      setTimeout(() => {
        for (const handler of [...server.handlers]) handler(event);
      }, 0);
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
import type { Workspace } from "../../app/store";

function workspace(id: string, title: string): Workspace {
  return {
    id,
    kind: "session",
    title,
    panes: [{ id: `${id}-pane`, tabs: [], activeTabId: null }],
    activePaneId: `${id}-pane`,
    splitRatio: 0.5,
    closable: true,
  };
}

function layout(leftWidth: number, title: string): PersistedLayout {
  return {
    v: 1,
    leftOpen: true,
    leftMode: "assets",
    rightOpen: true,
    leftWidth,
    rightWidth: 352,
    workspaces: [workspace("ws", title)],
    activeWorkspaceId: "ws",
  };
}

interface LayoutModule {
  startLayoutSync: () => void;
  flushLayout: () => Promise<void>;
  onRemoteChange: (payload: { revision?: number } | null) => void;
  layoutBootstrapped: Promise<void>;
}

interface StoreModule {
  useUi: {
    getState: () => { leftWidth: number; workspaces: Workspace[] };
    setState: (partial: Record<string, unknown>) => void;
  };
}

async function openTab(): Promise<{
  layout: LayoutModule;
  store: StoreModule["useUi"];
}> {
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
  server.getCalls = 0;
  server.putCalls = 0;
  server.holdNextGet = null;
  server.heldRelease = null;
  server.handlers.length = 0;
});

afterEach(() => {
  vi.resetModules();
  vi.clearAllMocks();
});

describe("两个浏览器标签页的 layout://changed 同步", () => {
  it("一个标签保存后另一个标签实时拉取并按 revision 去重", async () => {
    const tabA = await openTab();
    server.data = JSON.stringify(layout(248, "initial"));
    const tabB = await openTab();
    expect(server.getCalls).toBe(2);
    expect(server.handlers).toHaveLength(2);

    tabA.store.setState({ leftWidth: 300, workspaces: [workspace("ws", "from-a")] });
    await tabA.layout.flushLayout();
    expect(server.putCalls).toBe(1);
    expect(server.revision).toBe(1);

    await vi.waitFor(() => {
      expect(tabB.store.getState().leftWidth).toBe(300);
      expect(tabB.store.getState().workspaces[0]?.title).toBe("from-a");
    });
    expect(server.getCalls).toBe(3);

    for (const handler of [...server.handlers]) handler({ revision: 1 });
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(server.getCalls).toBe(3);

    tabB.layout.onRemoteChange(null);
    await vi.waitFor(() => expect(server.getCalls).toBe(4));
  });

  it("resync（无 payload）强制刷新，即使 revision 未变", async () => {
    server.data = JSON.stringify(layout(260, "server"));
    const tabA = await openTab();
    expect(tabA.store.getState().leftWidth).toBe(260);

    server.data = JSON.stringify(layout(280, "server-moved-on"));
    tabA.layout.onRemoteChange(null);
    await vi.waitFor(() => expect(tabA.store.getState().leftWidth).toBe(280));
  });

  it("拉取在途期间连续 5 个 revision 事件不丢失，结束后追到最新", async () => {
    server.data = JSON.stringify(layout(248, "v0"));
    const tabA = await openTab();
    const tabB = await openTab();
    expect(server.getCalls).toBe(2);

    server.holdNextGet = { revision: 1, updatedAt: 0, data: layout(249, "v1") };
    for (let i = 1; i <= 5; i += 1) {
      tabA.store.setState({ leftWidth: 248 + i, workspaces: [workspace("ws", `v${i}`)] });
      await tabA.layout.flushLayout();
    }
    expect(server.revision).toBe(5);
    await vi.waitFor(() => expect(server.getCalls).toBe(3));
    expect(tabB.store.getState().leftWidth).toBe(248);

    server.heldRelease?.();
    await vi.waitFor(() => {
      expect(tabB.store.getState().leftWidth).toBe(253);
      expect(tabB.store.getState().workspaces[0]?.title).toBe("v5");
    });
    expect(server.getCalls).toBe(4);

    for (const handler of [...server.handlers]) handler({ revision: 5 });
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(server.getCalls).toBe(4);
  });
});
