/** @vitest-environment jsdom */
// 桌面端账号同步控制台: 逐条对比数据来自 sync_compare IPC(Go 侧判定),
// 挂载后自动加载一次, 「刷新对比」重新拉取并反映最新行, 失败如实展示且可重试,
// 收集告警与云端对象数随结果呈现。
import { createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flushUntil, mount, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    status: vi.fn(),
    linkGet: vi.fn(),
    kindOptInGet: vi.fn(),
    kindOptInSet: vi.fn(),
    wake: vi.fn(),
    compare: vi.fn(),
    me: vi.fn(),
    adminUsers: vi.fn(),
    devices: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    syncApi: {
      ...actual.syncApi,
      status: mocks.status,
      linkGet: mocks.linkGet,
      kindOptInGet: mocks.kindOptInGet,
      kindOptInSet: mocks.kindOptInSet,
      wake: mocks.wake,
    },
  };
});

vi.mock("../ipc/remoteApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/remoteApi")>();
  return {
    ...actual,
    syncCompareApi: { compare: mocks.compare },
    remoteAccountApi: {
      ...actual.remoteAccountApi,
      me: mocks.me,
      adminUsers: mocks.adminUsers,
      devices: mocks.devices,
    },
  };
});

import { SyncCard } from "../features/settings/SyncCard";
import { useUi } from "../app/store";

let mounted: MountedView | undefined;

function mountSyncCard(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(SyncCard)));
}

function text(): string {
  return mounted?.container.textContent ?? "";
}

const FIRST_ROWS = [
  { id: "a1", kind: "asset", name: "web-01", state: "same" },
  { id: "g1", kind: "group", name: "生产环境", state: "local-only" },
];

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  window.localStorage.clear();
  useUi.setState({ pushToast: mocks.toast });
  mocks.status.mockResolvedValue({
    configured: true, loggedIn: true, username: "alice", userId: "u-alice",
    head: "head-1", seq: 7, verifiedAt: 1700000000000, lastError: "",
  });
  mocks.linkGet.mockResolvedValue({
    url: "https://sync.example.com", username: "alice", insecure: false,
    hasPassword: true, verifiedAt: 1700000000000, lastError: "",
  });
  mocks.kindOptInGet.mockResolvedValue({ knownHost: true, aiProfile: true, aiPermission: true, preferences: true });
  mocks.kindOptInSet.mockResolvedValue({ knownHost: true, aiProfile: true, aiPermission: true, preferences: true });
  mocks.wake.mockResolvedValue(undefined);
  mocks.me.mockRejectedValue({ code: "not_found", message: "不可用" });
  mocks.adminUsers.mockRejectedValue({ code: "forbidden", message: "需要超级管理员权限" });
  mocks.devices.mockResolvedValue({ devices: [] });
  mocks.compare.mockResolvedValue({ rows: FIRST_ROWS, remote: 2, warnings: [] });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("桌面端同步控制台·逐条对比", () => {
  it("挂载后自动加载对比: 汇总/云端对象数/行徽章真实呈现", async () => {
    mounted = mountSyncCard();
    await flushUntil(() => mocks.compare.mock.calls.length > 0 && text().includes("共 2 条"));
    expect(mocks.compare).toHaveBeenCalledTimes(1);
    expect(text()).toContain("云端 2 个对象");
    expect(text()).toContain("共 2 条 · 一致 1 条 · 差异 1 条");

    clickButton(mounted.container, "对比详情");
    await flushUntil(() => text().includes("web-01"));
    expect(text()).toContain("已一致");
    expect(text()).toContain("生产环境");
    expect(text()).toContain("仅本机");
  });

  it("「刷新对比」重新拉取并渲染最新行", async () => {
    mounted = mountSyncCard();
    await flushUntil(() => text().includes("共 2 条"));
    mocks.compare.mockResolvedValue({
      rows: [{ id: "s1", kind: "snippet", name: "部署片段", state: "remote-newer", updatedAtRemote: 300 }],
      remote: 3,
      warnings: [],
    });
    clickButton(mounted.container, "刷新对比");
    await flushUntil(() => mocks.compare.mock.calls.length >= 2 && text().includes("云端 3 个对象"));
    clickButton(mounted.container, "对比详情");
    await flushUntil(() => text().includes("部署片段"));
    expect(text()).toContain("云端较新");
    expect(text()).not.toContain("web-01");
  });

  it("对比失败如实展示并可重试恢复", async () => {
    mocks.compare.mockRejectedValueOnce({ code: "disconnected", message: "无法连接远端同步服务" });
    mounted = mountSyncCard();
    await flushUntil(() => text().includes("对比读取失败"));
    expect(text()).toContain("无法连接远端同步服务");

    mocks.compare.mockResolvedValue({ rows: FIRST_ROWS, remote: 2, warnings: [] });
    clickButton(mounted.container, "重试");
    await flushUntil(() => text().includes("共 2 条"));
    expect(text()).not.toContain("对比读取失败");
    expect(mocks.compare.mock.calls.length).toBe(2);
  });

  it("收集告警逐条展示", async () => {
    mocks.compare.mockResolvedValue({
      rows: FIRST_ROWS,
      remote: 2,
      warnings: ["凭据 abc 解密失败, 本次不同步: 凭据库已锁定"],
    });
    mounted = mountSyncCard();
    await flushUntil(() => text().includes("凭据 abc 解密失败"));
  });

  it("云端对象解不开时呈现「无法解密」行", async () => {
    mocks.compare.mockResolvedValue({
      rows: [{ id: "u1", kind: "", name: "u1", state: "remote-undecryptable" }],
      remote: 1,
      warnings: [],
    });
    mounted = mountSyncCard();
    await flushUntil(() => text().includes("共 1 条"));
    clickButton(mounted.container, "对比详情");
    await flushUntil(() => text().includes("无法解密"));
  });
});
