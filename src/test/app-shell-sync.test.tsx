/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flush, flushUntil, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  sessionList: vi.fn(),
  vaultStatus: vi.fn(),
  syncLinkGet: vi.fn(),
  syncLinkSet: vi.fn(),
  syncStatus: vi.fn(),
  vaultLock: vi.fn(),
}));

const envState = vi.hoisted(() => ({
  WEB: false,
  TRANSPORT: "desktop" as "desktop" | "web",
}));

vi.mock("../ipc/env", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/env")>();
  return {
    ...actual,
    get WEB() {
      return envState.WEB;
    },
    get TRANSPORT() {
      return envState.TRANSPORT;
    },
  };
});

vi.mock("../ipc/commands", () => ({
  assetApi: { list: mocks.assetList, search: vi.fn(), groupList: vi.fn(), snippetList: vi.fn() },
  sessionApi: {
    list: mocks.sessionList,
    connectLocal: vi.fn(),
    connect: vi.fn(),
    reconnect: vi.fn(),
    probe: vi.fn(),
  },
  vaultApi: { status: mocks.vaultStatus, listCredentials: vi.fn(), lock: mocks.vaultLock },
  syncApi: {
    linkGet: mocks.syncLinkGet,
    linkSet: mocks.syncLinkSet,
    status: mocks.syncStatus,
  },
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
vi.mock("../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../features/explorer/AssetTree", () => ({ AssetTree: () => null }));
vi.mock("../app/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));
vi.mock("../features/settings/SettingsView", () => ({ SettingsView: () => null }));

import App from "../app/App";
import { useUi } from "../app/store";
import { SyncCard } from "../features/settings/SyncCard";

let mounted: MountedView | undefined;

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(App)));
}

function syncStatusText(): string | null {
  const side = document.querySelector<HTMLElement>(".nx-statusbar-side");
  if (!side) return null;
  for (const el of side.querySelectorAll<HTMLElement>("span")) {
    const text = el.textContent ?? "";
    if (text.includes("同步") || text.includes("服务端")) return text;
  }
  return null;
}

function keyDown(target: EventTarget, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  Element.prototype.scrollIntoView = vi.fn();
  envState.WEB = false;
  envState.TRANSPORT = "desktop";
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

describe("状态栏同步状态", () => {
  it("未配置同步链接时显示本地模式", async () => {
    mocks.syncLinkGet.mockResolvedValue({
      url: "",
      username: "",
      insecure: false,
      hasPassword: false,
      verifiedAt: 0,
      lastError: null,
    });
    mounted = mountApp();
    await flushUntil(() => syncStatusText() !== null && syncStatusText() !== "同步：…");

    expect(syncStatusText()).toBe("同步：本地模式");
  });

  it("已配置同步链接时显示已配置", async () => {
    mocks.syncLinkGet.mockResolvedValue({
      url: "https://sync.example.com",
      username: "alice",
      insecure: false,
      hasPassword: true,
      verifiedAt: 1,
      lastError: null,
    });
    mounted = mountApp();
    await flushUntil(() => syncStatusText() !== null && syncStatusText() !== "同步：…");

    expect(syncStatusText()).toBe("同步：已配置");
  });

  it("url、username 或密码任一缺失时显示本地模式", async () => {
    for (const patch of [{ url: "" }, { username: "" }, { hasPassword: false }]) {
      mocks.syncLinkGet.mockResolvedValue({ ...SAVED_LINK, ...patch });
      const view = mountApp();
      mounted = view;
      await flushUntil(() => syncStatusText() !== null && syncStatusText() !== "同步：…");

      expect(syncStatusText()).toBe("同步：本地模式");
      view.unmount();
      mounted = undefined;
    }
  });

  it("浏览器（服务端）模式显示服务端浏览器模式，不读取同步链接", async () => {
    envState.WEB = true;
    envState.TRANSPORT = "web";
    mounted = mountApp();
    await flush();

    expect(syncStatusText()).toBe("服务端：浏览器模式");
    expect(mocks.syncLinkGet).not.toHaveBeenCalled();
  });
});

const EMPTY_LINK = {
  url: "",
  username: "",
  insecure: false,
  hasPassword: false,
  verifiedAt: 0,
  lastError: null,
};
const SAVED_LINK = {
  url: "https://sync.example.com",
  username: "alice",
  insecure: false,
  hasPassword: true,
  verifiedAt: 1,
  lastError: null,
};

function mountAppWithSyncCard(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(App),
      createElement(SyncCard),
    ),
  );
}

async function loginSyncAccount(): Promise<void> {
  await flushUntil(() => document.querySelector("#sync-url") !== null);
  setInputValue(
    document.querySelector<HTMLInputElement>("#sync-url")!,
    "https://sync.example.com",
  );
  setInputValue(document.querySelector<HTMLInputElement>("#sync-user")!, "alice");
  setInputValue(document.querySelector<HTMLInputElement>("#sync-pass")!, "correct horse battery staple");
  await flushUntil(() => {
    const btn = [...document.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "登录",
    ) as HTMLButtonElement | undefined;
    return !!btn && !btn.disabled;
  });
  clickButton(document.body, "登录");
}

describe("账号同步卡（桌面端）", () => {
  beforeEach(() => {
    mocks.syncStatus.mockResolvedValue({
      configured: false,
      loggedIn: false,
      seq: 0,
      verifiedAt: 0,
      lastError: "",
    });
    mocks.syncLinkGet.mockImplementation(async () =>
      mocks.syncLinkSet.mock.calls.length > 0 ? SAVED_LINK : EMPTY_LINK,
    );
    mocks.syncLinkSet.mockResolvedValue(SAVED_LINK);
  });

  it("登录时以账号+密码调用 linkSet 完成同步配置,不自动执行首次同步", async () => {
    mounted = mountAppWithSyncCard();
    await flush();
    expect(syncStatusText()).toBe("同步：本地模式");

    await loginSyncAccount();

    await flushUntil(() => syncStatusText() === "同步：已配置");
    expect(mocks.syncLinkSet).toHaveBeenCalledWith(
      expect.objectContaining({
        url: "https://sync.example.com",
        username: "alice",
        password: "correct horse battery staple",
      }),
    );
    expect(document.body.textContent).toContain("自动同步已开启");
    expect(document.body.textContent).not.toContain("立即同步");
  });

  it("单条同步开关指引指向「终端历史」分区", async () => {
    mounted = mountAppWithSyncCard();
    await flushUntil(() => document.querySelector("#sync-url") !== null);

    const text = document.body.textContent ?? "";
    expect(text).toContain("如需同步，请在「终端历史」中为对应记录开启同步");
    expect(text).not.toContain("到「会话记录」里");
  });
});

describe("手动同步入口已移除", () => {
  it("Mod+Shift+S 不再触发同步动作", async () => {
    mounted = mountApp();
    await flushUntil(() => syncStatusText() === "同步：本地模式");

    const event = keyDown(window, "s", { ctrlKey: true, shiftKey: true });
    expect(event.defaultPrevented).toBe(false);
  });

  it("Mod+Shift+L 锁定凭据库", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
    mounted = mountApp();
    await flush();

    keyDown(window, "l", { ctrlKey: true, shiftKey: true });
    await flushUntil(() => mocks.vaultLock.mock.calls.length > 0);
    expect(mocks.vaultLock).toHaveBeenCalled();
    expect(useUi.getState().toasts.some((toast) => toast.text === "凭据库已锁定")).toBe(true);
  });
});
