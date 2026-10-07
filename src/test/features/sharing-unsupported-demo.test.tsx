/** @vitest-environment jsdom */
// SHARE158: 演示模式与桌面端必须给显式不可用态; 即使 demo 默认登录了超管账号,
// 也不得发任何 HTTP 或落进 demoAuthRequest 假后端。
import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({ fetch: vi.fn(), demoAuthRequest: vi.fn() }));

vi.mock("../../demo/auth", () => ({ demoAuthRequest: mocks.demoAuthRequest }));

import { ShareCard } from "../../features/settings/ShareCard";
import { useAuth } from "../../features/auth/store";

const DEMO_ADMIN = {
  id: "u-demo",
  username: "demo",
  display_name: "Demo",
  role: "superadmin" as const,
  state: "active" as const,
  must_change_password: false,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1,
};

let mounted: MountedView | undefined;

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
});

describe("分享卡片 · 演示模式", () => {
  it("渲染显式不可用态且零请求 (未登录)", async () => {
    vi.stubGlobal("fetch", mocks.fetch);
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("演示模式不可用") ?? false);

    expect(document.body.textContent).toContain("不会伪造分享列表");
    expect(mocks.fetch).not.toHaveBeenCalled();
    expect(mocks.demoAuthRequest).not.toHaveBeenCalled();
  });

  it("demo 已登录超管时同样零 HTTP 且零 demo 假后端调用", async () => {
    vi.stubGlobal("fetch", mocks.fetch);
    useAuth.setState({
      status: { initialized: true, registration_open: false, auth: "on" },
      user: DEMO_ADMIN,
      dek: null,
      gate: "ready",
      pendingRecoveryKey: null,
      error: null,
    });
    mounted = mount(createElement(ShareCard));
    await flushUntil(() => document.body.textContent?.includes("演示模式不可用") ?? false);
    await flushUntil(() => document.body.textContent?.includes("去登录") === false);

    expect(document.body.textContent).toContain("不会伪造分享列表");
    expect(mocks.fetch).not.toHaveBeenCalled();
    expect(mocks.demoAuthRequest).not.toHaveBeenCalled();
  });
});
