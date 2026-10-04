/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  assetSearch: vi.fn(),
  sessionList: vi.fn(),
  vaultStatus: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: { list: mocks.assetList, search: mocks.assetSearch },
  sessionApi: {
    list: mocks.sessionList,
    connectLocal: vi.fn(),
    connect: vi.fn(),
    reconnect: vi.fn(),
    probe: vi.fn(),
  },
  vaultApi: { status: mocks.vaultStatus, listCredentials: vi.fn() },
  terminalApi: {},
  dbApi: {},
}));

vi.mock("../../features/terminal/TerminalPane", () => ({ TerminalPane: () => null }));
vi.mock("../../features/terminal/BackgroundSessions", () => ({ BackgroundSessions: () => null }));
vi.mock("../../features/files/FileBrowser", () => ({ FileBrowser: () => null }));
vi.mock("../../features/files/FileTree", () => ({ FileTree: () => null }));
vi.mock("../../features/files/MountPanel", () => ({ MountPanel: () => null }));
vi.mock("../../features/files/FileEditor", () => ({ FileEditor: () => null }));
vi.mock("../../features/forward/ForwardPanel", () => ({ ForwardPanel: () => null }));
vi.mock("../../features/docker/DockerPanel", () => ({ DockerPanel: () => null }));
vi.mock("../../features/db/DbPanel", () => ({ DbPanel: () => null }));
vi.mock("../../features/ai/AiSidebar", () => ({ AiSidebar: () => null }));
vi.mock("../../features/settings/SettingsView", () => ({ SettingsView: () => null }));
vi.mock("../../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../../features/credentials/CredentialsView", () => ({ CredentialsView: () => null }));
vi.mock("../../features/explorer/AssetTree", () => ({ AssetTree: () => null }));
vi.mock("../../app/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));

import App from "../../app/App";
import { useUi } from "../../app/store";

function keyDown(target: EventTarget, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

function tablist(container: ParentNode, label: string): HTMLElement {
  const list = container.querySelector<HTMLElement>(`[role="tablist"][aria-label="${label}"]`);
  if (!list) throw new Error(`tablist not found: ${label}`);
  return list;
}

function tabsOf(list: HTMLElement): HTMLElement[] {
  return [...list.querySelectorAll<HTMLElement>('[role="tab"]')];
}

function tabByText(list: HTMLElement, text: string): HTMLElement {
  const tab = tabsOf(list).find((candidate) => candidate.textContent?.includes(text));
  if (!tab) throw new Error(`tab not found: ${text}`);
  return tab;
}

let mounted: MountedView | undefined;

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(App)));
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.assetList.mockResolvedValue([]);
  mocks.assetSearch.mockResolvedValue([]);
  mocks.sessionList.mockResolvedValue([]);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  useUi.setState({
    sessions: [],
    toasts: [],
    leftOpen: false,
    rightOpen: false,
    leftMode: "assets",
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title: "ws-1",
        closable: true,
        sessionId: "s1",
        panes: [
          {
            id: "p1",
            activeTabId: "t1",
            tabs: [
              { id: "t1", kind: "settings", title: "设置", closable: true },
              { id: "t2", kind: "audit", title: "审计", closable: true },
            ],
          },
        ],
        activePaneId: "p1",
        splitRatio: 0.5,
      },
      {
        id: "ws2",
        kind: "session",
        title: "ws-2",
        closable: true,
        sessionId: "s2",
        panes: [
          {
            id: "p2",
            activeTabId: "t3",
            tabs: [{ id: "t3", kind: "settings", title: "设置", closable: true }],
          },
        ],
        activePaneId: "p2",
        splitRatio: 0.5,
      },
    ],
    activeWorkspaceId: "ws1",
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("workspace tab strip keyboard support", () => {
  it("exposes tablist/tab roles with aria-selected and roving tabindex", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container, "工作区");
    const [first, second] = tabsOf(list);
    expect(first.getAttribute("aria-selected")).toBe("true");
    expect(first.tabIndex).toBe(0);
    expect(second.getAttribute("aria-selected")).toBe("false");
    expect(second.tabIndex).toBe(-1);

    const panel = document.getElementById(first.getAttribute("aria-controls") ?? "");
    expect(panel?.getAttribute("role")).toBe("tabpanel");
    expect(panel?.getAttribute("aria-labelledby")).toBe(first.id);
  });

  it("switches workspaces with ArrowRight/ArrowLeft/Home/End and moves focus", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container, "工作区");
    const first = tabByText(list, "ws-1");
    act(() => first.focus());

    keyDown(first, "ArrowRight");
    expect(useUi.getState().activeWorkspaceId).toBe("ws2");
    expect(document.activeElement).toBe(tabByText(list, "ws-2"));
    expect(tabByText(list, "ws-2").getAttribute("aria-selected")).toBe("true");

    keyDown(document.activeElement as HTMLElement, "End");
    expect(useUi.getState().activeWorkspaceId).toBe("ws2");
    keyDown(document.activeElement as HTMLElement, "Home");
    expect(useUi.getState().activeWorkspaceId).toBe("ws1");
    expect(document.activeElement).toBe(first);

    keyDown(first, "ArrowLeft");
    expect(useUi.getState().activeWorkspaceId).toBe("ws2");
    keyDown(document.activeElement as HTMLElement, "Enter");
    expect(useUi.getState().activeWorkspaceId).toBe("ws2");
  });

  it("closes a workspace from a real, labeled button", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container, "工作区");
    const close = list.querySelector<HTMLButtonElement>('button[aria-label="关闭工作区 ws-1"]');
    expect(close).not.toBeNull();
    click(close as HTMLButtonElement);
    await waitFor(() => expect(useUi.getState().workspaces).toHaveLength(1));
    expect(useUi.getState().activeWorkspaceId).toBe("ws2");
  });
});

