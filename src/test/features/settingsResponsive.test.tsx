/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, flush, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    getPermission: vi.fn(),
    setPermission: vi.fn(),
    testProvider: vi.fn(),
    vaultStatus: vi.fn(),
    auditQuery: vi.fn(),
    ask: vi.fn(),
    promptText: vi.fn(),
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
      testProvider: mocks.testProvider,
    },
    vaultApi: {
      status: mocks.vaultStatus,
      initDpapi: vi.fn(),
      initMaster: vi.fn(),
      changePassword: vi.fn(),
    },
    assetApi: {
      auditQuery: mocks.auditQuery,
    },
  };
});
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, promptText: mocks.promptText }));
vi.mock("../../features/settings/MemoryCard", () => ({ MemoryCard: () => null }));
vi.mock("../../features/settings/KnownHostsCard", () => ({ KnownHostsCard: () => null }));
vi.mock("../../features/settings/SyncCard", () => ({ SyncCard: () => null }));
vi.mock("../../features/ai/ModelPanel", () => ({ ModelManager: () => null }));

import { SettingsView } from "../../features/settings/SettingsView";
import { AuditView } from "../../features/settings/AuditView";
import { useUi } from "../../app/store";
import { KEYBINDING_ACTIONS } from "../../app/keybindings";
import { UI_FONT_SCALE_STEPS } from "../../app/preferences";

const PERMISSIONS = { mode: "read_write" as const, dangerRules: ["kubectl delete"] };
const VAULT = { initialized: true, mode: "dpapi" as const, unlocked: true, autoLockMinutes: 0 };

