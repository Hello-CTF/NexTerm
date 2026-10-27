/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, flushUntil, mount, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  sessionList: vi.fn(),
  vaultStatus: vi.fn(),
  layoutGet: vi.fn(),
  layoutPut: vi.fn(),
}));

vi.mock("../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("./helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../ipc/commands")>(), {
    assetApi: { list: mocks.assetList, search: vi.fn() },
    sessionApi: {
      list: mocks.sessionList,
      connectLocal: vi.fn(),
      connect: vi.fn(),
      reconnect: vi.fn(),
      probe: vi.fn(),
    },
    vaultApi: { status: mocks.vaultStatus, listCredentials: vi.fn() },
    layoutApi: { get: mocks.layoutGet, put: mocks.layoutPut },
    syncApi: {
      linkGet: vi.fn().mockResolvedValue({
        url: "",
        username: "",
        insecure: false,
        hasPassword: false,
        verifiedAt: 0,
        lastError: null,
      }),
      status: vi.fn().mockResolvedValue({ configured: false, loggedIn: false, lastError: "", warnings: [] }),
      kindOptInGet: vi.fn().mockResolvedValue({ knownHost: false, aiProfile: false, aiPermission: false, aiGrant: false, preferences: false }),
      wake: vi.fn().mockResolvedValue(undefined),
    },
  });
});

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
vi.mock("../app/JumpPalette", () => ({ JumpPalette: () => null }));
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
import { flushLayout, resetLayoutSyncForTest } from "../app/layout";
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
  mocks.layoutGet.mockResolvedValue({ revision: 0, updatedAt: 0, data: null });
  mocks.layoutPut.mockResolvedValue({ saved: true, revision: 1, conflict: false });
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
  resetLayoutSyncForTest();
});

