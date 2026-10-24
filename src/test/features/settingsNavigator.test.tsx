/** @vitest-environment jsdom */
// M195 acceptance: settings section navigation (small-tab jump, keyboard reachable) and a unified login entry
// (Web logged-out: the whole settings page has a single login button; account/sync/sharing no longer each place one).
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
  const strip = nav().querySelector<HTMLElement>('[data-testid="settings-nav-chips"]');
  if (!strip) throw new Error("narrow-screen chip strip not found");
  return [...strip.querySelectorAll("button")];
}

function toc(): HTMLElement {
  const el = nav().querySelector<HTMLElement>('[data-testid="settings-nav-toc"]');
  if (!el) throw new Error("wide-screen section toc not found");
  return el;
}

function tocButtons(): HTMLButtonElement[] {
  return [...toc().querySelectorAll<HTMLButtonElement>("button")].filter(
    (button) => !button.getAttribute("aria-label")?.endsWith("设置目录"),
  );
}

function tocToggle(): HTMLButtonElement {
  const el = toc().querySelector<HTMLButtonElement>('button[aria-label$="设置目录"]');
  if (!el) throw new Error("toc collapse toggle not found");
  return el;
}

const GROUP_LABELS = ["通用", "AI", "安全", "账号与同步"];
const GENERAL_TOC = ["通用", "外观", "终端", "快捷键", "诊断与调试", "AI", "安全", "账号与同步"];

