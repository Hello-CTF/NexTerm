/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { flush, mount } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  overview: vi.fn(),
  usageSummary: vi.fn(),
  presets: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  modelApi: {
    overview: mocks.overview,
    usageSummary: mocks.usageSummary,
    presets: mocks.presets,
    save: vi.fn(),
    remove: vi.fn(),
    activate: vi.fn(),
    refresh: vi.fn(),
    preset: vi.fn(),
  },
}));
vi.mock("../../app/store", () => ({
  useUi: Object.assign(vi.fn(), {
    getState: () => ({ pushToast: vi.fn(), bumpModelProfilesRevision: vi.fn() }),
  }),
}));
vi.mock("../../ui/dialogs", () => ({ ask: vi.fn() }));

import { UsageSummarySection } from "../../features/ai/ModelPanel";

describe("模型用量汇总", () => {
  beforeEach(() => {
    mocks.overview.mockResolvedValue({
      profiles: [{ id: "profile-1", name: "公司 DeepSeek" }],
      activeId: "profile-1",
    });
    mocks.usageSummary.mockResolvedValue([
      { source: "chat", profileId: "profile-1", runs: 3, tokensIn: 1200, tokensOut: 300, cacheCreationTokens: 128, averageLatencyMs: 1500 },
      { source: "cron", profileId: "", runs: 1, tokensIn: 800, tokensOut: 0, cacheCreationTokens: 0, averageLatencyMs: 240 },
      { source: "subagent", profileId: "profile-1", runs: 2, tokensIn: 400, tokensOut: 90, cacheCreationTokens: 12, averageLatencyMs: 620 },
    ]);
    mocks.presets.mockResolvedValue([]);
  });

  afterEach(() => {
    vi.clearAllMocks();
    document.body.innerHTML = "";
  });

  it("按场景与档案渲染 token、缓存写入与延迟", async () => {
    const view = mount(createElement(UsageSummarySection));
    await flush();
    const text = view.container.textContent ?? "";
    expect(text).toContain("交互聊天");
    expect(text).toContain("定时任务");
    expect(text).toContain("子代理");
    expect(text).toContain("公司 DeepSeek");
    expect(text).toContain("未记录");
    expect(text).toContain("1.2k");
    expect(text).toContain("128");
    expect(text).toContain("1.5s");
    expect(text).toContain("240ms");
    view.unmount();
  });

  it("用量接口失败时显示错误而不是空白", async () => {
    mocks.usageSummary.mockRejectedValue(new Error("boom"));
    const view = mount(createElement(UsageSummarySection));
    await flush();
    expect(view.container.textContent).toContain("用量加载失败");
    view.unmount();
  });

  it("空数据显示占位文案", async () => {
    mocks.usageSummary.mockResolvedValue([]);
    const view = mount(createElement(UsageSummarySection));
    await flush();
    expect(view.container.textContent).toContain("还没有已完成的 AI 运行");
    view.unmount();
  });
});
