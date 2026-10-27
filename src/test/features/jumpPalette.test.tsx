/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, flushUntil, mount, mountWithClient, setInputValue, type MountedView } from "./reactTestUtils";

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
import { noteTabActivated, resetTabRecency, useTabRecencyTracker } from "../../app/jump/tabRecency";
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

async function mountPalette(props: Partial<Parameters<typeof JumpPalette>[0]> = {}): Promise<MountedView> {
  mounted = mountWithClient(createElement(JumpPalette, { onClose: vi.fn(), ...props }));
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
    tracker.unmount();

    expect(optionLabels(view).slice(0, 3)).toEqual(["logs", "build", "deploy"]);
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

  it("lists background terminals of connected hosts and takes one over", async () => {
    useUi.setState({ sessions: [SESSION] });
    mocks.listDurable.mockResolvedValue([
      { id: "durable-1", incarnation: "i1", createdAt: 1, title: "long job", metadataRevision: 1, cols: 80, rows: 24, dead: false },
    ]);
    const onClose = vi.fn();
    const view = await mountPalette({ onClose });
    await flushUntil(() => optionLabels(view).includes("long job"));

    const row = options(view).find((option) => option.textContent?.includes("long job"));
    expect(row?.textContent).toContain("后台");
    click(row!);
    expect(onClose).toHaveBeenCalledTimes(1);
    await flushUntil(() =>
      useUi.getState().workspaces.some((item) => item.panes.some((pane) => pane.tabs.some((tab) => tab.tabId === "durable-1"))),
    );
    const owner = useUi.getState().workspaces.find((item) => item.panes.some((pane) => pane.tabs.some((tab) => tab.tabId === "durable-1")));
    expect(owner?.id).toBe("ws-a");
  });

  it("keeps the palette usable and reports it when background terminals cannot be listed", async () => {
    useUi.setState({ sessions: [SESSION] });
    mocks.listDurable.mockRejectedValue(new Error("helper offline"));
    const view = await mountPalette();
    await flushUntil(() => (view.container.textContent ?? "").includes("helper offline"));
    expect(view.container.textContent).toContain("部分后台会话未列出");
    expect(optionLabels(view)).toContain("deploy");
  });
});

describe("jump palette scopes", () => {
  it("switches between all results and hosts with Tab in the search field", async () => {
    const view = await mountPalette();
    expect(optionLabels(view)).toContain("打开设置");

    const event = keyDown(combobox(view), "Tab");
    expect(event.defaultPrevented).toBe(true);
    expect(optionLabels(view)).toEqual(["web-01"]);
    expect(combobox(view).getAttribute("aria-label")).toBe("资产搜索");

    keyDown(combobox(view), "Tab");
    expect(optionLabels(view)).toContain("打开设置");
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
