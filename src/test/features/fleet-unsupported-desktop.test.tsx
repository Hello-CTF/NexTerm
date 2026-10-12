/** @vitest-environment jsdom */
// 桌面端设备管理 · 未登录: 入口可见, 点进来给「登录服务端账号后可用」引导视图
// (按钮打开「设置」并滚向「账号同步」分区), 不发任何请求。
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

import { DevicesView } from "../../features/fleet/DevicesView";
import { useUi } from "../../app/store";

let mounted: MountedView | undefined;

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useUi.setState({ workspaces: [], activeWorkspaceId: null });
  vi.unstubAllGlobals();
});

describe("设备管理 · 桌面端未登录", () => {
  it("渲染登录引导视图且零请求, 按钮打开「设置 → 账号同步」", async () => {
    vi.stubGlobal("fetch", mocks.fetch);
    mocks.status.mockResolvedValue({ configured: false, loggedIn: false, seq: 0, verifiedAt: 0, lastError: "" });
    Element.prototype.scrollIntoView = vi.fn();
    mounted = mountWithClient(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("登录服务端账号后可用") ?? false);

    expect(document.body.textContent).toContain("账号同步");
    expect(mocks.fetch).not.toHaveBeenCalled();

    const guide = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("去设置"));
    expect(guide).toBeTruthy();
    click(guide as HTMLButtonElement);
    const tabs = useUi.getState().workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs));
    expect(tabs.some((t) => t.id === "settings" && t.kind === "settings")).toBe(true);
  });
});
