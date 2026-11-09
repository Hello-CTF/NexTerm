/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, deferred, flush, flushUntil, mount, setInputValue, waitForRendered, type MountedView } from "./reactTestUtils";

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
import { jumpAttentionOf } from "../../app/jump/jumpAttention";
import { applyAwareness, resetAwarenessStore } from "../../app/sessionAwarenessStore";
import type { TerminalAttentionEvent } from "../../ipc/events";
import { useUi, type AppTab, type Workspace } from "../../app/store";
import type { Asset, SessionInfo } from "../../ipc/commands";
import { onSettingsSection } from "../../features/settings/settingsNavigation";
import { recordCommandUse } from "../../app/jump/commandRecency";
import { isMac, setMacPlatform } from "../../app/platform";

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
  resetAwarenessStore();
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
    await waitForRendered(() => expect(mocks.probeBatch).toHaveBeenCalledTimes(1));
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

function attention(tabId: string, version: number, patch: Partial<TerminalAttentionEvent> = {}): TerminalAttentionEvent {
  return { tabId, state: "needs_me", confidence: "signal", source: "helper", since: "2026-10-27T10:00:00Z", version, ...patch };
}

describe("jump palette attention from the awareness store", () => {
  it("ranks needs_me, then just_finished, then the rest; seen and unknown states stay in the rest", async () => {
    act(() => applyAwareness([
      attention("kernel-t-logs", 1),
      attention("kernel-t-deploy", 1, { state: "just_finished", exitCode: 0 }),
      attention("kernel-t-build", 1, { state: "needs_me", seen: true }),
    ]));
    const view = await mountPalette({ attentionOf: jumpAttentionOf });
    expect(optionLabels(view).slice(0, 3)).toEqual(["logs", "deploy", "build"]);
    expect(jumpAttentionOf({ terminalId: "kernel-t-build" } as never)).toBeUndefined();
    expect(jumpAttentionOf({ terminalId: null } as never)).toBeUndefined();
  });

  it("shows the state badge on session rows and updates it without reordering", async () => {
    act(() => applyAwareness([attention("kernel-t-logs", 1)]));
    const view = await mountPalette({ attentionOf: jumpAttentionOf });
    const rowOf = (label: string) => options(view).find((option) => option.textContent?.startsWith(label))!;
    expect(rowOf("logs").querySelector('[data-attention="needs_me"]')).not.toBeNull();
    expect(rowOf("deploy").querySelector("[data-attention]")).toBeNull();
    const before = optionLabels(view).slice(0, 3);

    act(() => applyAwareness([attention("kernel-t-deploy", 1, { state: "just_finished", exitCode: 2 })]));
    expect(rowOf("deploy").querySelector('[data-attention="just_finished"]')).not.toBeNull();
    expect(optionLabels(view).slice(0, 3)).toEqual(before);

    setInputValue(combobox(view), "o");
    setInputValue(combobox(view), "");
    expect(optionLabels(view).slice(0, 2)).toEqual(["logs", "deploy"]);
  });
});

