/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { act } from "react";
import { clickButton, flush, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  status: vi.fn(),
  arm: vi.fn(),
  openFolder: vi.fn(),
  pack: vi.fn(),
}));

vi.mock("../../ipc/diagnosticsApi", () => ({
  diagnosticsApi: mocks,
}));

import { DiagnosticsCard } from "../../features/settings/DiagnosticsCard";

let mounted: MountedView | undefined;

const idle = {
  armed: false,
  active: false,
  root: "/logs/debug-capture",
  canOpenFolder: true,
  captures: [] as Array<{ name: string; path: string; sizeBytes: number; timeMs: number; archive?: string }>,
};

function checkbox(): HTMLInputElement {
  const el = mounted?.container.querySelector<HTMLInputElement>('input[type="checkbox"]');
  if (!el) throw new Error("checkbox missing");
  return el;
}

function text(): string {
  return mounted?.container.textContent ?? "";
}

beforeEach(() => {
  for (const fn of Object.values(mocks)) fn.mockReset();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("DiagnosticsCard", () => {
  it("arms the next launch through IPC and reflects the returned status", async () => {
    mocks.status.mockResolvedValue(idle);
    mocks.arm.mockResolvedValue({ ...idle, armed: true });
    mounted = mount(createElement(DiagnosticsCard));
    await flushUntil(() => !checkbox().disabled);
    expect(text()).toContain("下次启动时开启调试模式（仅一次）");
    expect(text()).toContain("不记录终端输出");
    expect(checkbox().checked).toBe(false);

    await act(async () => {
      checkbox().click();
    });
    await flushUntil(() => checkbox().checked);
    expect(mocks.arm).toHaveBeenCalledWith(true);
    expect(text()).toContain("请重启应用以开始记录");
  });

  it("shows the active banner with folder actions and packs the running capture", async () => {
    const dir = "/logs/debug-capture/20260101T000000Z";
    const active = {
      ...idle,
      active: true,
      activeDir: dir,
      captures: [{ name: "20260101T000000Z", path: dir, sizeBytes: 2048, timeMs: 1_700_000_000_000 }],
    };
    mocks.status.mockResolvedValue(active);
    mocks.pack.mockResolvedValue({ path: `${dir}.zip` });
    mocks.openFolder.mockResolvedValue(null);
    mounted = mount(createElement(DiagnosticsCard));
    await flushUntil(() => text().includes("本次启动正在记录调试信息"));
    expect(text()).toContain(dir);

    clickButton(mounted.container, "打开文件夹");
    expect(mocks.openFolder).toHaveBeenCalledWith(dir);

    clickButton(mounted.container, "打包");
    await flush();
    expect(mocks.pack).toHaveBeenCalledWith(dir);
    await flushUntil(() => text().includes(`${dir}.zip`));
  });

  it("hides open-folder actions when the host cannot open folders", async () => {
    mocks.status.mockResolvedValue({
      ...idle,
      canOpenFolder: false,
      captures: [{ name: "old", path: "/logs/debug-capture/old", sizeBytes: 10, timeMs: 0 }],
    });
    mounted = mount(createElement(DiagnosticsCard));
    await flushUntil(() => text().includes("历史记录"));
    expect(mounted.container.querySelector('[aria-label="打开 old"]')).toBeNull();
    expect(mounted.container.querySelector('[aria-label="打包 old"]')).not.toBeNull();
  });

  it("shows an error when arming fails and keeps the toggle unchanged", async () => {
    mocks.status.mockResolvedValue(idle);
    mocks.arm.mockRejectedValue({ code: "io", message: "无法更新调试模式开关：disk full" });
    mounted = mount(createElement(DiagnosticsCard));
    await flushUntil(() => !checkbox().disabled);
    await act(async () => {
      checkbox().click();
    });
    await flushUntil(() => text().includes("disk full"));
    expect(checkbox().checked).toBe(false);
  });

  it("explains the missing permission instead of leaving an empty section", async () => {
    mocks.status.mockRejectedValue({ code: "forbidden", message: "仅管理员可使用诊断功能" });
    mounted = mount(createElement(DiagnosticsCard));
    await flushUntil(() => text().includes("仅管理员可以使用调试模式"));
    expect(mounted?.container.querySelector('input[type="checkbox"]')).toBeNull();
  });

  it("surfaces status load failures", async () => {
    mocks.status.mockRejectedValue({ code: "internal", message: "boom" });
    mounted = mount(createElement(DiagnosticsCard));
    await flushUntil(() => text().includes("boom"));
  });
});
