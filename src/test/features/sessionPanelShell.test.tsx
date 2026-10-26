/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  sessionList: vi.fn(),
  vaultStatus: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: { list: mocks.assetList, search: vi.fn().mockResolvedValue([]) },
  sessionApi: { list: mocks.sessionList, connectLocal: vi.fn(), connect: vi.fn(), reconnect: vi.fn(), probe: vi.fn() },
  vaultApi: { status: mocks.vaultStatus, listCredentials: vi.fn() },
  syncApi: {
    linkGet: vi.fn().mockResolvedValue({ url: "", username: "", insecure: false, hasPassword: false, verifiedAt: 0, lastError: null }),
  },
  layoutApi: {
    get: vi.fn().mockResolvedValue({ revision: 0, updatedAt: 0, data: null }),
    put: vi.fn().mockResolvedValue({ saved: true, revision: 1, conflict: false }),
  },
  terminalApi: {},
  dbApi: {},
}));

vi.mock("../../features/terminal/TerminalPane", () => ({ TerminalPane: () => null }));
vi.mock("../../features/terminal/BackgroundSessions", () => ({ BackgroundSessions: () => null }));
vi.mock("../../features/files/FileBrowser", () => ({ FileBrowser: () => null }));
vi.mock("../../features/files/FileTree", () => ({ FileTree: () => <div data-testid="file-tree" /> }));
vi.mock("../../features/files/MountPanel", () => ({ MountPanel: () => null }));
vi.mock("../../features/files/FileEditor", () => ({ FileEditor: () => null }));
vi.mock("../../features/forward/ForwardPanel", () => ({ ForwardPanel: () => null }));
vi.mock("../../features/docker/DockerPanel", () => ({ DockerPanel: () => null }));
vi.mock("../../features/db/DbPanel", () => ({ DbPanel: () => null }));
vi.mock("../../features/ai/AiSidebar", () => ({ AiSidebar: () => <div data-testid="ai-sidebar" /> }));
vi.mock("../../features/settings/SettingsView", () => ({ SettingsView: () => null }));
vi.mock("../../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../../features/explorer/AssetTree", () => ({ AssetTree: () => null }));
vi.mock("../../app/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../../app/QuickConnect", () => ({ QuickConnect: () => null }));
vi.mock("../../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));

import App from "../../app/App";
import { useUi } from "../../app/store";
import { bindingConflicts, getKeybindings, resetAllKeybindings, setKeybinding } from "../../app/keybindings";
import { sanitizeLayout } from "../../app/layout";
import { loadDeviceLayout } from "../../app/layoutFocus";
import { terminalShortcutConflicts } from "../../features/terminal/terminalInput/shortcutConflicts";
import { resetSessionPanelForTest } from "../../features/session-panel/sessionPanelStore";
import { useThrottledValue } from "../../features/session-panel/useThrottledValue";

let mounted: MountedView | undefined;
const realInnerWidth = window.innerWidth;

function keyDown(key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    window.dispatchEvent(event);
  });
  return event;
}

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(App)));
}

function panel(): HTMLElement | null {
  return document.querySelector('section[aria-label="会话侧栏"]');
}

async function settlePanel(): Promise<void> {
  await flushUntil(() => panel() !== null, 200);
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  window.localStorage.clear();
  resetSessionPanelForTest();
  mocks.assetList.mockResolvedValue([]);
  mocks.sessionList.mockResolvedValue([{ id: "s1", assetId: null, name: "web-01", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }]);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  useUi.setState({
    sessions: [{ id: "s1", assetId: null, name: "web-01", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }],
    toasts: [],
    leftOpen: false,
    rightOpen: false,
    leftMode: "assets",
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title: "web-01",
        closable: true,
        sessionId: "s1",
        panes: [
          {
            id: "p1",
            activeTabId: "t1",
            tabs: [{ id: "t1", kind: "terminal", title: "终端 1", sessionId: "s1", tabId: "k1", closable: true }],
          },
        ],
        activePaneId: "p1",
      },
    ],
    activeWorkspaceId: "ws1",
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  resetAllKeybindings();
  Object.defineProperty(window, "innerWidth", { configurable: true, value: realInnerWidth });
});

