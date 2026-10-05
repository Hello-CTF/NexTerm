/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flush, mount, type MountedView } from "./features/reactTestUtils";

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
vi.mock("../features/credentials/CredentialsView", () => ({ CredentialsView: () => null }));
vi.mock("../features/explorer/AssetTree", () => ({ AssetTree: () => null }));
vi.mock("../app/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));

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

describe("地标与跳转链接", () => {
  it("主内容暴露 main 地标并作为跳转目标", async () => {
    mounted = mountApp();
    await flush();

    const main = document.querySelector("main");
    expect(main).not.toBeNull();
    expect(main?.classList.contains("nx-workspace-main")).toBe(true);
    expect(main?.id).toBe("nx-main");
    expect(main?.getAttribute("tabindex")).toBe("-1");
  });

  it("每个 navigation 地标都有可访问名称", async () => {
    mounted = mountApp();
    await flush();

    const navs = [...document.querySelectorAll<HTMLElement>("nav, [role='navigation']")];
    expect(navs.length).toBeGreaterThan(0);
    for (const nav of navs) {
      expect(nav.getAttribute("aria-label")).toBeTruthy();
    }
    const dock = document.querySelector<HTMLElement>(".nx-left-dock");
    expect(dock?.getAttribute("role")).toBe("navigation");
    expect(dock?.getAttribute("aria-label")).toBe("左栏");
  });

  it("跳转链接是应用首个子元素并指向主内容", async () => {
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

describe("toast 播报", () => {
  it("普通 toast 通过 role=status 礼貌播报", async () => {
    useUi.setState({ toasts: [toast(1, "info", "已取消连接")] });
    mounted = mountApp();
    await flush();

    const status = announcer()?.querySelector('[role="status"]');
    expect(status?.textContent).toBe("已取消连接");
    expect(announcer()?.querySelector('[role="alert"]')?.textContent).toBe("");
  });

  it("成功 toast 也通过 role=status 礼貌播报", async () => {
    useUi.setState({ toasts: [toast(1, "success", "已结束进程")] });
    mounted = mountApp();
    await flush();

    const status = announcer()?.querySelector('[role="status"]');
    expect(status?.textContent).toBe("已结束进程");
    expect(announcer()?.querySelector('[role="alert"]')?.textContent).toBe("");
  });

  it("错误 toast 通过 role=alert 立即播报", async () => {
    useUi.setState({ toasts: [toast(1, "error", "连接失败：超时")] });
    mounted = mountApp();
    await flush();

    const alert = announcer()?.querySelector('[role="alert"]');
    expect(alert?.textContent).toBe("连接失败：超时");
    expect(announcer()?.querySelector('[role="status"]')?.textContent).toBe("");
  });

  it("同类多条只保留最新文本，避免重复播报", async () => {
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

  it("多条错误只保留最新一条", async () => {
    useUi.setState({
      toasts: [toast(1, "error", "连接失败"), toast(2, "error", "连接被拒绝")],
    });
    mounted = mountApp();
    await flush();

    const box = announcer();
    expect(box?.querySelectorAll('[role="alert"]').length).toBe(1);
    expect(box?.querySelector('[role="alert"]')?.textContent).toBe("连接被拒绝");
  });

  it("普通与错误各自播报，互不干扰", async () => {
    useUi.setState({
      toasts: [toast(1, "info", "已取消连接"), toast(2, "error", "连接失败")],
    });
    mounted = mountApp();
    await flush();

    const box = announcer();
    expect(box?.querySelector('[role="status"]')?.textContent).toBe("已取消连接");
    expect(box?.querySelector('[role="alert"]')?.textContent).toBe("连接失败");
  });

  it("可见 toast 列表自身不再挂实时角色，播报只经 announcer 一份", async () => {
    useUi.setState({
      toasts: [toast(1, "info", "已取消连接"), toast(2, "error", "连接失败")],
    });
    mounted = mountApp();
    await flush();

    const holder = document.querySelector(".nx-toasts");
    expect(holder?.querySelectorAll('[role="status"], [role="alert"]').length).toBe(0);
    expect(holder?.querySelectorAll("button").length).toBe(2);
  });
});
