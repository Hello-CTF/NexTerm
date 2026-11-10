/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { clickButton, flush, flushUntil, mountWithClient, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    platform: vi.fn(),
    vaultStatus: vi.fn(),
    vaultUnlock: vi.fn(),
    bioStatus: vi.fn(),
    bioUnlock: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    systemApi: { ...actual.systemApi, platform: mocks.platform, removeNotification: () => Promise.resolve() },
    vaultApi: { ...actual.vaultApi, status: mocks.vaultStatus, unlock: mocks.vaultUnlock },
    biometricApi: { ...actual.biometricApi, status: mocks.bioStatus, unlock: mocks.bioUnlock },
  };
});

import {
  isAppLocked,
  lockApp,
  requestAppLock,
  resetAppLockForTest,
  setAppLockIdleMinutes,
  startAppLockIdleTimer,
  unlockApp,
} from "../app/appLock";
import { AppLockLayer } from "../app/AppLockScreen";
import { lockAware, type SystemNotifier } from "../features/inbox/systemNotifications";
import { useUi } from "../app/store";

let mounted: MountedView | undefined;
let passwordless = false;

function lockScreen(): HTMLElement | null {
  return document.querySelector<HTMLElement>("#nx-app-lock-root [role='dialog']");
}

function lockPassword(): HTMLInputElement | null {
  return document.querySelector<HTMLInputElement>("#nx-app-lock-root input[type='password']");
}

function lockAlert(): string {
  return document.querySelector("#nx-app-lock-root [role='alert']")?.textContent ?? "";
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  window.localStorage.clear();
  resetAppLockForTest();
  passwordless = false;
  useUi.setState({ pushToast: mocks.toast });
  mocks.platform.mockResolvedValue("linux");
  mocks.vaultStatus.mockImplementation(() =>
    Promise.resolve({
      initialized: true,
      mode: "master",
      unlocked: true,
      autoLockMinutes: 0,
      passwordless,
      systemProtection: false,
      managed: false,
    }),
  );
  mocks.vaultUnlock.mockImplementation((password: string) =>
    password === "correct horse" ? Promise.resolve() : Promise.reject(new Error("保护密码错误")),
  );
  mocks.bioStatus.mockResolvedValue({ available: true, canEvaluate: true, hasMaterial: true });
  mocks.bioUnlock.mockResolvedValue({ ok: true, password: "correct horse" });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.useRealTimers();
  resetAppLockForTest();
});

// mountApp renders a stand-in for the app shell: a terminal's hidden textarea and a remote desktop canvas that
// record the input they receive, next to the lock layer.
function mountApp() {
  const received: string[] = [];
  const app = document.createElement("div");
  app.id = "root";
  const terminal = document.createElement("textarea");
  terminal.className = "xterm-helper-textarea";
  terminal.addEventListener("keydown", (e) => received.push(`terminal:${e.key}`));
  terminal.addEventListener("paste", () => received.push("terminal:paste"));
  const canvas = document.createElement("canvas");
  canvas.addEventListener("pointerdown", () => received.push("canvas:pointerdown"));
  canvas.addEventListener("keydown", (e) => received.push(`canvas:${e.key}`));
  canvas.tabIndex = 0;
  app.append(terminal, canvas);
  document.body.append(app);
  mounted = mountWithClient(createElement(AppLockLayer));
  return { app, terminal, canvas, received };
}

function press(target: Element, key: string) {
  target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
}

