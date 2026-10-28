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

describe("AuthCard signed-out account card", () => {
  it("waits for automatic session restoration before offering login", async () => {
    seed({ initialized: true, auth: "on" });
    useAuth.setState({ gate: "loading" });
    mounted = mountCard();
    await flush();
    expect(mounted.container.querySelector('[role="status"]')?.textContent).toContain("正在检查登录状态");
    expect(mounted.container.querySelector("button")).toBeNull();
  });

  it("shows only the auth-disabled explanation for auth=off, with no login or initialization entry", async () => {
    seed({ initialized: false, auth: "off" });
    mounted = mountCard();
    await flush();
    expect(mounted.container.textContent).toContain("这台服务器关闭了账号功能");
    const buttons = [...mounted.container.querySelectorAll("button")];
    expect(buttons.some((b) => b.textContent?.includes("初始化账号"))).toBe(false);
    expect(buttons.some((b) => b.textContent?.includes("登录"))).toBe(false);
    expect(useAuth.getState().gate).toBe("ready");
  });

  it("shows an explicit initialization entry and anonymous-use explanation for an uninitialized loopback instance", async () => {
    seed({ initialized: false, auth: "loopback" });
    mounted = mountCard();
    await flush();
    expect(mounted.container.textContent).toContain("这台服务器尚无任何账号");
    expect(mounted.container.textContent).toContain("当前允许匿名使用");
    clickButton(mounted.container, "初始化账号");
    expect(useAuth.getState().gate).toBe("setup");
  });

  it("also offers initialization for an uninitialized auth=on instance", async () => {
    seed({ initialized: false, auth: "on" });
    mounted = mountCard();
    await flush();
    expect(mounted.container.textContent).toContain("这台服务器尚无任何账号");
    expect(mounted.container.textContent).not.toContain("当前允许匿名使用");
    clickButton(mounted.container, "初始化账号");
    expect(useAuth.getState().gate).toBe("setup");
  });

  it("shows login but not initialization for an initialized signed-out instance", async () => {
    seed({ initialized: true, auth: "on" });
    mounted = mountCard();
    await flush();
    expect(mounted.container.textContent).toContain("当前未登录");
    expect([...mounted.container.querySelectorAll("button")].some((b) => b.textContent?.includes("初始化账号"))).toBe(false);
    clickButton(mounted.container, "登录");
    expect(useAuth.getState().gate).toBe("login");
  });

  it("explains automatic synchronization and continued local use while signed out", async () => {
    seed({ initialized: true, auth: "on" });
    mounted = mountCard();
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("账号当前未登录");
    expect(text).toContain("登录后，数据会自动在本机与服务端之间同步；不登录也能继续本地使用");
    expect(text).not.toContain("读取服务端副本进行对比，不会自动推送或应用，需手动触发");
  });
});
