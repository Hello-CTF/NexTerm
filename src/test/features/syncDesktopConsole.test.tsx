/** @vitest-environment jsdom */
// Desktop account sync console: after login (link established) shows sync status / sync-content toggles / sync now;
// logged out shows only the login card, no sync console and no opt-in reads.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flushUntil, mount, type MountedView } from "./reactTestUtils";
import { formatTime } from "../../ui/format";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    linkGet: vi.fn(),
    status: vi.fn(),
    wake: vi.fn(),
    kindOptInGet: vi.fn(),
    kindOptInSet: vi.fn(),
    compare: vi.fn(),
    repair: vi.fn(),
    ask: vi.fn(),
    roundCompleteHandlers: [] as Array<(payload: unknown) => void>,
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
vi.mock("../../ipc/remoteApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/remoteApi")>();
  return {
    ...actual,
    syncCompareApi: { ...actual.syncCompareApi, compare: mocks.compare, repairUndecryptable: mocks.repair },
  };
});
vi.mock("../../ui/dialogs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ui/dialogs")>()),
  ask: mocks.ask,
}));
vi.mock("../../ipc/events", () => ({
  listenEvent: vi.fn((event: string, handler: (payload: unknown) => void) => {
    if (event === "sync://round-complete") mocks.roundCompleteHandlers.push(handler);
    return Promise.resolve(() => {
      const index = mocks.roundCompleteHandlers.indexOf(handler);
      if (index >= 0) mocks.roundCompleteHandlers.splice(index, 1);
    });
  }),
}));
vi.mock("../../features/settings/AuthCard", () => ({ AuthCard: () => null }));
vi.mock("../../features/settings/AccountCard", () => ({ AccountCard: () => null }));

import { SyncCard } from "../../features/settings/SyncCard";
import { useUi } from "../../app/store";

const LINK = { url: "https://nexterm.example.com", username: "alice", insecure: false, hasPassword: true, verifiedAt: 1700000000000, lastError: "" };
const OPT_IN = { knownHost: true, aiProfile: false, aiPermission: true, aiGrant: false, preferences: false };

let mounted: MountedView | undefined;

function withClient(node: React.ReactElement): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function fireRoundComplete(): void {
  act(() => {
    for (const handler of [...mocks.roundCompleteHandlers]) handler({});
  });
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
  mocks.roundCompleteHandlers.length = 0;
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
  mocks.wake.mockResolvedValue(undefined);
  mocks.compare.mockResolvedValue({ rows: [], remote: 0, warnings: [], userId: "u-1", compareToken: "compare-1" });
  mocks.repair.mockResolvedValue({ repaired: true });
  mocks.ask.mockResolvedValue(true);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.useRealTimers();
});