describe("jump palette number keys and split", () => {
  const altDigit = (view: MountedView, digit: number, init: KeyboardEventInit = {}) =>
    keyDown(combobox(view), String(digit), { altKey: true, code: `Digit${digit}`, ...init });

  it("opens the Nth shown row with Alt+digit", async () => {
    noteTabActivated("t-logs");
    noteTabActivated("t-deploy");
    noteTabActivated("t-build");
    const onClose = vi.fn();
    const view = await mountPalette({ onClose });
    expect(optionLabels(view).slice(0, 2)).toEqual(["deploy", "logs"]);
    expect(options(view)[1].getAttribute("aria-keyshortcuts")).toBe("Alt+2");

    const event = altDigit(view, 2);
    expect(event.defaultPrevented).toBe(true);
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(useUi.getState().activeWorkspaceId).toBe("ws-b");
  });

  it("counts the rows as filtered when the box has text", async () => {
    const onClose = vi.fn();
    const view = await mountPalette({ onClose });
    setInputValue(combobox(view), "logs");
    altDigit(view, 1);
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(useUi.getState().activeWorkspaceId).toBe("ws-b");
  });

  it("leaves plain digits as text, ignores missing rows and IME composition", async () => {
    const onClose = vi.fn();
    const view = await mountPalette({ onClose });
    const plain = keyDown(combobox(view), "1", { code: "Digit1" });
    expect(plain.defaultPrevented).toBe(false);
    setInputValue(combobox(view), "logs");
    altDigit(view, 5);
    altDigit(view, 1, { isComposing: true });
    expect(useUi.getState().activeWorkspaceId).toBe("ws-a");
    expect(onClose).not.toHaveBeenCalled();
  });

  it("Shift+Enter moves a session of the current workspace into a new pane to the right", async () => {
    const onClose = vi.fn();
    const view = await mountPalette({ onClose });
    setInputValue(combobox(view), "deploy");
    keyDown(combobox(view), "Enter", { shiftKey: true });

    expect(onClose).toHaveBeenCalledTimes(1);
    const ws = useUi.getState().workspaces.find((item) => item.id === "ws-a")!;
    expect(ws.panes).toHaveLength(2);
    const created = ws.panes.find((pane) => pane.id !== "ws-a-pane")!;
    expect(created.tabs.map((tab) => tab.id)).toEqual(["t-deploy"]);
    expect(ws.activePaneId).toBe(created.id);
    expect(ws.panes.find((pane) => pane.id === "ws-a-pane")!.tabs.map((tab) => tab.id)).toEqual(["t-build"]);
  });

  it("Shift+Enter on a session of another workspace opens it normally and says why", async () => {
    const view = await mountPalette();
    setInputValue(combobox(view), "logs");
    keyDown(combobox(view), "Enter", { shiftKey: true });

    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("已直接打开"));
    expect(useUi.getState().activeWorkspaceId).toBe("ws-b");
    expect(useUi.getState().workspaces.find((item) => item.id === "ws-b")!.panes).toHaveLength(1);
  });

  it("Shift+Enter on the current workspace's own host splits it to the right", async () => {
    const onClose = vi.fn();
    const view = await mountPalette({ scope: "hosts", onClose });
    keyDown(combobox(view), "Enter", { shiftKey: true });

    expect(onClose).toHaveBeenCalledTimes(1);
    expect(useUi.getState().workspaces.find((item) => item.id === "ws-a")!.panes).toHaveLength(2);
  });

  it("plain Enter on a session does not split", async () => {
    const view = await mountPalette();
    setInputValue(combobox(view), "deploy");
    keyDown(combobox(view), "Enter");
    expect(useUi.getState().workspaces.find((item) => item.id === "ws-a")!.panes).toHaveLength(1);
  });
});

describe("jump palette search extras", () => {
  const sectionHeaders = (view: MountedView) =>
    [...view.container.querySelectorAll<HTMLElement>("[data-command-section]")].map((header) => header.textContent);

  it("lists matching settings under their own header and opens the exact setting", async () => {
    const onClose = vi.fn();
    const view = await mountPalette({ onClose });
    expect(sectionHeaders(view)).not.toContain("设置");

    setInputValue(combobox(view), "OSC 52");
    expect(sectionHeaders(view)).toEqual(["设置"]);
    const row = options(view).find((option) => option.textContent?.includes("允许远程主机写入剪贴板（OSC 52）"));
    expect(row).toBeDefined();

    const requests: [string, string | undefined][] = [];
    const off = onSettingsSection((sectionId, itemId) => requests.push([sectionId, itemId]));
    click(row!);
    off();
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(requests).toEqual([["settings-terminal", "osc52"]]);
    const tabs = useUi.getState().workspaces.flatMap((item) => item.panes.flatMap((pane) => pane.tabs));
    expect(tabs.some((tab) => tab.kind === "settings")).toBe(true);
  });

  it("offers to ask AI when nothing matches well and only prefills the composer", async () => {
    const onClose = vi.fn();
    useUi.setState({ rightOpen: false, aiPendingPrompt: null });
    const view = await mountPalette({ onClose });

    setInputValue(combobox(view), "logs");
    expect(options(view).some((option) => option.textContent?.includes("问 AI"))).toBe(false);

    setInputValue(combobox(view), "why is nginx slow");
    const ask = options(view).find((option) => option.textContent?.includes("问 AI：why is nginx slow"));
    expect(ask).toBeDefined();
    expect(view.container.textContent).toContain("没有匹配的会话、主机或命令");

    keyDown(combobox(view), "End");
    keyDown(combobox(view), "Enter");
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(useUi.getState().aiPendingPrompt).toBe("why is nginx slow");
    expect(useUi.getState().rightOpen).toBe(true);
  });

  it("marks the matched part of a row label", async () => {
    const view = await mountPalette();
    expect(view.container.querySelector(".nx-command-match")).toBeNull();
    setInputValue(combobox(view), "dep");
    const deploy = options(view).find((option) => option.textContent?.startsWith("deploy"))!;
    expect([...deploy.querySelectorAll("mark.nx-command-match")].map((mark) => mark.textContent)).toEqual(["dep"]);
  });

  it("writes the Alt+N row hint the way the platform does", async () => {
    const wasMac = isMac();
    const hint = (view: MountedView) => options(view)[0].querySelector('[aria-hidden="true"].nx-command-hint')?.textContent;
    try {
      setMacPlatform(false);
      let view = await mountPalette();
      expect(hint(view)).toBe("Alt+1");
      mounted?.unmount();
      setMacPlatform(true);
      view = await mountPalette();
      expect(hint(view)).toBe("⌥1");
    } finally {
      setMacPlatform(wasMac);
    }
  });

  it("puts recently used commands first within their section and remembers a run command", async () => {
    const commandLabels = (view: MountedView) =>
      optionLabels(view).filter((label) => label === "快速连接…" || label === "打开设置");
    let view = await mountPalette();
    expect(commandLabels(view)).toEqual(["快速连接…", "打开设置"]);
    mounted?.unmount();

    recordCommandUse("settings");
    view = await mountPalette();
    expect(commandLabels(view)).toEqual(["打开设置", "快速连接…"]);
    const headers = sectionHeaders(view);
    expect(new Set(headers).size).toBe(headers.length);
    mounted?.unmount();

    localStorage.clear();
    view = await mountPalette();
    setInputValue(combobox(view), "打开设置");
    keyDown(combobox(view), "Enter");
    expect(JSON.parse(localStorage.getItem("nexterm.commandRecency.v1") ?? "{}")).toHaveProperty("settings");
  });
});

