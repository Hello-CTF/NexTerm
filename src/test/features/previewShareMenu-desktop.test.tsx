/** @vitest-environment jsdom */
// 桌面端只读预览: PreviewShareMenu 按 TRANSPORT 分流 — 创建走 preview_share_create
// 中继 (展示后端拼好的 url), 列表/吊销走 remoteSharingApi 并按当前 tab 活跃中继
// 的壳 id 过滤; TerminalPane 入口的登录门控 hook 在未登录桌面返回 false (隐藏入口)。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { clickButton, flushUntil, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    status: vi.fn(),
    create: vi.fn(),
    active: vi.fn(),
    previewLinks: vi.fn(),
    revokePreviewLink: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, syncApi: { ...actual.syncApi, status: mocks.status } };
});
vi.mock("../../ipc/remoteApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/remoteApi")>();
  return {
    ...actual,
    remotePreviewApi: { ...actual.remotePreviewApi, create: mocks.create, active: mocks.active },
    remoteSharingApi: {
      ...actual.remoteSharingApi,
      previewLinks: mocks.previewLinks,
      revokePreviewLink: mocks.revokePreviewLink,
    },
  };
});

import { PreviewShareMenu } from "../../features/sharing/PreviewShareMenu";
import { usePreviewShareEnabled } from "../../features/terminal/TerminalPane";

const NOW = Date.now();

function linkOn(sessionId: string, id: string, overrides: Record<string, unknown> = {}) {
  return {
    id,
    owner_id: "u-1",
    session_id: sessionId,
    created_at: NOW - 60_000,
    expires_at: NOW + 3_600_000,
    ...overrides,
  };
}

let mounted: MountedView | undefined;

beforeEach(() => {
  mocks.status.mockResolvedValue({
    configured: true,
    loggedIn: true,
    username: "alice",
    userId: "u-1",
    head: "h-1",
    seq: 0,
    verifiedAt: 0,
    lastError: "",
  });
  mocks.create.mockResolvedValue({
    token: "tok-1",
    url: "https://sync.example.com/share/preview/tok-1",
    sessionId: "shell-1",
  });
  mocks.active.mockResolvedValue({ sessionId: "shell-1" });
  mocks.previewLinks.mockResolvedValue({ links: [] });
  mocks.revokePreviewLink.mockResolvedValue({ ok: true });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.clearAllMocks();
});

function mountMenu(): HTMLDivElement {
  mounted = mountWithClient(createElement(PreviewShareMenu, { tabId: "tab-1", sessionName: "web-01" }));
  clickButton(mounted.container, "只读预览");
  return mounted.container;
}

describe("PreviewShareMenu 桌面分流", () => {
  it("创建走 preview_share_create 中继并展示后端拼好的 url", async () => {
    const container = mountMenu();
    await flushUntil(() => container.querySelector("select") !== null);
    clickButton(container, "创建链接");
    await flushUntil(() => container.textContent?.includes("https://sync.example.com/share/preview/tok-1") === true);
    expect(mocks.create).toHaveBeenCalledWith("tab-1", 3_600_000);
  });

  it("列表按当前 tab 活跃中继的壳 id 过滤, 吊销走 remoteSharingApi", async () => {
    mocks.previewLinks.mockResolvedValue({
      links: [
        linkOn("shell-1", "l-1"),
        linkOn("shell-2", "l-2"),
        linkOn("shell-1", "l-3", { revoked_at: NOW - 1_000 }),
        linkOn("shell-1", "l-4", { expires_at: NOW - 1_000 }),
      ],
    });
    const container = mountMenu();
    await flushUntil(() => container.textContent?.includes("进行中的预览") === true);
    const revokes = [...container.querySelectorAll("button")].filter((b) => b.textContent?.trim() === "吊销");
    expect(revokes).toHaveLength(1);
    expect(mocks.active).toHaveBeenCalledWith("tab-1");

    clickButton(container, "吊销");
    await flushUntil(() => mocks.revokePreviewLink.mock.calls.length === 1);
    expect(mocks.revokePreviewLink).toHaveBeenCalledWith("l-1");
    await flushUntil(() => mocks.previewLinks.mock.calls.length >= 2);
  });

  it("当前 tab 无活跃中继时不列出任何链接", async () => {
    mocks.active.mockResolvedValue({});
    mocks.previewLinks.mockResolvedValue({ links: [linkOn("shell-9", "l-9")] });
    const container = mountMenu();
    await flushUntil(() => mocks.active.mock.calls.length >= 1);
    await flushUntil(() => container.textContent?.includes("创建链接") === true);
    expect(mocks.previewLinks).not.toHaveBeenCalled();
    expect(container.textContent?.includes("进行中的预览")).toBe(false);
  });
});

describe("TerminalPane 只读预览登录门控", () => {
  function GateProbe() {
    const enabled = usePreviewShareEnabled();
    return <span>{enabled ? "on" : "off"}</span>;
  }

  it("已登录桌面返回 true", async () => {
    mounted = mountWithClient(createElement(GateProbe));
    await flushUntil(() => mounted!.container.textContent === "on");
  });

  it("未登录桌面返回 false (入口隐藏)", async () => {
    mocks.status.mockResolvedValue({ configured: false, loggedIn: false, seq: 0, verifiedAt: 0, lastError: "" });
    mounted = mountWithClient(createElement(GateProbe));
    await flushUntil(() => mounted!.container.textContent === "off");
  });
});