function groupShown(id: string): boolean {
  return mounted!.container.querySelector<HTMLElement>(`[data-settings-group="${id}"]`)?.hidden === false;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
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

describe("Settings section navigation", () => {
  it("does not label the tool's UI ownership as global", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    expect(mounted.container.querySelector(".nx-toolbar")?.textContent).toContain("设置");
    expect(
      mounted.container.querySelector('.nx-toolbar span[title="全局面板：内容不随工作区切换"]'),
    ).toBeNull();
  });

  it("groups the sections and shows one group at a time", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    expect(chips().map((c) => c.textContent?.trim())).toEqual(GROUP_LABELS);
    expect(tocButtons().map((c) => c.textContent?.trim())).toEqual(GENERAL_TOC);
    expect(groupShown("general")).toBe(true);
    expect(groupShown("ai")).toBe(false);
    // Hidden groups stay mounted, so every section keeps a real target.
    for (const id of [
      "settings-appearance", "settings-terminal", "settings-shortcuts", "settings-ai-model", "settings-ai-rules",
      "settings-memory", "settings-vault", "settings-known-hosts", "settings-production",
      "settings-account", "settings-bundle", "settings-share",
    ]) {
      expect(document.getElementById(id), `missing section #${id}`).not.toBeNull();
    }
  });

  it("switches groups from the chips and remembers the choice", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    for (const chip of chips()) {
      expect(chip.tagName).toBe("BUTTON");
    }
    click(chips().find((c) => c.textContent?.trim() === "安全")!);
    await flush();
    expect(groupShown("security")).toBe(true);
    expect(groupShown("general")).toBe(false);
    expect(localStorage.getItem("nexterm.settingsGroup.v1")).toBe("security");
    expect(tocButtons().map((c) => c.textContent?.trim())).toEqual(["通用", "AI", "安全", "凭据保护", "已知主机", "生产安全", "账号与同步"]);

    mounted.unmount();
    mounted = withClient(createElement(SettingsView));
    await flush();
    expect(groupShown("security")).toBe(true);
  });

  it("scrolls to a section of the open group from the TOC and marks it current", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    click(tocButtons().find((c) => c.textContent?.trim() === "安全")!);
    await flush();
    const target = document.getElementById("settings-vault")!;
    const vaultEntry = tocButtons().find((c) => c.textContent?.trim() === "凭据保护")!;
    click(vaultEntry);
    await flush();
    expect(target.scrollIntoView).toHaveBeenCalledWith({ behavior: "smooth", block: "start" });
    const current = tocButtons().find((c) => c.getAttribute("aria-current") === "true");
    expect(current?.textContent?.trim()).toBe("凭据保护");
    expect(current?.className).toContain("bg-neutral-800/70");
  });

  it("shows group icons after collapsing the TOC and restores the sections after expanding", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    const toggle = tocToggle();
    expect(toggle.getAttribute("aria-label")).toBe("收起设置目录");
    click(toggle);
    await flush();
    expect(tocToggle().getAttribute("aria-label")).toBe("展开设置目录");
    expect(tocToggle().getAttribute("aria-expanded")).toBe("false");
    const icons = tocButtons();
    expect(icons.map((b) => b.getAttribute("aria-label"))).toEqual(GROUP_LABELS);
    expect(icons.find((b) => b.getAttribute("aria-current") === "true")?.getAttribute("aria-label")).toBe("通用");
    click(icons.find((b) => b.getAttribute("aria-label") === "AI")!);
    await flush();
    expect(groupShown("ai")).toBe(true);
    click(tocToggle());
    await flush();
    expect(tocButtons().map((c) => c.textContent?.trim())).toEqual(["通用", "AI", "模型", "拦截规则", "技能", "记忆", "安全", "账号与同步"]);
  });

  it("persists TOC expansion state to local storage", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    click(tocToggle());
    await flush();
    expect(localStorage.getItem("nexterm.settingsNav.v1")).toBe("collapsed");
    mounted.unmount();
    mounted = withClient(createElement(SettingsView));
    await flush();
    expect(tocToggle().getAttribute("aria-expanded")).toBe("false");
    click(tocToggle());
    await flush();
    expect(localStorage.getItem("nexterm.settingsNav.v1")).toBe("open");
  });

  it("follows the current section of the open group after the content scrolls", async () => {
    localStorage.setItem("nexterm.settingsGroup.v1", "ai");
    mounted = withClient(createElement(SettingsView));
    await flush();
    const scrollContainer = mounted.container.querySelector<HTMLElement>("div.overflow-y-auto");
    expect(scrollContainer).not.toBeNull();
    const offsets: Record<string, number> = { "settings-ai-model": -120, "settings-ai-rules": 30 };
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
      const top = this === scrollContainer ? 0 : this.id in offsets ? offsets[this.id] : 1000;
      return { top, bottom: top + 100, left: 0, right: 100, width: 100, height: 100, x: 0, y: top, toJSON: () => ({}) } as DOMRect;
    });
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      cb(0);
      return 0;
    });
    act(() => {
      scrollContainer!.dispatchEvent(new Event("scroll"));
    });
    const current = tocButtons().find((c) => c.getAttribute("aria-current") === "true");
    expect(current?.textContent?.trim()).toBe("拦截规则");
  });
});

describe("AI interception rule copy", () => {
  it("describes the behavior for each mode accurately in the main text and footnote", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    const text = document.getElementById("settings-ai-rules")?.textContent ?? "";
    expect(text).toContain("读写与自动执行模式下每次先向你确认");
    expect(text).toContain("只读与无人值守模式下直接拒绝");
    expect(text).toContain("不可逆操作（格式化磁盘、清空系统目录、删除数据库）会直接拒绝");
    expect(text).not.toContain("规则命中 → 读写/完全静默先确认");
    expect(text).not.toContain("命中即拦截");
  });
});

describe("AI interception rule drafts", () => {
  it("locates the rule section for prefilled drafts, does not save on blur, and writes only on Enter", async () => {
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

describe("Unified login entry (Web logged out)", () => {
  it("has only one login button across settings and no second login entry in the share card", async () => {
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
    // The share card keeps its description but has no buttons
    const share = document.getElementById("settings-share")!;
    expect(share.textContent).toContain("登录后可以把你接入的设备分享给其他注册用户");
    expect(share.querySelectorAll("button").length).toBe(0);
    // The single login button opens the account gate
    click(loginButtons[0]);
    expect(useAuth.getState().gate).toBe("login");
  });
});
