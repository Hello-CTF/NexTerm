/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, deferred, flush, flushUntil, mount, setInputValue, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  snippetList: vi.fn(),
  probeBatch: vi.fn(),
  sessionList: vi.fn(),
  listLive: vi.fn(),
  listDurable: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: { list: mocks.list, snippetList: mocks.snippetList, probeBatch: mocks.probeBatch },
    sessionApi: { list: mocks.sessionList },
    dbApi: {},
    vaultApi: {},
    terminalApi: { listLive: mocks.listLive, listDurable: mocks.listDurable },
  };
});

import { JumpPalette } from "../../app/JumpPalette";
import { noteTabActivated, resetTabRecency, tabRecency, useTabRecencyTracker } from "../../app/jump/tabRecency";
import { useUi, type AppTab, type Workspace } from "../../app/store";
import type { Asset, SessionInfo } from "../../ipc/commands";

const WEB: Asset = {
  id: "a1",
  groupId: null,
  kind: "ssh",
  name: "web-01",
  host: "10.0.0.8",
  port: 22,
  username: "root",
  authKind: "agent",
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

const SESSION: SessionInfo = {
  id: "s1",
  assetId: "a1",
  name: "web-01",
  kind: "ssh",
  status: "connected",
  tabs: [],
  createdAt: 1,
};

const SESSION_B: SessionInfo = { ...SESSION, id: "s2" };

const DURABLE = { id: "durable-1", incarnation: "i1", createdAt: 1, title: "long job", metadataRevision: 1, cols: 80, rows: 24, dead: false };

function terminal(id: string, title: string, sessionId: string): AppTab {
  return { id, kind: "terminal", title, sessionId, tabId: `kernel-${id}`, closable: true };
}

function workspace(id: string, sessionId: string, tabs: AppTab[]): Workspace {
  return {
    id,
    kind: "session",
    title: id,
    sessionId,
    assetId: "a1",
    assetKind: "ssh",
    closable: true,
    panes: [{ id: `${id}-pane`, tabs, activeTabId: tabs[0]?.id ?? null }],
    activePaneId: `${id}-pane`,
  };
}

function options(view: MountedView): HTMLElement[] {
  return [...view.container.querySelectorAll<HTMLElement>('[role="option"]')];
}

function optionLabels(view: MountedView): string[] {
  return options(view).map((option) => (option.querySelector("span span") ?? option.querySelector("span"))?.textContent ?? "");
}

function combobox(view: MountedView): HTMLInputElement {
  const input = view.container.querySelector<HTMLInputElement>('[role="combobox"]');
  if (!input) throw new Error("combobox not found");
  return input;
}

function keyDown(target: EventTarget, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

let mounted: MountedView | undefined;
let currentClient: QueryClient | undefined;

function ownerOf(terminalId: string): Workspace | undefined {
  return useUi.getState().workspaces.find((item) => item.panes.some((pane) => pane.tabs.some((tab) => tab.tabId === terminalId)));
}

async function mountPalette(props: Partial<Parameters<typeof JumpPalette>[0]> = {}): Promise<MountedView> {
  currentClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(createElement(QueryClientProvider, { client: currentClient },
    createElement(JumpPalette, { onClose: vi.fn(), ...props })));
  await flush();
  await flush();
  return mounted;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  resetTabRecency();
  mocks.list.mockResolvedValue([WEB]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.probeBatch.mockResolvedValue({ results: [] });
  mocks.sessionList.mockResolvedValue([SESSION]);
  mocks.listLive.mockResolvedValue([]);
  mocks.listDurable.mockResolvedValue([]);
  useUi.setState({
    sessions: [],
    workspaces: [
      workspace("ws-a", "s1", [terminal("t-build", "build", "s1"), terminal("t-deploy", "deploy", "s1")]),
      workspace("ws-b", "s2", [terminal("t-logs", "logs", "s2")]),
    ],
    activeWorkspaceId: "ws-a",
    connectingAssetIds: [],
    pushToast: mocks.toast,
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("jump palette sessions", () => {
  it("lists open terminals first, most recently used first and the front tab last", async () => {
    noteTabActivated("t-logs");
    noteTabActivated("t-deploy");
    noteTabActivated("t-build");
    const view = await mountPalette();

    expect(view.container.querySelector('[data-command-section]')?.textContent).toBe("会话");
    expect(optionLabels(view).slice(0, 4)).toEqual(["deploy", "logs", "build", "web-01"]);
    expect(options(view)[2].textContent).toContain("当前");
  });

  it("learns recency from tab activations while the app runs", async () => {
    function Tracker() {
      useTabRecencyTracker();
      return null;
    }
    const tracker = mount(createElement(Tracker));
    act(() => useUi.getState().setActiveTab("t-logs"));
    act(() => useUi.getState().setActiveTab("t-deploy"));
    const view = await mountPalette();
    expect(optionLabels(view).slice(0, 3)).toEqual(["logs", "build", "deploy"]);

    expect(tabRecency("t-logs")).toBeGreaterThan(0);
    act(() => useUi.setState((state) => ({
      workspaces: state.workspaces.filter((item) => item.id !== "ws-b"),
    })));
    tracker.unmount();
    expect(tabRecency("t-logs")).toBe(0);
    expect(tabRecency("t-deploy")).toBeGreaterThan(0);
  });

  it("jumps to the selected terminal in its workspace with Enter and closes", async () => {
    const onClose = vi.fn();
    const view = await mountPalette({ onClose });
    setInputValue(combobox(view), "logs");
    keyDown(combobox(view), "Enter");

    expect(onClose).toHaveBeenCalledTimes(1);
    const state = useUi.getState();
    expect(state.activeWorkspaceId).toBe("ws-b");
    expect(state.workspaces.find((item) => item.id === "ws-b")?.panes[0].activeTabId).toBe("t-logs");
  });

  it("matches sessions by fuzzy subsequence and ignores Enter while an IME composes", async () => {
    const onClose = vi.fn();
    const view = await mountPalette({ onClose });
    setInputValue(combobox(view), "dpy");
    expect(optionLabels(view)[0]).toBe("deploy");

    keyDown(combobox(view), "Enter", { isComposing: true });
    expect(onClose).not.toHaveBeenCalled();
  });

  it("ranks sessions by the attention lookup when one is provided", async () => {
    const view = await mountPalette({
      attentionOf: (session) => (session.label === "build" ? "needsMe" : undefined),
    });
    expect(optionLabels(view)[0]).toBe("build");
  });

  it("takes a background terminal over into its own host workspace, not the active one", async () => {
    useUi.setState({ sessions: [SESSION, SESSION_B] });
    mocks.sessionList.mockResolvedValue([SESSION, SESSION_B]);
    mocks.listDurable.mockImplementation((sessionId: string) => Promise.resolve(sessionId === "s2" ? [DURABLE] : []));
    const onClose = vi.fn();
    const view = await mountPalette({ onClose });
    await flushUntil(() => optionLabels(view).includes("long job"));

    const row = options(view).find((option) => option.textContent?.includes("long job"));
    expect(row?.textContent).toContain("后台");
    click(row!);
    expect(onClose).toHaveBeenCalledTimes(1);
    await flushUntil(() => ownerOf("durable-1") !== undefined);
    expect(ownerOf("durable-1")?.id).toBe("ws-b");
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("已接管"));
  });

  it("reports a takeover whose host disconnected meanwhile", async () => {
    useUi.setState({ sessions: [SESSION, SESSION_B] });
    mocks.listDurable.mockImplementation((sessionId: string) => Promise.resolve(sessionId === "s2" ? [DURABLE] : []));
    mocks.sessionList.mockResolvedValue([SESSION, { ...SESSION_B, status: "disconnected" }]);
    const view = await mountPalette();
    await flushUntil(() => optionLabels(view).includes("long job"));
    click(options(view).find((option) => option.textContent?.includes("long job"))!);

    await flushUntil(() => mocks.toast.mock.calls.length > 0);
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("主机未连接"));
    expect(ownerOf("durable-1")).toBeUndefined();
  });

  it("drops a takeover that resolves after the workspace runtime was reset", async () => {
    useUi.setState({ sessions: [SESSION, SESSION_B] });
    mocks.listDurable.mockImplementation((sessionId: string) => Promise.resolve(sessionId === "s2" ? [DURABLE] : []));
    const pending = deferred<SessionInfo[]>();
    mocks.sessionList.mockReturnValue(pending.promise);
    const view = await mountPalette();
    await flushUntil(() => optionLabels(view).includes("long job"));
    click(options(view).find((option) => option.textContent?.includes("long job"))!);
    mounted?.unmount();
    mounted = undefined;

    act(() => useUi.setState((state) => ({ workspaceRuntimeEpoch: state.workspaceRuntimeEpoch + 1 })));
    await act(async () => {
      pending.resolve([SESSION, SESSION_B]);
      await pending.promise;
    });
    await flush();
    expect(ownerOf("durable-1")).toBeUndefined();
    expect(mocks.toast).not.toHaveBeenCalled();
  });

  it("notes unlisted background terminals without repeating the error and stays usable", async () => {
    useUi.setState({ sessions: [SESSION] });
    mocks.listDurable.mockRejectedValue(new Error("helper offline"));
    const view = await mountPalette();
    await flushUntil(() => (view.container.textContent ?? "").includes("未能列出"));
    expect(view.container.textContent).not.toContain("helper offline");
    expect(optionLabels(view)).toContain("deploy");
  });

  it("keeps the shown order while the palette is open and reorders on a query change", async () => {
    const needs = (label: string) => (session: { label: string }) => (session.label === label ? "needsMe" as const : undefined);
    const view = await mountPalette({ attentionOf: needs("build") });
    expect(optionLabels(view)[0]).toBe("build");

    act(() => view.root.render(createElement(QueryClientProvider, { client: currentClient! },
      createElement(JumpPalette, { onClose: vi.fn(), attentionOf: needs("logs") }))));
    await flush();
    expect(optionLabels(view)[0]).toBe("build");

    setInputValue(combobox(view), "o");
    setInputValue(combobox(view), "");
    expect(optionLabels(view)[0]).toBe("logs");
  });
});

describe("jump palette hosts and commands", () => {
  it("leaves inventory-only assets out of the host rows", async () => {
    mocks.list.mockResolvedValue([
      WEB,
      { ...WEB, id: "k8s-1", name: "ops/api-0", kind: "kubernetes", host: null, options: { inventory: { source: "kubernetes", key: "pod/ops/api-0" } } },
    ]);
    const view = await mountPalette({ scope: "hosts" });
    expect(optionLabels(view)).toEqual(["web-01"]);
  });

  it("matches hosts by kind", async () => {
    const view = await mountPalette({ scope: "hosts" });
    setInputValue(combobox(view), "ssh");
    expect(optionLabels(view)).toEqual(["web-01"]);
  });

  it("shows each section header once, as presentation, with asset actions in their own section", async () => {
    const view = await mountPalette();
    const headers = [...view.container.querySelectorAll<HTMLElement>("[data-command-section]")];
    const names = headers.map((header) => header.textContent);
    expect(new Set(names).size).toBe(names.length);
    expect(names).toEqual(expect.arrayContaining(["会话", "主机", "命令", "资产操作"]));
    expect(headers.every((header) => header.getAttribute("role") === "presentation")).toBe(true);
  });

  it("probes reachability once and not again when a tab title changes while open", async () => {
    const view = await mountPalette();
    await waitFor(() => expect(mocks.probeBatch).toHaveBeenCalledTimes(1));
    act(() => useUi.setState((state) => ({
      workspaces: state.workspaces.map((item) => ({
        ...item,
        panes: item.panes.map((pane) => ({ ...pane, tabs: pane.tabs.map((tab) => ({ ...tab, title: `${tab.title}!` })) })),
      })),
    })));
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 400));
    });
    expect(optionLabels(view)).toContain("logs!");
    expect(mocks.probeBatch).toHaveBeenCalledTimes(1);
  });
});

