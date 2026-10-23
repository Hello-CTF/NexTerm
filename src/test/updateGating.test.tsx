/** @vitest-environment jsdom */
// WEB gating: even when the server reports canInstall as true, the frontend must not show the install button (in-app install is desktop-only),
// nor the desktop update banner.
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
import { promptUpdateIfNeeded } from "../app/updatePrompt";

const DEMO_STATUS: UpdateStatusDto = {
  available: true,
  currentVersion: "0.1.0-demo",
  version: "0.2.0-demo",
  notes: "Demo update",
  prerelease: false,
  assetName: "nexterm-desktop_linux_amd64_0.2.0-demo.deb",
  assetSize: 8 << 20,
  canInstall: true,
  unavailableReason: "",
};

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  window.localStorage.clear();
  useUpdateStore.setState({
    status: null,
    checking: false,
    checkError: null,
    install: { active: false, phase: null, transferred: 0, total: 0, error: null, taskId: null },
    installedVersion: null,
    restarting: false,
    restartError: null,
    skippedVersion: null,
    remindedVersion: null,
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("software updates - WEB gating", () => {
  it("does not show the install button for available updates and explains the server-mode restriction", async () => {
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

  it("does not show the update banner on WEB", async () => {
    useUpdateStore.setState({ status: DEMO_STATUS });
    mounted = mount(createElement(UpdateBanner, { onOpenSettings: vi.fn() }));
    expect(mounted.container.querySelector('[aria-label="软件更新"]')).toBeNull();
  });

  it("does not prompt for updates on WEB", async () => {
    useUpdateStore.setState({ status: DEMO_STATUS });
    await promptUpdateIfNeeded();
    expect(mocks.install).not.toHaveBeenCalled();
    expect(window.localStorage.getItem("nexterm.update.v1")).toBeNull();
    expect(useUpdateStore.getState().remindedVersion).toBeNull();
  });
});