describe("app lock screen", () => {
  it("hides the app and withholds keyboard, pointer and paste input from terminals and canvases", async () => {
    const { app, terminal, canvas, received } = mountApp();
    terminal.focus();
    press(terminal, "a");
    expect(received).toEqual(["terminal:a"]);

    await act(async () => {
      await requestAppLock();
    });
    await flushUntil(() => lockPassword() !== null);

    expect(document.activeElement).not.toBe(terminal);
    expect(app.hasAttribute("data-nx-app-locked")).toBe(true);
    expect(app.hasAttribute("inert")).toBe(true);
    expect(app.getAttribute("aria-hidden")).toBe("true");
    press(terminal, "b");
    press(canvas, "c");
    canvas.dispatchEvent(new Event("pointerdown", { bubbles: true, cancelable: true }));
    terminal.dispatchEvent(new Event("paste", { bubbles: true, cancelable: true }));
    expect(received).toEqual(["terminal:a"]);

    // A dialog portaled to the body while locked is hidden as well.
    const lateDialog = document.createElement("div");
    document.body.append(lateDialog);
    await flush();
    expect(lateDialog.hasAttribute("data-nx-app-locked")).toBe(true);

    // The lock screen itself still takes input.
    const input = lockPassword()!;
    const typed: string[] = [];
    input.addEventListener("keydown", (e) => typed.push(e.key));
    press(input, "x");
    expect(typed).toEqual(["x"]);

    setInputValue(input, "correct horse");
    clickButton(document.body, "解锁");
    await flushUntil(() => lockScreen() === null);

    expect(mocks.vaultUnlock).toHaveBeenCalledWith("correct horse");
    expect(isAppLocked()).toBe(false);
    expect(app.hasAttribute("data-nx-app-locked")).toBe(false);
    expect(app.hasAttribute("inert")).toBe(false);
    expect(app.hasAttribute("aria-hidden")).toBe(false);
    press(terminal, "d");
    expect(received).toEqual(["terminal:a", "terminal:d"]);
  });

  it("stays locked and clears the field after a wrong password", async () => {
    mountApp();
    act(() => lockApp());
    await flushUntil(() => lockPassword() !== null);

    setInputValue(lockPassword()!, "wrong");
    clickButton(document.body, "解锁");
    await flushUntil(() => lockAlert().includes("保护密码错误"));

    expect(isAppLocked()).toBe(true);
    expect(lockScreen()).not.toBeNull();
    expect(lockPassword()!.value).toBe("");
    expect(window.localStorage.getItem("nexterm.appLock.v1")).toBe("1");
  });

  it("unlocks with mobile biometrics through the vault password check", async () => {
    mocks.platform.mockResolvedValue("ios");
    mountApp();
    act(() => lockApp());
    await flushUntil(() => [...document.querySelectorAll("#nx-app-lock-root button")].some((b) => b.textContent === "用面容 ID解锁"));

    clickButton(document.body, "用面容 ID解锁");
    await flushUntil(() => lockScreen() === null);

    expect(mocks.bioUnlock).toHaveBeenCalledTimes(1);
    expect(mocks.vaultUnlock).toHaveBeenCalledWith("correct horse");
    expect(isAppLocked()).toBe(false);
  });

  it("refuses to lock when no vault password can verify the unlock", async () => {
    passwordless = true;
    let result = true;
    await act(async () => {
      result = await requestAppLock();
    });

    expect(result).toBe(false);
    expect(isAppLocked()).toBe(false);
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("凭据保护"));
  });

  it("keeps the lock across a reload", async () => {
    act(() => lockApp());
    vi.resetModules();
    const reloaded = await import("../app/appLockState");
    expect(reloaded.isAppLocked()).toBe(true);
    // Leave the reloaded copy unlocked so its window guard stays inert for later tests.
    reloaded.unlockApp();
    expect(window.localStorage.getItem("nexterm.appLock.v1")).toBeNull();
  });

  it("follows a lock or unlock made in another tab", () => {
    window.localStorage.setItem("nexterm.appLock.v1", "1");
    act(() => {
      window.dispatchEvent(new StorageEvent("storage", { key: "nexterm.appLock.v1" }));
    });
    expect(isAppLocked()).toBe(true);
    window.localStorage.removeItem("nexterm.appLock.v1");
    act(() => {
      window.dispatchEvent(new StorageEvent("storage", { key: "nexterm.appLock.v1" }));
    });
    expect(isAppLocked()).toBe(false);
  });
});

describe("idle auto-lock", () => {
  it("locks after the configured idle minutes and restarts the wait on input", async () => {
    vi.useFakeTimers();
    setAppLockIdleMinutes(5);
    const stop = startAppLockIdleTimer();
    try {
      await vi.advanceTimersByTimeAsync(4 * 60_000);
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "a" }));
      await vi.advanceTimersByTimeAsync(4 * 60_000);
      expect(isAppLocked()).toBe(false);

      await vi.advanceTimersByTimeAsync(60_000 + 10);
      expect(isAppLocked()).toBe(true);
    } finally {
      stop();
    }
  });

  it("does nothing while the setting is off", async () => {
    vi.useFakeTimers();
    const stop = startAppLockIdleTimer();
    try {
      await vi.advanceTimersByTimeAsync(48 * 60 * 60_000);
      expect(isAppLocked()).toBe(false);
      expect(mocks.vaultStatus).not.toHaveBeenCalled();
    } finally {
      stop();
    }
  });
});

describe("notifications while locked", () => {
  function fakeNotifier() {
    const shown: { id: string; title: string; body?: string; tabId?: string }[] = [];
    const notifier: SystemNotifier = {
      status: () => Promise.resolve({ available: true, permission: "granted" }),
      request: () => Promise.resolve({ available: true, permission: "granted" }),
      show: (notification) => {
        shown.push(notification);
        return Promise.resolve();
      },
      remove: () => {},
      onClick: () => {},
    };
    return { notifier: lockAware(notifier), shown };
  }

  it("shows only a locked notice with a count instead of the content", async () => {
    const { notifier, shown } = fakeNotifier();
    act(() => lockApp());
    await notifier.show({ id: "tab-1", title: "prod-db 需要处理", body: "Password: hunter2", tabId: "tab-1" });
    await notifier.show({ id: "ai", title: "AI 通知", body: "deploy key leaked" });

    expect(shown).toHaveLength(2);
    for (const notification of shown) {
      expect(notification.title).toBe("NexTerm 已锁定");
      expect(JSON.stringify(notification)).not.toMatch(/prod-db|hunter2|deploy key|tab-1/);
    }
    expect(shown[0].id).toBe(shown[1].id);
    expect(shown[1].body).toContain("2");

    act(() => unlockApp());
    await notifier.show({ id: "tab-1", title: "prod-db 需要处理", body: "来自 prod", tabId: "tab-1" });
    expect(shown[2]).toEqual({ id: "tab-1", title: "prod-db 需要处理", body: "来自 prod", tabId: "tab-1" });
  });
});
