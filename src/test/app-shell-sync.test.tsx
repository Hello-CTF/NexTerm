/** @vitest-environment jsdom */

import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flush, flushUntil as flushUntilBase, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

async function flushUntil(predicate: () => boolean, maxTicks = 60): Promise<void> {
  await flush();
  await flushUntilBase(predicate, maxTicks);
  await flush();
}

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  sessionList: vi.fn(),
  vaultStatus: vi.fn(),
  syncLinkGet: vi.fn(),
  syncLinkSet: vi.fn(),
  syncStatus: vi.fn(),
  vaultLock: vi.fn(),
  call: vi.fn(),
  syncEventHandlers: [] as Array<{ topic: string; handler: (payload: unknown) => void }>,
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
  call: mocks.call,
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

vi.mock("../ipc/events", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../ipc/events")>()),
  listenEvent: vi.fn((topic: string, handler: (payload: unknown) => void) => {
    mocks.syncEventHandlers.push({ topic, handler });
    return Promise.resolve(() => {
      const index = mocks.syncEventHandlers.findIndex((entry) => entry.topic === topic && entry.handler === handler);
      if (index >= 0) mocks.syncEventHandlers.splice(index, 1);
    });
  }),
}));

vi.mock("../features/terminal/TerminalPane", () => ({ TerminalPane: () => null }));
vi.mock("../ipc/remoteApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/remoteApi")>();
  return {
    ...actual,
    remoteLinkApi: { ...actual.remoteLinkApi, linkSet: mocks.syncLinkSet },
  };
});
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
vi.mock("../features/settings/SettingsView", () => ({ SettingsView: () => null }));

import App from "../app/App";
import { useUi } from "../app/store";
import { SyncCard, WEB_SYNC_ERROR_EVENT, WEB_SYNC_RESULT_EVENT } from "../features/settings/SyncCard";

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

function fireSyncEvent(topic: string): void {
  act(() => {
    for (const entry of [...mocks.syncEventHandlers]) {
      if (entry.topic === topic) entry.handler({});
    }
  });
}

