/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  assetSearch: vi.fn(),
  sessionList: vi.fn(),
  vaultStatus: vi.fn(),
}));

vi.mock("../ipc/commands", () => ({
  assetApi: { list: mocks.assetList, search: mocks.assetSearch },
  sessionApi: {
    list: mocks.sessionList,
    connectLocal: vi.fn(),
    connect: vi.fn(),
    reconnect: vi.fn(),
    probe: vi.fn(),
  },
  vaultApi: { status: mocks.vaultStatus, listCredentials: vi.fn() },
  layoutApi: {
    get: vi.fn().mockResolvedValue({ revision: 0, updatedAt: 0, data: null }),
    put: vi.fn().mockResolvedValue({ saved: true, revision: 1, conflict: false }),
  },
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
vi.mock("../features/settings/SettingsView", () => ({ SettingsView: () => null }));
vi.mock("../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../features/explorer/AssetTree", () => ({ AssetTree: () => null }));
vi.mock("../app/JumpPalette", () => ({ JumpPalette: () => null }));
vi.mock("../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));
vi.mock("../features/fleet/DeviceSessionBoundary", () => ({ DeviceSessionBoundary: () => null }));

import App from "../app/App";
import { useUi, type ToastItem } from "../app/store";

let mounted: MountedView | undefined;

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(App)));
}

function toast(id: number, kind: ToastItem["kind"], text: string): ToastItem {
  return { id, kind, text };
}

function announcer(): HTMLElement | null {
  return document.querySelector<HTMLElement>("[data-a11y-announcer]");
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.assetList.mockResolvedValue([]);
  mocks.assetSearch.mockResolvedValue([]);
  mocks.sessionList.mockResolvedValue([]);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  useUi.setState({
    toasts: [],
    leftOpen: true,
    rightOpen: false,
    leftMode: "assets",
    sessions: [],
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("landmarks and skip link", () => {
  it("exposes the main content as a main landmark and skip target", async () => {
    mounted = mountApp();
    await flush();

    const main = document.querySelector("main");
    expect(main).not.toBeNull();
    expect(main?.classList.contains("nx-workspace-main")).toBe(true);
    expect(main?.id).toBe("nx-main");
    expect(main?.getAttribute("tabindex")).toBe("-1");
  });

  it("gives every navigation landmark an accessible name", async () => {
    mounted = mountApp();
    await flush();

    const navs = [...document.querySelectorAll<HTMLElement>("nav, [role='navigation']")];
    expect(navs.length).toBeGreaterThan(0);
    for (const nav of navs) {
      expect(nav.getAttribute("aria-label")).toBeTruthy();
    }
    const dock = document.querySelector<HTMLElement>(".nx-left-dock");
    expect(dock?.getAttribute("role")).toBe("navigation");
    expect(dock?.getAttribute("aria-label")).toBe("左侧栏");
  });

  it("makes the skip link the app's first child and points it at the main content", async () => {
    mounted = mountApp();
    await flush();

    const app = document.querySelector(".nx-app");
    const skip = app?.firstElementChild;
    expect(skip?.tagName).toBe("A");
    expect(skip?.getAttribute("href")).toBe("#nx-main");
    expect(skip?.className).toContain("sr-only");
    expect(document.querySelector("#nx-main")?.tagName).toBe("MAIN");
  });
});

describe("main navigation tooltips", () => {
  it("uses purpose descriptions longer than the short labels for every rail button", async () => {
    mounted = mountApp();
    await flush();

    const buttons = [...document.querySelectorAll<HTMLButtonElement>(".nx-rail button.nx-rail-btn")];
    expect(buttons.length).toBeGreaterThan(0);
    for (const button of buttons) {
      const label = button.getAttribute("aria-label");
      const title = button.getAttribute("data-tip");
      expect(label, title ?? undefined).toBeTruthy();
      expect(title, label ?? undefined).toBeTruthy();
      expect(title!.length, `${label} tooltip should be longer than its label`).toBeGreaterThan(label!.length);
    }
  });

  it("keeps terminal creation in the workspace instead of the global rail", async () => {
    useUi.setState({
      workspaces: [{
        id: "ws",
        kind: "session",
        title: "host",
        closable: true,
        panes: [{ id: "pane", tabs: [], activeTabId: null }],
        activePaneId: "pane",
      }],
      activeWorkspaceId: "ws",
    });
    mounted = mountApp();
    await flush();

    expect(document.querySelector('.nx-rail button[aria-label="新建终端"]')).toBeNull();
    expect(document.querySelector('button[aria-label="新建终端标签页"]')).not.toBeNull();
  });
});

function accessibleName(element: HTMLElement): string {
  const labelledBy = element.getAttribute("aria-labelledby");
  const referenced = labelledBy
    ? labelledBy.split(/\s+/).map((id) => document.getElementById(id)?.textContent ?? "").join(" ")
    : "";
  return (
    element.getAttribute("aria-label") ||
    referenced ||
    element.textContent ||
    element.getAttribute("title") ||
    ""
  ).trim();
}

describe("button accessible names", () => {
  it("gives every button in the app shell a name and never repeats it on adjacent buttons", async () => {
    mounted = mountApp();
    await flush();

    const buttons = [...document.querySelectorAll<HTMLElement>("button, [role='button']")];
    expect(buttons.length).toBeGreaterThan(0);
    const unnamed = buttons.filter((button) => accessibleName(button) === "").map((button) => button.outerHTML.slice(0, 120));
    expect(unnamed, "icon-only buttons need aria-label").toEqual([]);

    const duplicates: string[] = [];
    for (const button of buttons) {
      const next = button.nextElementSibling;
      if (next instanceof HTMLElement && next.matches("button, [role='button']") && accessibleName(next) === accessibleName(button)) {
        duplicates.push(accessibleName(button));
      }
    }
    expect(duplicates, "adjacent buttons must not share a label").toEqual([]);
  });
});

describe("toast announcements", () => {
  it("politely announces regular toasts through role=status", async () => {
    useUi.setState({ toasts: [toast(1, "info", "已取消连接")] });
    mounted = mountApp();
    await flush();

    const status = announcer()?.querySelector('[role="status"]');
    expect(status?.textContent).toBe("已取消连接");
    expect(announcer()?.querySelector('[role="alert"]')?.textContent).toBe("");
  });

  it("also politely announces success toasts through role=status", async () => {
    useUi.setState({ toasts: [toast(1, "success", "已结束进程")] });
    mounted = mountApp();
    await flush();

    const status = announcer()?.querySelector('[role="status"]');
    expect(status?.textContent).toBe("已结束进程");
    expect(announcer()?.querySelector('[role="alert"]')?.textContent).toBe("");
  });

  it("immediately announces error toasts through role=alert", async () => {
    useUi.setState({ toasts: [toast(1, "error", "连接失败：超时")] });
    mounted = mountApp();
    await flush();

    const alert = announcer()?.querySelector('[role="alert"]');
    expect(alert?.textContent).toBe("连接失败：超时");
    expect(announcer()?.querySelector('[role="status"]')?.textContent).toBe("");
  });

  it("keeps only the latest text of each kind to avoid duplicate announcements", async () => {
    useUi.setState({
      toasts: [toast(1, "info", "布局已更新"), toast(2, "info", "布局同步失败")],
    });
    mounted = mountApp();
    await flush();

    const box = announcer();
    expect(box?.querySelectorAll('[role="status"]').length).toBe(1);
    expect(box?.querySelector('[role="status"]')?.textContent).toBe("布局同步失败");
    expect(box?.textContent).not.toContain("布局已更新");
  });

  it("keeps only the latest of multiple errors", async () => {
    useUi.setState({
      toasts: [toast(1, "error", "连接失败"), toast(2, "error", "连接被拒绝")],
    });
    mounted = mountApp();
    await flush();

    const box = announcer();
    expect(box?.querySelectorAll('[role="alert"]').length).toBe(1);
    expect(box?.querySelector('[role="alert"]')?.textContent).toBe("连接被拒绝");
  });

  it("announces regular and error toasts independently", async () => {
    useUi.setState({
      toasts: [toast(1, "info", "已取消连接"), toast(2, "error", "连接失败")],
    });
    mounted = mountApp();
    await flush();

    const box = announcer();
    expect(box?.querySelector('[role="status"]')?.textContent).toBe("已取消连接");
    expect(box?.querySelector('[role="alert"]')?.textContent).toBe("连接失败");
  });

  it("keeps live roles off the visible toast list so only the announcer speaks", async () => {
    useUi.setState({
      toasts: [toast(1, "info", "已取消连接"), toast(2, "error", "连接失败")],
    });
    mounted = mountApp();
    await flush();

    const holder = document.querySelector(".nx-toasts");
    expect(holder?.querySelectorAll('[role="status"], [role="alert"]').length).toBe(0);
    expect(holder?.querySelectorAll("button").length).toBe(2);
  });

  it("renders and runs a toast action separately from dismissing the message", async () => {
    const onSelect = vi.fn();
    useUi.setState({
      toasts: [{
        id: 3,
        kind: "error",
        text: "会话异常断开",
        action: { label: "重新连接", onSelect },
      }],
    });
    mounted = mountApp();
    await flush();

    const action = [...document.querySelectorAll<HTMLButtonElement>(".nx-toasts button")]
      .find((button) => button.textContent === "重新连接");
    expect(action).toBeDefined();
    click(action as HTMLButtonElement);
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(useUi.getState().toasts).toEqual([]);
  });
});

describe("workspace reconnect status", () => {
  it("shows the attempt count and next retry time for the active workspace", async () => {
    const reconnecting = {
      id: "s1",
      assetId: "a1",
      name: "web-01",
      kind: "ssh",
      status: "reconnecting" as const,
      tabs: [],
      createdAt: 1,
      attempt: 2,
      nextRetryAt: 1_800_000_000_000,
    };
    mocks.sessionList.mockResolvedValue([reconnecting]);
    useUi.setState({
      sessions: [reconnecting],
      workspaces: [{
        id: "ws",
        kind: "session",
        title: "web-01",
        sessionId: "s1",
        closable: true,
        panes: [{ id: "pane", tabs: [], activeTabId: null }],
        activePaneId: "pane",
      }],
      activeWorkspaceId: "ws",
    });
    mounted = mountApp();
    await flush();

    const status = document.querySelector("[data-testid='workspace-connection-bar']");
    expect(status?.getAttribute("role")).toBe("status");
    expect(status?.textContent).toContain("正在重新连接（第 2 次");
    expect(status?.textContent).toContain("秒后重试");
  });
});
