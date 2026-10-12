/** @vitest-environment jsdom */
// 桌面端分享卡 · 未登录: 给「登录服务端账号后可用」引导卡 (按钮滚向「账号同步」分区), 不发任何请求。
import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flushUntil, mountWithClient, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return { fetch: vi.fn(), status: vi.fn() };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    syncApi: { ...actual.syncApi, status: mocks.status },
  };
});

import { ShareCard } from "../../features/settings/ShareCard";

let mounted: MountedView | undefined;

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  document.body.replaceChildren();
  vi.unstubAllGlobals();
});

describe("分享卡片 · 桌面端未登录", () => {
  it("渲染登录引导卡且零请求, 按钮滚向「账号同步」分区", async () => {
    vi.stubGlobal("fetch", mocks.fetch);
    mocks.status.mockResolvedValue({ configured: false, loggedIn: false, seq: 0, verifiedAt: 0, lastError: "" });
    const scrollIntoView = vi.fn();
    Element.prototype.scrollIntoView = scrollIntoView;
    // 分享卡在设置页内, 「账号同步」分区必然存在; 补一个目标节点验证滚动行为。
    const anchor = document.createElement("div");
    anchor.id = "settings-account";
    document.body.appendChild(anchor);
    mounted = mountWithClient(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("登录服务端账号后可用") ?? false);

    expect(document.body.textContent).toContain("账号同步");
    expect(mocks.fetch).not.toHaveBeenCalled();

    const guide = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("去设置"));
    expect(guide).toBeTruthy();
    click(guide as HTMLButtonElement);
    expect(scrollIntoView).toHaveBeenCalledWith({ behavior: "smooth", block: "start" });
  });
});
