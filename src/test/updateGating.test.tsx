/** @vitest-environment jsdom */
// WEB 门控: 服务端把 canInstall 报成 true, 前端仍不得显示安装按钮(应用内安装仅桌面端),
// 也不得显示桌面端更新横幅。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { clickButton, flushUntil, mount, type MountedView } from "./features/reactTestUtils";
import type { UpdateStatusDto } from "../ipc/types";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    check: vi.fn(),
    install: vi.fn(),
    restart: vi.fn(),
  };
});

vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    appUpdateApi: { check: mocks.check, install: mocks.install, restart: mocks.restart },
  };
});

import { UpdateCard } from "../features/settings/UpdateCard";
import { UpdateBanner } from "../app/UpdateBanner";
import { useUpdateStore } from "../app/useUpdate";

const DEMO_STATUS: UpdateStatusDto = {
  available: true,
  currentVersion: "0.1.0-demo",
  version: "0.2.0-demo",
  notes: "演示更新",
  prerelease: false,
  assetName: "NexTerm-desktop_0.2.0-demo_linux_amd64.deb",
  assetSize: 8 << 20,
  canInstall: true,
  unavailableReason: "",
};

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
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
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("软件更新 · WEB 门控", () => {
  it("有更新但不显示安装按钮,并说明服务端模式原因", async () => {
    mocks.check.mockResolvedValue(DEMO_STATUS);
    mounted = mount(createElement(UpdateCard));
    clickButton(mounted.container, "立即检查");
    await flushUntil(() => (mounted?.container.textContent ?? "").includes("发现新版本 v0.2.0-demo"));

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("服务端模式不支持应用内安装");
    expect(
      [...mounted.container.querySelectorAll("button")].some(
        (b) => b.textContent?.trim() === "下载并安装",
      ),
    ).toBe(false);
    expect(mocks.install).not.toHaveBeenCalled();
  });

  it("WEB 不显示更新横幅", async () => {
    useUpdateStore.setState({ status: DEMO_STATUS });
    mounted = mount(createElement(UpdateBanner, { onOpenSettings: vi.fn() }));
    expect(mounted.container.querySelector('[aria-label="软件更新"]')).toBeNull();
  });
});