describe("jump palette scopes", () => {
  it("switches between all results and hosts with Tab in the search field and announces it", async () => {
    const view = await mountPalette();
    expect(optionLabels(view)).toContain("打开设置");

    const event = keyDown(combobox(view), "Tab");
    expect(event.defaultPrevented).toBe(true);
    expect(optionLabels(view)).toEqual(["web-01"]);
    expect(combobox(view).getAttribute("aria-label")).toBe("资产搜索");
    expect(view.container.querySelector('[aria-live="polite"][aria-atomic="true"]')?.textContent).toBe("只看主机");

    keyDown(combobox(view), "Tab");
    expect(optionLabels(view)).toContain("打开设置");
  });

  it("leaves Tab to focus movement while the panel shows a retry control", async () => {
    mocks.snippetList.mockRejectedValue(new Error("offline"));
    const view = await mountPalette();
    await flushUntil(() => (view.container.textContent ?? "").includes("片段列表加载失败"));
    const event = keyDown(combobox(view), "Tab");
    expect(event.defaultPrevented).toBe(false);
    expect(combobox(view).getAttribute("aria-label")).toBe("搜索会话、主机或命令");
  });

  it("clears the query when the quick connect command switches to hosts", async () => {
    const onClose = vi.fn();
    const view = await mountPalette({ onClose });
    setInputValue(combobox(view), "快速连接");
    click(options(view).find((option) => option.textContent?.includes("快速连接…"))!);
    await flush();

    expect(onClose).not.toHaveBeenCalled();
    expect(combobox(view).value).toBe("");
    expect(optionLabels(view)).toEqual(["web-01"]);
    expect(options(view)[0].getAttribute("aria-selected")).toBe("true");
  });

  it("opens in the hosts scope for quick connect and offers the scope toggle as buttons", async () => {
    const view = await mountPalette({ scope: "hosts" });
    expect(optionLabels(view)).toEqual(["web-01"]);
    const all = [...view.container.querySelectorAll<HTMLButtonElement>('[aria-label="跳转范围"] button')].find(
      (button) => button.textContent === "全部",
    );
    click(all!);
    await flush();
    expect(optionLabels(view)).toContain("deploy");
  });

  it("keeps the database picker limited to database assets without a scope toggle", async () => {
    const view = await mountPalette({ scope: "database" });
    expect(view.container.querySelector('[aria-label="跳转范围"]')).toBeNull();
    keyDown(combobox(view), "Tab");
    expect(combobox(view).getAttribute("aria-label")).toBe("数据库资产搜索");
    expect(optionLabels(view)).toEqual([]);
  });
});