describe("session side panel in the shell", () => {
  it("toggles with the default shortcut and remembers the state on this device", async () => {
    mounted = mountApp();
    await flush();
    expect(panel()).toBeNull();

    const open = keyDown("e", { ctrlKey: true, shiftKey: true });
    expect(open.defaultPrevented).toBe(true);
    await settlePanel();
    expect(panel()).not.toBeNull();
    expect(JSON.parse(window.localStorage.getItem("nexterm.sessionPanel.v1") ?? "{}").open).toBe(true);
    expect(document.querySelector('[data-testid="file-tree"]')).not.toBeNull();

    keyDown("e", { ctrlKey: true, shiftKey: true });
    await flush();
    expect(panel()).toBeNull();
    expect(JSON.parse(window.localStorage.getItem("nexterm.sessionPanel.v1") ?? "{}").open).toBe(false);
  });

  it("honours a rebound shortcut", async () => {
    setKeybinding("toggleSessionPanel", "Alt+Shift+f");
    mounted = mountApp();
    await flush();
    expect(keyDown("e", { ctrlKey: true, shiftKey: true }).defaultPrevented).toBe(false);
    keyDown("f", { altKey: true, shiftKey: true });
    await settlePanel();
    expect(panel()).not.toBeNull();
  });

  it("toggles from the header button and docks beside the AI sidebar", async () => {
    useUi.setState({ rightOpen: true });
    mounted = mountApp();
    await flush();
    const toggle = document.querySelector<HTMLButtonElement>('button[aria-label="会话侧栏"]')!;
    expect(toggle.getAttribute("aria-pressed")).toBe("false");
    click(toggle);
    await settlePanel();
    expect(toggle.getAttribute("aria-pressed")).toBe("true");
    const dock = document.querySelector(".nx-session-dock")!;
    expect(dock.classList.contains("is-overlay")).toBe(false);
    // Docked left of the AI sidebar, which stays open.
    expect(dock.nextElementSibling?.classList.contains("nx-right-dock")).toBe(true);
    expect(document.querySelector('[data-testid="ai-sidebar"]')).not.toBeNull();
    expect(useUi.getState().rightOpen).toBe(true);
  });

  it("opens as a full-screen sheet over the workspace on narrow screens", async () => {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 390 });
    mounted = mountApp();
    await flush();
    keyDown("e", { ctrlKey: true, shiftKey: true });
    await settlePanel();
    expect(document.querySelector(".nx-session-dock")?.classList.contains("is-overlay")).toBe(true);
    expect(document.querySelector(".nx-dock-backdrop")).not.toBeNull();

    click(document.querySelector<HTMLButtonElement>('button[aria-label="收起会话侧栏"]')!);
    await flush();
    expect(panel()).toBeNull();
  });

  it("no longer offers the file tree in the left rail", async () => {
    mounted = mountApp();
    await flush();
    const rail = document.querySelector('nav[aria-label="主导航"]')!;
    expect(rail.querySelector('button[aria-label="文件树"]')).toBeNull();
    expect(rail.querySelector('button[aria-label="资产"]')).not.toBeNull();
    expect(rail.querySelector('button[aria-label="凭据"]')).not.toBeNull();
  });
});

describe("session panel shortcut", () => {
  it("has no default conflict with other shortcuts or terminal input", () => {
    const bindings = getKeybindings();
    expect(bindings.toggleSessionPanel).toBe("Mod+Shift+e");
    for (const mac of [false, true]) {
      expect(bindingConflicts(bindings, mac).filter((c) => c.actions.some((a) => a.id === "toggleSessionPanel"))).toEqual([]);
      const withheld = terminalShortcutConflicts(bindings, mac, false).find((c) => c.action.id === "toggleSessionPanel");
      expect(withheld?.bytes ?? null).toBeNull();
    }
  });
});

describe("retired left files mode", () => {
  it("reads a stored files mode as the asset tree", () => {
    const layout = sanitizeLayout({ v: 4, leftOpen: true, leftMode: "files", rightOpen: true, workspaces: [], activeWorkspaceId: null, aiBoards: {} });
    expect(layout?.leftMode).toBe("assets");
    const kept = sanitizeLayout({ v: 4, leftOpen: true, leftMode: "credentials", rightOpen: true, workspaces: [], activeWorkspaceId: null, aiBoards: {} });
    expect(kept?.leftMode).toBe("credentials");
  });

  it("keeps a device layout saved with the files mode and reads it as the asset tree", () => {
    window.localStorage.setItem(
      "device-layout-test",
      JSON.stringify({
        v: 1,
        leftOpen: true,
        leftMode: "files",
        rightOpen: false,
        leftWidth: 300,
        rightWidth: 400,
        activeWorkspace: null,
        workspaces: [],
        aiBoards: [],
      }),
    );
    const layout = loadDeviceLayout("device-layout-test");
    expect(layout).toMatchObject({ leftMode: "assets", leftWidth: 300, rightOpen: false });
  });
});

describe("useThrottledValue", () => {
  function Probe({ value, resetKey, seen }: { value: string; resetKey: string; seen: string[] }) {
    const out = useThrottledValue(value, 400, resetKey);
    seen.push(out);
    return null;
  }

  it("passes the first change at once, collapses a burst into the latest value, and resets per key", () => {
    vi.useFakeTimers();
    vi.setSystemTime(10_000);
    const seen: string[] = [];
    let setProps: (props: { value: string; resetKey: string }) => void = () => {};
    function Host() {
      const [props, set] = useState({ value: "/a", resetKey: "k1" });
      setProps = set;
      return createElement(Probe, { ...props, seen });
    }
    mounted = mount(createElement(Host));
    act(() => {
      vi.advanceTimersByTime(500);
    });

    act(() => setProps({ value: "/b", resetKey: "k1" }));
    expect(seen.at(-1)).toBe("/b");
    act(() => setProps({ value: "/c", resetKey: "k1" }));
    act(() => setProps({ value: "/d", resetKey: "k1" }));
    expect(seen.at(-1)).toBe("/b");
    act(() => {
      vi.advanceTimersByTime(400);
    });
    expect(seen.at(-1)).toBe("/d");
    expect(seen).not.toContain("/c");

    act(() => setProps({ value: "/other", resetKey: "k2" }));
    expect(seen.at(-1)).toBe("/other");
  });
});
