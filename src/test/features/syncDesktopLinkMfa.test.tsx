/** @vitest-environment jsdom */
// 桌面端账号同步登录的分步入职: linkSet 返回 mfa_required 时进入 TOTP 第二步
// (动态码或恢复码), reset_required 时进入强制改密 (完成后一次性展示恢复密钥,
// 复制确认后才能进入); 两分支的错误内联展示并可重试, 「返回」回到登录表单。
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
  };
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
  mocks.kindOptInGet.mockResolvedValue({ knownHost: true, aiProfile: false, aiPermission: false, preferences: false });
}

async function mountAndLogin() {
  mounted = withClient(createElement(SyncCard));
  await flushUntil(() => (document.querySelector("#sync-url") as HTMLInputElement | null)?.value === LINK.url);
  click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "登录") as HTMLButtonElement);
  await flushUntil(() => mocks.linkSet.mock.calls.length === 1);
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

describe("桌面端登录 · 直连成功", () => {
  it("linkSet 返回 connected: 不进分步, 直接完成登录并展示同步控制台", async () => {
    seedForm();
    mocks.linkSet.mockImplementation(() => {
      loggedIn = true;
      return Promise.resolve({ ...LINK, connect: "connected" });
    });
    await mountAndLogin();
    await flushUntil(() => document.body.textContent?.includes("立即同步") ?? false);

    expect(document.body.textContent).not.toContain("两步验证");
    expect(document.body.textContent).not.toContain("设置新密码");
    expect(mocks.mfa).not.toHaveBeenCalled();
    expect(mocks.resetPassword).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("success", "登录信息已保存，自动同步已开启");
  });
});

describe("桌面端登录 · mfa_required 分支", () => {
  it("进入两步验证: 提交动态码经 remoteLinkApi.mfa 完成后进入已登录态", async () => {
    seedForm();
    mocks.linkSet.mockResolvedValue({ ...LINK, connect: "mfa_required", mfaTicket: "ticket-1", mfaExpiresAt: 1700000300000 });
    mocks.mfa.mockImplementation(() => {
      loggedIn = true;
      return Promise.resolve({ ...LINK, connect: "connected" });
    });
    await mountAndLogin();
    await flushUntil(() => document.body.textContent?.includes("两步验证") ?? false);

    // 主表单隐藏, 只留动态码输入
    expect(document.querySelector("#sync-url")).toBeNull();
    expect(document.body.textContent).toContain("恢复码");

    setInputValue(document.querySelector("#sync-mfa-code") as HTMLInputElement, "123456");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("验证并登录")) as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("立即同步") ?? false);

    expect(mocks.mfa).toHaveBeenCalledWith("123456");
    expect(mocks.toast).toHaveBeenCalledWith("success", "登录信息已保存，自动同步已开启");
  });

  it("动态码错误内联展示, 可重试; 「返回」回到登录表单", async () => {
    seedForm();
    mocks.linkSet.mockResolvedValue({ ...LINK, connect: "mfa_required", mfaTicket: "ticket-1" });
    mocks.mfa.mockRejectedValueOnce(new Error("动态码错误")).mockImplementation(() => {
      loggedIn = true;
      return Promise.resolve({ ...LINK, connect: "connected" });
    });
    await mountAndLogin();
    await flushUntil(() => document.body.textContent?.includes("两步验证") ?? false);

    setInputValue(document.querySelector("#sync-mfa-code") as HTMLInputElement, "000000");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("验证并登录")) as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("动态码错误") ?? false);
    expect(document.body.textContent).not.toContain("立即同步");

    // 「返回」回主表单后可重新登录
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "返回") as HTMLButtonElement);
    await flushUntil(() => document.querySelector("#sync-url") !== null);
    expect(document.body.textContent).not.toContain("动态码错误");

    mocks.linkSet.mockResolvedValue({ ...LINK, connect: "mfa_required", mfaTicket: "ticket-2" });
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "登录") as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("两步验证") ?? false);
    setInputValue(document.querySelector("#sync-mfa-code") as HTMLInputElement, "123456");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("验证并登录")) as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("立即同步") ?? false);
  });
});

describe("桌面端登录 · reset_required 分支", () => {
  async function enterReset() {
    seedForm();
    mocks.linkSet.mockResolvedValue({ ...LINK, connect: "reset_required" });
    await mountAndLogin();
    await flushUntil(() => document.body.textContent?.includes("必须先设置新密码") ?? false);
  }

  it("前端校验: 新密码至少 8 位且两次输入一致, 不发请求", async () => {
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

  it("改密成功先一次性展示恢复密钥: 复制确认后才能进入, 密钥默认掩码", async () => {
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
    // 默认掩码: 密钥本体不出现在页面上
    expect(document.body.textContent).not.toContain(RECOVERY_KEY);
    const finish = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("已保存，进入同步")) as HTMLButtonElement;
    expect(finish.disabled).toBe(true);

    // 显示切换可见密钥, 复制后才能进入
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "显示") as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes(RECOVERY_KEY) ?? false);
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "复制") as HTMLButtonElement);
    await flushUntil(() => writeText.mock.calls.length === 1);
    expect(writeText).toHaveBeenCalledWith(RECOVERY_KEY);
    await flushUntil(
      () =>
        !([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("已保存，进入同步")) as HTMLButtonElement)
          .disabled,
    );

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("已保存，进入同步")) as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("立即同步") ?? false);
    expect(document.body.textContent).not.toContain(RECOVERY_KEY);
    expect(mocks.toast).toHaveBeenCalledWith("success", "登录信息已保存，自动同步已开启");
  });

  it("clipboard 不可用: 自动显示密钥并提示手动复制, 同样放行进入", async () => {
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
    await flushUntil(() => document.body.textContent?.includes("复制失败，请手动复制恢复密钥") ?? false);
    expect(document.body.textContent).toContain(RECOVERY_KEY);

    await flushUntil(
      () =>
        !([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("已保存，进入同步")) as HTMLButtonElement)
          .disabled,
    );
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("已保存，进入同步")) as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("立即同步") ?? false);
  });

  it("改密失败内联展示, 停留改密步可重试", async () => {
    await enterReset();
    mocks.resetPassword.mockRejectedValueOnce(new Error("登录状态已过期，请重新连接")).mockImplementation(() => {
      loggedIn = true;
      return Promise.resolve({ ...LINK, connect: "connected", recoveryKey: RECOVERY_KEY });
    });

    setInputValue(document.querySelector("#sync-new-pass") as HTMLInputElement, "new-password-1");
    setInputValue(document.querySelector("#sync-new-pass2") as HTMLInputElement, "new-password-1");
    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("设置新密码")) as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("登录状态已过期，请重新连接") ?? false);
    expect(document.body.textContent).toContain("必须先设置新密码");

    click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("设置新密码")) as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("恢复密钥（仅显示一次）") ?? false);
  });
});
