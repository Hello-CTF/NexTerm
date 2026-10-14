/** @vitest-environment jsdom */
// DekUnlockBanner desktop branch: when the vault is locked, desktop (darwin/windows/linux) shows the master-password unlock guidance,
// unlock success shows a toast and the banner disappears, wrong password is inline retryable, no re-show after dismiss;
// the biometric branch is mobile-only, desktop neither queries nor displays it.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import {
  clickButton,
  flush,
  flushUntil,
  mountWithClient,
  setInputValue,
  type MountedView,
} from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    platform: vi.fn(),
    vaultStatus: vi.fn(),
    unlock: vi.fn(),
    bioStatus: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    systemApi: { ...actual.systemApi, platform: mocks.platform },
    vaultApi: { ...actual.vaultApi, status: mocks.vaultStatus, unlock: mocks.unlock },
    biometricApi: { ...actual.biometricApi, status: mocks.bioStatus },
  };
});

import { DekUnlockBanner } from "../app/DekUnlockBanner";
import { useUi } from "../app/store";

let mounted: MountedView | undefined;
let vaultLocked = true;

function mountBanner(): MountedView {
  return mountWithClient(createElement(DekUnlockBanner));
}

function banner(): HTMLElement | null {
  return mounted?.container.querySelector<HTMLElement>("[role='region'][aria-label='凭据库已锁定']") ?? null;
}

function passwordInput(): HTMLInputElement | null {
  return mounted?.container.querySelector<HTMLInputElement>("input[type='password']") ?? null;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  window.localStorage.clear();
  vaultLocked = true;
  useUi.setState({ pushToast: mocks.toast });
  mocks.platform.mockResolvedValue("darwin");
  mocks.vaultStatus.mockImplementation(() =>
    Promise.resolve({
      initialized: true,
      mode: "master",
      unlocked: !vaultLocked,
      autoLockMinutes: 0,
      passwordless: false,
      systemProtection: false,
    }),
  );
  mocks.unlock.mockImplementation(() => {
    vaultLocked = false;
    return Promise.resolve();
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("desktop visibility", () => {
  it("shows unlock guidance for a locked master-password vault on darwin without the biometric branch", async () => {
    mounted = mountBanner();
    await flushUntil(() => banner() !== null);
    expect(banner()!.textContent).toContain("凭据库已锁定，同步已暂停；输入保护密码即可恢复同步并使用凭据");
    expect(passwordInput()).not.toBeNull();
    expect(banner()!.textContent).not.toContain("生物识别");
    expect(mocks.bioStatus).not.toHaveBeenCalled();
  });

  it("also shows the banner on Windows and Linux", async () => {
    for (const os of ["windows", "linux"]) {
      mocks.platform.mockResolvedValue(os);
      mounted = mountBanner();
      await flushUntil(() => banner() !== null);
      mounted.unmount();
      mounted = undefined;
      await flush();
    }
  });

  it("hides the banner when the vault is unlocked", async () => {
    vaultLocked = false;
    mounted = mountBanner();
    await flush();
    await flush();
    expect(banner()).toBeNull();
  });
});

describe("desktop unlock", () => {
  it("calls vault_unlock with the master password, shows a toast, and dismisses the banner", async () => {
    mounted = mountBanner();
    await flushUntil(() => banner() !== null);
    setInputValue(passwordInput()!, "master-pw-1");
    clickButton(mounted.container, "解锁");
    await flushUntil(() => banner() === null);
    expect(mocks.unlock).toHaveBeenCalledTimes(1);
    expect(mocks.unlock.mock.calls[0]?.[0]).toBe("master-pw-1");
    expect(mocks.toast).toHaveBeenCalledWith("success", "已解锁，正在恢复同步");
  });

  it("shows an inline error and clears the password on failure, then unlocks with the correct password", async () => {
    mocks.unlock.mockImplementationOnce(() => Promise.reject(new Error("主密码错误，无法解锁凭据库")));
    mounted = mountBanner();
    await flushUntil(() => banner() !== null);
    setInputValue(passwordInput()!, "bad-pw");
    clickButton(mounted.container, "解锁");
    await flushUntil(() => banner()!.textContent!.includes("主密码错误，无法解锁凭据库"));
    expect(mounted.container.querySelector("[role='alert']")).not.toBeNull();
    expect(passwordInput()!.value).toBe("");
    expect(banner()).not.toBeNull();
    expect(mocks.toast).not.toHaveBeenCalled();

    setInputValue(passwordInput()!, "master-pw-2");
    clickButton(mounted.container, "解锁");
    await flushUntil(() => banner() === null);
    expect(mocks.unlock.mock.calls[1]?.[0]).toBe("master-pw-2");
    expect(mocks.toast).toHaveBeenCalledWith("success", "已解锁，正在恢复同步");
  });
});

describe("later", () => {
  it("stays hidden after dismiss", async () => {
    mounted = mountBanner();
    await flushUntil(() => banner() !== null);
    clickButton(mounted.container, "稍后");
    expect(banner()).toBeNull();
    await flush();
    expect(banner()).toBeNull();
  });
});
