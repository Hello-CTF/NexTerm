/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { clickButton, flushUntil, mountWithClient, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return { verifyPassword: vi.fn(), toast: vi.fn(), vaultUnlock: vi.fn() };
});

vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return { ...actual, vaultApi: { ...actual.vaultApi, unlock: mocks.vaultUnlock } };
});

import { isAppLocked, lockApp, requestAppLock, resetAppLockForTest } from "../app/appLock";
import { AppLockLayer } from "../app/AppLockScreen";
import { useAuth } from "../features/auth/store";
import { useUi } from "../app/store";

let mounted: MountedView | undefined;

function lockPassword(): HTMLInputElement | null {
  return document.querySelector<HTMLInputElement>("#nx-app-lock-root input[type='password']");
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  window.localStorage.clear();
  resetAppLockForTest();
  useUi.setState({ pushToast: mocks.toast });
  mocks.verifyPassword.mockImplementation((password: string) =>
    password === "account-pass" ? Promise.resolve() : Promise.reject(new Error("解密失败")),
  );
  useAuth.setState({
    gate: "ready",
    user: { id: "u1", username: "alice" } as never,
    verifyPassword: mocks.verifyPassword,
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  resetAppLockForTest();
});

describe("app lock in browser server mode", () => {
  it("unlocks with the account password verified against the data key envelope", async () => {
    mounted = mountWithClient(createElement(AppLockLayer));
    await act(async () => {
      await requestAppLock();
    });
    await flushUntil(() => lockPassword() !== null);
    expect(document.body.textContent).toContain("alice");

    setInputValue(lockPassword()!, "nope");
    clickButton(document.body, "解锁");
    await flushUntil(() => mocks.verifyPassword.mock.calls.length === 1 && lockPassword()?.value === "");
    expect(isAppLocked()).toBe(true);

    setInputValue(lockPassword()!, "account-pass");
    clickButton(document.body, "解锁");
    await flushUntil(() => !isAppLocked());

    expect(mocks.verifyPassword).toHaveBeenLastCalledWith("account-pass");
    expect(mocks.vaultUnlock).not.toHaveBeenCalled();
  });

  it("hands over to the account gate when the session ends while locked", async () => {
    mounted = mountWithClient(createElement(AppLockLayer));
    act(() => lockApp());
    await flushUntil(() => lockPassword() !== null);

    act(() => useAuth.setState({ gate: "login", user: null }));
    await flushUntil(() => !isAppLocked());
    expect(document.querySelector("#nx-app-lock-root [role='dialog']")).toBeNull();
  });

  it("cannot lock without a signed-in account", async () => {
    useAuth.setState({ gate: "ready", user: null });
    let result = true;
    await act(async () => {
      result = await requestAppLock();
    });
    expect(result).toBe(false);
    expect(isAppLocked()).toBe(false);
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("账号"));
  });
});
