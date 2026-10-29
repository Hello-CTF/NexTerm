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
  remove: vi.fn(),
  clear: vi.fn(),
  ask: vi.fn(),
  revealArchive: vi.fn(),
  copyText: vi.fn(),
}));

vi.mock("../../ipc/diagnosticsApi", () => ({
  diagnosticsApi: mocks,
}));
vi.mock("../../ui/clipboard", () => ({ copyText: mocks.copyText }));
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});

import { DiagnosticsCard } from "../../features/settings/DiagnosticsCard";

let mounted: MountedView | undefined;

const idle = {
  armed: false,
  active: false,
  root: "/logs/debug-capture",
  canOpenFolder: true,
  captures: [] as Array<{ name: string; path: string; sizeBytes: number; timeMs: number; archive?: string }>,
};

function button(label: string): HTMLButtonElement {
  const el = [...(mounted?.container.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === label);
  if (!el) throw new Error(`button missing: ${label}`);
  return el;
}

const hasButton = (label: string) =>
  [...(mounted?.container.querySelectorAll("button") ?? [])].some((b) => b.textContent?.trim() === label);

function text(): string {
  return mounted?.container.textContent ?? "";
}

beforeEach(() => {
  for (const fn of Object.values(mocks)) fn.mockReset();
  mocks.ask.mockResolvedValue(true);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("DiagnosticsCard", () => {
  it("arms the next launch with a button and shows a cancel button once armed", async () => {
    mocks.status.mockResolvedValue(idle);
    mocks.arm.mockResolvedValueOnce({ ...idle, armed: true }).mockResolvedValueOnce(idle);
    mounted = mount(createElement(DiagnosticsCard));
    await flushUntil(() => hasButton("开启下次启动的调试模式") && !button("开启下次启动的调试模式").disabled);
    expect(mounted.container.querySelector('input[type="checkbox"]')).toBeNull();
    expect(text()).toContain("发送前请自行检查");

    await act(async () => {
      button("开启下次启动的调试模式").click();
    });
    await flushUntil(() => hasButton("取消"));
    expect(mocks.arm).toHaveBeenLastCalledWith(true);
    expect(text()).toContain("重启应用后开始记录");
    expect(hasButton("开启下次启动的调试模式")).toBe(false);

    await act(async () => {
      button("取消").click();
    });
    await flushUntil(() => hasButton("开启下次启动的调试模式"));
    expect(mocks.arm).toHaveBeenLastCalledWith(false);
  });

  it("deletes one record after confirmation and never offers deleting the running capture", async () => {
    const running = "/logs/debug-capture/20260102T000000Z";
    const old = "/logs/debug-capture/20260101T000000Z";
    const both = {
      ...idle,
      active: true,
      activeDir: running,
      captures: [
        { name: "20260102T000000Z", path: running, sizeBytes: 1, timeMs: 0 },
        { name: "20260101T000000Z", path: old, sizeBytes: 1, timeMs: 0 },
      ],
    };
    mocks.status.mockResolvedValue(both);
    mocks.remove.mockResolvedValue({ ...both, captures: [both.captures[0]] });
    mounted = mount(createElement(DiagnosticsCard));
    await flushUntil(() => text().includes("历史记录"));
    expect(mounted.container.querySelector('[aria-label="删除 20260102T000000Z"]')).toBeNull();

    await act(async () => {
      mounted!.container.querySelector<HTMLButtonElement>('[aria-label="删除 20260101T000000Z"]')!.click();
    });
    await flushUntil(() => mocks.remove.mock.calls.length > 0);
    expect(mocks.ask).toHaveBeenCalled();
    expect(mocks.remove).toHaveBeenCalledWith(old);
    await flushUntil(() => mounted!.container.querySelector('[aria-label="删除 20260101T000000Z"]') === null);
  });

  it("does not delete when the confirmation is declined, and clears everything on confirm", async () => {
    const dir = "/logs/debug-capture/20260101T000000Z";
    const withOne = { ...idle, captures: [{ name: "20260101T000000Z", path: dir, sizeBytes: 1, timeMs: 0 }] };
    mocks.status.mockResolvedValue(withOne);
    mocks.clear.mockResolvedValue(idle);
    mounted = mount(createElement(DiagnosticsCard));
    await flushUntil(() => hasButton("清空全部记录"));

    mocks.ask.mockResolvedValueOnce(false);
    await act(async () => {
      button("清空全部记录").click();
    });
    await flush();
    expect(mocks.clear).not.toHaveBeenCalled();

    await act(async () => {
      button("清空全部记录").click();
    });
    await flushUntil(() => mocks.clear.mock.calls.length === 1);
    await flushUntil(() => !text().includes("历史记录"));
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

  it("shows an error when arming fails and stays unarmed", async () => {
    mocks.status.mockResolvedValue(idle);
    mocks.arm.mockRejectedValue({ code: "io", message: "无法更新调试模式开关：disk full" });
    mounted = mount(createElement(DiagnosticsCard));
    await flushUntil(() => hasButton("开启下次启动的调试模式") && !button("开启下次启动的调试模式").disabled);
    await act(async () => {
      button("开启下次启动的调试模式").click();
    });
    await flushUntil(() => text().includes("disk full"));
    expect(hasButton("开启下次启动的调试模式")).toBe(true);
    expect(hasButton("取消")).toBe(false);
  });

  it("explains the missing permission instead of leaving an empty section", async () => {
    mocks.status.mockRejectedValue({ code: "forbidden", message: "仅管理员可使用诊断功能" });
    mounted = mount(createElement(DiagnosticsCard));
    await flushUntil(() => text().includes("仅管理员可使用调试模式"));
    expect(hasButton("开启下次启动的调试模式")).toBe(false);
  });

  it("surfaces status load failures", async () => {
    mocks.status.mockRejectedValue({ code: "internal", message: "boom" });
    mounted = mount(createElement(DiagnosticsCard));
    await flushUntil(() => text().includes("应用内部出现错误，请稍后重试"));
  });

  it("opens the capture root from the top-level button", async () => {
    mocks.status.mockResolvedValue(idle);
    mocks.openFolder.mockResolvedValue(null);
    mounted = mount(createElement(DiagnosticsCard));
    await flushUntil(() => hasButton("打开记录目录"));
    clickButton(mounted.container, "打开记录目录");
    await flush();
    expect(mocks.openFolder).toHaveBeenCalledWith("");
  });

  it("hides the capture root button when the host cannot open folders", async () => {
    mocks.status.mockResolvedValue({ ...idle, canOpenFolder: false });
    mounted = mount(createElement(DiagnosticsCard));
    await flushUntil(() => hasButton("开启下次启动的调试模式") && !button("开启下次启动的调试模式").disabled);
    expect(hasButton("打开记录目录")).toBe(false);
  });

  it("reveals and copies the path of an existing archive, and offers neither without one", async () => {
    const dir = "/logs/debug-capture/a";
    mocks.status.mockResolvedValue({
      ...idle,
      captures: [
        { name: "a", path: dir, sizeBytes: 1, timeMs: 0, archive: `${dir}.zip` },
        { name: "b", path: "/logs/debug-capture/b", sizeBytes: 1, timeMs: 0 },
      ],
    });
    mocks.revealArchive.mockResolvedValue(null);
    mocks.copyText.mockResolvedValue(undefined);
    mounted = mount(createElement(DiagnosticsCard));
    await flushUntil(() => hasButton("在文件夹中显示"));
    expect(mounted.container.querySelectorAll('[aria-label^="在文件夹中显示"]').length).toBe(1);

    await act(async () => {
      button("在文件夹中显示").click();
    });
    expect(mocks.revealArchive).toHaveBeenCalledWith(`${dir}.zip`);
    await act(async () => {
      button("复制路径").click();
    });
    expect(mocks.copyText).toHaveBeenCalledWith(`${dir}.zip`);
  });

  it("hides reveal but keeps copy-path when the host cannot open folders", async () => {
    const dir = "/logs/debug-capture/a";
    mocks.status.mockResolvedValue({
      ...idle,
      canOpenFolder: false,
      captures: [{ name: "a", path: dir, sizeBytes: 1, timeMs: 0, archive: `${dir}.zip` }],
    });
    mounted = mount(createElement(DiagnosticsCard));
    await flushUntil(() => hasButton("复制路径"));
    expect(hasButton("在文件夹中显示")).toBe(false);
  });
});
