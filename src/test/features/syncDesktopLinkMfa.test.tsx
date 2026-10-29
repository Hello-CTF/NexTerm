/** @vitest-environment jsdom */
// Desktop account-sync login stepped onboarding: linkSet returning mfa_required enters the TOTP second step
// (OTP or recovery code), reset_required enters the forced password change (one-time recovery key display after completion,
// entry only after copy confirmation); errors in both branches surface inline and can be retried, "Back" returns to the login form.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    linkGet: vi.fn(),
    status: vi.fn(),
    kindOptInGet: vi.fn(),
    compare: vi.fn(),
    linkSet: vi.fn(),
    mfa: vi.fn(),
    resetPassword: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    syncApi: {
      ...actual.syncApi,
      linkGet: mocks.linkGet,
      status: mocks.status,
      kindOptInGet: mocks.kindOptInGet,
    },
  };
});
vi.mock("../../ipc/remoteApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/remoteApi")>();
  return {
    ...actual,
    remoteLinkApi: {
      linkSet: mocks.linkSet,
      mfa: mocks.mfa,
      resetPassword: mocks.resetPassword,
    },
    syncCompareApi: { ...actual.syncCompareApi, compare: mocks.compare },
  };
});
vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return { ...actual, listenEvent: vi.fn(async () => () => undefined) };
});
vi.mock("../../features/settings/AuthCard", () => ({ AuthCard: () => null }));
vi.mock("../../features/settings/AccountCard", () => ({ AccountCard: () => null }));

import { SyncCard } from "../../features/settings/SyncCard";
import { useUi } from "../../app/store";

const LINK = { url: "https://nexterm.example.com", username: "alice", insecure: false, hasPassword: true, verifiedAt: 1700000000000, lastError: "" };
const RECOVERY_KEY = "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567";

let loggedIn = false;
let mounted: MountedView | undefined;

function statusPayload() {
  return loggedIn
    ? { configured: true, loggedIn: true, username: "alice", userId: "u-1", head: "h-1", seq: 0, verifiedAt: 1700000000000, lastError: "" }
    : { configured: true, loggedIn: false, seq: 0, verifiedAt: 0, lastError: "" };
}

function withClient(node: React.ReactElement): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function seedForm() {
  mocks.linkGet.mockResolvedValue({ ...LINK });
  mocks.status.mockImplementation(() => Promise.resolve(statusPayload()));
  mocks.kindOptInGet.mockResolvedValue({ knownHost: true, aiProfile: false, aiPermission: false, aiGrant: false, preferences: false, stateRules: false, runbookTemplates: false, skills: false, memory: false, cron: false });
  mocks.compare.mockResolvedValue({ rows: [], remote: 0, warnings: [], userId: "u-1", compareToken: "compare-1" });
}

function consoleReady(): boolean {
  return document.body.textContent?.includes("立即同步") === true &&
    document.body.textContent?.includes("已开启 1 / 10 项") === true &&
    document.body.textContent?.includes("服务端共 0 项数据") === true;
}

async function waitForConsole(): Promise<void> {
  await flushUntil(consoleReady);
}

async function mountAndLogin(ready: () => boolean) {
  mounted = withClient(createElement(SyncCard));
  await flushUntil(() => (document.querySelector("#sync-url") as HTMLInputElement | null)?.value === LINK.url);
  click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "登录") as HTMLButtonElement);
  await flushUntil(ready);
  expect(mocks.linkSet).toHaveBeenCalledTimes(1);
}

beforeEach(() => {
  vi.clearAllMocks();
  loggedIn = false;
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
});

describe("Desktop login - direct success", () => {
  it("returns connected from linkSet, skips the stepped flow, completes login, and shows the sync console", async () => {
    seedForm();
    mocks.linkSet.mockImplementation(() => {
      loggedIn = true;
      return Promise.resolve({ ...LINK, connect: "connected" });
    });
    await mountAndLogin(consoleReady);

    expect(document.body.textContent).not.toContain("两步验证");
    expect(document.body.textContent).not.toContain("设置新密码");
    expect(mocks.mfa).not.toHaveBeenCalled();
    expect(mocks.resetPassword).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("success", "登录信息已保存，自动同步已开启");
  });
});

describe("Desktop login - mfa_required branch", () => {
  it("enters two-step verification, completes through remoteLinkApi.mfa, and reaches the logged-in state", async () => {
    seedForm();
    mocks.linkSet.mockResolvedValue({ ...LINK, connect: "mfa_required", mfaTicket: "ticket-1", mfaExpiresAt: 1700000300000 });
    mocks.mfa.mockImplementation(() => {
      loggedIn = true;
      return Promise.resolve({ ...LINK, connect: "connected" });
    });
    await mountAndLogin(() => document.body.textContent?.includes("两步验证") ?? false);

    // The main form is hidden, only the OTP input remains
    expect(document.querySelector("#sync-url")).toBeNull();
    expect(document.body.textContent).toContain("恢复码");

    setInputValue(document.querySelector("#sync-mfa-code") as HTMLInputElement, "123456");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("验证并登录")) as HTMLButtonElement);
    await waitForConsole();

    expect(mocks.mfa).toHaveBeenCalledWith("123456");
    expect(mocks.toast).toHaveBeenCalledWith("success", "登录信息已保存，自动同步已开启");
  });

  it("shows an invalid-code error inline with retry and returns to the login form with Back", async () => {
    seedForm();
    mocks.linkSet.mockResolvedValue({ ...LINK, connect: "mfa_required", mfaTicket: "ticket-1" });
    mocks.mfa.mockRejectedValueOnce(new Error("incorrect verification code")).mockImplementation(() => {
      loggedIn = true;
      return Promise.resolve({ ...LINK, connect: "connected" });
    });
    await mountAndLogin(() => document.body.textContent?.includes("两步验证") ?? false);

    setInputValue(document.querySelector("#sync-mfa-code") as HTMLInputElement, "000000");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("验证并登录")) as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("incorrect verification code") ?? false);
    expect(document.body.textContent).not.toContain("立即同步");

    // After "Back" returns to the main form, logging in again works
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "返回") as HTMLButtonElement);
    await flushUntil(() => document.querySelector("#sync-url") !== null);
    expect(document.body.textContent).not.toContain("incorrect verification code");

    mocks.linkSet.mockResolvedValue({ ...LINK, connect: "mfa_required", mfaTicket: "ticket-2" });
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "登录") as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("两步验证") ?? false);
    setInputValue(document.querySelector("#sync-mfa-code") as HTMLInputElement, "123456");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("验证并登录")) as HTMLButtonElement);
    await waitForConsole();
  });
});