function keyDown(target: EventTarget, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

let mounted: MountedView | undefined;

function withClient(node: React.ReactElement): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  useUi.setState({ pushToast: mocks.toast });
  mocks.getPermission.mockResolvedValue(PERMISSIONS);
  mocks.vaultStatus.mockResolvedValue(VAULT);
  mocks.auditQuery.mockResolvedValue([]);
  mocks.testProvider.mockResolvedValue({ modelsOk: true, chatOk: true });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("SettingsView responsive structure", () => {
  it("keeps horizontal chips on narrow screens and uses a left TOC with independently scrolling content on wide screens", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    const nav = mounted.container.querySelector<HTMLElement>('nav[aria-label="设置分区"]');
    expect(nav).toBeTruthy();
    const chips = nav!.querySelector<HTMLElement>('[data-testid="settings-nav-chips"]');
    expect(chips?.className).toContain("md:hidden");
    const toc = nav!.querySelector<HTMLElement>('[data-testid="settings-nav-toc"]');
    expect(toc?.className).toContain("hidden");
    expect(toc?.className).toContain("md:flex");
    expect(nav!.className).not.toContain("md:border-r");
    expect(nav!.className).not.toContain("bg-[var(--nx-bg-pane)]");
    const root = mounted.container.firstElementChild as HTMLElement;
    expect(root.className).toContain("nx-pane");
    expect(root.className).not.toContain("overflow-y-auto");
    const scroller = mounted.container.querySelector<HTMLElement>("div.overflow-y-auto");
    expect(scroller).toBeTruthy();
    expect(scroller!.querySelector("#settings-vault")).not.toBeNull();
    expect(scroller!.querySelector("#settings-shortcuts")).not.toBeNull();
  });

  it("uses one shortcuts column on narrow screens and two when the card is 640px wide or more", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    const grid = [...mounted.container.querySelectorAll("div")].find(
      (d) => d.className.includes("grid-cols") && d.querySelector(".nx-kbd"),
    );
    expect(grid).toBeTruthy();
    expect(grid!.className).toContain("grid-cols-1");
    expect(grid!.className).toContain("@min-[640px]:grid-cols-2");
    expect(grid!.querySelectorAll(".nx-kbd").length).toBe(KEYBINDING_ACTIONS.length);
  });

  it("allows the UI font scale segment to wrap while other segments stay on one line", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    const container = mounted.container;
    clickButton(document.getElementById("settings-appearance")!, "高级设置");
    const segmentByLabel = (label: string) =>
      [...container.querySelectorAll(".nx-segment")].find(
        (s) => s.getAttribute("aria-label") === label,
      );
    const scale = segmentByLabel("界面文字倍率");
    expect(scale).toBeTruthy();
    expect(scale!.className).toContain("flex-wrap");
    const items = scale!.querySelectorAll(".nx-segment-item");
    expect(items.length).toBe(UI_FONT_SCALE_STEPS.length);
    expect([...items].map((it) => it.textContent)).toEqual(
      UI_FONT_SCALE_STEPS.map((step) => `${Math.round(step * 100)}%`),
    );
    for (const label of ["界面主题", "界面字号", "终端与编辑器主题"]) {
      expect(segmentByLabel(label)?.className ?? "").not.toContain("flex-wrap");
    }
  });

  it("collapses appearance advanced settings and connectivity testing by default and expands them on demand", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    const appearance = document.getElementById("settings-appearance")!;
    const appearanceToggle = [...appearance.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "高级设置",
    )!;
    expect(appearanceToggle.getAttribute("aria-expanded")).toBe("false");
    expect(appearance.querySelector('[aria-label="界面字号"]')).toBeNull();
    click(appearanceToggle);
    expect(appearance.querySelector('[aria-label="界面字号"]')).not.toBeNull();

    const aiModel = document.getElementById("settings-ai-model")!;
    const connToggle = [...aiModel.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "连通性测试",
    )!;
    expect(connToggle.getAttribute("aria-expanded")).toBe("false");
    expect(aiModel.textContent).not.toContain("测试当前模型连通性");
    click(connToggle);
    expect(connToggle.getAttribute("aria-expanded")).toBe("true");
    expect(aiModel.textContent).toContain("测试当前模型连通性");
  });

  it("wraps the AI model card header and connectivity test button", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    const title = [...mounted.container.querySelectorAll(".nx-card-title")].find(
      (s) => s.textContent === "AI 模型",
    );
    expect(title?.parentElement?.className).toContain("flex-wrap");
    clickButton(document.getElementById("settings-ai-model")!, "连通性测试");
    const button = [...mounted.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("测试当前模型连通性"),
    );
    expect(button?.className).toContain("whitespace-normal");
    expect(button?.className).toContain("h-auto");
    expect(button?.className).toContain("shrink");
  });

  it("distinguishes Web key storage location from sync conditions in the AI model card copy", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("密钥加密保存在当前实例");
    expect(text).toContain("启用档案同步并解锁凭据库后");
    expect(text).toContain("端到端加密同步");
    expect(text).not.toContain("不会上传");
    expect(text).not.toContain("明文存在本机 sqlite");
  });

  it("uses success styling for successful connectivity and danger styling for failures", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    clickButton(mounted.container, "连通性测试");
    clickButton(mounted.container, "测试当前模型连通性");
    await flush();
    const okAlert = [...mounted.container.querySelectorAll(".nx-alert")].find((el) =>
      el.textContent?.includes("测试结果"),
    );
    expect(okAlert?.className).toContain("nx-alert-success");
    expect(okAlert?.className).not.toContain("nx-alert-danger");

    mocks.testProvider.mockResolvedValueOnce({
      modelsOk: false,
      chatOk: false,
      modelsError: "boom",
      chatError: "bang",
    });
    clickButton(mounted.container, "测试当前模型连通性");
    await flush();
    const failAlert = [...mounted.container.querySelectorAll(".nx-alert")].find((el) =>
      el.textContent?.includes("测试结果"),
    );
    expect(failAlert?.className).toContain("nx-alert-danger");
    expect(failAlert?.textContent).toContain("boom");
  });

  it("submits new rules on Enter but not during IME composition", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    clickButton(mounted.container, "添加一条规则");
    const input = mounted.container.querySelector<HTMLInputElement>(
      'input[placeholder*="Enter 保存"]',
    );
    expect(input).toBeTruthy();
    setInputValue(input!, "rm -rf /");
    keyDown(input!, "Enter", { isComposing: true });
    await flush();
    expect(mocks.setPermission).not.toHaveBeenCalled();
    keyDown(input!, "Enter");
    await flush();
    expect(mocks.setPermission).toHaveBeenCalledTimes(1);
    expect(mocks.setPermission.mock.calls[0][0].dangerRules).toContain("rm -rf /");
  });

  it("saves edited rules on Enter but not during IME composition", async () => {
    localStorage.setItem("nexterm.settingsGroup.v1", "ai");
    mounted = withClient(createElement(SettingsView));
    await flush();
    const input = mounted.container.querySelector<HTMLInputElement>(
      'input[title="kubectl delete"]',
    );
    expect(input).toBeTruthy();
    setInputValue(input!, "kubectl delete --all");
    act(() => input!.focus());
    keyDown(input!, "Enter", { isComposing: true });
    await flush();
    expect(mocks.setPermission).not.toHaveBeenCalled();
    keyDown(input!, "Enter");
    await flush();
    expect(mocks.setPermission).toHaveBeenCalledTimes(1);
    expect(mocks.setPermission.mock.calls[0][0].dangerRules).toContain("kubectl delete --all");
  });

  it("makes the credential protection toggle row a label so clicking its text expands the enable form", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    const label = [...mounted.container.querySelectorAll("label")].find((l) =>
      l.textContent?.includes("用密码保护凭据"),
    );
    expect(label).toBeTruthy();
    expect(label!.querySelector('input[type="checkbox"]')).toBeTruthy();
    click(label!);
    await flush();
    expect(mounted.container.querySelector('input[type="password"]')).toBeTruthy();
  });

  it("disables the terminal OSC 52 toggle by default and allows remote clipboard writes only after explicit opt-in", async () => {
    act(() => useUi.setState({ terminalOsc52: false }));
    localStorage.clear();
    mounted = withClient(createElement(SettingsView));
    await flush();
    const label = [...mounted.container.querySelectorAll("label")].find((l) =>
      l.textContent?.includes("允许远程主机写入剪贴板"),
    );
    expect(label).toBeTruthy();
    const box = label!.querySelector<HTMLInputElement>('input[type="checkbox"]');
    expect(box).toBeTruthy();
    expect(box!.checked).toBe(false);
    mocks.ask.mockResolvedValueOnce(true);
    click(box!);
    await flush();
    expect(useUi.getState().terminalOsc52).toBe(true);
    expect(localStorage.getItem("nexterm.terminal.v1")).toBe('{"osc52":true}');
    act(() => useUi.setState({ terminalOsc52: false }));
  });

  it("explains the host-lifetime limit of local background terminals", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    const terminal = document.getElementById("settings-terminal");
    expect(terminal?.textContent).toContain("支持后台的交互终端");
    expect(terminal?.textContent).toContain("本机后台终端仅在宿主应用运行期间保留");
    expect(terminal?.textContent).toContain("不是 durable SSH");
  });
});

