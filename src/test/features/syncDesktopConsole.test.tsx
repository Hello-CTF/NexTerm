/** @vitest-environment jsdom */
// 桌面端同步台: 登录 (link 已建立) 后展示同步状态/同步内容开关/立即同步;
// 未登录只有登录卡, 不出现同步台也不读 opt-in。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    linkGet: vi.fn(),
    status: vi.fn(),
    wake: vi.fn(),
    kindOptInGet: vi.fn(),
    kindOptInSet: vi.fn(),
    toast: vi.fn(),
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
      wake: mocks.wake,
      kindOptInGet: mocks.kindOptInGet,
      kindOptInSet: mocks.kindOptInSet,
    },
  };
});
vi.mock("../../features/settings/AuthCard", () => ({ AuthCard: () => null }));
vi.mock("../../features/settings/AccountCard", () => ({ AccountCard: () => null }));

import { SyncCard } from "../../features/settings/SyncCard";
import { useUi } from "../../app/store";

const LINK = { url: "https://nexterm.example.com", username: "alice", insecure: false, hasPassword: true, verifiedAt: 1700000000000, lastError: "" };
const OPT_IN = { knownHost: true, aiProfile: false, aiPermission: true, preferences: false };

let mounted: MountedView | undefined;

function withClient(node: React.ReactElement): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function seedLoggedIn(statusPatch: Record<string, unknown> = {}) {
  mocks.linkGet.mockResolvedValue(LINK);
  mocks.status.mockResolvedValue({
    configured: true,
    loggedIn: true,
    username: "alice",
    userId: "u-1",
    head: "head-1",
    seq: 7,
    verifiedAt: 1700000000000,
    lastError: "",
    ...statusPatch,
  });
  mocks.kindOptInGet.mockResolvedValue({ ...OPT_IN });
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
  mocks.wake.mockResolvedValue(undefined);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("桌面端同步台", () => {
  it("未登录只渲染登录卡: 无同步台, 不读 opt-in", async () => {
    mocks.linkGet.mockResolvedValue({ url: "", username: "", insecure: false, hasPassword: false, verifiedAt: 0, lastError: "" });
    mocks.status.mockResolvedValue({ configured: false, loggedIn: false, seq: 0, verifiedAt: 0, lastError: "" });
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => document.body.textContent?.includes("账号同步") ?? false);

    expect(document.body.textContent).not.toContain("同步台");
    expect(mocks.kindOptInGet).not.toHaveBeenCalled();
  });

  it("登录后展示同步台: 状态/开关摘要与勾选来自真实 IPC", async () => {
    seedLoggedIn();
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => document.body.textContent?.includes("同步台") ?? false);

    const text = document.body.textContent ?? "";
    expect(text).toContain("自动同步");
    expect(text).toContain("每 60 秒");
    expect(text).toContain("已应用云端第 7 号");
    expect(text).toContain("账号验证于");
    expect(text).toContain("已知主机 已开启");
    expect(text).toContain("AI 模型档案 已关闭");

    const knownHost = [...document.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')].find(
      (c) => c.closest("label")?.textContent?.includes("同步已知主机"),
    );
    const aiProfile = [...document.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')].find(
      (c) => c.closest("label")?.textContent?.includes("同步 AI 模型档案"),
    );
    expect(knownHost?.checked).toBe(true);
    expect(aiProfile?.checked).toBe(false);
  });

  it("切换开关调用 kindOptInSet 并以返回值刷新", async () => {
    seedLoggedIn();
    mocks.kindOptInSet.mockResolvedValue({ ...OPT_IN, knownHost: false });
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => document.body.textContent?.includes("同步台") ?? false);

    const knownHost = [...document.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')].find(
      (c) => c.closest("label")?.textContent?.includes("同步已知主机"),
    )!;
    click(knownHost);
    await flushUntil(() => mocks.kindOptInSet.mock.calls.length === 1);
    expect(mocks.kindOptInSet).toHaveBeenCalledWith({ knownHost: false });
    await flushUntil(() => (document.body.textContent ?? "").includes("已知主机 已关闭"));
  });

  it("「立即同步」触发 sync_wake 并提示", async () => {
    seedLoggedIn();
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => document.body.textContent?.includes("同步台") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("立即同步")) as HTMLButtonElement);
    await flushUntil(() => mocks.wake.mock.calls.length === 1);
    expect(mocks.toast).toHaveBeenCalledWith("success", "已触发一轮后台同步");
  });

  it("status.lastError 如实显示上次同步失败", async () => {
    seedLoggedIn({ lastError: "连接被拒绝" });
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => (document.body.textContent ?? "").includes("上次同步失败：连接被拒绝"));
  });
});
