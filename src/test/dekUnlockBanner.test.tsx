/** @vitest-environment jsdom */
// DekUnlockBanner: the main-screen unlock entry when the session is restored via cookie after a browser refresh but the DEK is empty.
// Covers the three visibility states, unlock-success toast + dismissal, inline retry on wrong password, and no re-show after dismiss (until refresh).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { clickButton, flush, flushUntil, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    dekGet: vi.fn(),
    unwrap: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../ipc/authApi", async (importOriginal) => {
  const original = await importOriginal<typeof import("../ipc/authApi")>();
  return {
    ...original,
    authApi: {
      status: vi.fn().mockResolvedValue({ initialized: true, registration_open: false, auth: "on" }),
      me: vi.fn(),
      logout: vi.fn().mockResolvedValue({ ok: true }),
      dekGet: mocks.dekGet,
    },
  };
});

// Avoid real argon2id: stub unwrap, keep the rest (base64 etc.) real.
vi.mock("../features/auth/crypto", async (importOriginal) => {
  const original = await importOriginal<typeof import("../features/auth/crypto")>();
  return { ...original, unwrapDEKWithPassword: mocks.unwrap };
});

vi.mock("../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
  syncApi: {},
}));

import { DekUnlockBanner } from "../app/DekUnlockBanner";
import { useAuth } from "../features/auth/store";
import { useUi } from "../app/store";

const DEK = new Uint8Array(32).fill(9);
const DEMO_USER = {
  id: "u-1",
  username: "alice",
  display_name: "Alice",
  role: "user" as const,
  state: "active" as const,
  must_change_password: false,
  mfa_enabled: false,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1,
};

let mounted: MountedView | undefined;

function seedAuth(patch: Partial<{ user: typeof DEMO_USER | null; dek: Uint8Array | null; gate: "ready" | "login" }>) {
  useAuth.setState({
    status: { initialized: true, registration_open: false, auth: "on" },
    user: patch.user !== undefined ? patch.user : DEMO_USER,
    dek: patch.dek !== undefined ? patch.dek : null,
    gate: patch.gate ?? "ready",
    pendingRecoveryKey: null,
    error: null,
  });
}

function mountBanner(): MountedView {
  return mount(createElement(DekUnlockBanner));
}

function banner(): HTMLElement | null {
  return mounted?.container.querySelector<HTMLElement>("[role='region'][aria-label='数据密钥未解锁']") ?? null;
}

function passwordInput(): HTMLInputElement | null {
  return mounted?.container.querySelector<HTMLInputElement>("input[type='password']") ?? null;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  window.localStorage.clear();
  useUi.setState({ pushToast: mocks.toast });
  mocks.dekGet.mockResolvedValue({
    dek_envelope: "",
    kdf_salt: "",
    kdf_params: '{"t":3,"m":65536,"p":4}',
    recovery_envelope: "",
    recovery_hash: "",
  });
  mocks.unwrap.mockResolvedValue(DEK);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useAuth.setState({ user: null, dek: null, gate: "ready", pendingRecoveryKey: null, error: null, status: null });
});

describe("visibility", () => {
  it("hidden without a signed-in user", () => {
    seedAuth({ user: null });
    mounted = mountBanner();
    expect(banner()).toBeNull();
  });

  it("hidden when the DEK is already in memory (unlocked)", () => {
    seedAuth({ dek: DEK });
    mounted = mountBanner();
    expect(banner()).toBeNull();
  });

  it("hidden while the gate is not ready", () => {
    seedAuth({ gate: "login" });
    mounted = mountBanner();
    expect(banner()).toBeNull();
  });

  it("shows the banner when the session is restored but the DEK is locked", () => {
    seedAuth({});
    mounted = mountBanner();
    expect(banner()).not.toBeNull();
    expect(banner()!.textContent).toContain("数据密钥未解锁，同步已暂停；输入账号密码即可恢复同步与加密内容");
    expect(passwordInput()).not.toBeNull();
  });
});

describe("unlock", () => {
  it("calls unlockDEK with the correct password, shows a toast, and dismisses the banner", async () => {
    seedAuth({});
    mounted = mountBanner();
    setInputValue(passwordInput()!, "pw-1");
    clickButton(mounted.container, "解锁");
    await flushUntil(() => useAuth.getState().dek !== null);
    expect(mocks.unwrap.mock.calls[0]?.[0]).toBe("pw-1");
    expect(useAuth.getState().dek).toEqual(DEK);
    expect(mocks.toast).toHaveBeenCalledTimes(1);
    expect(mocks.toast).toHaveBeenCalledWith("success", "已解锁，正在恢复同步");
    expect(banner()).toBeNull();
  });

  it("shows an inline error and clears the password on failure, then unlocks with the correct password on Enter", async () => {
    mocks.unwrap.mockRejectedValueOnce(new Error("密码错误，无法解开数据密钥"));
    seedAuth({});
    mounted = mountBanner();
    setInputValue(passwordInput()!, "bad-pw");
    clickButton(mounted.container, "解锁");
    await flushUntil(() => banner()!.textContent!.includes("密码错误，无法解开数据密钥"));
    expect(mounted.container.querySelector("[role='alert']")).not.toBeNull();
    expect(passwordInput()!.value).toBe("");
    expect(banner()).not.toBeNull();
    expect(useAuth.getState().dek).toBeNull();
    expect(mocks.toast).not.toHaveBeenCalled();

    setInputValue(passwordInput()!, "pw-2");
    act(() => {
      passwordInput()!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    });
    await flushUntil(() => useAuth.getState().dek !== null);
    expect(mocks.unwrap.mock.calls[1]?.[0]).toBe("pw-2");
    expect(mocks.toast).toHaveBeenCalledWith("success", "已解锁，正在恢复同步");
    expect(banner()).toBeNull();
  });
});

describe("later", () => {
  it("stays hidden after dismiss and reappears after remounting (refresh)", async () => {
    seedAuth({});
    mounted = mountBanner();
    expect(banner()).not.toBeNull();
    clickButton(mounted.container, "稍后");
    expect(banner()).toBeNull();
    await flush();
    expect(banner()).toBeNull();

    mounted.unmount();
    mounted = mountBanner();
    expect(banner()).not.toBeNull();
  });
});
