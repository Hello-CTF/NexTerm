/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, mount, waitForRendered, type MountedView } from "./reactTestUtils";
import type { PrivacyStatus } from "../../ipc/privacy";

const mocks = vi.hoisted(() => ({ status: vi.fn(), addTab: vi.fn(), show: vi.fn() }));
vi.mock("../../ipc/commands", () => ({
  call: (command: string) => {
    if (command === "ai_privacy_status") return mocks.status();
    throw new Error(`Unexpected command: ${command}`);
  },
}));
vi.mock("../../app/store", () => ({ useUi: { getState: () => ({ addTab: mocks.addTab }) } }));
vi.mock("../../features/settings/settingsNavigation", () => ({ showSettingsSection: mocks.show }));
import { PrivacyIndicator, usePrivacyGuard } from "../../features/ai/PrivacyIndicator";

const status = (patch: Partial<PrivacyStatus>): PrivacyStatus => ({
  config: { enabled: true, precision: "int8", revision: "a".repeat(40), version: "v0.1.3" },
  installed: [],
  runtime: { supported: true },
  selection: { available: true },
  diskUsage: 0,
  canManage: true,
  download: { running: false, precision: "int8", progress: { file: "", downloaded: 0, total: 0 } },
  ...patch,
});
function Probe() {
  const guard = usePrivacyGuard();
  return createElement("div", null,
    createElement("button", { "aria-label": "添加图片", disabled: guard.enabled, title: guard.enabled ? "已开启 AI 信息脱敏，暂不支持图片" : "添加图片" }),
    createElement(PrivacyIndicator, { guard }));
}
let mounted: MountedView | undefined;
beforeEach(() => { vi.resetAllMocks(); });
afterEach(() => { mounted?.unmount(); mounted = undefined; });

describe("AI chat privacy indicator", () => {
  it("shows the guard is active and blocks image attachment", async () => {
    mocks.status.mockResolvedValue(status({}));
    mounted = mount(createElement(Probe));
    await waitForRendered(() => expect(mounted!.container.textContent).toContain("已脱敏"));
    expect(mounted.container.querySelector<HTMLButtonElement>('[aria-label="添加图片"]')!.disabled).toBe(true);
  });

  it("links a broken guard to the privacy settings", async () => {
    mocks.status.mockResolvedValue(status({ selection: { available: false, error: "本地隐私模型文件缺失" } }));
    mounted = mount(createElement(Probe));
    await waitForRendered(() => expect(mounted!.container.textContent).toContain("脱敏不可用"));
    const link = [...mounted.container.querySelectorAll("button")].find((item) => item.textContent === "脱敏不可用")!;
    click(link);
    expect(mocks.addTab).toHaveBeenCalledWith(expect.objectContaining({ kind: "settings" }));
    expect(mocks.show).toHaveBeenCalledWith("settings-ai-privacy");
  });

  it("stays hidden when the guard is off or its status cannot be read", async () => {
    mocks.status.mockRejectedValue(new Error("forbidden"));
    mounted = mount(createElement(Probe));
    await waitForRendered(() => expect(mocks.status).toHaveBeenCalled());
    expect(mounted.container.textContent).not.toContain("脱敏");
    expect(mounted.container.querySelector<HTMLButtonElement>('[aria-label="添加图片"]')!.disabled).toBe(false);
  });
});
