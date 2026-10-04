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
vi.mock("../../features/settings/CronCard", () => ({ CronCard: () => null }));
vi.mock("../../features/settings/KnownHostsCard", () => ({ KnownHostsCard: () => null }));
vi.mock("../../features/settings/SyncCard", () => ({ SyncCard: () => null }));
vi.mock("../../features/ai/ModelPanel", () => ({ ModelManager: () => null }));

import { SettingsView } from "../../features/settings/SettingsView";
import { AuditView } from "../../features/settings/AuditView";
import { useUi } from "../../app/store";

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

describe("SettingsView 响应式结构", () => {
  it("快捷键网格窄屏单列、≥480px 双列", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    const grid = [...mounted.container.querySelectorAll("div")].find(
      (d) => d.className.includes("grid-cols") && d.querySelector(".nx-kbd"),
    );
    expect(grid).toBeTruthy();
    expect(grid!.className).toContain("grid-cols-1");
    expect(grid!.className).toContain("min-[480px]:grid-cols-2");
    expect(grid!.querySelectorAll(".nx-kbd").length).toBe(8);
  });

  it("AI 模型卡头可换行、连通性测试按钮可折行", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    const title = [...mounted.container.querySelectorAll(".nx-card-title")].find(
      (s) => s.textContent === "AI 模型",
    );
    expect(title?.parentElement?.className).toContain("flex-wrap");
    const button = [...mounted.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("连通性测试"),
    );
    expect(button?.className).toContain("whitespace-normal");
    expect(button?.className).toContain("h-auto");
    expect(button?.className).toContain("shrink");
  });

  it("连通性成功用成功色、失败用危险色", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    clickButton(mounted.container, "连通性测试（两步）· 当前模型");
    await flush();
    const okAlert = [...mounted.container.querySelectorAll(".nx-alert")].find((el) =>
      el.textContent?.includes("测试结果"),
    );
    expect(okAlert?.className).toContain("text-green-300");
    expect(okAlert?.className).not.toContain("nx-alert-danger");

    mocks.testProvider.mockResolvedValueOnce({
      modelsOk: false,
      chatOk: false,
      modelsError: "boom",
      chatError: "bang",
    });
    clickButton(mounted.container, "连通性测试（两步）· 当前模型");
    await flush();
    const failAlert = [...mounted.container.querySelectorAll(".nx-alert")].find((el) =>
      el.textContent?.includes("测试结果"),
    );
    expect(failAlert?.className).toContain("nx-alert-danger");
    expect(failAlert?.textContent).toContain("boom");
  });

  it("新增规则输入框 Enter 提交、IME 组词中 Enter 不提交", async () => {
    mounted = withClient(createElement(SettingsView));
    await flush();
    clickButton(mounted.container, "添加一条规则");
    const input = mounted.container.querySelector<HTMLInputElement>(
      'input[placeholder="例如 kubectl delete"]',
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

  it("编辑规则输入框 Enter 失焦保存、IME 组词中 Enter 不触发", async () => {
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

  it("凭据保护开关整行是 label，点文字即可展开启用表单", async () => {
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

  it("终端 OSC 52 开关默认关闭，显式勾选后才允许远程写剪贴板", async () => {
    useUi.setState({ terminalOsc52: false });
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
    click(box!);
    await flush();
    expect(useUi.getState().terminalOsc52).toBe(true);
    expect(localStorage.getItem("nexterm.terminal.v1")).toBe('{"osc52":true}');
    useUi.setState({ terminalOsc52: false });
  });
});

describe("AuditView 响应式与退出码语义", () => {
  it("工具栏可换行，刷新无需横向滚动即可到达", async () => {
    mocks.auditQuery.mockResolvedValue([]);
    mounted = mount(createElement(AuditView));
    await flush();
    const toolbar = mounted.container.querySelector(".nx-toolbar");
    expect(toolbar?.className).toContain("flex-wrap");
  });

  it("退出码除颜色外带 ✓/✗ 符号与文字提示", async () => {
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
