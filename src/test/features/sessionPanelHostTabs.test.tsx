/** @vitest-environment jsdom */
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  roots: vi.fn(),
  ps: vi.fn(),
  images: vi.fn(),
  composeStacks: vi.fn(),
  forwardEnv: vi.fn(),
  forwardList: vi.fn(),
  logStart: vi.fn(),
  logCancel: vi.fn(),
  assetList: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    fsApi: { ...actual.fsApi, list: mocks.list, roots: mocks.roots },
    assetApi: { ...actual.assetApi, list: mocks.assetList },
    dockerApi: { ...actual.dockerApi, ps: mocks.ps, images: mocks.images, composeStacks: mocks.composeStacks },
    forwardApi: { ...actual.forwardApi, env: mocks.forwardEnv, list: mocks.forwardList },
    sessionApi: { connect: vi.fn(), connectLocal: vi.fn() },
    terminalApi: { write: vi.fn(), closeTab: vi.fn(() => Promise.resolve()) },
  };
});

vi.mock("../../ipc/logTailApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/logTailApi")>();
  return {
    ...actual,
    logTailApi: { start: mocks.logStart, cancel: mocks.logCancel, save: vi.fn() },
    createLogTailRunId: () => "run-1",
  };
});

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    listenEvent: () => Promise.resolve(() => {}),
    createJsonChannel: vi.fn(() => ({ id: "channel-log-tail", onmessage: vi.fn(), toJSON: () => "channel-log-tail" })),
    disposeChannel: vi.fn(),
  };
});

import { SessionPanel } from "../../features/session-panel/SessionPanel";
import { resetSessionPanelForTest, useSessionPanel, type SessionPanelTabId } from "../../features/session-panel/sessionPanelStore";
import { useAiPanelSlot } from "../../features/session-panel/aiSlot";
import { MovableHost } from "../../app/shell/MovableHost";
import { resetCwdFollowForTest } from "../../features/files/cwdFollow";
import { useUi, type AppTab, type Workspace } from "../../app/store";
import type { SessionInfo } from "../../ipc/commands";

let mounted: MountedView | undefined;

// The tabs load their feature panels lazily; the modules are imported up front so the waits below cover only rendering.
beforeAll(async () => {
  await Promise.all([
    import("../../features/docker/DockerPanel"),
    import("../../features/forward/ForwardPanel"),
    import("../../features/logs/MultiHostLogTail"),
  ]);
});

// waitFor polls in short act() steps: lazy panels and queries settle between steps, which one long act() would hold back.
async function waitFor(assertion: () => void, timeoutMs = 3000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    try {
      assertion();
      return;
    } catch (error) {
      if (Date.now() > deadline) throw error;
    }
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
  }
}

function terminal(id: string, terminalId: string, sessionId: string): AppTab {
  return { id, kind: "terminal", title: id, sessionId, tabId: terminalId, closable: true };
}

function workspace(id: string, title: string, sessionId: string, tabs: AppTab[]): Workspace {
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

function seedHosts(sessions: SessionInfo[] = [session("s1", "web-01"), session("s2", "db-01")]): void {
  useUi.setState({
    workspaces: [
      workspace("ws1", "web-01", "s1", [terminal("t1", "k1", "s1")]),
      workspace("ws2", "db-01", "s2", [terminal("t2", "k2", "s2")]),
    ],
    activeWorkspaceId: "ws1",
    sessions,
  });
}

function container(id: string, name: string) {
  return { id, name, image: "nginx:1", state: "running", status: "Up 1 hour", ports: "", composeProject: null };
}

function forward(id: string, sessionId: string, listenPort: number) {
  return { id, sessionId, status: "active", listenPort, targetHost: "127.0.0.1", targetPort: 80, kind: "local", createdAt: 0 };
}

function mountPanel(tab: SessionPanelTabId, aiDocked = false): MountedView {
  act(() => useSessionPanel.getState().setTab(tab));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(SessionPanel, { overlay: false, aiDocked, onClose: () => undefined }),
    ),
  );
  mounted = view;
  return view;
}

function text(): string {
  return mounted?.container.textContent ?? "";
}

function focusWorkspace(id: string): void {
  act(() => useUi.setState({ activeWorkspaceId: id }));
}

