/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, flushUntil, mount, type MountedView } from "./reactTestUtils";
import type { AiPermissionConfig } from "../../ipc/commands";
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

function renderPanels(perm: AiPermissionConfig, permScope?: string, applyModeGlobally = vi.fn()) {
  const switchMode = vi.fn();
  const ctx = {
    permOpen: true,
    setPermOpen: vi.fn(),
    permError: null,
    loadPermission: vi.fn(),
    perm,
    permScope,
    permDeviceOverride: false,
    followGlobalMode: vi.fn(),
    switchMode,
    applyModeGlobally,
    planMode: false,
    setPlanMode: vi.fn(),
    aiScopeAssetId: undefined,
    tasksOpen: false,
  } as unknown as AiChatCtx;
  mounted = mount(createElement(AiChatPanels, { ctx }));
  return switchMode;
}

function modeButton(label: string): HTMLButtonElement {
  const group = document.querySelector('[role="radiogroup"][aria-label="AI 权限模式"]');
  const button = [...(group?.querySelectorAll<HTMLButtonElement>('[role="radio"]') ?? [])].find((item) => item.textContent === label);
  if (!button) throw new Error(`mode button ${label} not found`);
  return button;
}

describe("full access permission mode", () => {
  it("asks before switching and keeps the previous mode when cancelled", async () => {
    mocks.ask.mockResolvedValue(false);
    const switchMode = renderPanels({ mode: "read_write", dangerRules: [] });
    click(modeButton("完全放行"));
    await flushUntil(() => mocks.ask.mock.calls.length > 0);
    await Promise.resolve();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const [message, options] = mocks.ask.mock.calls[0] as [string, { title?: string; kind?: string }];
    expect(message).toContain("完全放行");
    expect(message).toContain("高危");
    expect(message).toContain("在所有未单独设置模式的主机上");
    expect(options).toMatchObject({ kind: "warning" });
    expect(switchMode).not.toHaveBeenCalled();
    expect(modeButton("读写").getAttribute("aria-checked")).toBe("true");
    expect(modeButton("完全放行").getAttribute("aria-checked")).toBe("false");
  });

  it("switches after the confirmation is accepted, naming the current host for a per-host mode", async () => {
    mocks.ask.mockResolvedValue(true);
    const switchMode = renderPanels({ mode: "silent", dangerRules: [] }, "session-1");
    click(modeButton("完全放行"));
    await flushUntil(() => switchMode.mock.calls.length > 0);
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("在这台主机上");
    expect(switchMode).toHaveBeenCalledWith("full_access");
  });

  it("does not ask for the other modes", async () => {
    const switchMode = renderPanels({ mode: "full_access", dangerRules: [] });
    expect(document.body.textContent).toContain("命中拦截规则仍需确认");
    click(modeButton("自动执行"));
    await flushUntil(() => switchMode.mock.calls.length > 0);
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(switchMode).toHaveBeenCalledWith("silent");
  });

  it("makes a host's full access the global default after confirming for every host", async () => {
    mocks.ask.mockResolvedValue(true);
    const applyModeGlobally = vi.fn();
    const switchMode = renderPanels({ mode: "full_access", dangerRules: [] }, "session-1", applyModeGlobally);
    const button = [...document.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.textContent === "设为全局默认");
    if (!button) throw new Error("global default button not found");
    click(button);
    await flushUntil(() => applyModeGlobally.mock.calls.length > 0);
    expect(String(mocks.ask.mock.calls[0]?.[0])).toContain("在所有未单独设置模式的主机上");
    expect(applyModeGlobally).toHaveBeenCalledWith("full_access");
    expect(switchMode).not.toHaveBeenCalled();
  });

  it("offers the global default only for a per-host mode", () => {
    renderPanels({ mode: "full_access", dangerRules: [] });
    expect([...document.querySelectorAll("button")].some((item) => item.textContent === "设为全局默认")).toBe(false);
  });
});
