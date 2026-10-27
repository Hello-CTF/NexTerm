/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, deferred, flush, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  roots: vi.fn(),
  handlers: new Map<string, (payload: unknown) => void>(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    fsApi: { ...actual.fsApi, list: mocks.list, roots: mocks.roots },
    sessionApi: { connect: vi.fn(), connectLocal: vi.fn() },
    terminalApi: { write: vi.fn() },
  };
});

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    listenEvent: (topic: string, handler: (payload: unknown) => void) => {
      mocks.handlers.set(topic, handler);
      return Promise.resolve(() => {});
    },
  };
});

import { SessionPanel } from "../../features/session-panel/SessionPanel";
import { resetSessionPanelForTest, useSessionPanel } from "../../features/session-panel/sessionPanelStore";
import { resetCwdFollowForTest } from "../../features/files/cwdFollow";
import { useUi, type AppTab, type Workspace } from "../../app/store";
import type { SessionInfo } from "../../ipc/commands";
import type { FileEntryDto } from "../../ipc/types";

let mounted: MountedView | undefined;

function terminal(id: string, terminalId: string, sessionId: string, extra: Partial<AppTab> = {}): AppTab {
  return { id, kind: "terminal", title: id, sessionId, tabId: terminalId, closable: true, ...extra };
}

function workspace(id: string, title: string, sessionId: string | undefined, tabs: AppTab[]): Workspace {
  return {
    id,
    kind: "session",
    title,
    sessionId,
    panes: [{ id: `${id}-p`, tabs, activeTabId: tabs[0]?.id ?? null }],
    activePaneId: `${id}-p`,
    closable: true,
  };
}

function session(id: string, name: string, kind = "ssh", status: SessionInfo["status"] = "connected"): SessionInfo {
  return { id, assetId: null, name, kind, status, tabs: [], createdAt: 0 };
}

function seedTwoHosts(): void {
  useUi.setState({
    workspaces: [
      workspace("ws1", "web-01", "s1", [terminal("t1", "k1", "s1")]),
      workspace("ws2", "db-01", "s2", [terminal("t2", "k2", "s2")]),
    ],
    activeWorkspaceId: "ws1",
    sessions: [session("s1", "web-01"), session("s2", "db-01")],
  });
}

function mountPanel(overlay = false): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(SessionPanel, { overlay, onClose: () => useSessionPanel.getState().setOpen(false) }),
    ),
  );
  mounted = view;
  return view;
}

function emitCwd(terminalId: string, version: number, cwd: string): void {
  const handler = mocks.handlers.get("terminal://control");
  if (!handler) throw new Error("terminal://control handler not registered");
  act(() => handler({ tabId: terminalId, version, cwd }));
}

function listCalls(): [string, string][] {
  return mocks.list.mock.calls.map((c) => [String(c[0]), String(c[1])]);
}

// The cwd throttle updates from a timer, which an enclosing act() would hold back; poll in short act() steps instead.
async function waitUntil(predicate: () => boolean, timeoutMs = 2000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!predicate()) {
    if (Date.now() > deadline) throw new Error("condition not met in time");
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 25));
    });
  }
  // Let the listing that satisfied the condition settle into the tree.
  await flush();
  await flush();
}

async function waitForList(entry: [string, string]): Promise<void> {
  try {
    await waitUntil(() => listCalls().some(([sid, dir]) => sid === entry[0] && dir === entry[1]));
  } catch {
    expect(listCalls()).toContainEqual(entry);
  }
}