describe("Desktop login - reset_required branch", () => {
  async function enterReset() {
    seedForm();
    mocks.linkSet.mockResolvedValue({ ...LINK, connect: "reset_required" });
    await mountAndLogin(() => document.body.textContent?.includes("请先设置新密码") ?? false);
  }

  it("requires at least eight characters and matching new passwords without making a request", async () => {
    await enterReset();

    setInputValue(document.querySelector("#sync-new-pass") as HTMLInputElement, "short");
    setInputValue(document.querySelector("#sync-new-pass2") as HTMLInputElement, "short");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("设置新密码")) as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("新密码至少 8 位") ?? false);
    expect(mocks.resetPassword).not.toHaveBeenCalled();

    setInputValue(document.querySelector("#sync-new-pass") as HTMLInputElement, "new-password-1");
    setInputValue(document.querySelector("#sync-new-pass2") as HTMLInputElement, "new-password-2");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("设置新密码")) as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("两次输入的密码不一致") ?? false);
    expect(mocks.resetPassword).not.toHaveBeenCalled();
  });

  it("shows a one-time recovery key after a successful password change, requires copy confirmation, and masks it by default", async () => {
    await enterReset();
    mocks.resetPassword.mockImplementation(() => {
      loggedIn = true;
      return Promise.resolve({ ...LINK, connect: "connected", recoveryKey: RECOVERY_KEY });
    });
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { clipboard: { writeText } });

    setInputValue(document.querySelector("#sync-new-pass") as HTMLInputElement, "new-password-1");
    setInputValue(document.querySelector("#sync-new-pass2") as HTMLInputElement, "new-password-1");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("设置新密码")) as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("恢复密钥（仅显示一次）") ?? false);

    expect(mocks.resetPassword).toHaveBeenCalledWith("new-password-1");
    // Masked by default: the key itself does not appear on the page
    expect(document.body.textContent).not.toContain(RECOVERY_KEY);
    const finish = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("已保存，继续")) as HTMLButtonElement;
    expect(finish.disabled).toBe(true);

    // The show toggle reveals the key; entry is allowed only after copying
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "显示") as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes(RECOVERY_KEY) ?? false);
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "复制") as HTMLButtonElement);
    await flushUntil(() => writeText.mock.calls.length === 1 && !finish.disabled);
    expect(writeText).toHaveBeenCalledWith(RECOVERY_KEY);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("已保存，继续")) as HTMLButtonElement);
    await waitForConsole();
    expect(document.body.textContent).not.toContain(RECOVERY_KEY);
    expect(mocks.toast).toHaveBeenCalledWith("success", "登录信息已保存，自动同步已开启");
  });

  it("reveals the key and shows a manual-copy hint when the clipboard is unavailable, while still allowing entry", async () => {
    await enterReset();
    mocks.resetPassword.mockImplementation(() => {
      loggedIn = true;
      return Promise.resolve({ ...LINK, connect: "connected", recoveryKey: RECOVERY_KEY });
    });
    vi.stubGlobal("navigator", { clipboard: undefined });

    setInputValue(document.querySelector("#sync-new-pass") as HTMLInputElement, "new-password-1");
    setInputValue(document.querySelector("#sync-new-pass2") as HTMLInputElement, "new-password-1");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("设置新密码")) as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("恢复密钥（仅显示一次）") ?? false);

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "复制") as HTMLButtonElement);
    await flushUntil(() =>
      document.body.textContent?.includes("复制失败，请手动复制恢复密钥") === true &&
      !([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("已保存，继续")) as HTMLButtonElement).disabled,
    );
    expect(document.body.textContent).toContain(RECOVERY_KEY);
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("已保存，继续")) as HTMLButtonElement);
    await waitForConsole();
  });

  it("shows password change failures inline and stays on the step for retry", async () => {
    await enterReset();
    mocks.resetPassword.mockRejectedValueOnce(new Error("login session expired; reconnect")).mockImplementation(() => {
      loggedIn = true;
      return Promise.resolve({ ...LINK, connect: "connected", recoveryKey: RECOVERY_KEY });
    });

    setInputValue(document.querySelector("#sync-new-pass") as HTMLInputElement, "new-password-1");
    setInputValue(document.querySelector("#sync-new-pass2") as HTMLInputElement, "new-password-1");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("设置新密码")) as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("login session expired; reconnect") ?? false);
    expect(document.body.textContent).toContain("请先设置新密码");

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("设置新密码")) as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("恢复密钥（仅显示一次）") ?? false);
  });
});