function keyDown(target: EventTarget, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

// The shell loads these panels through React.lazy or dynamic imports. A cold import transforms the module
// in real time, which no tick budget bounds under machine load, so warm them before flushUntil counts ticks.
beforeAll(async () => {
  await Promise.all([
    import("../app/AutoSync"),
    import("../app/JumpPalette"),
  ]);
});

beforeEach(() => {
  vi.clearAllMocks();
  mocks.syncEventHandlers.length = 0;
  document.body.replaceChildren();
  Element.prototype.scrollIntoView = vi.fn();
  envState.WEB = false;
  envState.TRANSPORT = "desktop";
  mocks.syncLinkGet.mockResolvedValue(EMPTY_LINK);
  mocks.syncStatus.mockResolvedValue({
    configured: false,
    loggedIn: false,
    seq: 0,
    verifiedAt: 0,
    lastError: "",
    warnings: [],
  });
  mocks.assetList.mockResolvedValue([]);
  mocks.sessionList.mockResolvedValue([]);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  mocks.call.mockImplementation((command: string) => {
    if (command === "forward_rule_recovery_settings_get") {
      return Promise.resolve({ enabled: false, maxConcurrentConnections: 4, skipProduction: true });
    }
    if (command === "forward_rule_recover_now") return Promise.resolve([]);
    return Promise.reject(new Error(`unexpected command: ${command}`));
  });
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

afterEach(async () => {
  await flush();
  mounted?.unmount();
  mounted = undefined;
  await flush();
});

describe("status bar sync state", () => {
  it("shows local mode when no sync link is configured", async () => {
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

  it("shows configured when a sync link is configured", async () => {
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

  it("shows local mode when the URL, username, or password is missing", async () => {
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

  it("shows server browser mode in browser mode without reading the sync link", async () => {
    envState.WEB = true;
    envState.TRANSPORT = "web";
    mounted = mountApp();
    await flush();

    expect(syncStatusText()).toBe("服务端：浏览器模式");
    expect(mocks.syncLinkGet).not.toHaveBeenCalled();
  });

  it("refreshes failure and recovery from round-complete and keeps only latest warnings", async () => {
    let current = {
      configured: true, loggedIn: true, username: "alice", userId: "u-1", seq: 1, verifiedAt: 1,
      lastError: "", lastAttemptAt: "2026-01-02T03:04:00.000Z", lastSuccessAt: "2026-01-02T03:04:00.000Z",
      warnings: ["credential skipped"],
    };
    mocks.syncLinkGet.mockResolvedValue(SAVED_LINK);
    mocks.syncStatus.mockImplementation(() => Promise.resolve(current));
    mounted = mountApp();
    await flushUntil(() => syncStatusText() === "同步：已配置");
    await flushUntil(() => document.body.textContent?.includes("同步警告 1") ?? false);

    current = {
      ...current, lastError: "connection refused", lastAttemptAt: "2026-01-02T04:05:00.000Z",
      warnings: [],
    };
    fireSyncEvent("sync://round-complete");
    await flushUntil(() => syncStatusText() === "同步失败");
    expect(document.querySelector(".nx-statusbar-side .text-red-300")).not.toBeNull();
    expect(document.body.textContent).not.toContain("同步警告 1");

    current = {
      ...current, lastError: "", lastAttemptAt: "2026-01-02T05:06:00.000Z",
      lastSuccessAt: "2026-01-02T05:06:00.000Z",
    };
    fireSyncEvent("sync://round-complete");
    await flushUntil(() => syncStatusText() === "同步：已配置");
    expect(document.body.textContent).not.toContain("同步失败");
  });

  it("shows a locked configured sync as paused rather than failed", async () => {
    mocks.syncLinkGet.mockResolvedValue(SAVED_LINK);
    mocks.syncStatus.mockResolvedValue({
      configured: true, loggedIn: false, seq: 0, verifiedAt: 1,
      lastError: "凭据库已锁定，请先解锁", lastAttemptAt: "2026-01-02T04:05:00.000Z",
      lastSuccessAt: "2026-01-02T03:04:00.000Z", paused: true, vaultLocked: true, warnings: [],
    });
    mounted = mountApp();
    await flushUntil(() => syncStatusText() === "同步已暂停：凭据库锁定");
    expect(document.body.textContent).not.toContain("同步失败");
    expect(document.querySelector(".nx-statusbar-side .text-red-300")).toBeNull();
  });

  it("does not toast a deliberate web vault pause", async () => {
    envState.WEB = true;
    envState.TRANSPORT = "web";
    mounted = mountApp();
    await flush();
    act(() => {
      window.dispatchEvent(new CustomEvent(WEB_SYNC_ERROR_EVENT, {
        detail: { code: "vault_locked", message: "凭据库已锁定，请先解锁" },
      }));
    });
    await flushUntil(() => syncStatusText() === "同步已暂停：凭据库锁定");
    expect(useUi.getState().toasts.some((toast) => toast.kind === "error" && toast.text.includes("同步失败"))).toBe(false);
    expect(document.querySelector(".nx-statusbar-side .text-red-300")).toBeNull();
  });

  it("deduplicates web error toasts and recovers warnings from result events", async () => {
    envState.WEB = true;
    envState.TRANSPORT = "web";
    mounted = mountApp();
    await flush();
    const failure = new Error("offline");
    act(() => {
      window.dispatchEvent(new CustomEvent(WEB_SYNC_ERROR_EVENT, { detail: failure }));
    });
    await flushUntil(() => syncStatusText() === "同步失败");
    expect(useUi.getState().toasts.filter((toast) => toast.text === "同步失败：offline")).toHaveLength(1);

    act(() => {
      window.dispatchEvent(new CustomEvent(WEB_SYNC_ERROR_EVENT, { detail: failure }));
    });
    await flush();
    expect(useUi.getState().toasts.filter((toast) => toast.text === "同步失败：offline")).toHaveLength(1);

    act(() => {
      window.dispatchEvent(new CustomEvent(WEB_SYNC_RESULT_EVENT, {
        detail: { warnings: ["latest warning"], lastSuccessAt: Date.now() },
      }));
    });
    await flushUntil(() => syncStatusText() === "服务端：浏览器模式");
    await flushUntil(() => document.body.textContent?.includes("同步警告 1") ?? false);
    expect(document.querySelector("button.nx-badge")?.getAttribute("title")).toBe("latest warning");

    act(() => {
      window.dispatchEvent(new CustomEvent(WEB_SYNC_RESULT_EVENT, {
        detail: { warnings: [], lastSuccessAt: Date.now() },
      }));
    });
    await flushUntil(() => !document.body.textContent?.includes("同步警告 1"));
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
  await act(async () => {
    clickButton(document.body, "登录");
    await flush();
  });
}

describe("desktop account sync card", () => {
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

  it("calls linkSet with the account and password on login without automatically running the first sync", async () => {
    mounted = mountAppWithSyncCard();
    await flushUntil(() => syncStatusText() !== null && syncStatusText() !== "同步：…");
    expect(syncStatusText()).toBe("同步：本地模式");

    await loginSyncAccount();

    await flushUntil(() => {
      const login = [...document.querySelectorAll<HTMLButtonElement>("button")].find(
        (button) => button.textContent?.trim() === "登录",
      );
      return syncStatusText() === "同步：已配置" && login?.disabled === false;
    });
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

  it("points per-record sync guidance to the terminal history section", async () => {
    mounted = mountAppWithSyncCard();
    await flushUntil(() => document.querySelector("#sync-url") !== null);

    const text = document.body.textContent ?? "";
    expect(text).toContain("终端录像默认不同步，可在「终端历史」中单独开启");
    expect(text).not.toContain("到「会话记录」里");
  });
});

describe("startup forward recovery", () => {
  it("consumes the persisted opt-in through the real App startup path", async () => {
    mocks.call.mockImplementation((command: string) => {
      if (command === "forward_rule_recovery_settings_get") {
        return Promise.resolve({ enabled: true, maxConcurrentConnections: 4, skipProduction: true });
      }
      if (command === "forward_rule_recover_now") {
        return Promise.resolve([{ assetId: "asset-1", sessionId: "session-1", started: ["rule-1"] }]);
      }
      return Promise.reject(new Error(`unexpected command: ${command}`));
    });

    mounted = mountApp();

    await flushUntil(() => mocks.call.mock.calls.some(([command]) => command === "forward_rule_recover_now"));
    expect(mocks.call.mock.calls.filter(([command]) => command === "forward_rule_recover_now")).toHaveLength(1);
  });
});

describe("removed manual sync entry", () => {
  it("does not trigger a sync action on Mod+Shift+S", async () => {
    mounted = mountApp();
    await flushUntil(() => syncStatusText() === "同步：本地模式");

    const event = keyDown(window, "s", { ctrlKey: true, shiftKey: true });
    expect(event.defaultPrevented).toBe(false);
  });

  it("locks the credential vault on Mod+Shift+L", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
    mounted = mountApp();
    await flush();

    keyDown(window, "l", { ctrlKey: true, shiftKey: true });
    await flushUntil(() => mocks.vaultLock.mock.calls.length > 0);
    expect(mocks.vaultLock).toHaveBeenCalled();
    expect(useUi.getState().toasts.some((toast) => toast.text === "凭据库已锁定")).toBe(true);
  });
});
