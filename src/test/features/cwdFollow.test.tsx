/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, deferred, flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  handlers: new Map<string, (payload: unknown) => void>(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    fsApi: { ...actual.fsApi, list: mocks.list },
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

import { FileTree } from "../../features/files/FileTree";
import {
  cwdFollowMapSizeForTest,
  focusedTerminalCwd,
  normalizeFollowTarget,
  resetCwdFollowForTest,
  startCwdTracking,
} from "../../features/files/cwdFollow";
import { useUi, type AppTab } from "../../app/store";

const SID = "s1";

let mounted: MountedView | undefined;

function seedWorkspace(options: { kind?: string; tabId?: string; secondTabId?: string } = {}): void {
  const first: AppTab = {
    id: "t1",
    kind: "terminal",
    title: "Terminal 1",
    sessionId: SID,
    tabId: options.tabId ?? "k1",
    closable: true,
  };
  const tabs = [first];
  if (options.secondTabId) {
    tabs.push({
      id: "t2",
      kind: "terminal",
      title: "Terminal 2",
      sessionId: SID,
      tabId: options.secondTabId,
      closable: true,
    });
  }
  useUi.setState({
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title: "web-01",
        sessionId: SID,
        panes: [{ id: "p1", tabs, activeTabId: first.id }],
        activePaneId: "p1",
        closable: true,
      },
    ],
    activeWorkspaceId: "ws1",
    sessions: [
      {
        id: SID,
        assetId: null,
        name: "web-01",
        kind: options.kind ?? "ssh",
        status: "connected",
        tabs: [],
        createdAt: 0,
      },
    ],
    leftOpen: true,
    leftWidth: 260,
    toasts: [],
  });
}

function mountTree(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = mount(
    createElement(QueryClientProvider, { client }, createElement(FileTree, { sessionId: SID })),
  );
  mounted = view;
  return view;
}

function emitControl(payload: Record<string, unknown>): void {
  const handler = mocks.handlers.get("terminal://control");
  if (!handler) throw new Error("terminal://control handler not registered");
  act(() => {
    handler(payload);
  });
}

function followButton(): HTMLButtonElement | undefined {
  return mounted?.container.querySelector<HTMLButtonElement>(
    'button[aria-label="跟随终端目录"]',
  ) ?? undefined;
}

function toastTexts(): string[] {
  return useUi.getState().toasts.map((t) => t.text);
}

