/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
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
vi.mock("../../features/settings/SettingsView", () => ({
  SettingsView: (): never => {
    throw new Error("settings exploded");
  },
}));
vi.mock("../../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../../features/explorer/AssetTree", () => ({
  AssetTree: (): never => {
    throw new Error("asset tree exploded");
  },
}));
vi.mock("../../app/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));

import App from "../../app/App";
import { useUi } from "../../app/store";

let mounted: MountedView | undefined;

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(App)));
}

function seedWorkspace(): void {
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
    ],
    activeWorkspaceId: "ws1",
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.spyOn(console, "error").mockImplementation(() => {});
  document.body.replaceChildren();
  mocks.assetList.mockResolvedValue([]);
  mocks.assetSearch.mockResolvedValue([]);
  mocks.sessionList.mockResolvedValue([]);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  seedWorkspace();
});

afterEach(() => {
  vi.restoreAllMocks();
  mounted?.unmount();
  mounted = undefined;
});

describe("panel-level error boundary", () => {
  it("contains a crashing panel and closes only that tab", async () => {
    mounted = mountApp();
    await flush();

    const panel = mounted.container.querySelector<HTMLElement>("#nx-tab-panel-ws1-p1-t1");
    expect(panel).not.toBeNull();
    const alert = panel?.querySelector<HTMLElement>('[role="alert"]');
    expect(alert).not.toBeNull();
    expect(alert?.textContent).toContain("这个面板出了点问题");
    expect(alert?.textContent).toContain("settings exploded");

    const tablist = mounted.container.querySelector('[role="tablist"][aria-label="标签页"]');
    expect(tablist?.textContent).toContain("审计");

    const close = panel?.querySelector<HTMLButtonElement>('[role="alert"] button:last-child');
    expect(close?.textContent).toBe("关闭标签");
    if (!close) throw new Error("close button missing");
    click(close);
    await waitFor(() =>
      expect(useUi.getState().workspaces[0]?.panes[0]?.tabs).toHaveLength(1),
    );
    expect(useUi.getState().workspaces[0]?.panes[0]?.activeTabId).toBe("t2");
    expect(mounted.container.textContent).not.toContain("这个面板出了点问题");
    expect(mounted.container.querySelector('[role="tablist"]')).not.toBeNull();
  });
});

describe("shell-level error boundary", () => {
  it("falls back to a retry-only shell when chrome outside any panel crashes", async () => {
    useUi.setState({ leftOpen: true });
    mounted = mountApp();
    await flush();

    const alert = mounted.container.querySelector<HTMLElement>('[role="alert"]');
    expect(alert).not.toBeNull();
    expect(alert?.textContent).toContain("应用界面出了点问题");
    expect(alert?.textContent).toContain("asset tree exploded");

    const buttons = [...(alert?.querySelectorAll<HTMLButtonElement>("button") ?? [])];
    expect(buttons.map((b) => b.textContent)).toEqual(["重试"]);

    click(buttons[0]);
    await flush();
    expect(mounted.container.querySelector('[role="alert"]')?.textContent).toContain(
      "应用界面出了点问题",
    );
  });
});
