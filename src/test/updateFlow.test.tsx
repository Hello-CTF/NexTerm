/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { clickButton, deferred, flush, flushUntil, mount, type MountedView } from "./features/reactTestUtils";
import type { UpdateResultDto, UpdateStatusDto } from "../ipc/types";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    check: vi.fn(),
    install: vi.fn(),
    restart: vi.fn(),
    progressHandlers: [] as Array<(payload: unknown) => void>,
  };
});

vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    appUpdateApi: { check: mocks.check, install: mocks.install, restart: mocks.restart },
  };
});

vi.mock("../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/events")>();
  return {
    ...actual,
    listenEvent: (event: string, handler: (payload: unknown) => void) => {
      if (event === actual.EVENTS.updateProgress) {
        mocks.progressHandlers.push(handler);
        return Promise.resolve(() => {
          const index = mocks.progressHandlers.indexOf(handler);
          if (index >= 0) mocks.progressHandlers.splice(index, 1);
        });
      }
      return Promise.resolve(() => {});
    },
  };
});

import { UpdateCard } from "../features/settings/UpdateCard";
import { UpdateBanner } from "../app/UpdateBanner";
import { updateVerdict, useUpdateStore } from "../app/useUpdate";

function makeStatus(patch: Partial<UpdateStatusDto> = {}): UpdateStatusDto {
  return {
    available: true,
    currentVersion: "0.2.2",
    version: "0.3.0",
    notes: "",
    prerelease: false,
    assetName: "NexTerm-desktop_0.3.0_linux_amd64.tar.gz",
    assetSize: 8 << 20,
    canInstall: true,
    unavailableReason: "",
    ...patch,
  };
}

function resetUpdateStore(): void {
  useUpdateStore.setState({
    status: null,
    checking: false,
    checkError: null,
    install: { active: false, phase: null, transferred: 0, total: 0, error: null, taskId: null },
    installedVersion: null,
    restarting: false,
    restartError: null,
    dismissedVersion: null,
  });
}

function emitProgress(payload: unknown): void {
  for (const handler of [...mocks.progressHandlers]) handler(payload);
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  resetUpdateStore();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("updateVerdict 三态与 available/canInstall 分离", () => {
  it("已最新: available=false 且无不可用原因", () => {
    expect(
      updateVerdict({
        status: makeStatus({ available: false, version: "", assetName: "", assetSize: 0 }),
        checking: false,
        checkError: null,
      }),
    ).toBe("uptodate");
  });

  it("有更新但不可安装: available=true 且 canInstall=false", () => {
    expect(
      updateVerdict({ status: makeStatus({ canInstall: false }), checking: false, checkError: null }),
    ).toBe("unavailable");
  });

  it("检查失败: 传输层抛错", () => {
    expect(
      updateVerdict({ status: null, checking: false, checkError: "网络不可达" }),
    ).toBe("check-failed");
  });

  it("检查失败: 后端降级进 unavailableReason", () => {
    expect(
      updateVerdict({
        status: makeStatus({
          available: false,
          version: "",
          assetName: "",
          assetSize: 0,
          unavailableReason: "检查更新失败: 连接超时",
        }),
        checking: false,
        checkError: null,
      }),
    ).toBe("check-failed");
  });

  it("未检查与检查中", () => {
    expect(updateVerdict({ status: null, checking: false, checkError: null })).toBe("idle");
    expect(updateVerdict({ status: null, checking: true, checkError: null })).toBe("checking");
  });
});

describe("UpdateCard 检查流程", () => {
  it("展示当前版本与新版本,桌面端可安装时出现安装按钮", async () => {
    useUpdateStore.setState({ status: makeStatus() });
    mounted = mount(createElement(UpdateCard));
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("当前版本 v0.2.2");
    expect(text).toContain("发现新版本 v0.3.0");
    expect(text).toContain("有更新");
    expect(text).not.toContain("不可安装");
    expect(
      [...mounted.container.querySelectorAll("button")].some(
        (b) => b.textContent?.trim() === "下载并安装",
      ),
    ).toBe(true);
  });

  it("有更新但不可安装: 说明原因且不显示安装按钮", async () => {
    useUpdateStore.setState({
      status: makeStatus({ canInstall: false, unavailableReason: "当前平台没有安装包" }),
    });
    mounted = mount(createElement(UpdateCard));
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("有更新 · 不可安装");
    expect(text).toContain("当前平台没有安装包");
    expect(
      [...mounted.container.querySelectorAll("button")].some(
        (b) => b.textContent?.trim() === "下载并安装",
      ),
    ).toBe(false);
  });

  it("检查失败展示原因,立即检查可重试成功", async () => {
    mocks.check.mockRejectedValueOnce(new Error("网络不可达"));
    mounted = mount(createElement(UpdateCard));
    clickButton(mounted.container, "立即检查");
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("检查失败"));
    expect(mounted.container.textContent).toContain("网络不可达");

    mocks.check.mockResolvedValue(makeStatus({ available: false, version: "", assetName: "", assetSize: 0 }));
    clickButton(mounted.container, "立即检查");
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("已是最新"));
    expect(mocks.check).toHaveBeenCalledTimes(2);
  });

  it("检查在途时按钮进入检查中并禁用", async () => {
    const pending = deferred<UpdateStatusDto>();
    mocks.check.mockReturnValue(pending.promise);
    mounted = mount(createElement(UpdateCard));
    const button = clickButton(mounted.container, "立即检查");
    await flush();
    expect(button.disabled).toBe(true);
    expect(mounted.container.textContent).toContain("正在检查");
    pending.resolve(makeStatus());
    await flushUntil(() => !button.disabled);
  });
});

