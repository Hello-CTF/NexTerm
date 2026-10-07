/** @vitest-environment jsdom */
// FLEET149: 演示模式必须给显式不可用态, 不发任何 HTTP, 不走 demo 假后端。
import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({ fetch: vi.fn() }));

import { DevicesView } from "../../features/fleet/DevicesView";

let mounted: MountedView | undefined;

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
});

describe("设备管理 · 演示模式", () => {
  it("渲染显式不可用态且零请求", async () => {
    vi.stubGlobal("fetch", mocks.fetch);
    mounted = mount(createElement(DevicesView));
    await flushUntil(() => document.body.textContent?.includes("演示模式没有设备管理") ?? false);

    expect(document.body.textContent).toContain("不会伪造设备列表");
    expect(mocks.fetch).not.toHaveBeenCalled();
  });
});