function button(label: string): HTMLButtonElement {
  const el = mounted?.container.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`);
  if (!el) throw new Error(`button ${label} not found`);
  return el;
}

function text(): string {
  return mounted?.container.textContent ?? "";
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.handlers.clear();
  window.localStorage.clear();
  resetCwdFollowForTest();
  resetSessionPanelForTest();
  mocks.list.mockResolvedValue([]);
  mocks.roots.mockResolvedValue({ pathStyle: "posix", roots: [], homePath: null, supported: false });
  useUi.setState({ workspaces: [], activeWorkspaceId: null, sessions: [], leftOpen: false, toasts: [] });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.useRealTimers();
});

describe("session side panel context", () => {
  it("lists the focused terminal's host and switches to the new host when focus moves", async () => {
    seedTwoHosts();
    mountPanel();
    await flush();
    emitCwd("k1", 1, "/srv/app");
    emitCwd("k2", 1, "/var/lib/db");
    await waitForList(["s1", "/srv/app"]);
    expect(listCalls().some(([sid]) => sid === "s2")).toBe(false);
    expect(mounted!.container.querySelector('[data-testid="session-panel-context"]')?.textContent).toBe(
      "web-01 · /srv/app",
    );

    act(() => useUi.setState({ activeWorkspaceId: "ws2" }));

    await waitForList(["s2", "/var/lib/db"]);
    expect(mounted!.container.querySelector('[data-testid="session-panel-context"]')?.textContent).toBe(
      "db-01 · /var/lib/db",
    );
  });

  it("keeps the last terminal while a non-terminal tab has focus", async () => {
    seedTwoHosts();
    mountPanel();
    await flush();
    emitCwd("k1", 1, "/srv/app");
    await waitForList(["s1", "/srv/app"]);

    act(() =>
      useUi.setState({
        workspaces: [
          ...useUi.getState().workspaces,
          { ...workspace("tools", "工具", undefined, [{ id: "set", kind: "settings", title: "设置", closable: true }]), kind: "tools" },
        ],
        activeWorkspaceId: "tools",
      }),
    );
    await flush();

    expect(text()).toContain("上一个终端");
    expect(mounted!.container.querySelector('[data-testid="session-panel-context"]')?.textContent).toBe(
      "web-01 · /srv/app",
    );
    expect(text()).not.toContain("没有聚焦的终端");
  });

  it("lock pins the current host until unlocked", async () => {
    seedTwoHosts();
    mountPanel();
    await flush();
    emitCwd("k1", 1, "/srv/app");
    emitCwd("k2", 1, "/var/lib/db");
    await waitForList(["s1", "/srv/app"]);

    click(button("锁定"));
    expect(button("锁定").getAttribute("aria-pressed")).toBe("true");
    act(() => useUi.setState({ activeWorkspaceId: "ws2" }));
    await flush();
    expect(listCalls().some(([sid]) => sid === "s2")).toBe(false);
    expect(text()).toContain("web-01");

    click(button("锁定"));
    expect(button("锁定").getAttribute("aria-pressed")).toBe("false");
    await waitForList(["s2", "/var/lib/db"]);
  });

  it("offers to unlock when the locked terminal closes", async () => {
    seedTwoHosts();
    mountPanel();
    await flush();
    click(button("锁定"));
    act(() =>
      useUi.setState({
        workspaces: useUi.getState().workspaces.filter((w) => w.id !== "ws1"),
        activeWorkspaceId: "ws2",
      }),
    );
    await flush();
    expect(text()).toContain("锁定的会话已关闭");

    const unlock = Array.from(mounted!.container.querySelectorAll("button")).find((b) => b.textContent === "解除锁定");
    click(unlock!);
    await waitUntil(() => listCalls().some(([sid]) => sid === "s2"));
  });
});

describe("session side panel context edge cases", () => {
  function entry(name: string, dir: string): FileEntryDto {
    return { name, path: `${dir}/${name}`, kind: "file", size: 1, mode: "", owner: null, group: null, mtime: 0, symlinkTarget: null };
  }

  function rowNames(): string[] {
    return Array.from(mounted!.container.querySelectorAll('[role="treeitem"]')).map((row) => row.textContent ?? "");
  }

  it("names the host from the session the listing reads", async () => {
    seedTwoHosts();
    useUi.setState({ sessions: [session("s1", "web-01 (prod)"), session("s2", "db-01")] });
    mountPanel();
    await flush();
    emitCwd("k1", 1, "/srv/app");
    await waitForList(["s1", "/srv/app"]);
    expect(mounted!.container.querySelector('[data-testid="session-panel-context"]')?.textContent).toBe(
      "web-01 (prod) · /srv/app",
    );
  });

  it("never shows a slow listing from the previous host after a quick switch", async () => {
    seedTwoHosts();
    const slow = deferred<FileEntryDto[]>();
    mocks.list.mockImplementation((sid: string, dir: string) =>
      sid === "s1" ? slow.promise : Promise.resolve([entry("db.conf", dir)]),
    );
    mountPanel();
    await flush();
    emitCwd("k1", 1, "/srv/app");
    emitCwd("k2", 1, "/var/lib/db");
    await waitUntil(() => listCalls().some(([sid]) => sid === "s1"));

    act(() => useUi.setState({ activeWorkspaceId: "ws2" }));
    await waitForList(["s2", "/var/lib/db"]);
    await waitUntil(() => rowNames().some((name) => name.includes("db.conf")));

    await act(async () => {
      slow.resolve([entry("app.conf", "/srv/app")]);
      await slow.promise;
    });
    await flush();
    expect(rowNames().some((name) => name.includes("app.conf"))).toBe(false);
    expect(rowNames().some((name) => name.includes("db.conf"))).toBe(true);
  });

  it("follows a durable reconnect that swaps the terminal's session id", async () => {
    seedTwoHosts();
    mountPanel();
    await flush();
    emitCwd("k1", 1, "/srv/app");
    await waitForList(["s1", "/srv/app"]);

    act(() =>
      useUi.setState({
        sessions: [...useUi.getState().sessions, session("s1b", "web-01")],
        workspaces: useUi.getState().workspaces.map((w) =>
          w.id === "ws1"
            ? { ...w, sessionId: "s1b", panes: w.panes.map((p) => ({ ...p, tabs: p.tabs.map((t) => ({ ...t, sessionId: "s1b" })) })) }
            : w,
        ),
      }),
    );
    await waitForList(["s1b", "/srv/app"]);
  });

  it("follows focus between split panes of one workspace", async () => {
    useUi.setState({
      workspaces: [
        {
          id: "ws1",
          kind: "session",
          title: "web-01",
          sessionId: "s1",
          panes: [
            { id: "left", tabs: [terminal("t1", "k1", "s1")], activeTabId: "t1" },
            { id: "right", tabs: [terminal("t2", "k2", "s1")], activeTabId: "t2" },
          ],
          activePaneId: "left",
          closable: true,
        },
      ],
      activeWorkspaceId: "ws1",
      sessions: [session("s1", "web-01")],
    });
    mountPanel();
    await flush();
    emitCwd("k1", 1, "/srv/left");
    emitCwd("k2", 1, "/srv/right");
    await waitForList(["s1", "/srv/left"]);
    expect(listCalls()).not.toContainEqual(["s1", "/srv/right"]);

    act(() => useUi.getState().setActivePane("right", "ws1"));
    await waitForList(["s1", "/srv/right"]);
    expect(mounted!.container.querySelector('[data-testid="session-panel-context"]')?.textContent).toBe(
      "web-01 · /srv/right",
    );
  });
});

describe("files tab follows the terminal directory", () => {
  it("collapses a burst of cd into one listing of the latest directory", async () => {
    seedTwoHosts();
    mountPanel();
    await flush();
    emitCwd("k1", 1, "/a");
    emitCwd("k1", 2, "/b");
    emitCwd("k1", 3, "/c");

    await waitForList(["s1", "/c"]);
    expect(listCalls()).not.toContainEqual(["s1", "/b"]);
  });

  it("manual navigation detaches from the terminal until 跟随终端目录 is pressed", async () => {
    seedTwoHosts();
    mountPanel();
    await flush();
    emitCwd("k1", 1, "/srv/www");
    await waitForList(["s1", "/srv/www"]);
    expect(button("跟随终端目录").getAttribute("aria-pressed")).toBe("true");

    const up = mounted!.container.querySelector<HTMLButtonElement>('button[aria-label^="上级目录"]');
    click(up!);
    await waitForList(["s1", "/srv"]);
    expect(button("跟随终端目录").getAttribute("aria-pressed")).toBe("false");

    emitCwd("k1", 2, "/opt/tools");
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 600));
    });
    expect(listCalls()).not.toContainEqual(["s1", "/opt/tools"]);

    click(button("跟随终端目录"));
    expect(button("跟随终端目录").getAttribute("aria-pressed")).toBe("true");
    await waitForList(["s1", "/opt/tools"]);
  });
});

describe("files tab empty states", () => {
  it("shows a hint when no terminal or host has ever had focus", async () => {
    mountPanel();
    await flush();
    expect(text()).toContain("没有聚焦的终端");
    expect(mocks.list).not.toHaveBeenCalled();
  });

  it("explains an offline host instead of listing it", async () => {
    seedTwoHosts();
    useUi.setState({ sessions: [session("s1", "web-01", "ssh", "disconnected"), session("s2", "db-01")] });
    mountPanel();
    await flush();
    expect(text()).toContain("web-01 已离线");
    expect(mocks.list).not.toHaveBeenCalled();
  });

  it("waits while the host connects", async () => {
    seedTwoHosts();
    useUi.setState({ sessions: [session("s1", "web-01", "ssh", "connecting")] });
    mountPanel();
    await flush();
    expect(text()).toContain("正在连接 web-01");
    expect(mocks.list).not.toHaveBeenCalled();
  });

  it("explains a closed session", async () => {
    seedTwoHosts();
    useUi.setState({ sessions: [] });
    mountPanel();
    await flush();
    expect(text()).toContain("会话已关闭");
  });

  it("does not show host files for a container terminal", async () => {
    useUi.setState({
      workspaces: [workspace("ws1", "web-01", "s1", [terminal("t1", "k1", "s1", { containerId: "c1" })])],
      activeWorkspaceId: "ws1",
      sessions: [session("s1", "web-01")],
    });
    mountPanel();
    await flush();
    expect(text()).toContain("容器终端");
    expect(mocks.list).not.toHaveBeenCalled();
  });

  it("browses the local machine for a local terminal", async () => {
    useUi.setState({
      workspaces: [workspace("wsl", "当前设备", "local-1", [terminal("tl", "kl", "local-1")])],
      activeWorkspaceId: "wsl",
      sessions: [session("local-1", "当前设备", "local")],
    });
    mountPanel();
    await flush();
    emitCwd("kl", 1, "/home/me/src");
    await waitForList(["local-1", "/home/me/src"]);
  });

  it("browses a host workspace that has no terminal yet", async () => {
    useUi.setState({
      workspaces: [workspace("wsd", "device-1", "s-dev", [])],
      activeWorkspaceId: "wsd",
      sessions: [session("s-dev", "device-1", "device")],
    });
    mountPanel();
    await flush();
    await waitUntil(() => listCalls().some(([sid]) => sid === "s-dev"));
  });
});

describe("session side panel tabs", () => {
  it("exposes tablist semantics and moves focus into the panel on an explicit open", async () => {
    seedTwoHosts();
    mountPanel();
    await flush();
    const tablist = mounted!.container.querySelector('[role="tablist"]');
    expect(tablist?.getAttribute("aria-label")).toBe("会话侧栏页签");
    const tab = mounted!.container.querySelector<HTMLButtonElement>('[role="tab"]');
    expect(tab?.textContent).toBe("文件");
    expect(tab?.getAttribute("aria-selected")).toBe("true");
    const panel = mounted!.container.querySelector('[role="tabpanel"]');
    expect(panel?.getAttribute("aria-labelledby")).toBe(tab?.id);

    expect(document.activeElement).not.toBe(tab);
    act(() => useSessionPanel.setState((s) => ({ focusRequest: s.focusRequest + 1 })));
    expect(document.activeElement).toBe(tab);
  });

  it("closes the overlay sheet with Escape", async () => {
    seedTwoHosts();
    useSessionPanel.getState().setOpen(true);
    mountPanel(true);
    await flush();
    const tab = mounted!.container.querySelector<HTMLButtonElement>('[role="tab"]')!;
    act(() => {
      tab.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    });
    expect(useSessionPanel.getState().open).toBe(false);
  });
});

describe("session side panel host link", () => {
  function hostTab(): AppTab | undefined {
    return useUi.getState().workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs)).find((t) => t.kind === "host");
  }

  it("opens the detail page of the focused session's host", async () => {
    useUi.setState({
      workspaces: [{ ...workspace("ws1", "web-01", "s1", [terminal("t1", "k1", "s1")]), assetId: "a-web", assetKind: "ssh" }],
      activeWorkspaceId: "ws1",
      sessions: [{ ...session("s1", "web-01"), assetId: "a-web" }],
    });
    mountPanel();
    await flush();
    click(button("查看主机详情"));
    expect(hostTab()?.assetId).toBe("a-web");
    // Opening the page neither connects nor adds a tab to the host workspace.
    const hostWorkspace = useUi.getState().workspaces.find((w) => w.id === "ws1");
    expect(hostWorkspace?.panes.flatMap((p) => p.tabs).map((t) => t.id)).toEqual(["t1"]);
  });

  it("has no host link for a session without a saved host", async () => {
    seedTwoHosts();
    mountPanel();
    await flush();
    expect(mounted!.container.querySelector('button[aria-label="查看主机详情"]')).toBeNull();
  });
});
