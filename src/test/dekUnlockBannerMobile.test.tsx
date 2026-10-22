/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { clickButton, flushUntil, mountWithClient, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    platform: vi.fn(),
    vaultStatus: vi.fn(),
    vaultUnlock: vi.fn(),
    bioStatus: vi.fn(),
    bioUnlock: vi.fn(),
    bioEnable: vi.fn(),
    bioDisable: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    systemApi: { ...actual.systemApi, platform: mocks.platform },
    vaultApi: { ...actual.vaultApi, status: mocks.vaultStatus, unlock: mocks.vaultUnlock },
    biometricApi: {
      status: mocks.bioStatus,
      unlock: mocks.bioUnlock,
      enable: mocks.bioEnable,
      disable: mocks.bioDisable,
    },
  };
});

import { DekUnlockBanner } from "../app/DekUnlockBanner";
import { useUi } from "../app/store";

let mounted: MountedView | undefined;
let vaultLocked = true;
let hasMaterial = true;

function lockedBanner(): HTMLElement | null {
  return mounted?.container.querySelector<HTMLElement>("[role='region'][aria-label='凭据库已锁定']") ?? null;
}

function passwordInput(): HTMLInputElement | null {
  return mounted?.container.querySelector<HTMLInputElement>("input[type='password']") ?? null;
}

function hasButton(text: string): boolean {
  return [...(mounted?.container.querySelectorAll("button") ?? [])].some((button) => button.textContent?.trim() === text);
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  window.localStorage.clear();
  vaultLocked = true;
  hasMaterial = true;
  useUi.setState({ pushToast: mocks.toast });
  mocks.platform.mockResolvedValue("ios");
  mocks.vaultStatus.mockImplementation(() => Promise.resolve({
    initialized: true,
    mode: "master",
    unlocked: !vaultLocked,
    autoLockMinutes: 0,
    passwordless: false,
    systemProtection: false,
  }));
  mocks.vaultUnlock.mockImplementation(() => {
    vaultLocked = false;
    return Promise.resolve();
  });
  mocks.bioStatus.mockImplementation(() => Promise.resolve({ available: true, canEvaluate: true, hasMaterial }));
  mocks.bioUnlock.mockResolvedValue({ ok: true, password: "stored-master-password" });
  mocks.bioEnable.mockResolvedValue(undefined);
  mocks.bioDisable.mockImplementation(() => {
    hasMaterial = false;
    return Promise.resolve();
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("mobile biometric unlock", () => {
  it("uses the password only after successful iOS biometric verification", async () => {
    mounted = mountWithClient(createElement(DekUnlockBanner));
    await flushUntil(() => hasButton("用面容 ID解锁"));

    clickButton(mounted.container, "用面容 ID解锁");
    await flushUntil(() => mocks.vaultUnlock.mock.calls.length === 1);
    await flushUntil(() => mocks.toast.mock.calls.some(([kind, text]) => kind === "success" && text === "已解锁，正在恢复同步"));
    await flushUntil(() => lockedBanner() === null);

    expect(mocks.bioUnlock).toHaveBeenCalledTimes(1);
    expect(mocks.vaultUnlock).toHaveBeenCalledWith("stored-master-password");
    expect(mocks.toast).toHaveBeenCalledWith("success", "已解锁，正在恢复同步");
  });

  it("keeps the Android password prompt after cancellation without calling vault unlock", async () => {
    mocks.platform.mockResolvedValue("android");
    mocks.bioUnlock.mockResolvedValue({ ok: false, message: "用户取消" });
    mounted = mountWithClient(createElement(DekUnlockBanner));
    await flushUntil(() => hasButton("用生物识别解锁"));

    clickButton(mounted.container, "用生物识别解锁");
    await flushUntil(() => mounted?.container.querySelector("[role='status']")?.textContent === "用户取消");

    expect(mocks.vaultUnlock).not.toHaveBeenCalled();
    expect(passwordInput()).not.toBeNull();
    expect(lockedBanner()).not.toBeNull();
    expect(mocks.toast).not.toHaveBeenCalled();
  });

  it("offers to store the just-used password after a password unlock", async () => {
    hasMaterial = false;
    mounted = mountWithClient(createElement(DekUnlockBanner));
    await flushUntil(() => lockedBanner() !== null && passwordInput() !== null);

    setInputValue(passwordInput()!, "new-master-password");
    clickButton(mounted.container, "解锁");
    await flushUntil(() => hasButton("启用面容 ID"));
    clickButton(mounted.container, "启用面容 ID");
    await flushUntil(() => mocks.toast.mock.calls.some(([kind, text]) => kind === "success" && text === "已启用面容 ID快捷解锁"));

    expect(mocks.vaultUnlock).toHaveBeenCalledWith("new-master-password");
    expect(mocks.bioEnable).toHaveBeenCalledWith("new-master-password");
    expect(mocks.toast).toHaveBeenCalledWith("success", "已启用面容 ID快捷解锁");
  });

  it("disables quick unlock and removes its enrolled material", async () => {
    mounted = mountWithClient(createElement(DekUnlockBanner));
    await flushUntil(() => hasButton("关闭面容 ID"));

    clickButton(mounted.container, "关闭面容 ID");
    clickButton(mounted.container, "确认关闭");
    await flushUntil(() => mocks.toast.mock.calls.some(([kind, text]) => kind === "success" && text === "已关闭面容 ID快捷解锁"));

    expect(mocks.bioDisable).toHaveBeenCalledTimes(1);
    expect(mocks.vaultUnlock).not.toHaveBeenCalled();
  });
});
