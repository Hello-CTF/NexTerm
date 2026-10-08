/** @vitest-environment jsdom */
// M195 验收: 设置分区导航(小标签直达、键盘可达)与统一登录入口
// (Web 未登录时全设置页只有一个登录按钮; 账号/同步/分享不再各放一个)。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    getPermission: vi.fn(),
    setPermission: vi.fn(),
    vaultStatus: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    aiApi: {
      getPermission: mocks.getPermission,
      setPermission: mocks.setPermission,
      testProvider: vi.fn(),
    },
    vaultApi: {
      status: mocks.vaultStatus,
      initDpapi: vi.fn(),
      initMaster: vi.fn(),
      changePassword: vi.fn(),
      setAutoLock: vi.fn(),
    },
  };
});
vi.mock("../../ui/dialogs", () => ({ ask: vi.fn(), promptText: vi.fn() }));
vi.mock("../../features/settings/MemoryCard", () => ({ MemoryCard: () => null }));
vi.mock("../../features/settings/CronCard", () => ({ CronCard: () => null }));
vi.mock("../../features/settings/KnownHostsCard", () => ({ KnownHostsCard: () => null }));
vi.mock("../../features/ai/ModelPanel", () => ({ ModelManager: () => null }));

import { SettingsView } from "../../features/settings/SettingsView";
import { useAuth } from "../../features/auth/store";
import { useUi } from "../../app/store";

let mounted: MountedView | undefined;

function withClient(node: React.ReactElement): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function nav(): HTMLElement {
  const el = mounted?.container.querySelector<HTMLElement>('nav[aria-label="设置分区"]');
  if (!el) throw new Error("settings section navigator not found");
  return el;
}

function chips(): HTMLButtonElement[] {
  return [...nav().querySelectorAll("button")];
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  Element.prototype.scrollIntoView = vi.fn();
  useUi.setState({ pushToast: mocks.toast });
  useAuth.setState({
    status: { initialized: true, registration_open: false, auth: "on" },
    user: null,
    dek: null,
    gate: "ready",
    pendingRecoveryKey: null,
    error: null,
  });
  mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
  mocks.vaultStatus.mockResolvedValue({ initialized: true, mode: "dpapi", unlocked: true, autoLockMinutes: 0 });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useAuth.setState({ user: null, dek: null, gate: "ready", pendingRecoveryKey: null, error: null, status: null });
});

describe("设置分区导航", () => {
  it("渲染全部分区小标签,目标分区都真实存在", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    const labels = chips().map((c) => c.textContent?.trim());
    expect(labels).toEqual([
      "外观", "终端", "AI 模型", "AI 拦截", "长期记忆", "定时任务",
      "凭据保护", "已知主机", "账号同步", "资产包", "文件链接", "分享", "快捷键",
    ]);
    const ids = [
      "settings-appearance", "settings-terminal", "settings-ai-model", "settings-ai-rules",
      "settings-memory", "settings-cron", "settings-vault", "settings-known-hosts",
      "settings-account", "settings-bundle", "settings-files", "settings-share", "settings-shortcuts",
    ];
    for (const id of ids) {
      expect(document.getElementById(id), `missing section #${id}`).not.toBeNull();
    }
  });

  it("小标签是原生按钮(键盘可达),点击直达对应分区", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    for (const chip of chips()) {
      expect(chip.tagName).toBe("BUTTON");
      expect(chip.disabled).toBe(false);
    }
    const target = document.getElementById("settings-vault")!;
    const vaultChip = chips().find((c) => c.textContent?.trim() === "凭据保护")!;
    click(vaultChip);
    expect(target.scrollIntoView).toHaveBeenCalledWith({ behavior: "smooth", block: "start" });
  });
});

describe("AI 拦截规则文案", () => {
  it("主文与脚注都如实写明各模式下的命中行为", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    const text = document.getElementById("settings-ai-rules")?.textContent ?? "";
    expect(text).toContain("读写与完全静默模式下每次先向你确认");
    expect(text).toContain("只读与无人值守模式下直接拒绝");
    expect(text).toContain("规则命中 → 读写/完全静默先确认，只读/无人值守直接拒绝");
    expect(text).not.toContain("命中即拦截");
  });
});

describe("AI 拦截规则草稿", () => {
  it("预填草稿定位到规则区且失焦不静默保存，Enter 才写入", async () => {
    useUi.setState({ aiRulePrefill: "kubectl delete" });
    mounted = withClient(createElement(SettingsView));
    await flush();

    expect(useUi.getState().aiRulePrefill).toBeNull();
    const section = document.getElementById("settings-ai-rules")!;
    expect(section.scrollIntoView).toHaveBeenCalled();
    const draft = section.querySelector<HTMLInputElement>('input[placeholder*="Enter 保存"]')!;
    expect(draft.value).toBe("kubectl delete");

    act(() => draft.blur());
    await flush();
    expect(mocks.setPermission).not.toHaveBeenCalled();
    expect(section.querySelector('input[placeholder*="Enter 保存"]')).not.toBeNull();

    setInputValue(draft, "kubectl delete pod");
    act(() => {
      draft.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    });
    await flush();
    expect(mocks.setPermission).toHaveBeenCalledWith(
      expect.objectContaining({ dangerRules: ["kubectl delete pod"] }),
    );
  });
});

describe("统一登录入口(Web 未登录)", () => {
  it("整个设置页只有一个登录按钮,分享卡不再放第二个登录入口", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    const loginButtons = [...mounted!.container.querySelectorAll("button")].filter(
      (b) => b.textContent?.trim() === "登录",
    );
    expect(loginButtons.length).toBe(1);
    const text = mounted!.container.textContent ?? "";
    expect(text).toContain("当前未登录");
    expect(text).not.toContain("登录以启用同步");
    expect(text).not.toContain("去登录");
    // 分享卡保留说明但没有任何按钮
    const share = document.getElementById("settings-share")!;
    expect(share.textContent).toContain("登录后可以把你接入的守护主机分享给其他注册用户");
    expect(share.querySelectorAll("button").length).toBe(0);
    // 唯一的登录按钮打开账号门
    click(loginButtons[0]);
    expect(useAuth.getState().gate).toBe("login");
  });
});
