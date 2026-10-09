/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, waitFor, type MountedView } from "./reactTestUtils";

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
import { FileBrowser } from "../../features/files/FileBrowser";
import {
  cwdFollowMapSizeForTest,
  normalizeFollowTarget,
  resetCwdFollowForTest,
} from "../../features/files/cwdFollow";
import { useUi, type AppTab } from "../../app/store";

const SID = "s1";

let mounted: MountedView | undefined;

function seedWorkspace(options: { kind?: string; tabId?: string; secondTabId?: string } = {}): void {
  const first: AppTab = {
    id: "t1",
    kind: "terminal",
    title: "终端 1",
    sessionId: SID,
    tabId: options.tabId ?? "k1",
    closable: true,
  };
  const tabs = [first];
  if (options.secondTabId) {
    tabs.push({
      id: "t2",
      kind: "terminal",
      title: "终端 2",
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
        splitRatio: 0.5,
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

function mountBrowser(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = mount(
    createElement(QueryClientProvider, { client }, createElement(FileBrowser, { sessionId: SID })),
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
  it("去掉尾部斜杠并保留根与盘符", () => {
    expect(normalizeFollowTarget("/srv/www/")).toBe("/srv/www");
    expect(normalizeFollowTarget("/")).toBe("/");
    expect(normalizeFollowTarget("C:\\")).toBe("C:/");
    expect(normalizeFollowTarget("C:/Users/")).toBe("C:/Users");
  });
});

describe("FileTree 跟随终端目录", () => {
  it("默认开启：cwd 事件后文件树跳到终端目录", async () => {
    seedWorkspace();
    mountTree();
    await flush();
    expect(followButton()?.getAttribute("aria-pressed")).toBe("true");

    emitControl({ tabId: "k1", version: 1, cwd: "/srv/www" });

    await waitFor(() => expect(listCalls()).toContain("/srv/www"));
    expect(mounted!.container.textContent).toContain("srv");
  });

  it("手动导航不被跟随抢回：终端重报同一 cwd 时不重复跳转", async () => {
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

  it("关闭开关后不跟随，重新开启立即对齐到终端当前目录", async () => {
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

  it("跟随失败停留当前目录，只提示一次，不影响后续跟随", async () => {
    seedWorkspace();
    mocks.list.mockImplementation((_sid: string, dir: string) =>
      dir.startsWith("/bad") ? Promise.reject(new Error("permission denied")) : Promise.resolve([]),
    );
    mountTree();
    await flush();

    emitControl({ tabId: "k1", version: 1, cwd: "/bad/one" });
    await waitFor(() => expect(toastTexts().join("\n")).toContain("无法跟随终端目录到 /bad/one"));
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

  it("忽略过期版本与无关终端的 cwd 事件", async () => {
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

  it("切换到另一个终端标签后跟随新终端的 cwd", async () => {
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

  it("winrm 会话不显示跟随开关", async () => {
    seedWorkspace({ kind: "winrm" });
    mountTree();
    await flush();
    expect(followButton()).toBeUndefined();
  });
});

describe("FileBrowser 跟随终端目录", () => {
  it("cwd 事件后文件列表跳到终端目录", async () => {
    seedWorkspace();
    mountBrowser();
    await flush();
    expect(followButton()?.getAttribute("aria-pressed")).toBe("true");

    emitControl({ tabId: "k1", version: 1, cwd: "/srv/www" });

    await waitFor(() => expect(listCalls()).toContain("/srv/www"));
    const input = mounted!.container.querySelector("input");
    expect(input?.value).toBe("/srv/www");
  });
});

describe("cwd 记录生命周期", () => {
  it("tab 关闭后清理其 cwd 记录", async () => {
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
            splitRatio: 0.5,
            closable: true,
          },
        ],
      });
    });
    expect(cwdFollowMapSizeForTest()).toBe(0);
  });

  it("会话工作区移除后清理其终端的 cwd 记录", async () => {
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

  it("存活 tab 的 cwd 记录不受剪枝影响", async () => {
    seedWorkspace({ secondTabId: "k2" });
    mountTree();
    await flush();

    emitControl({ tabId: "k1", version: 1, cwd: "/first" });
    emitControl({ tabId: "k2", version: 1, cwd: "/second" });
    expect(cwdFollowMapSizeForTest()).toBe(2);

    act(() => {
      useUi.getState().setActiveTab("t2");
    });
    expect(cwdFollowMapSizeForTest()).toBe(2);
  });
});
