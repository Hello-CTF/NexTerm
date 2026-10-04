/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flush, mount, type MountedView } from "./reactTestUtils";

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
vi.mock("../../app/CommandPalette", () => ({
  CommandPalette: () => <div data-testid="command-palette" />,
}));
vi.mock("../../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));

import App from "../../app/App";
import { useUi } from "../../app/store";
import { getKeybinding, resetAllKeybindings, setKeybinding } from "../../app/keybindings";

function keyDown(target: EventTarget, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

let mounted: MountedView | undefined;

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(App)));
}

function activePaneTabId(): string | null {
  return useUi.getState().workspaces[0]?.panes[0]?.activeTabId ?? null;
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
              { id: "t3", kind: "settings", title: "重命名过的标签", closable: true },
            ],
          },
        ],
        activePaneId: "p1",
        splitRatio: 0.5,
      },
    ],
    activeWorkspaceId: "ws1",
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  resetAllKeybindings();
});

describe("Ctrl+1-9 tab navigation", () => {
  it("switches to the nth tab of the active pane", async () => {
    mounted = mountApp();
    await flush();

    const second = keyDown(window, "2", { ctrlKey: true });
    expect(second.defaultPrevented).toBe(true);
    expect(activePaneTabId()).toBe("t2");

    keyDown(window, "3", { ctrlKey: true });
    expect(activePaneTabId()).toBe("t3");

    const first = keyDown(window, "1", { ctrlKey: true });
    expect(first.defaultPrevented).toBe(true);
    expect(activePaneTabId()).toBe("t1");
  });

  it("keeps renamed tabs addressable by position", async () => {
    mounted = mountApp();
    await flush();
    keyDown(window, "3", { ctrlKey: true });
    expect(activePaneTabId()).toBe("t3");
    const titles = useUi.getState().workspaces[0]?.panes[0]?.tabs.map((t) => t.title);
    expect(titles?.[2]).toBe("重命名过的标签");
  });

  it("does not swallow digits beyond the tab count", async () => {
    mounted = mountApp();
    await flush();
    const event = keyDown(window, "9", { ctrlKey: true });
    expect(event.defaultPrevented).toBe(false);
    expect(activePaneTabId()).toBe("t1");
  });

  it("honours a rebound switchTab binding", async () => {
    setKeybinding("switchTab", "Alt+1-9");
    mounted = mountApp();
    await flush();
    keyDown(window, "2", { altKey: true });
    expect(activePaneTabId()).toBe("t2");
    const legacy = keyDown(window, "1", { ctrlKey: true });
    expect(legacy.defaultPrevented).toBe(false);
    expect(activePaneTabId()).toBe("t2");
  });
});

describe("rebound app shortcuts", () => {
  it("opens the palette via the new binding and not the old one", async () => {
    setKeybinding("commandPalette", "Mod+Shift+O");
    mounted = mountApp();
    await flush();

    keyDown(window, "o", { ctrlKey: true, shiftKey: true });
    expect(mounted.container.querySelector('[data-testid="command-palette"]')).not.toBeNull();

    mounted.unmount();
    mounted = mountApp();
    await flush();
    keyDown(window, "p", { ctrlKey: true, shiftKey: true });
    expect(mounted.container.querySelector('[data-testid="command-palette"]')).toBeNull();
    keyDown(window, "o", { ctrlKey: true, shiftKey: true });
    expect(mounted.container.querySelector('[data-testid="command-palette"]')).not.toBeNull();
  });

  it("closes the active tab via a rebound closeTab binding", async () => {
    setKeybinding("closeTab", "Mod+Shift+W");
    mounted = mountApp();
    await flush();

    keyDown(window, "w", { ctrlKey: true });
    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs).toHaveLength(3);

    keyDown(window, "w", { ctrlKey: true, shiftKey: true });
    await flush();
    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs).toHaveLength(2);
    expect(getKeybinding("closeTab")).toBe("Mod+Shift+w");
  });

  it("persists reboundings across remounts through localStorage", async () => {
    setKeybinding("toggleSidebar", "Mod+Shift+B");
    mounted = mountApp();
    await flush();
    expect(useUi.getState().leftOpen).toBe(false);
    keyDown(window, "b", { ctrlKey: true, shiftKey: true });
    await flush();
    expect(useUi.getState().leftOpen).toBe(true);

    mounted.unmount();
    useUi.setState({ leftOpen: false });
    mounted = mountApp();
    await flush();
    expect(useUi.getState().leftOpen).toBe(false);
    keyDown(window, "b", { ctrlKey: true, shiftKey: true });
    await flush();
    expect(useUi.getState().leftOpen).toBe(true);
    const plain = keyDown(window, "b", { ctrlKey: true });
    expect(plain.defaultPrevented).toBe(false);
  });

  it("leaves the shortcut inert when its binding is cleared", async () => {
    setKeybinding("toggleAiSidebar", null);
    mounted = mountApp();
    await flush();
    const event = keyDown(window, "j", { ctrlKey: true });
    expect(event.defaultPrevented).toBe(false);
    expect(useUi.getState().rightOpen).toBe(false);
  });
});