beforeEach(() => {
  vi.clearAllMocks();
  window.localStorage.clear();
  resetCwdFollowForTest();
  resetSessionPanelForTest();
  mocks.list.mockResolvedValue([]);
  mocks.roots.mockResolvedValue({ pathStyle: "posix", roots: [], homePath: null, supported: false });
  mocks.ps.mockImplementation((sessionId: string) =>
    Promise.resolve(sessionId === "s1" ? [container("c1", "web-nginx")] : [container("c2", "db-postgres")]),
  );
  mocks.images.mockResolvedValue([]);
  mocks.composeStacks.mockResolvedValue([]);
  mocks.forwardEnv.mockResolvedValue({ available: true, platform: "other", listenHost: "127.0.0.1" });
  mocks.forwardList.mockResolvedValue([forward("f1", "s1", 15432), forward("f2", "s2", 16379)]);
  mocks.logStart.mockResolvedValue({ runId: "run-1" });
  mocks.logCancel.mockResolvedValue({ confirmed: true });
  mocks.assetList.mockResolvedValue([]);
  useUi.setState({ workspaces: [], activeWorkspaceId: null, sessions: [], rightOpen: false, toasts: [] });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("session side panel host tabs", () => {
  it("lists the focused host's containers and follows focus to the next host", async () => {
    seedHosts();
    mountPanel("containers");
    await waitFor(() => expect(text()).toContain("web-nginx"));
    expect(mocks.ps).toHaveBeenCalledWith("s1");
    expect(text()).not.toContain("db-postgres");

    focusWorkspace("ws2");
    await waitFor(() => expect(text()).toContain("db-postgres"));
    expect(mocks.ps).toHaveBeenLastCalledWith("s2");
    expect(text()).not.toContain("web-nginx");
  });

  it("shows the offline state instead of querying a disconnected host", async () => {
    seedHosts([session("s1", "web-01", "ssh", "disconnected"), session("s2", "db-01")]);
    mountPanel("containers");
    await flush();
    expect(text()).toContain("web-01 已离线");
    expect(mocks.ps).not.toHaveBeenCalled();
  });

  it("shows only the forwards of the focused host", async () => {
    seedHosts();
    mountPanel("ports");
    await waitFor(() => expect(text()).toContain("15432"));
    expect(text()).not.toContain("16379");

    focusWorkspace("ws2");
    await waitFor(() => expect(text()).toContain("16379"));
    expect(text()).not.toContain("15432");
  });

  it("follows logs on the focused host only, without the multi-host picker", async () => {
    seedHosts();
    mountPanel("logs");
    await waitFor(() => expect(mounted?.container.querySelector('[aria-label="来源 web-01"]')).not.toBeNull());
    expect(text()).not.toContain("并发数");
    expect(mounted?.container.querySelector('[aria-label="来源 db-01"]')).toBeNull();

    const source = mounted!.container.querySelector<HTMLInputElement>('[aria-label="来源 web-01"]')!;
    setInputValue(source, "/var/log/app.log");
    const start = [...mounted!.container.querySelectorAll("button")].find((b) => b.textContent?.includes("开始跟随"))!;
    click(start);
    await waitFor(() => expect(mocks.logStart).toHaveBeenCalledTimes(1));
    expect(mocks.logStart.mock.calls[0][0].sources).toEqual([
      { sourceId: "s1", sessionId: "s1", kind: "file", value: "/var/log/app.log" },
    ]);
  });

  it("explains that local terminals have no log follow", async () => {
    useUi.setState({
      workspaces: [workspace("ws1", "本地终端", "l1", [terminal("t1", "k1", "l1")])],
      activeWorkspaceId: "ws1",
      sessions: [session("l1", "本地终端", "local")],
    });
    mountPanel("logs");
    await flush();
    expect(text()).toContain("此会话不支持日志跟随");
    expect(mocks.logStart).not.toHaveBeenCalled();
  });
});

// Counter stands in for the AI sidebar: its state must survive moving between the dock and the panel.
function Counter() {
  const [count, setCount] = useState(0);
  return createElement("button", { type: "button", "data-testid": "counter", onClick: () => setCount((c) => c + 1) }, `count ${count}`);
}

function Shell({ docked }: { docked: boolean }) {
  const slot = useAiPanelSlot();
  return createElement(
    "div",
    null,
    createElement(SessionPanel, { overlay: false, aiDocked: docked, onClose: () => undefined }),
    createElement(
      "div",
      { "data-testid": "dock" },
      createElement(MovableHost, { target: slot && !docked ? slot : null, children: createElement(Counter) }),
    ),
  );
}

describe("session side panel AI tab", () => {
  function counter(): HTMLButtonElement {
    return mounted!.container.querySelector<HTMLButtonElement>('[data-testid="counter"]')!;
  }

  function inPanel(): boolean {
    return Boolean(counter().closest('[data-testid="session-panel-ai"]'));
  }

  function renderShell(docked: boolean): void {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const node = createElement(QueryClientProvider, { client }, createElement(Shell, { docked }));
    if (mounted) act(() => mounted!.root.render(node));
    else mounted = mount(node);
  }

  it("moves the one AI instance into the panel and back without losing its state", async () => {
    seedHosts();
    renderShell(false);
    await flush();
    expect(inPanel()).toBe(false);
    click(counter());

    act(() => useSessionPanel.getState().setTab("ai"));
    await flush();
    expect(inPanel()).toBe(true);
    expect(counter().textContent).toBe("count 1");
    expect(mounted!.container.querySelectorAll('[data-testid="counter"]')).toHaveLength(1);

    act(() => useSessionPanel.getState().setTab("files"));
    await flush();
    expect(inPanel()).toBe(false);
    expect(counter().textContent).toBe("count 1");
  });

  it("keeps the AI in the open right dock and offers to move it into the panel", async () => {
    seedHosts();
    act(() => useUi.setState({ rightOpen: true }));
    act(() => useSessionPanel.getState().setTab("ai"));
    renderShell(true);
    await flush();
    expect(inPanel()).toBe(false);
    expect(mounted!.container.textContent).toContain("AI 助手在右侧栏中打开");

    const move = [...mounted!.container.querySelectorAll("button")].find((b) => b.textContent === "在会话侧栏中显示")!;
    click(move);
    expect(useUi.getState().rightOpen).toBe(false);
    renderShell(false);
    await flush();
    expect(inPanel()).toBe(true);
  });
});
