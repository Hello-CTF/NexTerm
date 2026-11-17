/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flushUntil, mount, type MountedView } from "./reactTestUtils";
import type { AiPermissionMode, AiPermissionState } from "../../ipc/commands";
import type { AiChatCtx } from "../../features/ai/panel/aiChatCtx";

const mocks = vi.hoisted(() => ({ ask: vi.fn() }));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});

vi.mock("../../features/ai/GrantPanel", () => ({ GrantPanel: () => null }));

import { AiChatPanels } from "../../features/ai/panel/AiChatPanels";

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function stateOf(globalMode: AiPermissionMode, hostMode?: AiPermissionMode, hostResolved = true): AiPermissionState {
  return {
    config: { mode: hostMode ?? globalMode, dangerRules: [] },
    globalMode,
    hostMode,
    hostResolved,
    source: hostMode ? "host" : "global",
  };
}

function renderPanels(permState: AiPermissionState, permScope?: Record<string, unknown>, production = false) {
  const switchMode = vi.fn();
  const setHostOverride = vi.fn();
  const ctx = {
    permOpen: true,
    setPermOpen: vi.fn(),
    permError: null,
    loadPermission: vi.fn(),
    perm: permState.config,
    permState,
    permScope,
    switchMode,
    setHostOverride,
    planMode: false,
    setPlanMode: vi.fn(),
    aiScopeAssetId: undefined,
    aiScopeProduction: production ? { production: true, environment: "生产" } : null,
    tasksOpen: false,
  } as unknown as AiChatCtx;
  mounted = mount(createElement(AiChatPanels, { ctx }));
  return { switchMode, setHostOverride };
}

function modeButton(label: string): HTMLButtonElement {
  const group = document.querySelector('[role="radiogroup"][aria-label="AI 权限模式"]');
  const button = [...(group?.querySelectorAll<HTMLButtonElement>('[role="radio"]') ?? [])].find((item) => item.textContent === label);
  if (!button) throw new Error(`mode button ${label} not found`);
  return button;
}

function hostSwitch(): HTMLButtonElement | null {
  return [...document.querySelectorAll<HTMLButtonElement>('button[role="switch"]')].find((item) =>
    item.textContent?.includes("这台主机单独设置"),
  ) ?? null;
}

const scope = { sessionId: "session-1", tabId: "tab-1" };

describe("AI permission panel", () => {
  it("asks before switching to full access and keeps the previous mode when cancelled", async () => {
    mocks.ask.mockResolvedValue(false);
    const { switchMode } = renderPanels(stateOf("read_write"), scope);
    click(modeButton("完全放行"));
    await flushUntil(() => mocks.ask.mock.calls.length > 0);
    await Promise.resolve();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const [message, options] = mocks.ask.mock.calls[0] as [string, { title?: string; kind?: string }];
    expect(message).toContain("完全放行");
    expect(message).toContain("高危");
    // Without a host override the switch applies to every host, even when the panel is opened on a terminal.
    expect(message).toContain("在所有未单独设置模式的主机上");
    expect(options).toMatchObject({ kind: "warning" });
    expect(switchMode).not.toHaveBeenCalled();
    expect(modeButton("读写").getAttribute("aria-checked")).toBe("true");
  });

  it("names the current host when it has its own mode", async () => {
    mocks.ask.mockResolvedValue(true);
    const { switchMode } = renderPanels(stateOf("read_write", "silent"), scope);
    click(modeButton("完全放行"));
    await flushUntil(() => switchMode.mock.calls.length > 0);
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("在这台主机上");
    expect(switchMode).toHaveBeenCalledWith("full_access");
  });

  it("does not ask for the other modes", async () => {
    const { switchMode } = renderPanels(stateOf("full_access"));
    expect(document.body.textContent).toContain("命中拦截规则仍需确认");
    click(modeButton("自动执行"));
    await flushUntil(() => switchMode.mock.calls.length > 0);
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(switchMode).toHaveBeenCalledWith("silent");
  });

  it("labels the global mode and offers an explicit host override without a separate global button", async () => {
    const { setHostOverride, switchMode } = renderPanels(stateOf("silent"), scope);
    expect(document.body.textContent).toContain("全局模式");
    expect(document.body.textContent).toContain("所有主机");
    expect([...document.querySelectorAll("button")].some((item) => item.textContent === "设为全局默认")).toBe(false);
    const toggle = hostSwitch();
    expect(toggle?.getAttribute("aria-checked")).toBe("false");
    click(toggle!);
    await flushUntil(() => setHostOverride.mock.calls.length > 0);
    expect(setHostOverride).toHaveBeenCalledWith(true);
    expect(switchMode).not.toHaveBeenCalled();
  });

  it("shows an explicit host override with the global mode it hides", async () => {
    const { setHostOverride } = renderPanels(stateOf("read_write", "read_only"), scope);
    expect(document.body.textContent).toContain("这台主机的模式");
    expect(document.body.textContent).toContain("仅这台主机");
    const toggle = hostSwitch();
    expect(toggle?.getAttribute("aria-checked")).toBe("true");
    expect(toggle?.textContent).toContain("全局模式为「读写」");
    expect(modeButton("只读").getAttribute("aria-checked")).toBe("true");
    click(toggle!);
    await flushUntil(() => setHostOverride.mock.calls.length > 0);
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(setHostOverride).toHaveBeenCalledWith(false);
  });

  it("asks before a host follows a global full access", async () => {
    mocks.ask.mockResolvedValue(false);
    const { setHostOverride } = renderPanels(stateOf("full_access", "read_write"), scope);
    click(hostSwitch()!);
    await flushUntil(() => mocks.ask.mock.calls.length > 0);
    await Promise.resolve();
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("在这台主机上");
    expect(setHostOverride).not.toHaveBeenCalled();
  });

  it("offers no host override without a terminal host", () => {
    renderPanels(stateOf("read_write"));
    expect(hostSwitch()).toBeNull();
    mounted?.unmount();
    renderPanels(stateOf("read_write", undefined, false), scope);
    expect(hostSwitch()).toBeNull();
  });

  it("explains that full access is decided as 自动执行 on a production host", () => {
    renderPanels(stateOf("full_access"), scope, true);
    expect(document.body.textContent).toContain("完全放行在这里按「自动执行」处理");
  });
});