describe("pane tab strip keyboard support", () => {
  it("switches tabs with arrows and closes with a real, labeled button", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container, "标签页");
    const settings = tabByText(list, "设置");
    expect(settings.getAttribute("aria-selected")).toBe("true");
    act(() => settings.focus());

    keyDown(settings, "ArrowRight");
    expect(useUi.getState().workspaces[0]?.panes[0]?.activeTabId).toBe("t2");
    expect(document.activeElement).toBe(tabByText(list, "审计"));

    const close = list.querySelector<HTMLButtonElement>('button[aria-label="关闭标签 审计"]');
    expect(close).not.toBeNull();
    click(close as HTMLButtonElement);
    await waitFor(() =>
      expect(useUi.getState().workspaces[0]?.panes[0]?.tabs).toHaveLength(1),
    );
    expect(useUi.getState().workspaces[0]?.panes[0]?.activeTabId).toBe("t1");
  });

  it("opens the tab context menu with Shift+F10 and closes it with Escape", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container, "标签页");
    const settings = tabByText(list, "设置");
    act(() => settings.focus());

    keyDown(settings, "F10", { shiftKey: true });
    const menu = document.querySelector<HTMLElement>('[role="menu"]');
    expect(menu).not.toBeNull();
    expect(menu?.getAttribute("aria-label")).toBe("设置");
    expect(menu?.textContent).toContain("关闭标签");

    keyDown(document.activeElement as HTMLElement, "Escape");
    await waitFor(() => expect(document.querySelector('[role="menu"]')).toBeNull());
    expect(document.activeElement).toBe(settings);
  });

  it("never swallows Enter pressed on the tab's own close button", async () => {
    mounted = mountApp();
    await flush();

    const list = tablist(mounted.container, "标签页");
    const close = list.querySelector<HTMLButtonElement>('button[aria-label="关闭标签 设置"]');
    if (!close) throw new Error("close button not found");
    act(() => close.focus());

    // 标签的 keydown 不得 preventDefault，否则浏览器无法为按钮合成原生 click。
    const event = keyDown(close, "Enter");
    expect(event.defaultPrevented).toBe(false);
    expect(useUi.getState().workspaces[0]?.panes[0]?.activeTabId).toBe("t1");

    click(close);
    await waitFor(() => expect(useUi.getState().workspaces[0]?.panes[0]?.tabs).toHaveLength(1));
  });
});

describe("rail and toast accessibility", () => {
  it("labels every rail button and marks the active panel", async () => {
    mounted = mountApp();
    await flush();

    const buttons = [...mounted.container.querySelectorAll<HTMLButtonElement>(".nx-rail button")];
    expect(buttons.length).toBeGreaterThan(0);
    expect(buttons.every((b) => b.getAttribute("aria-label"))).toBe(true);
    const assets = buttons.find((b) => b.getAttribute("aria-label") === "资产");
    expect(assets?.getAttribute("aria-current")).toBe("true");
    const credentials = buttons.find((b) => b.getAttribute("aria-label") === "凭据");
    expect(credentials?.getAttribute("aria-current")).toBeNull();
  });

  it("renders the success toast with semantic tokens instead of raw colors", async () => {
    mounted = mountApp();
    await flush();

    act(() => {
      useUi.getState().pushToast("success", "已保存");
    });
    const toast = mounted.container.querySelector<HTMLElement>(".nx-toasts button");
    expect(toast).not.toBeNull();
    expect(toast?.textContent).toContain("已保存");
    expect(toast?.className).toContain("text-green-300");
    expect(toast?.className).not.toMatch(/#[0-9a-fA-F]{3,8}/);
  });
});
