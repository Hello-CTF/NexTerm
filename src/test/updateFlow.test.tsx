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
    state: vi.fn(),
    progressHandlers: [] as Array<(payload: unknown) => void>,
  };
});

vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    appUpdateApi: { check: mocks.check, install: mocks.install, restart: mocks.restart, state: mocks.state },
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
import { useUi } from "../app/store";
import { applyLocale } from "../i18n";

function makeStatus(patch: Partial<UpdateStatusDto> = {}): UpdateStatusDto {
  return {
    available: true,
    currentVersion: "0.2.2",
    version: "0.3.0",
    notes: "",
    prerelease: false,
    assetName: "nexterm-desktop_linux_amd64_0.3.0.deb",
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
    skippedVersion: null,
    dismissedVersion: null,
    restartDeferred: false,
    remindedVersion: null,
  });
}

function emitProgress(payload: unknown): void {
  for (const handler of [...mocks.progressHandlers]) handler(payload);
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  window.localStorage.clear();
  resetUpdateStore();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("updateVerdict three states and separation of available/canInstall", () => {
  it("up to date: available=false and no unavailable reason", () => {
    expect(
      updateVerdict({
        status: makeStatus({ available: false, version: "", assetName: "", assetSize: 0 }),
        checking: false,
        checkError: null,
      }),
    ).toBe("uptodate");
  });

  it("update available but not installable: available=true and canInstall=false", () => {
    expect(
      updateVerdict({ status: makeStatus({ canInstall: false }), checking: false, checkError: null }),
    ).toBe("unavailable");
  });

  it("check failed: transport error thrown", () => {
    expect(
      updateVerdict({ status: null, checking: false, checkError: "网络不可达" }),
    ).toBe("check-failed");
  });

  it("check failed: backend degradation reported in unavailableReason", () => {
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

  it("external channel: platforms that update elsewhere are not a failed check", () => {
    expect(
      updateVerdict({
        status: makeStatus({ available: false, version: "", assetName: "", assetSize: 0, canInstall: false, channel: "app-store" }),
        checking: false,
        checkError: null,
      }),
    ).toBe("external");
  });

  it("idle and checking states", () => {
    expect(updateVerdict({ status: null, checking: false, checkError: null })).toBe("idle");
    expect(updateVerdict({ status: null, checking: true, checkError: null })).toBe("checking");
  });
});

describe("UpdateCard check flow", () => {
  it("shows current and new versions, with an install button when installation is available on desktop", async () => {
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

  it("update available but not installable: explains the reason and hides the install button", async () => {
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

  it("shows check failure reasons and succeeds when immediate check is retried", async () => {
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

  it.each([
    ["app-store", "TestFlight"],
    ["release", "Releases"],
  ])("explains the %s update channel instead of reporting a failure", async (channel, hint) => {
    mocks.check.mockResolvedValue(
      makeStatus({ available: false, version: "", assetName: "", assetSize: 0, canInstall: false, channel }),
    );
    mounted = mount(createElement(UpdateCard));
    await act(async () => {
      await useUpdateStore.getState().check();
    });
    await flushUntil(() => (mounted?.container.textContent ?? "").includes(hint));
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("不支持应用内更新");
    expect(text).not.toContain("检查失败");
    expect([...mounted.container.querySelectorAll("button")].some((b) => b.textContent?.includes("立即检查"))).toBe(false);
  });

  it("disables the button and shows checking state while a check is in flight", async () => {
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

describe("UpdateCard installation flow", () => {
  it("renders download percentage from total and uses indeterminate progress when total is missing", async () => {
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
    expect(mounted.container.textContent).toContain("请稍候");
    expect(mounted.container.textContent).not.toContain("进度未知");

    pending.resolve({ installed: true, restarted: false, version: "0.3.0", unavailableReason: "" });
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("已安装 v0.3.0"));
  });

  it("shows installation failure reasons and calls install again on retry", async () => {
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

  it("reports a failed install once, from the install result rather than the progress event", async () => {
    const toast = vi.fn();
    useUi.setState({ pushToast: toast });
    const pending = deferred<UpdateResultDto>();
    mocks.install.mockReturnValue(pending.promise);
    useUpdateStore.setState({ status: makeStatus() });
    mounted = mount(createElement("div", null, createElement(UpdateCard), createElement(UpdateBanner, { onOpenSettings: vi.fn() })));
    clickButton(mounted.container, "下载并安装");
    act(() => {
      emitProgress({ taskId: "t1", phase: "verify", transferred: 8, total: 8, done: false });
    });
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("正在校验"));
    act(() => {
      emitProgress({ taskId: "t1", phase: "verify", transferred: 8, total: 8, done: true, error: "raw checksum mismatch" });
    });
    await flush();
    expect(toast).not.toHaveBeenCalled();
    pending.reject(new Error("SHA-256 校验失败"));
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("安装失败"));
    expect(mounted.container.textContent).toContain("SHA-256 校验失败");
    expect(toast).toHaveBeenCalledTimes(1);
    expect(toast).toHaveBeenCalledWith("error", "更新安装失败：SHA-256 校验失败");
  });

  it("refreshes the status before a retry so a newer release replaces the failed one", async () => {
    mocks.install.mockRejectedValueOnce(new Error("待安装版本 0.3.0 与最新版本 0.3.1 不一致"));
    useUpdateStore.setState({ status: makeStatus() });
    mounted = mount(createElement(UpdateCard));
    clickButton(mounted.container, "下载并安装");
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("安装失败"));

    mocks.check.mockResolvedValue(makeStatus({ version: "0.3.1" }));
    mocks.install.mockResolvedValueOnce({ installed: true, restarted: false, version: "0.3.1", unavailableReason: "" });
    clickButton(mounted.container, "重试");
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("已安装 v0.3.1"));
    expect(mocks.check).toHaveBeenCalledTimes(1);
    expect(mocks.install).toHaveBeenLastCalledWith("0.3.1");
  });

  it("keeps the install waiting for a restart and explains what a restart ends", async () => {
    mocks.install.mockResolvedValue({ installed: true, restarted: false, version: "0.3.0", unavailableReason: "" });
    useUpdateStore.setState({ status: makeStatus() });
    mounted = mount(createElement(UpdateCard));
    expect(mounted.container.textContent).toContain("不会自动重启");
    clickButton(mounted.container, "下载并安装");
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("已安装 v0.3.0"));
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("临时终端会结束");
    expect(text).toContain("持久终端继续运行");
    expect(mocks.restart).not.toHaveBeenCalled();
  });

  it("prompts for restart after installation and shows restart failure reasons", async () => {
    mocks.restart.mockResolvedValue({ restarted: false, version: "", unavailableReason: "Unable to determine current executable path" });
    useUpdateStore.setState({ status: makeStatus(), installedVersion: "0.3.0" });
    mounted = mount(createElement(UpdateCard));
    expect(mounted.container.textContent).toContain("重启应用后生效");
    clickButton(mounted.container, "立即重启");
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("Unable to determine current executable path"));
    expect(mocks.restart).toHaveBeenCalledTimes(1);
  });
});

describe("UpdateBanner desktop banner", () => {
  it("shows version and entry point for available updates; closing hides it for this launch without skipping", async () => {
    useUpdateStore.setState({ status: makeStatus() });
    const onOpenSettings = vi.fn();
    mounted = mount(createElement(UpdateBanner, { onOpenSettings }));
    const banner = mounted.container.querySelector('[aria-label="软件更新"]');
    expect(banner?.textContent).toContain("v0.3.0");
    expect(banner?.textContent).toContain("当前 v0.2.2");

    clickButton(mounted.container, "查看更新");
    expect(onOpenSettings).toHaveBeenCalledTimes(1);

    act(() => {
      mounted?.container.querySelector<HTMLButtonElement>('[aria-label="本次启动内不再提示"]')?.dispatchEvent(
        new MouseEvent("click", { bubbles: true }),
      );
    });
    await flush();
    expect(mounted.container.querySelector('[aria-label="软件更新"]')).toBeNull();
    expect(window.localStorage.getItem("nexterm.update.v1")).toBeNull();
    expect(useUpdateStore.getState().skippedVersion).toBeNull();

    act(() => {
      useUpdateStore.setState({ status: makeStatus({ version: "0.3.1" }) });
    });
    await flush();
    expect(mounted.container.querySelector('[aria-label="软件更新"]')?.textContent).toContain("v0.3.1");
  });

  it("offers a later restart that hides the restart banner", async () => {
    useUpdateStore.setState({ status: makeStatus(), installedVersion: "0.3.0" });
    mounted = mount(createElement(UpdateBanner, { onOpenSettings: vi.fn() }));
    expect(mounted.container.textContent).toContain("重启应用后生效");
    clickButton(mounted.container, "稍后");
    await flush();
    expect(mounted.container.querySelector('[aria-label="软件更新"]')).toBeNull();
    expect(mocks.restart).not.toHaveBeenCalled();
  });

  it("hides the banner for skipped versions", async () => {
    useUpdateStore.setState({ status: makeStatus(), skippedVersion: "0.3.0" });
    mounted = mount(createElement(UpdateBanner, { onOpenSettings: vi.fn() }));
    expect(mounted.container.querySelector('[aria-label="软件更新"]')).toBeNull();
  });

  it("shows progress during installation and uses indeterminate progress when total is missing", async () => {
    useUpdateStore.setState({
      status: makeStatus(),
      install: {
        active: true,
        phase: "download",
        transferred: 4 << 20,
        total: 8 << 20,
        error: null,
        taskId: "t1",
      },
    });
    mounted = mount(createElement(UpdateBanner, { onOpenSettings: vi.fn() }));
    expect(mounted.container.textContent).toContain("正在下载");
    expect(mounted.container.textContent).toContain("v0.3.0");
    expect(
      mounted.container.querySelector('[role="progressbar"]')?.getAttribute("aria-valuenow"),
    ).toBe("50");
    expect(mounted.container.textContent).toContain("50%");

    act(() => {
      useUpdateStore.setState({
        install: {
          active: true,
          phase: "install",
          transferred: 0,
          total: 0,
          error: null,
          taskId: "t1",
        },
      });
    });
    await flush();
    expect(mounted.container.textContent).toContain("正在安装");
    expect(mounted.container.textContent).toContain("请稍候");
    expect(mounted.container.textContent).not.toContain("进度未知");
    expect(
      mounted.container.querySelector('[role="progressbar"]')?.getAttribute("aria-valuenow"),
    ).toBeNull();
  });

  it("shows an error toast when installation fails", async () => {
    const toast = vi.fn();
    useUi.setState({ pushToast: toast });
    useUpdateStore.setState({ status: makeStatus() });
    mounted = mount(createElement(UpdateBanner, { onOpenSettings: vi.fn() }));
    act(() => {
      useUpdateStore.setState({
        install: {
          active: false,
          phase: null,
          transferred: 0,
          total: 0,
          error: "磁盘已满",
          taskId: null,
        },
      });
    });
    await flush();
    expect(toast).toHaveBeenCalledWith("error", "更新安装失败：磁盘已满");
  });

  it("hides the banner when there is no update and shows the restart view after installation", async () => {
    useUpdateStore.setState({ status: makeStatus({ available: false, version: "" }) });
    mounted = mount(createElement(UpdateBanner, { onOpenSettings: vi.fn() }));
    expect(mounted.container.querySelector('[aria-label="软件更新"]')).toBeNull();

    const restart = vi.fn();
    act(() => {
      useUpdateStore.setState({ status: makeStatus(), installedVersion: "0.3.0", restart });
    });
    await flush();
    const banner = mounted.container.querySelector('[aria-label="软件更新"]');
    expect(banner).not.toBeNull();
    expect(banner!.textContent).toContain("已安装");
    expect(banner!.textContent).toContain("v0.3.0");
    expect(banner!.textContent).toContain("重启应用后生效");
    clickButton(mounted.container, "立即重启");
    expect(restart).toHaveBeenCalled();
  });
});

describe("UpdateCard release details", () => {
  it("shows release notes collapsed, the release page link and an explicit skip", async () => {
    useUpdateStore.setState({
      status: makeStatus({ notes: "## Fixes\n- faster", releaseUrl: "https://github.com/Hello-CTF/NexTerm/releases/tag/v0.3.0" }),
    });
    mounted = mount(createElement(UpdateCard));
    const details = mounted.container.querySelector<HTMLDetailsElement>('[data-testid="update-notes"]');
    expect(details?.open).toBe(false);
    expect(details?.textContent).toContain("faster");
    const link = [...mounted.container.querySelectorAll("a")].find((a) => a.textContent === "在 GitHub 查看发布页");
    expect(link?.getAttribute("href")).toBe("https://github.com/Hello-CTF/NexTerm/releases/tag/v0.3.0");

    clickButton(mounted.container, "跳过此版本");
    await flush();
    expect(window.localStorage.getItem("nexterm.update.v1")).toBe("0.3.0");
    expect(mounted.container.textContent).toContain("已跳过此版本");
    clickButton(mounted.container, "恢复提醒");
    await flush();
    expect(window.localStorage.getItem("nexterm.update.v1")).toBeNull();
  });

  it("links the package when the app cannot install it and notes a release still uploading", async () => {
    useUpdateStore.setState({
      status: makeStatus({
        canInstall: false,
        unavailableReason: "安装目录 /usr/bin 不可写，无法在应用内更新；请下载新版 .deb 安装包，用系统软件包管理器安装",
        downloadUrl: "https://example.invalid/nexterm-desktop_linux_amd64_0.3.0.deb",
        pendingVersion: "0.4.0",
      }),
    });
    mounted = mount(createElement(UpdateCard));
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("/usr/bin 不可写");
    expect(text).toContain("v0.4.0 已发布");
    const link = [...mounted.container.querySelectorAll("a")].find((a) => a.textContent === "下载安装包");
    expect(link?.getAttribute("href")).toBe("https://example.invalid/nexterm-desktop_linux_amd64_0.3.0.deb");
    expect(
      [...mounted.container.querySelectorAll("button")].some((b) => b.textContent?.trim() === "下载并安装"),
    ).toBe(false);
  });

  it("translates backend reasons like errors", async () => {
    applyLocale("en", {}, { "当前平台没有安装包": "No package for this platform" });
    try {
      useUpdateStore.setState({ status: makeStatus({ canInstall: false, unavailableReason: "当前平台没有安装包" }) });
      mounted = mount(createElement(UpdateCard));
      expect(mounted.container.textContent).toContain("No package for this platform");
    } finally {
      applyLocale("zh-CN", {}, {});
    }
  });
});

describe("install state recovery after a reload", () => {
  it("resumes a running install and settles it from the backend state", async () => {
    mocks.state.mockResolvedValueOnce({
      phase: "download", version: "0.3.0", taskId: "t9", transferred: 2, total: 8, error: "", installedVersion: "",
    });
    useUpdateStore.setState({ status: makeStatus() });
    mounted = mount(createElement(UpdateBanner, { onOpenSettings: vi.fn() }));
    await act(async () => {
      await useUpdateStore.getState().syncInstallState();
    });
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("25%"));
    await flushUntil(() => mocks.progressHandlers.length > 0);

    mocks.state.mockResolvedValueOnce({
      phase: "installed", version: "0.3.0", taskId: "t9", transferred: 0, total: 0, error: "", installedVersion: "0.3.0",
    });
    act(() => {
      emitProgress({ taskId: "t9", phase: "install", transferred: 0, total: 0, done: true });
    });
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("重启应用后生效"));
    expect(useUpdateStore.getState().installedVersion).toBe("0.3.0");
    expect(useUpdateStore.getState().install.active).toBe(false);
    expect(mocks.progressHandlers.length).toBe(0);
  });
});
