/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, flushUntil, mount, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  sessionList: vi.fn(),
  vaultStatus: vi.fn(),
}));

vi.mock("../ipc/commands", () => ({
  assetApi: { list: mocks.assetList, search: vi.fn() },
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

vi.mock("../features/terminal/TerminalPane", () => ({ TerminalPane: () => null }));
vi.mock("../features/terminal/BackgroundSessions", () => ({ BackgroundSessions: () => null }));
vi.mock("../features/files/FileBrowser", () => ({ FileBrowser: () => null }));
vi.mock("../features/files/FileTree", () => ({ FileTree: () => null }));
vi.mock("../features/files/MountPanel", () => ({ MountPanel: () => null }));
vi.mock("../features/files/FileEditor", () => ({ FileEditor: () => null }));
vi.mock("../features/forward/ForwardPanel", () => ({ ForwardPanel: () => null }));
vi.mock("../features/docker/DockerPanel", () => ({ DockerPanel: () => null }));
vi.mock("../features/db/DbPanel", () => ({ DbPanel: () => null }));
vi.mock("../features/ai/AiSidebar", () => ({ AiSidebar: () => null }));
vi.mock("../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../features/explorer/AssetTree", () => ({ AssetTree: () => null }));
vi.mock("../app/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));

vi.mock("../features/settings/SettingsView", async () => {
  const { createElement: h } = await import("react");
  const { useQuery } = await import("@tanstack/react-query");
  return {
    SettingsView: () => {
      const status = useQuery({ queryKey: ["vault-status"], queryFn: () => mocks.vaultStatus() });
      return h(
        "div",
        null,
        h("section", { className: "nx-card" }, h("span", { className: "nx-card-title" }, "Terminal")),
        h(
          "section",
          { className: "nx-card" },
          h("span", { className: "nx-card-title" }, "凭据保护"),
          h(
            "span",
            { "data-testid": "settings-vault-state" },
            status.data?.initialized ? "已初始化" : "未初始化",
          ),
        ),
      );
    },
  };
});

import App from "../app/App";
import { useUi } from "../app/store";

let mounted: MountedView | undefined;
let client: QueryClient | undefined;

function mountApp(): MountedView {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(App)));
}

function vaultStatusControl(): HTMLElement | null {
  const info = document.querySelector<HTMLElement>(".nx-statusbar-info");
  if (!info) return null;
  for (const el of info.querySelectorAll<HTMLElement>("span, button")) {
    if (el.textContent?.includes("凭据库")) return el;
  }
  return null;
}

function openTabKinds(): string[] {
  return useUi
    .getState()
    .workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs))
    .map((t) => t.kind);
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  Element.prototype.scrollIntoView = vi.fn();
  mocks.assetList.mockResolvedValue([]);
  mocks.sessionList.mockResolvedValue([]);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  useUi.setState({
    toasts: [],
    leftOpen: true,
    rightOpen: false,
    leftMode: "assets",
    sessions: [],
    workspaces: [],
    activeWorkspaceId: null,
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("status bar credential vault state", () => {
  it("renders a clickable enable-protection entry when uninitialized", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: false, unlocked: false });
    mounted = mountApp();
    await flush();

    const control = vaultStatusControl();
    expect(control?.tagName).toBe("BUTTON");
    expect(control?.textContent).toContain("凭据库未初始化");
    expect(control?.textContent).toContain("去开启保护");
    expect(control?.getAttribute("title")).toContain("凭据保护");
  });

  it("opens the settings tab and locates the credential protection card after clicking the entry", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: false, unlocked: false });
    mounted = mountApp();
    await flush();

    click(vaultStatusControl() as HTMLElement);
    await flush();

    expect(openTabKinds()).toContain("settings");
    const card = [...document.querySelectorAll<HTMLElement>(".nx-card")].find((c) =>
      c.textContent?.includes("凭据保护"),
    );
    expect(card).toBeTruthy();
    const scroll = vi.fn();
    (card as HTMLElement).scrollIntoView = scroll;

    click(vaultStatusControl() as HTMLElement);
    await flush();

    expect(scroll).toHaveBeenCalledWith({ block: "start" });
  });

  it("deduplicates and activates an existing settings tab in another workspace and scrolls that settings page", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: false, unlocked: false });
    useUi.setState({
      workspaces: [
        {
          id: "ws-a",
          kind: "session",
          title: "web-01",
          panes: [
            {
              id: "pane-a",
              tabs: [{ id: "settings", kind: "settings", title: "Settings", closable: true }],
              activeTabId: "settings",
            },
          ],
          activePaneId: "pane-a",
          splitRatio: 0.5,
          closable: true,
        },
        {
          id: "ws-b",
          kind: "tools",
          title: "Tools",
          panes: [{ id: "pane-b", tabs: [], activeTabId: null }],
          activePaneId: "pane-b",
          splitRatio: 0.5,
          closable: true,
        },
      ],
      activeWorkspaceId: "ws-b",
    });
    mounted = mountApp();
    await flush();

    click(vaultStatusControl() as HTMLElement);
    await flush();

    const wsA = useUi.getState().workspaces.find((w) => w.id === "ws-a");
    const wsB = useUi.getState().workspaces.find((w) => w.id === "ws-b");
    expect(wsA?.panes.flatMap((p) => p.tabs).map((t) => t.kind)).toEqual(["settings"]);
    expect(wsB?.panes.flatMap((p) => p.tabs)).toHaveLength(0);
    expect(useUi.getState().activeWorkspaceId).toBe("ws-a");

    const card = [...document.querySelectorAll<HTMLElement>("#nx-ws-panel-ws-a .nx-card")].find(
      (c) => c.textContent?.includes("凭据保护"),
    );
    expect(card).toBeTruthy();
    const scroll = vi.fn();
    (card as HTMLElement).scrollIntoView = scroll;

    click(vaultStatusControl() as HTMLElement);
    await flush();

    expect(scroll).toHaveBeenCalledWith({ block: "start" });
  });

  it("keeps the locked state as plain text without an enable-protection entry", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: false });
    mounted = mountApp();
    await flush();

    const control = vaultStatusControl();
    expect(control?.tagName).toBe("SPAN");
    expect(control?.textContent).toBe("凭据库已锁定");
    expect(control?.textContent).not.toContain("去开启保护");
  });

  it("keeps the unlocked state as plain text", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
    mounted = mountApp();
    await flush();

    const control = vaultStatusControl();
    expect(control?.tagName).toBe("SPAN");
    expect(control?.textContent).toBe("凭据库已解锁");
  });

  it("shows unavailable without an enable-protection entry when the status request fails", async () => {
    mocks.vaultStatus.mockRejectedValue(new Error("boom"));
    mounted = mountApp();
    // react-query settles its failure state via internal scheduling; a single flush does not guarantee the timing
    await flushUntil(() => vaultStatusControl()?.textContent === "凭据库不可用");

    const control = vaultStatusControl();
    expect(control?.tagName).toBe("SPAN");
    expect(control?.textContent).toBe("凭据库不可用");
  });

  it("refreshes an open settings page through the shared vault-status query after initialization elsewhere", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: false, unlocked: false });
    mounted = mountApp();
    await flush();

    click(vaultStatusControl() as HTMLElement);
    await flush();
    expect(openTabKinds()).toContain("settings");
    const state = () => document.querySelector('[data-testid="settings-vault-state"]');
    expect(state()?.textContent).toBe("未初始化");

    mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
    await act(() => client!.invalidateQueries({ queryKey: ["vault-status"] }));
    await flush();

    expect(state()?.textContent).toBe("已初始化");
  });
});