describe("UpdateCard 安装流程", () => {
  it("下载进度按 total 渲染百分比,total 缺失时走不确定进度", async () => {
    const pending = deferred<UpdateResultDto>();
    mocks.install.mockReturnValue(pending.promise);
    useUpdateStore.setState({ status: makeStatus() });
    mounted = mount(createElement(UpdateCard));
    clickButton(mounted.container, "下载并安装");
    expect(mocks.install).toHaveBeenCalledWith("0.3.0");

    act(() => {
      emitProgress({ taskId: "t1", phase: "download", transferred: 1 << 20, total: 8 << 20, done: false });
    });
    await flushUntil(
      () =>
        mounted?.container
          .querySelector('[role="progressbar"]')
          ?.getAttribute("aria-valuenow") === "13",
    );
    expect(mounted.container.textContent).toContain("正在下载");

    act(() => {
      emitProgress({ taskId: "t1", phase: "install", transferred: 0, total: 0, done: false });
    });
    await flushUntil(
      () =>
        mounted?.container.querySelector('[role="progressbar"]')?.getAttribute("aria-valuenow") ===
        null,
    );
    expect(mounted.container.textContent).toContain("正在安装");
    expect(mounted.container.textContent).toContain("进度未知");

    pending.resolve({ installed: true, restarted: false, version: "0.3.0", unavailableReason: "" });
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("已安装 v0.3.0"));
  });

  it("安装失败展示原因,重试会再次调用安装", async () => {
    mocks.install.mockRejectedValueOnce(new Error("磁盘已满"));
    useUpdateStore.setState({ status: makeStatus() });
    mounted = mount(createElement(UpdateCard));
    clickButton(mounted.container, "下载并安装");
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("安装失败"));
    expect(mounted.container.textContent).toContain("磁盘已满");

    mocks.install.mockResolvedValueOnce({
      installed: true,
      restarted: false,
      version: "0.3.0",
      unavailableReason: "",
    });
    clickButton(mounted.container, "重试");
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("已安装 v0.3.0"));
    expect(mocks.install).toHaveBeenCalledTimes(2);
  });

  it("进度事件携带错误时安装失败", async () => {
    const pending = deferred<UpdateResultDto>();
    mocks.install.mockReturnValue(pending.promise);
    useUpdateStore.setState({ status: makeStatus() });
    mounted = mount(createElement(UpdateCard));
    clickButton(mounted.container, "下载并安装");
    act(() => {
      emitProgress({ taskId: "t1", phase: "download", transferred: 0, total: 0, done: true, error: "校验和不匹配" });
    });
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("校验和不匹配"));
  });

  it("安装完成后提示重启,重启失败展示原因", async () => {
    mocks.restart.mockResolvedValue({ restarted: false, version: "", unavailableReason: "无法确定当前可执行文件路径" });
    useUpdateStore.setState({ status: makeStatus(), installedVersion: "0.3.0" });
    mounted = mount(createElement(UpdateCard));
    expect(mounted.container.textContent).toContain("重启应用后生效");
    clickButton(mounted.container, "立即重启");
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("无法确定当前可执行文件路径"));
    expect(mocks.restart).toHaveBeenCalledTimes(1);
  });
});

describe("UpdateBanner 桌面端横幅", () => {
  it("有更新时展示版本与入口,忽略后同版本不再出现", async () => {
    useUpdateStore.setState({ status: makeStatus() });
    const onOpenSettings = vi.fn();
    mounted = mount(createElement(UpdateBanner, { onOpenSettings }));
    const banner = mounted.container.querySelector('[aria-label="软件更新"]');
    expect(banner?.textContent).toContain("v0.3.0");
    expect(banner?.textContent).toContain("当前 v0.2.2");

    clickButton(mounted.container, "查看更新");
    expect(onOpenSettings).toHaveBeenCalledTimes(1);

    act(() => {
      mounted?.container.querySelector<HTMLButtonElement>('[aria-label="忽略此版本"]')?.dispatchEvent(
        new MouseEvent("click", { bubbles: true }),
      );
    });
    await flush();
    expect(mounted.container.querySelector('[aria-label="软件更新"]')).toBeNull();

    act(() => {
      useUpdateStore.setState({ status: makeStatus({ version: "0.3.1" }) });
    });
    await flush();
    expect(mounted.container.querySelector('[aria-label="软件更新"]')?.textContent).toContain("v0.3.1");
  });

  it("无更新或已安装待重启时不显示横幅", async () => {
    useUpdateStore.setState({ status: makeStatus({ available: false, version: "" }) });
    mounted = mount(createElement(UpdateBanner, { onOpenSettings: vi.fn() }));
    expect(mounted.container.querySelector('[aria-label="软件更新"]')).toBeNull();

    act(() => {
      useUpdateStore.setState({ status: makeStatus(), installedVersion: "0.3.0" });
    });
    await flush();
    expect(mounted.container.querySelector('[aria-label="软件更新"]')).toBeNull();
  });
});
