/** @vitest-environment jsdom */
// Desktop account-sync card state self-refresh: background auto-sync (startup probe / unlock wake / periodic poll) establishes a session or
// records a probe failure, then broadcasts via the existing sync://status event; a mounted card refetches linkGet/status on the event,
// the "pending sync" badge automatically becomes "logged in", and LastError surfaces automatically on failure.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, deferred, flush, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    linkGet: vi.fn(),
    status: vi.fn(),
    kindOptInGet: vi.fn(),
    compare: vi.fn(),
    linkSet: vi.fn(),
    ask: vi.fn(),
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
vi.mock("../../ipc/remoteApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/remoteApi")>();
  return { ...actual, remoteLinkApi: { ...actual.remoteLinkApi, linkSet: mocks.linkSet }, syncCompareApi: { ...actual.syncCompareApi, compare: mocks.compare } };
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
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));
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
  act(() => {
    for (const handler of [...mocks.syncStatusHandlers]) handler({});
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.syncStatusHandlers.length = 0;
  loggedIn = false;
  lastError = "";
  document.body.replaceChildren();
  mocks.linkGet.mockResolvedValue({ ...LINK });
  mocks.ask.mockResolvedValue(true);
  mocks.status.mockImplementation(() => Promise.resolve(statusPayload()));
  mocks.kindOptInGet.mockResolvedValue({ knownHost: false, aiProfile: false, aiPermission: false, aiGrant: false, preferences: false });
  mocks.compare.mockResolvedValue({ rows: [], remote: 0, warnings: [], userId: "u-1", compareToken: "compare-1" });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("Desktop account sync card - automatic status refresh", () => {
  it("changes from pending sync to logged in after a background session is established and sync://status is broadcast", async () => {
    mounted = mountCard();
    await flushUntil(() => document.body.textContent?.includes("待同步") ?? false);
    expect(mocks.syncStatusHandlers.length).toBeGreaterThan(0);

    loggedIn = true;
    const statusCalls = mocks.status.mock.calls.length;
    fireSyncStatus();
    await flushUntil(() =>
      document.body.textContent?.includes("已登录 alice") === true &&
      document.body.textContent?.includes("已知主机 已关闭") === true &&
      document.body.textContent?.includes("云端 0 个对象") === true,
    );
    expect(mocks.status.mock.calls.length).toBeGreaterThan(statusCalls);
  });

  it("surfaces LastError after a background probe failure and sync://status broadcast", async () => {
    mounted = mountCard();
    await flushUntil(() => document.body.textContent?.includes("待同步") ?? false);

    lastError = "cannot reach remote sync service: connection refused";
    fireSyncStatus();
    await flushUntil(() => document.body.textContent?.includes("上次失败：cannot reach remote sync service") ?? false);
    expect(document.body.textContent).not.toContain("已登录 alice");
  });
});


describe("Desktop login controls", () => {
  it("does not flash login controls while saved login state is loading", async () => {
    const pending = deferred<ReturnType<typeof statusPayload>>();
    mocks.status.mockReturnValue(pending.promise);
    mounted = mountCard();
    expect(mounted.container.textContent).toContain("正在检查登录状态");
    expect(mounted.container.querySelector("#sync-url")).toBeNull();
    expect([...mounted.container.querySelectorAll("button")].some((button) => button.textContent?.trim() === "登录")).toBe(false);
    await act(async () => pending.resolve(statusPayload()));
    await flushUntil(() => mounted!.container.querySelector("#sync-url") !== null);
  });

  it("shows only logout for an automatically signed-in account even if a sync round failed", async () => {
    loggedIn = true;
    lastError = "temporary network failure";
    mounted = mountCard();
    await flushUntil(() => mounted!.container.textContent?.includes("已登录 alice") === true);
    expect(mounted.container.querySelector("#sync-pass")).toBeNull();
    expect([...mounted.container.querySelectorAll("button")].some((button) => button.textContent?.trim() === "登录")).toBe(false);
    expect(mounted.container.textContent).toContain("上次同步失败");
    expect(mounted.container.textContent).toContain("登出");
  });

  it("keeps the account connected when logout confirmation is canceled", async () => {
    loggedIn = true;
    mocks.ask.mockResolvedValue(false);
    mounted = mountCard();
    await flushUntil(() => mounted!.container.textContent?.includes("已登录 alice") === true);
    clickButton(mounted.container, "登出");
    await flush();
    expect(mocks.ask).toHaveBeenCalledWith(expect.stringContaining("保留本地数据"), expect.objectContaining({ kind: "warning" }));
    expect(mocks.linkSet).not.toHaveBeenCalled();
    expect(mounted.container.textContent).toContain("已登录 alice");
  });

  it("clears saved credentials only after confirmation and restores the signed-out login form", async () => {
    loggedIn = true;
    mocks.linkSet.mockImplementation(async () => {
      loggedIn = false;
      const cleared = { ...LINK, username: "", hasPassword: false };
      mocks.linkGet.mockResolvedValue(cleared);
      return { ...cleared, connect: "" };
    });
    mounted = mountCard();
    await flushUntil(() => mounted!.container.textContent?.includes("已登录 alice") === true);
    const stale = deferred<ReturnType<typeof statusPayload>>();
    mocks.status.mockReturnValueOnce(stale.promise);
    fireSyncStatus();
    clickButton(mounted.container, "登出");
    await flushUntil(() => mounted!.container.querySelector("#sync-pass") !== null);
    expect(mocks.linkSet).toHaveBeenCalledExactlyOnceWith({ username: "", password: "" });
    expect(mounted.container.querySelector<HTMLInputElement>("#sync-url")?.value).toBe(LINK.url);
    expect(mounted.container.querySelector<HTMLInputElement>("#sync-user")?.value).toBe("");
    await act(async () => stale.resolve({ ...statusPayload(), loggedIn: true, username: "alice" }));
    expect(mounted.container.textContent).not.toContain("已登录 alice");
  });

  it("does not disconnect a different account that replaced the one being confirmed", async () => {
    loggedIn = true;
    const confirmation = deferred<boolean>();
    mocks.ask.mockReturnValue(confirmation.promise);
    mounted = mountCard();
    await flushUntil(() => mounted!.container.textContent?.includes("已登录 alice") === true);
    clickButton(mounted.container, "登出");
    mocks.linkGet.mockResolvedValue({ ...LINK, username: "bob" });
    mocks.status.mockResolvedValue({ ...statusPayload(), username: "bob", userId: "u-2" });
    fireSyncStatus();
    await flushUntil(() => mounted!.container.textContent?.includes("已登录 bob") === true);
    await act(async () => confirmation.resolve(true));
    expect(mocks.linkSet).not.toHaveBeenCalled();
    expect(mounted.container.textContent).toContain("已登录 bob");
  });
});
