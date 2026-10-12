/** @vitest-environment jsdom */
// WEB 端只读预览: PreviewShareMenu 分流保持 previewApi 直连 — 创建经
// createPreviewLink (sessionId 即 tabId), URL 用 previewPublicUrl 拼, 列表按
// tabId 直接过滤, 吊销走 revokePreviewLink; 不触桌面中继入口。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { clickButton, flushUntil, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  createPreviewLink: vi.fn(),
  listPreviewLinks: vi.fn(),
  revokePreviewLink: vi.fn(),
  remoteCreate: vi.fn(),
  remoteActive: vi.fn(),
}));

vi.mock("../../features/sharing/previewApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../features/sharing/previewApi")>();
  return {
    ...actual,
    createPreviewLink: mocks.createPreviewLink,
    listPreviewLinks: mocks.listPreviewLinks,
    revokePreviewLink: mocks.revokePreviewLink,
  };
});
vi.mock("../../ipc/remoteApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/remoteApi")>();
  return {
    ...actual,
    remotePreviewApi: { ...actual.remotePreviewApi, create: mocks.remoteCreate, active: mocks.remoteActive },
  };
});

import { PreviewShareMenu } from "../../features/sharing/PreviewShareMenu";

const NOW = Date.now();

let mounted: MountedView | undefined;

beforeEach(() => {
  mocks.createPreviewLink.mockResolvedValue({
    id: "pl-1",
    owner_id: "u-1",
    session_id: "tab-1",
    created_at: NOW,
    expires_at: NOW + 3_600_000,
    token: "tok-web",
  });
  mocks.listPreviewLinks.mockResolvedValue({ links: [] });
  mocks.revokePreviewLink.mockResolvedValue({ ok: true });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.clearAllMocks();
});

describe("PreviewShareMenu WEB 分流", () => {
  it("创建经 previewApi 直连并用 previewPublicUrl 拼公开 URL", async () => {
    mounted = mountWithClient(createElement(PreviewShareMenu, { tabId: "tab-1", sessionName: "web-01" }));
    const container = mounted.container;
    clickButton(container, "只读预览");
    await flushUntil(() => container.querySelector("select") !== null);
    clickButton(container, "创建链接");
    await flushUntil(
      () => container.textContent?.includes(`${window.location.origin}/share/preview/tok-web`) === true,
    );
    expect(mocks.createPreviewLink).toHaveBeenCalledWith({ sessionId: "tab-1", ttlMs: 3_600_000 });
    expect(mocks.remoteCreate).not.toHaveBeenCalled();
  });

  it("列表按 tabId 直接过滤, 不查桌面中继状态", async () => {
    mocks.listPreviewLinks.mockResolvedValue({
      links: [
        { id: "l-1", owner_id: "u-1", session_id: "tab-1", created_at: NOW - 60_000, expires_at: NOW + 3_600_000 },
        { id: "l-2", owner_id: "u-1", session_id: "tab-2", created_at: NOW - 60_000, expires_at: NOW + 3_600_000 },
      ],
    });
    mounted = mountWithClient(createElement(PreviewShareMenu, { tabId: "tab-1", sessionName: "web-01" }));
    const container = mounted.container;
    clickButton(container, "只读预览");
    await flushUntil(() => container.textContent?.includes("进行中的预览") === true);
    const revokes = [...container.querySelectorAll("button")].filter((b) => b.textContent?.trim() === "吊销");
    expect(revokes).toHaveLength(1);
    expect(mocks.remoteActive).not.toHaveBeenCalled();

    clickButton(container, "吊销");
    await flushUntil(() => mocks.revokePreviewLink.mock.calls.length === 1);
    expect(mocks.revokePreviewLink).toHaveBeenCalledWith("l-1");
  });
});
