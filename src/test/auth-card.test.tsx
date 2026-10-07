/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flush, mount, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return { devices: vi.fn() };
});

vi.mock("../ipc/authApi", async (importOriginal) => {
  const original = await importOriginal<typeof import("../ipc/authApi")>();
  return {
    ...original,
    authApi: { devices: mocks.devices },
  };
});

import { AuthCard } from "../features/settings/AuthCard";
import { useAuth } from "../features/auth/store";

let mounted: MountedView | undefined;

function mountCard(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(AuthCard)));
}

function seed(status: { initialized: boolean; auth: string }) {
  useAuth.setState({
    status: { ...status, registration_open: false },
    user: null,
    dek: null,
    gate: "ready",
    pendingRecoveryKey: null,
    error: null,
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.devices.mockResolvedValue({ devices: [] });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("AuthCard 未登录账号卡", () => {
  it("未初始化的 loopback 实例:显示显式初始化入口与匿名说明", async () => {
    seed({ initialized: false, auth: "loopback" });
    mounted = mountCard();
    await flush();
    expect(mounted.container.textContent).toContain("这台服务器还没有任何账号");
    expect(mounted.container.textContent).toContain("当前允许匿名使用");
    clickButton(mounted.container, "初始化账号");
    expect(useAuth.getState().gate).toBe("setup");
  });

  it("未初始化的 auth=on 实例:同样提供初始化入口", async () => {
    seed({ initialized: false, auth: "on" });
    mounted = mountCard();
    await flush();
    expect(mounted.container.textContent).toContain("这台服务器还没有任何账号");
    expect(mounted.container.textContent).not.toContain("当前允许匿名使用");
    clickButton(mounted.container, "初始化账号");
    expect(useAuth.getState().gate).toBe("setup");
  });

  it("已初始化未登录:显示登录入口,不显示初始化入口", async () => {
    seed({ initialized: true, auth: "on" });
    mounted = mountCard();
    await flush();
    expect(mounted.container.textContent).toContain("当前未登录");
    expect([...mounted.container.querySelectorAll("button")].some((b) => b.textContent?.includes("初始化账号"))).toBe(false);
    clickButton(mounted.container, "登录");
    expect(useAuth.getState().gate).toBe("login");
  });
});