function listCalls(): string[] {
  return mocks.list.mock.calls.map((c) => String(c[1]));
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.handlers.clear();
  window.localStorage.clear();
  resetCwdFollowForTest();
  mocks.list.mockResolvedValue([]);
  useUi.setState({
    workspaces: [],
    activeWorkspaceId: null,
    sessions: [],
    leftOpen: true,
    leftWidth: 260,
    toasts: [],
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("normalizeFollowTarget", () => {
  it("strips trailing slashes and preserves root and drive prefixes", () => {
    expect(normalizeFollowTarget("/srv/www/")).toBe("/srv/www");
    expect(normalizeFollowTarget("/")).toBe("/");
    expect(normalizeFollowTarget("C:\\")).toBe("C:/");
    expect(normalizeFollowTarget("C:/Users/")).toBe("C:/Users");
  });
});

describe("FileTree terminal-directory follow", () => {
  it("on by default: the file tree jumps to the terminal directory on cwd events", async () => {
    seedWorkspace();
    mountTree();
    await flush();
    expect(followButton()?.getAttribute("aria-pressed")).toBe("true");

    emitControl({ tabId: "k1", version: 1, cwd: "/srv/www" });

    await waitFor(() => expect(listCalls()).toContain("/srv/www"));
    expect(mounted!.container.textContent).toContain("srv");
  });

  it("manual navigation is not overridden: a re-reported same cwd does not jump again", async () => {
    seedWorkspace();
    mountTree();
    await flush();
    emitControl({ tabId: "k1", version: 1, cwd: "/srv/www" });
    await waitFor(() => expect(listCalls()).toContain("/srv/www"));

    const up = mounted!.container.querySelector<HTMLButtonElement>('button[aria-label^="上级目录"]');
    expect(up).toBeDefined();
    click(up!);
    await waitFor(() => expect(listCalls()).toContain("/srv"));
    const callsBefore = listCalls().filter((p) => p === "/srv/www").length;

    emitControl({ tabId: "k1", version: 2, cwd: "/srv/www" });
    await flush();

    expect(listCalls().filter((p) => p === "/srv/www")).toHaveLength(callsBefore);
  });

  it("no following while toggled off; re-enabling aligns to the terminal's current directory immediately", async () => {
    seedWorkspace();
    mountTree();
    await flush();
    const btn = followButton();
    expect(btn).toBeDefined();
    click(btn!);
    expect(followButton()?.getAttribute("aria-pressed")).toBe("false");
    expect(window.localStorage.getItem("nexterm.files.followCwd.v1")).toBe("0");

    emitControl({ tabId: "k1", version: 1, cwd: "/srv/www" });
    await flush();
    expect(listCalls()).not.toContain("/srv/www");

    click(followButton()!);
    expect(followButton()?.getAttribute("aria-pressed")).toBe("true");
    await waitFor(() => expect(listCalls()).toContain("/srv/www"));
  });

  it("a failed follow stays on the current directory, notifies once, and does not break later follows", async () => {
    seedWorkspace();
    mocks.list.mockImplementation((_sid: string, dir: string) =>
      dir.startsWith("/bad") ? Promise.reject(new Error("permission denied")) : Promise.resolve([]),
    );
    mountTree();
    await flush();

    emitControl({ tabId: "k1", version: 1, cwd: "/bad/one" });
    await waitFor(() => expect(toastTexts().join("\n")).toContain("无法跟随终端切换到 /bad/one"));
    expect(toastTexts()).toHaveLength(1);
    expect(mounted!.container.textContent).not.toContain("bad");

    emitControl({ tabId: "k1", version: 2, cwd: "/bad/two" });
    await flush();
    expect(toastTexts()).toHaveLength(1);
    expect(mounted!.container.textContent).not.toContain("bad");

    emitControl({ tabId: "k1", version: 3, cwd: "/good" });
    await waitFor(() => expect(listCalls()).toContain("/good"));
    expect(toastTexts()).toHaveLength(1);
  });

  it("ignores stale-version and unrelated-terminal cwd events", async () => {
    seedWorkspace({ secondTabId: "k2" });
    mountTree();
    await flush();

    emitControl({ tabId: "k2", version: 1, cwd: "/other/term" });
    await flush();
    expect(listCalls()).not.toContain("/other/term");

    emitControl({ tabId: "k1", version: 3, cwd: "/fresh" });
    await waitFor(() => expect(listCalls()).toContain("/fresh"));

    emitControl({ tabId: "k1", version: 2, cwd: "/stale" });
    await flush();
    expect(listCalls()).not.toContain("/stale");
  });

  it("follows the new terminal's cwd after switching to another terminal tab", async () => {
    seedWorkspace({ secondTabId: "k2" });
    mountTree();
    await flush();

    emitControl({ tabId: "k1", version: 1, cwd: "/first" });
    await waitFor(() => expect(listCalls()).toContain("/first"));

    act(() => {
      useUi.getState().setActiveTab("t2");
    });
    emitControl({ tabId: "k2", version: 1, cwd: "/second" });
    await waitFor(() => expect(listCalls()).toContain("/second"));

    emitControl({ tabId: "k1", version: 2, cwd: "/first/again" });
    await flush();
    expect(listCalls()).not.toContain("/first/again");
  });

  it("winrm sessions do not show the follow toggle", async () => {
    seedWorkspace({ kind: "winrm" });
    mountTree();
    await flush();
    expect(followButton()).toBeUndefined();
  });
});

describe("cwd record lifecycle", () => {
  it("closing a tab cleans up its cwd record", async () => {
    seedWorkspace();
    mountTree();
    await flush();

    emitControl({ tabId: "k1", version: 1, cwd: "/srv/www" });
    expect(cwdFollowMapSizeForTest()).toBe(1);

    act(() => {
      useUi.setState({
        workspaces: [
          {
            id: "ws1",
            kind: "session",
            title: "web-01",
            sessionId: SID,
            panes: [{ id: "p1", tabs: [], activeTabId: null }],
            activePaneId: "p1",
            closable: true,
          },
        ],
      });
    });
    expect(cwdFollowMapSizeForTest()).toBe(0);
  });

  it("removing a session workspace cleans up its terminals' cwd records", async () => {
    seedWorkspace({ secondTabId: "k2" });
    mountTree();
    await flush();

    emitControl({ tabId: "k2", version: 1, cwd: "/other/term" });
    expect(cwdFollowMapSizeForTest()).toBe(1);

    act(() => {
      useUi.setState({ workspaces: [], activeWorkspaceId: null });
    });
    expect(cwdFollowMapSizeForTest()).toBe(0);
  });

  it("live tabs' cwd records survive pruning", async () => {
    const firstList = deferred<never[]>();
    mocks.list.mockImplementation((_sid: string, dir: string) =>
      dir === "/first" ? firstList.promise : Promise.resolve([]),
    );
    seedWorkspace({ secondTabId: "k2" });
    mountTree();
    await flush();

    emitControl({ tabId: "k1", version: 1, cwd: "/first" });
    emitControl({ tabId: "k2", version: 1, cwd: "/second" });
    await waitFor(() => expect(listCalls()).toContain("/first"));
    await act(async () => {
      firstList.resolve([]);
      await firstList.promise;
    });
    await flush();
    expect(cwdFollowMapSizeForTest()).toBe(2);

    act(() => {
      useUi.getState().setActiveTab("t2");
    });
    await waitFor(() => expect(listCalls()).toContain("/second"));
    await flush();
    expect(cwdFollowMapSizeForTest()).toBe(2);
  });
});

describe("focusedTerminalCwd", () => {
  it("records directories once tracking starts, before any file view subscribes", async () => {
    seedWorkspace();
    startCwdTracking();
    await flush();
    expect(focusedTerminalCwd(useUi.getState().workspaces, SID)).toBeNull();

    emitControl({ tabId: "k1", version: 1, cwd: "/srv/www" });

    expect(focusedTerminalCwd(useUi.getState().workspaces, SID)).toBe("/srv/www");
    expect(focusedTerminalCwd(useUi.getState().workspaces, "other")).toBeNull();
  });
});