describe("status bar credential vault state", () => {
  it("bootstraps the server layout on startup and saves local changes back through layoutApi", async () => {
    const remoteLayout = {
      v: 2,
      leftOpen: false,
      leftMode: "assets",
      rightOpen: true,
      leftWidth: 248,
      rightWidth: 352,
      workspaces: [
        {
          id: "remote-ws",
          kind: "tools",
          title: "远程工具",
          panes: [
            {
              id: "remote-pane",
              tabs: [{ id: "remote-tab", kind: "history", title: "History", closable: true }],
              activeTabId: "remote-tab",
            },
          ],
          activePaneId: "remote-pane",
          closable: true,
        },
      ],
      activeWorkspaceId: "remote-ws",
      aiBoards: {},
    };
    mocks.layoutGet.mockResolvedValue({ revision: 7, updatedAt: 0, data: remoteLayout });
    mocks.layoutPut.mockResolvedValue({ saved: true, revision: 8, conflict: false });
    mocks.vaultStatus.mockResolvedValue({ initialized: false, unlocked: false });
    mounted = mountApp();
    await flushUntil(() => useUi.getState().layoutSyncCompleted);

    // The mocked remote layout is applied on boot instead of the fetch
    // error being swallowed by the layout bootstrap.
    expect(mocks.layoutGet).toHaveBeenCalledWith();
    const state = useUi.getState();
    expect(state.workspaces.map((w) => w.id)).toContain("remote-ws");
    expect(state.activeWorkspaceId).toBe("remote-ws");
    expect(state.layoutSyncCompleted).toBe(true);

    // A local change is saved back through layoutApi.put with the remote
    // workspace preserved in the serialized layout.
    click(vaultStatusControl() as HTMLElement);
    await flush();
    await flushLayout();
    expect(mocks.layoutPut).toHaveBeenCalled();
    const savedLayout = JSON.parse(mocks.layoutPut.mock.calls[0][0] as string);
    expect(savedLayout.workspaces.some((w: { id: string }) => w.id === "remote-ws")).toBe(true);
  });

  it("renders a clickable enable-protection entry when uninitialized", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: false, unlocked: false });
    mounted = mountApp();
    // The status bar shows the vault state once react-query delivers it; a single flush does not guarantee that.
    await flushUntil(() => vaultStatusControl() !== null);

    const control = vaultStatusControl();
    expect(control?.tagName).toBe("BUTTON");
    expect(control?.textContent).toContain("凭据库未初始化");
    expect(control?.textContent).toContain("去开启保护");
    expect(control?.getAttribute("title")).toContain("凭据保护");
  });

  it("opens the settings tab and locates the credential protection card after clicking the entry", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: false, unlocked: false });
    mounted = mountApp();
    // The status bar shows the vault state once react-query delivers it; a single flush does not guarantee that.
    await flushUntil(() => vaultStatusControl() !== null);

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

  it("migrates an existing settings tab into Tools and scrolls that settings page", async () => {
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
          closable: true,
        },
        {
          id: "ws-b",
          kind: "tools",
          title: "Tools",
          panes: [{ id: "pane-b", tabs: [], activeTabId: null }],
          activePaneId: "pane-b",
          closable: true,
        },
      ],
      activeWorkspaceId: "ws-b",
    });
    mounted = mountApp();
    // The status bar shows the vault state once react-query delivers it; a single flush does not guarantee that.
    await flushUntil(() => vaultStatusControl() !== null);

    click(vaultStatusControl() as HTMLElement);
    await flush();

    const wsA = useUi.getState().workspaces.find((w) => w.id === "ws-a");
    const wsB = useUi.getState().workspaces.find((w) => w.id === "ws-b");
    expect(wsA?.panes.flatMap((p) => p.tabs)).toHaveLength(0);
    expect(wsA?.panes[0].activeTabId).toBeNull();
    expect(wsB?.panes.flatMap((p) => p.tabs).map((t) => t.kind)).toEqual(["settings"]);
    expect(wsB?.title).toBe("工具");
    expect(useUi.getState().activeWorkspaceId).toBe("ws-b");

    const card = [...document.querySelectorAll<HTMLElement>("#nx-ws-panel-ws-b .nx-card")].find(
      (c) => c.textContent?.includes("凭据保护"),
    );
    expect(card).toBeTruthy();
    const scroll = vi.fn();
    (card as HTMLElement).scrollIntoView = scroll;

    click(vaultStatusControl() as HTMLElement);
    await flush();

    expect(scroll).toHaveBeenCalledWith({ block: "start" });
  });

  it("finds the settings card when the workspace shows two tab panels side by side", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: false, unlocked: false });
    useUi.setState({
      workspaces: [
        {
          id: "ws-b",
          kind: "tools",
          title: "工具",
          panes: [
            { id: "pane-a", tabs: [{ id: "a-audit", kind: "audit", title: "Audit", closable: true }], activeTabId: "a-audit" },
            { id: "pane-b", tabs: [{ id: "z-settings", kind: "settings", title: "Settings", closable: true }], activeTabId: "z-settings" },
          ],
          activePaneId: "pane-a",
          layoutTree: {
            type: "split",
            id: "root",
            dir: "row",
            ratio: 0.5,
            a: { type: "leaf", id: "leaf-a", paneId: "pane-a" },
            b: { type: "leaf", id: "leaf-b", paneId: "pane-b" },
          },
          closable: true,
        },
      ],
      activeWorkspaceId: "ws-b",
    });
    mounted = mountApp();
    await flushUntil(() => vaultStatusControl() !== null);
    await flush();

    expect(document.querySelectorAll("#nx-ws-panel-ws-b [data-tab-content]:not(.hidden) [role='tabpanel']")).toHaveLength(2);
    const card = [...document.querySelectorAll<HTMLElement>("#nx-ws-panel-ws-b .nx-card")].find(
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
    // The status bar shows the vault state once react-query delivers it; a single flush does not guarantee that.
    await flushUntil(() => vaultStatusControl() !== null);

    const control = vaultStatusControl();
    expect(control?.tagName).toBe("SPAN");
    expect(control?.textContent).toBe("凭据库已锁定");
    expect(control?.textContent).not.toContain("去开启保护");
  });

  it("keeps the unlocked state as plain text", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
    mounted = mountApp();
    // The status bar shows the vault state once react-query delivers it; a single flush does not guarantee that.
    await flushUntil(() => vaultStatusControl() !== null);

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
    // The status bar shows the vault state once react-query delivers it; a single flush does not guarantee that.
    await flushUntil(() => vaultStatusControl() !== null);

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
