/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, clickButton, flush, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({ reconnect: vi.fn(async (_id: string) => true) }));

vi.mock("../../app/store/workspaceReconnect", async (importOriginal) => {
  const original = await importOriginal<typeof import("../../app/store/workspaceReconnect")>();
  return { ...original, reconnectWorkspaceConnection: mocks.reconnect };
});

import { WorkspaceConnectionBar } from "../../app/shell/WorkspaceConnectionBar";
import { ProjectWorkspaceStrip } from "../../app/ProjectWorkspaceStrip";
import { projectFocusForGroup } from "../../app/projectFocus";
import { useUi, type AppTab, type SessionRuntimeInfo, type Workspace } from "../../app/store";

function session(status: SessionRuntimeInfo["status"], extra: Partial<SessionRuntimeInfo> = {}): SessionRuntimeInfo {
  return { id: "s1", assetId: "a1", name: "prod", kind: "ssh", status, tabs: [], createdAt: 1, ...extra };
}

function workspace(tabs: AppTab[]): Workspace {
  return {
    id: "ws1",
    kind: "session",
    title: "prod-host",
    sessionId: "s1",
    assetId: "a1",
    assetKind: "ssh",
    panes: [{ id: "p1", tabs, activeTabId: tabs[0]?.id ?? null }],
    activePaneId: "p1",
    closable: true,
  };
}

const filesTab = { id: "t-files", kind: "files", title: "文件", closable: true, sessionId: "s1" } as AppTab;
const termTab = { id: "t-term", kind: "terminal", title: "终端", closable: true, sessionId: "s1" } as AppTab;

let mounted: MountedView | undefined;

function renderBar(ws: Workspace, activeTab: AppTab | null) {
  mounted = mount(createElement(WorkspaceConnectionBar, { ws, activeTab }));
}

const bar = () => mounted?.container.querySelector('[data-testid="workspace-connection-bar"]');

beforeEach(() => {
  document.body.replaceChildren();
  mocks.reconnect.mockClear();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useUi.setState({ sessions: [], workspaces: [] });
});

describe("WorkspaceConnectionBar", () => {
  it("shows for a disconnected workspace with no tabs and with a files tab", () => {
    useUi.setState({ sessions: [session("disconnected", { error: "connection reset" })] });
    renderBar(workspace([]), null);
    expect(bar()?.textContent).toContain("与 prod-host 的连接已断开");
    expect(bar()?.textContent).toContain("connection reset");
    mounted?.unmount();
    renderBar(workspace([filesTab]), filesTab);
    expect(bar()).not.toBeNull();
  });

  it("is absent while connected", () => {
    useUi.setState({ sessions: [session("connected")] });
    renderBar(workspace([filesTab]), filesTab);
    expect(bar()).toBeNull();
  });

  it("calls the shared reconnect helper once with the workspace id", async () => {
    useUi.setState({ sessions: [session("failed")] });
    renderBar(workspace([]), null);
    clickButton(mounted!.container, "重新连接");
    await flush();
    expect(mocks.reconnect).toHaveBeenCalledTimes(1);
    expect(mocks.reconnect).toHaveBeenCalledWith("ws1");
  });

  it("shows the attempt count and retry countdown while reconnecting, without a reconnect button", () => {
    useUi.setState({ sessions: [session("reconnecting", { attempt: 3, nextRetryAt: Date.now() + 30_000 })] });
    renderBar(workspace([filesTab]), filesTab);
    expect(bar()?.textContent).toMatch(/正在重新连接（第 3 次，\d+ 秒后重试）/);
    expect(mounted!.container.querySelector("button")).toBeNull();
  });

  it("does not stack on a terminal pane that shows its own dead-session overlay", () => {
    const dead = { ...termTab, dead: true } as AppTab;
    useUi.setState({ sessions: [session("disconnected")] });
    renderBar(workspace([dead]), dead);
    expect(bar()).toBeNull();
    mounted?.unmount();
    renderBar(workspace([termTab]), termTab);
    expect(bar()).not.toBeNull();
  });
});

describe("workspace strip reconnect menu", () => {
  function openMenu(status: SessionRuntimeInfo["status"]) {
    const ws = workspace([]);
    useUi.setState({ sessions: [session(status)] });
    mounted = mount(createElement(ProjectWorkspaceStrip, {
      workspaces: [ws],
      activeWorkspaceId: "ws1",
      activeProject: projectFocusForGroup("g"),
      projectInventory: {
        groups: [{ id: "g", parentId: null, name: "g", sort: 0, createdAt: 1, updatedAt: 1 }],
        assets: [{ id: "a1", groupId: "g" } as never],
      },
      groups: [],
      onSelect: vi.fn(),
    }));
    const tab = mounted.container.querySelector('[data-project-workspace-id="ws1"]');
    if (!tab) throw new Error("tab missing");
    act(() => {
      tab.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 5, clientY: 5 }));
    });
  }
  const menuItem = () =>
    [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("重新连接"));

  it("offers 重新连接 only when disconnected, and runs the shared helper", () => {
    openMenu("disconnected");
    const item = menuItem();
    expect(item).toBeDefined();
    click(item as Element);
    expect(mocks.reconnect).toHaveBeenCalledWith("ws1");
  });

  it("offers nothing when connected", () => {
    openMenu("connected");
    expect(menuItem()).toBeUndefined();
  });
});
