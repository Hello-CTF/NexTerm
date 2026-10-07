/** @vitest-environment jsdom */
// SHARE158: 桌面端 (无账号体系) 必须给显式不可用态, 不发任何 HTTP。
import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return { fetch: vi.fn() };
});

import { ShareCard } from "../../features/settings/ShareCard";

let mounted: MountedView | undefined;

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
});

describe("分享卡片 · 桌面端", () => {
  it("渲染显式不可用态且零请求", async () => {
    vi.stubGlobal("fetch", mocks.fetch);
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("桌面端不可用") ?? false);

    expect(document.body.textContent).toContain("浏览器模式");
    expect(mocks.fetch).not.toHaveBeenCalled();
  });
});
