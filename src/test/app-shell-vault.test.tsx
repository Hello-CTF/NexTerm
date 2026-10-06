/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, type MountedView } from "./features/reactTestUtils";

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
vi.mock("../features/credentials/CredentialsView", () => ({ CredentialsView: () => null }));
vi.mock("../features/explorer/AssetTree", () => ({ AssetTree: () => null }));
vi.mock("../app/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));

vi.mock("../features/settings/SettingsView", async () => {
  const { createElement: h } = await import("react");
  return {
    SettingsView: () =>
      h(
        "div",
        null,
        h("section", { className: "nx-card" }, h("span", { className: "nx-card-title" }, "终端")),
        h(
          "section",
          { className: "nx-card" },
          h("span", { className: "nx-card-title" }, "凭据保护"),
        ),
      ),
  };
});

import App from "../app/App";
import { useUi } from "../app/store";

let mounted: MountedView | undefined;

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
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

describe("状态栏凭据库状态", () => {
  it("未初始化时渲染可点击的开启保护入口", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: false, unlocked: false });
    mounted = mountApp();
    await flush();

    const control = vaultStatusControl();
    expect(control?.tagName).toBe("BUTTON");
    expect(control?.textContent).toContain("凭据库未初始化");
    expect(control?.textContent).toContain("去开启保护");
    expect(control?.getAttribute("title")).toContain("凭据保护");
  });

  it("点击入口打开设置标签并定位到凭据保护卡片", async () => {
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

  it("已锁定时保持纯文本状态，不给出开启保护入口", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: false });
    mounted = mountApp();
    await flush();

    const control = vaultStatusControl();
    expect(control?.tagName).toBe("SPAN");
    expect(control?.textContent).toBe("凭据库已锁定");
    expect(control?.textContent).not.toContain("去开启保护");
  });

  it("已解锁时保持纯文本状态", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
    mounted = mountApp();
    await flush();

    const control = vaultStatusControl();
    expect(control?.tagName).toBe("SPAN");
    expect(control?.textContent).toBe("凭据库已解锁");
  });

  it("状态拉取失败时展示不可用，不给出开启保护入口", async () => {
    mocks.vaultStatus.mockRejectedValue(new Error("boom"));
    mounted = mountApp();
    await flush();

    const control = vaultStatusControl();
    expect(control?.tagName).toBe("SPAN");
    expect(control?.textContent).toBe("凭据库不可用");
  });
});
