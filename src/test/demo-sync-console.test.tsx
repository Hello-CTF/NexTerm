/** @vitest-environment jsdom */
// M210: 演示同步台的按钮文案必须与真实 WEB 同步台一致(推送到云端 (N) / 拉取并应用 (N)),
// 演示按钮全部禁用,不伪造同步动作。
import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flushUntil, mount, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({ fetch: vi.fn() }));

import { SyncCard } from "../features/settings/SyncCard";

let mounted: MountedView | undefined;

function mountSyncCard(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(SyncCard)));
}

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
});

describe("演示同步台 · 文案对齐真实同步台", () => {
  it("按钮与真实同步台同名(推送到云端 / 拉取并应用),且全部禁用", async () => {
    vi.stubGlobal("fetch", mocks.fetch);
    mounted = mountSyncCard();
    await flushUntil(() => document.body.textContent?.includes("演示模式下同步数据是假的") ?? false);

    const text = document.body.textContent ?? "";
    expect(text).toContain("推送到云端 (2)");
    expect(text).toContain("拉取并应用 (0)");
    expect(text).not.toContain("从云端拉取");
    expect(text).toContain("手动推送到云端或拉取并应用");

    const buttons = [...mounted.container.querySelectorAll("button")].filter(
      (b) => b.textContent?.includes("推送到云端") || b.textContent?.includes("拉取并应用"),
    );
    expect(buttons.length).toBe(2);
    expect(buttons.every((b) => (b as HTMLButtonElement).disabled)).toBe(true);
    expect(mocks.fetch).not.toHaveBeenCalled();
  });
});
