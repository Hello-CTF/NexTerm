/** @vitest-environment jsdom */
// Browser mode always relays: a device workspace's tabs show no path line and no 连接方式 submenu, and the
// connection path commands and event are never used.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    getMode: vi.fn(),
    setMode: vi.fn(),
    status: vi.fn(),
    diagnostics: vi.fn(),
    onPathChange: vi.fn(),
  };
});

vi.mock("../../ipc/fleetPathApi", () => ({
  DEVICE_PATH_EVENT: "fleet://device-path",
  fleetPathApi: mocks,
}));

import { PaneGroup } from "../../app/workspace/PaneGroup";
import { useUi, type Workspace } from "../../app/store";
import { connectionModeMenu, resetDevicePathForTest } from "../../features/fleet/devicePath";

const tab = { id: "dev-tab", kind: "terminal" as const, title: "终端 1", closable: true, sessionId: "session-dev" };
const DEVICE_WS: Workspace = {
  id: "ws-dev",
  kind: "session",
  title: "web-01",
  sessionId: "session-dev",
  assetId: "device/d-1",
  assetKind: "device",
  panes: [{ id: "dev-pane", tabs: [tab], activeTabId: tab.id }],
  activePaneId: "dev-pane",
  closable: true,
};

let mounted: MountedView | undefined;

beforeEach(() => {
  document.body.replaceChildren();
  resetDevicePathForTest();
  useUi.setState({
    workspaces: [DEVICE_WS],
    sessions: [{ id: "session-dev", assetId: "device/d-1", name: "web-01", kind: "ssh", status: "connected", tabs: [], createdAt: 1 }],
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("device connection path in browser mode", () => {
  it("hides the submenu and the tooltip line and sends nothing", async () => {
    mounted = mount(createElement(PaneGroup, {
      pane: DEVICE_WS.panes[0],
      idPrefix: "p",
      active: true,
      stripRef: () => undefined,
      split: false,
      singlePane: true,
      paneNumber: 1,
      paneCount: 1,
      splitAllowed: true,
      moveTargets: [],
      onSplit: vi.fn(),
      onClosePane: vi.fn(),
      onCyclePane: vi.fn(),
      onActivate: vi.fn(),
      onNewTerminal: vi.fn(),
      workspaceId: DEVICE_WS.id,
      leftOpen: true,
      onToggleLeft: vi.fn(),
    }));
    await flush();
    const element = document.querySelector<HTMLElement>("[data-tab-id='dev-tab']");
    if (!element) throw new Error("tab not found");
    expect(element.title).toBe("终端 1");

    act(() => {
      element.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, clientX: 10, clientY: 10 }));
    });
    const labels = [...document.querySelectorAll(".nx-menu .nx-menu-label")].map((label) => label.textContent);
    expect(labels).toContain("关闭标签页");
    expect(labels).not.toContain("连接方式");
    expect(connectionModeMenu("d-1")).toEqual([]);

    for (const fn of Object.values(mocks)) expect(fn).not.toHaveBeenCalled();
  });
});
