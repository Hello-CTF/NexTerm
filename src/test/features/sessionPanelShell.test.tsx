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
vi.mock("../../app/JumpPalette", () => ({ JumpPalette: () => null }));
vi.mock("../../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));

import App from "../../app/App";
import { WEB } from "../../ipc/env";
import { useUi } from "../../app/store";
import { bindingConflicts, getKeybindings, KEYBINDING_ACTIONS, resetAllKeybindings, setKeybinding } from "../../app/keybindings";
import { sanitizeLayout } from "../../app/layout";
import { loadDeviceLayout } from "../../app/layoutFocus";
import { terminalShortcutConflicts } from "../../features/terminal/terminalInput/shortcutConflicts";
import { resetSessionPanelForTest, useSessionPanel } from "../../features/session-panel/sessionPanelStore";
import { sessionPanelFits, WORKSPACE_MAIN_MIN_WIDTH } from "../../features/session-panel/sessionPanelFit";
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

// The default toggle: Ctrl+Alt+E in the web build (browsers keep Ctrl+Shift+E), Mod+Shift+E on desktop.
function pressToggle(): KeyboardEvent {
  return keyDown("e", WEB ? { ctrlKey: true, altKey: true } : { ctrlKey: true, shiftKey: true });
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

    const open = pressToggle();
    expect(open.defaultPrevented).toBe(true);
    await settlePanel();
    expect(panel()).not.toBeNull();
    expect(JSON.parse(window.localStorage.getItem("nexterm.sessionPanel.v1") ?? "{}").open).toBe(true);
    expect(document.querySelector('[data-testid="file-tree"]')).not.toBeNull();

    pressToggle();
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
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 1600 });
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
    pressToggle();
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

describe("session panel width fit", () => {
  it("docks only while the workspace keeps its minimum width", () => {
    const docks = { leftOpen: true, leftWidth: 248, rightOpen: true, rightWidth: 352, panelWidth: 300 };
    expect(sessionPanelFits({ ...docks, viewportWidth: 900 })).toBe(false);
    expect(sessionPanelFits({ ...docks, viewportWidth: 1600 })).toBe(true);
    expect(sessionPanelFits({ ...docks, leftOpen: false, rightOpen: false, viewportWidth: 900 })).toBe(true);
    // rail 48 + three docks with their handles: the remainder is exactly the minimum.
    const exact = 48 + 253 + 357 + 305 + WORKSPACE_MAIN_MIN_WIDTH;
    expect(sessionPanelFits({ ...docks, viewportWidth: exact })).toBe(true);
    expect(sessionPanelFits({ ...docks, viewportWidth: exact - 1 })).toBe(false);
  });

  it("shows the panel as a sheet when docking it would squeeze the terminal", async () => {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 900 });
    useUi.setState({ leftOpen: true, rightOpen: true });
    mounted = mountApp();
    await flush();
    pressToggle();
    await settlePanel();
    expect(document.querySelector(".nx-session-dock")?.classList.contains("is-overlay")).toBe(true);
    expect(document.querySelector(".nx-workspace-body")?.classList.contains("has-session-dock")).toBe(false);
  });

  it("docks the panel and keeps the workspace minimum when it fits", async () => {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 1600 });
    useUi.setState({ leftOpen: true, rightOpen: true });
    mounted = mountApp();
    await flush();
    pressToggle();
    await settlePanel();
    expect(document.querySelector(".nx-session-dock")?.classList.contains("is-overlay")).toBe(false);
    expect(document.querySelector(".nx-workspace-body")?.classList.contains("has-session-dock")).toBe(true);
  });

  it("does not cover the terminal at startup with a remembered open that no longer fits", async () => {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 900 });
    useUi.setState({ leftOpen: true, rightOpen: true });
    useSessionPanel.getState().setOpen(true);
    mounted = mountApp();
    await flush();
    expect(panel()).toBeNull();
  });
});

describe("session sheet", () => {
  it("closes when the active tab changes underneath it, e.g. a file opened from the sheet", async () => {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 390 });
    mounted = mountApp();
    await flush();
    pressToggle();
    await settlePanel();
    expect(panel()).not.toBeNull();

    act(() =>
      useUi.getState().addTab({ id: "f1", kind: "files", title: "app.conf", sessionId: "s1", path: "/srv/app.conf", closable: true }),
    );
    await flush();
    expect(panel()).toBeNull();
    expect(useSessionPanel.getState().open).toBe(true);
  });

  it("keeps focus inside while open and returns it to the toggle when closed", async () => {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 390 });
    mounted = mountApp();
    await flush();
    const toggle = document.querySelector<HTMLButtonElement>('button[aria-label="会话侧栏"]')!;
    toggle.focus();
    click(toggle);
    await settlePanel();
    await flush();
    const sheet = panel()!;
    expect(sheet.getAttribute("aria-modal")).toBe("true");
    expect(sheet.contains(document.activeElement)).toBe(true);

    // Focus leaving the sheet is pulled back in.
    act(() => toggle.focus());
    expect(sheet.contains(document.activeElement)).toBe(true);

    click(sheet.querySelector<HTMLButtonElement>('button[aria-label="收起会话侧栏"]')!);
    await flush();
    expect(panel()).toBeNull();
    expect(document.activeElement).toBe(toggle);
  });
});

describe("session panel shortcut", () => {
  it("has no default conflict with other shortcuts or terminal input", () => {
    const bindings = getKeybindings();
    const action = KEYBINDING_ACTIONS.find((a) => a.id === "toggleSessionPanel")!;
    expect(bindings.toggleSessionPanel).toBe(action.defaultBinding);
    expect(action.defaultBinding).toBe(WEB ? "Ctrl+Alt+e" : "Mod+Shift+e");
    for (const mac of [false, true]) {
      expect(bindingConflicts(bindings, mac).filter((c) => c.actions.some((a) => a.id === "toggleSessionPanel"))).toEqual([]);
      // The desktop default sends nothing to the terminal; the web one withholds Ctrl+Alt+E, listed in the conflict table.
      const desktop = { ...bindings, toggleSessionPanel: "Mod+Shift+e" };
      const desktopWithheld = terminalShortcutConflicts(desktop, mac, false).find((c) => c.action.id === "toggleSessionPanel");
      expect(desktopWithheld?.bytes ?? null).toBeNull();
      const web = { ...bindings, toggleSessionPanel: "Ctrl+Alt+e" };
      const webWithheld = terminalShortcutConflicts(web, mac, true).find((c) => c.action.id === "toggleSessionPanel");
      expect(webWithheld?.chord).toBe("Ctrl+Alt+E");
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
    // The first change after a terminal switch is not held back by the previous terminal's interval.
    act(() => setProps({ value: "/other/sub", resetKey: "k2" }));
    expect(seen.at(-1)).toBe("/other/sub");
  });
});
