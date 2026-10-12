/** @vitest-environment jsdom */
// 桌面端账号同步卡片的状态自刷新: 后台自动同步(启动探测/解锁唤醒/周期轮)建立会话或
// 记录探测失败后经既有 sync://status 事件广播, 已挂载卡片收到事件即重查 linkGet/status,
// 「待同步」徽标自动变「已登录」, 失败时 LastError 自动上屏。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    linkGet: vi.fn(),
    status: vi.fn(),
    kindOptInGet: vi.fn(),
    syncStatusHandlers: [] as Array<(payload: unknown) => void>,
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    syncApi: {
      ...actual.syncApi,
      linkGet: mocks.linkGet,
      status: mocks.status,
      kindOptInGet: mocks.kindOptInGet,
    },
  };
});
vi.mock("../../ipc/events", () => ({
  listenEvent: vi.fn((event: string, handler: (payload: unknown) => void) => {
    if (event === "sync://status") mocks.syncStatusHandlers.push(handler);
    return Promise.resolve(() => {
      const index = mocks.syncStatusHandlers.indexOf(handler);
      if (index >= 0) mocks.syncStatusHandlers.splice(index, 1);
    });
  }),
}));
vi.mock("../../features/settings/AuthCard", () => ({ AuthCard: () => null }));
vi.mock("../../features/settings/AccountCard", () => ({ AccountCard: () => null }));

import { SyncCard } from "../../features/settings/SyncCard";

const LINK = { url: "https://nexterm.example.com", username: "alice", insecure: false, hasPassword: true, verifiedAt: 1700000000000, lastError: "" };

let loggedIn = false;
let lastError = "";
let mounted: MountedView | undefined;

function statusPayload() {
  return {
    configured: true,
    loggedIn,
    username: loggedIn ? "alice" : "",
    userId: loggedIn ? "u-1" : "",
    head: "",
    seq: 0,
    verifiedAt: 1700000000000,
    lastError,
  };
}

function mountCard(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(SyncCard)));
}

function fireSyncStatus(): void {
  for (const handler of [...mocks.syncStatusHandlers]) handler({});
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.syncStatusHandlers.length = 0;
  loggedIn = false;
  lastError = "";
  document.body.replaceChildren();
  mocks.linkGet.mockResolvedValue({ ...LINK });
  mocks.status.mockImplementation(() => Promise.resolve(statusPayload()));
  mocks.kindOptInGet.mockResolvedValue({ knownHost: false, aiProfile: false, aiPermission: false, preferences: false });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("桌面端账号同步卡片 · 状态自刷新", () => {
  it("初始待同步, 后台建立会话后经 sync://status 广播自动变已登录", async () => {
    mounted = mountCard();
    await flushUntil(() => document.body.textContent?.includes("待同步") ?? false);
    expect(mocks.syncStatusHandlers.length).toBeGreaterThan(0);

    loggedIn = true;
    const statusCalls = mocks.status.mock.calls.length;
    fireSyncStatus();
    await flushUntil(() => document.body.textContent?.includes("已登录 alice") ?? false);
    expect(mocks.status.mock.calls.length).toBeGreaterThan(statusCalls);
  });

  it("后台探测失败经 sync://status 广播后 LastError 自动上屏", async () => {
    mounted = mountCard();
    await flushUntil(() => document.body.textContent?.includes("待同步") ?? false);

    lastError = "无法连接远端同步服务: connection refused";
    fireSyncStatus();
    await flushUntil(() => document.body.textContent?.includes("上次失败：无法连接远端同步服务") ?? false);
    expect(document.body.textContent).not.toContain("已登录 alice");
  });
});