describe("Desktop account sync console", () => {
  it("requires confirmation and uses the compared remote and local versions to repair an undecryptable object", async () => {
    seedLoggedIn();
    mocks.compare.mockResolvedValue({
      userId: "u-1", compareToken: "compare-1", remote: 1, warnings: [],
      rows: [{ id: "asset-1", kind: "asset", name: "我的主机", state: "remote-undecryptable",
        remoteSeq: 7, remoteBlobHash: "blob-hash", localPayloadHash: "payload-hash" }],
    });
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => (document.body.textContent ?? "").includes("差异 1 条"));
    click([...document.querySelectorAll("button")].find((button) => button.textContent?.includes("对比详情")) as HTMLButtonElement);
    click([...document.querySelectorAll("button")].find((button) => button.textContent?.includes("用本机副本修复")) as HTMLButtonElement);
    await flushUntil(() => mocks.ask.mock.calls.length === 1);
    expect(mocks.ask.mock.calls[0][0]).toContain("我的主机");
    await flushUntil(() => mocks.repair.mock.calls.length === 1);
    expect(mocks.repair).toHaveBeenCalledWith({
      id: "asset-1", action: "restore-local", userId: "u-1", compareToken: "compare-1",
      remoteSeq: 7, remoteBlobHash: "blob-hash", localKind: "asset", localPayloadHash: "payload-hash",
    });
    expect(mocks.toast).toHaveBeenCalledWith("success", "云端对象已用本机副本修复");
  });

  it("does not modify the cloud when the user cancels clearing a cloud-only object", async () => {
    seedLoggedIn();
    mocks.ask.mockResolvedValue(false);
    mocks.compare.mockResolvedValue({
      userId: "u-1", compareToken: "compare-1", remote: 1, warnings: [],
      rows: [{ id: "cloud-1", kind: "", name: "cloud-1", state: "remote-undecryptable",
        remoteSeq: 8, remoteBlobHash: "blob-hash" }],
    });
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => (document.body.textContent ?? "").includes("差异 1 条"));
    click([...document.querySelectorAll("button")].find((button) => button.textContent?.includes("对比详情")) as HTMLButtonElement);
    click([...document.querySelectorAll("button")].find((button) => button.textContent?.includes("清除云端副本")) as HTMLButtonElement);
    await flushUntil(() => mocks.ask.mock.calls.length === 1);
    expect(mocks.repair).not.toHaveBeenCalled();
  });

  it("renders only the login card when logged out, without the sync console or opt-in reads", async () => {
    mocks.linkGet.mockResolvedValue({ url: "", username: "", insecure: false, hasPassword: false, verifiedAt: 0, lastError: "" });
    mocks.status.mockResolvedValue({ configured: false, loggedIn: false, seq: 0, verifiedAt: 0, lastError: "" });
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => document.body.textContent?.includes("账号同步") ?? false);

    expect(document.body.textContent).not.toContain("立即同步");
    expect(mocks.kindOptInGet).not.toHaveBeenCalled();
  });

  it("shows the sync console after login with shared title, status rows, and guide, plus IPC-backed toggle summary and checked state", async () => {
    seedLoggedIn();
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => document.body.textContent?.includes("立即同步") ?? false);

    const text = document.body.textContent ?? "";
    expect(text).toContain("自动同步");
    expect(text).toContain("每 60 秒");
    expect(text).toContain("已知主机 已开启");
    expect(text).toContain("AI 模型档案 已关闭");

    const consoleCard = [...document.querySelectorAll("section.nx-card")].find((c) => c.textContent?.includes("立即同步"));
    expect(consoleCard?.querySelector(".nx-card-title")?.textContent).toBe("账号同步");
    expect(consoleCard?.textContent).toContain("数据会自动在本机与云端之间同步。");
    expect(consoleCard?.textContent).toContain("云端 0 个对象");

    const knownHost = [...document.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')].find(
      (c) => c.closest("label")?.textContent?.includes("同步已知主机"),
    );
    const aiProfile = [...document.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')].find(
      (c) => c.closest("label")?.textContent?.includes("同步 AI 模型档案"),
    );
    expect(knownHost?.checked).toBe(true);
    expect(aiProfile?.checked).toBe(false);
  });

  it("calls kindOptInSet when a toggle changes and refreshes from the returned value", async () => {
    seedLoggedIn();
    mocks.kindOptInSet.mockResolvedValue({ ...OPT_IN, knownHost: false });
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => document.body.textContent?.includes("立即同步") ?? false);

    const knownHost = [...document.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')].find(
      (c) => c.closest("label")?.textContent?.includes("同步已知主机"),
    )!;
    click(knownHost);
    await flushUntil(() => mocks.kindOptInSet.mock.calls.length === 1);
    expect(mocks.kindOptInSet).toHaveBeenCalledWith({ knownHost: false });
    await flushUntil(() => (document.body.textContent ?? "").includes("已知主机 已关闭"));
  });

  it("triggers sync_wake and shows a toast for immediate sync", async () => {
    seedLoggedIn();
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => document.body.textContent?.includes("立即同步") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("立即同步")) as HTMLButtonElement);
    await flushUntil(() => mocks.wake.mock.calls.length === 1);
    expect(mocks.toast).toHaveBeenCalledWith("success", "已触发一轮后台同步");
  });

  it("surfaces the previous sync failure from status.lastError", async () => {
    seedLoggedIn({ lastError: "connection refused" });
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => (document.body.textContent ?? "").includes("上次同步失败：connection refused"));
  });

  it("refreshes the real lastSuccessAt after an automatic round completes", async () => {
    const initial = "2026-01-02T03:04:00.000Z";
    const completed = "2026-01-02T04:05:00.000Z";
    const initialText = `上次同步：${formatTime(Date.parse(initial))}。`;
    const completedText = `上次同步：${formatTime(Date.parse(completed))}。`;
    seedLoggedIn({ lastSuccessAt: initial });
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => (document.body.textContent ?? "").includes(initialText));
    await flushUntil(() => mocks.roundCompleteHandlers.length === 1);
    const statusCalls = mocks.status.mock.calls.length;

    seedLoggedIn({ lastSuccessAt: completed });
    fireRoundComplete();

    await flushUntil(() => (document.body.textContent ?? "").includes(completedText));
    expect(mocks.status.mock.calls.length).toBeGreaterThan(statusCalls);
    expect(document.body.textContent).not.toContain(initialText);
  });

  it("waits for a slow manual round instead of refreshing after a fixed delay", async () => {
    vi.useFakeTimers();
    const initial = "2026-01-02T03:04:00.000Z";
    const completed = "2026-01-02T04:05:00.000Z";
    const initialText = `上次同步：${formatTime(Date.parse(initial))}。`;
    const completedText = `上次同步：${formatTime(Date.parse(completed))}。`;
    seedLoggedIn({ lastSuccessAt: initial });
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => (document.body.textContent ?? "").includes(initialText));
    await flushUntil(() => mocks.roundCompleteHandlers.length === 1);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("立即同步")) as HTMLButtonElement);
    await flushUntil(() => mocks.wake.mock.calls.length === 1);
    const statusCalls = mocks.status.mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(mocks.status.mock.calls.length).toBe(statusCalls);
    expect(document.body.textContent).toContain(initialText);

    seedLoggedIn({ lastSuccessAt: completed });
    fireRoundComplete();
    await flushUntil(() => (document.body.textContent ?? "").includes(completedText));
    expect(document.body.textContent).not.toContain(initialText);
  });

  it("explains content-type opt-out and per-transcript cloud deletion", async () => {
    seedLoggedIn();
    mounted = withClient(createElement(SyncCard));
    await flushUntil(() => document.body.textContent?.includes("立即同步") ?? false);

    expect(document.body.textContent).toContain(
      "关闭上方内容类型开关不会删除云端已有副本；在「终端历史」关闭单条录像同步会删除其云端副本，本机记录保留。",
    );
  });
});