describe("jump palette with many hosts", () => {
  const many = Array.from({ length: 5000 }, (_, index) => ({
    ...WEB,
    id: `host-${index}`,
    name: `host-${String(index).padStart(4, "0")}`,
    host: `10.1.${Math.floor(index / 250)}.${index % 250}`,
  }));

  // jsdom has no layout: give the listbox a viewport and each measured row a height, as a browser would.
  function stubLayout(): () => void {
    const height = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
    const width = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetWidth");
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
      configurable: true,
      get(this: HTMLElement) {
        if (this.getAttribute("role") === "listbox") return 340;
        return this.hasAttribute("data-index") ? 34 : 0;
      },
    });
    Object.defineProperty(HTMLElement.prototype, "offsetWidth", { configurable: true, get: () => 560 });
    return () => {
      if (height) Object.defineProperty(HTMLElement.prototype, "offsetHeight", height);
      if (width) Object.defineProperty(HTMLElement.prototype, "offsetWidth", width);
    };
  }

  async function expectWindowedKeyboardReach(view: MountedView) {
    expect(options(view).length).toBeGreaterThan(0);
    expect(options(view).length).toBeLessThan(60);
    expect(options(view)[0].getAttribute("aria-setsize")).toBe("5000");

    keyDown(combobox(view), "End");
    const active = combobox(view).getAttribute("aria-activedescendant");
    expect(active).toBeTruthy();
    const activeOption = document.getElementById(active!);
    expect(activeOption?.getAttribute("aria-selected")).toBe("true");
    expect(activeOption?.textContent).toContain("host-4999");
    expect(options(view).length).toBeLessThan(60);

    setInputValue(combobox(view), "host-12");
    expect(options(view).length).toBeLessThan(60);
    expect(optionLabels(view)[0]).toBe("host-1200");
  }

  it("renders only the rows in view while keyboard targeting still reaches every row", async () => {
    mocks.list.mockResolvedValue(many);
    const restore = stubLayout();
    try {
      const view = await mountPalette({ scope: "hosts" });
      expect(view.container.querySelector("[data-index]")).not.toBeNull();
      await expectWindowedKeyboardReach(view);
    } finally {
      restore();
    }
  });

  it("falls back to a bounded window while the list has no measured size", async () => {
    mocks.list.mockResolvedValue(many);
    const view = await mountPalette({ scope: "hosts" });
    expect(view.container.querySelector("[data-index]")).toBeNull();
    await expectWindowedKeyboardReach(view);
  });
});