describe("AuditView responsive behavior and exit-code semantics", () => {
  it("wraps the toolbar so refresh remains reachable without horizontal scrolling", async () => {
    mocks.auditQuery.mockResolvedValue([]);
    mounted = mount(createElement(AuditView));
    await flush();
    const toolbar = mounted.container.querySelector(".nx-toolbar");
    expect(toolbar?.className).toContain("flex-wrap");
  });

  it("shows check and cross symbols plus text hints in addition to exit-code colors", async () => {
    mocks.auditQuery.mockResolvedValue([
      { id: 1, ts: 1728000000000, sessionId: null, assetId: null, source: "user", kind: "exec", payload: { cmd: "ls" }, exitCode: 0, durationMs: 12 },
      { id: 2, ts: 1728000001000, sessionId: null, assetId: null, source: "ai", kind: "exec", payload: { cmd: "rm" }, exitCode: 2, durationMs: 30 },
      { id: 3, ts: 1728000002000, sessionId: null, assetId: null, source: "user", kind: "open", payload: {}, exitCode: null, durationMs: null },
    ]);
    mounted = mount(createElement(AuditView));
    await flush();
    const cells = [...mounted.container.querySelectorAll("tbody tr")].map(
      (row) => row.querySelectorAll("td")[4],
    );
    expect(cells.length).toBe(3);
    expect(cells[0]?.textContent).toContain("✓ 0");
    expect(cells[0]?.querySelector("[title='成功']")).toBeTruthy();
    expect(cells[1]?.textContent).toContain("✗ 2");
    expect(cells[1]?.querySelector("[title='失败']")).toBeTruthy();
    expect(cells[2]?.textContent).toContain("—");
  });
});
