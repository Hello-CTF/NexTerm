/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  call: vi.fn(),
  forwardEnv: vi.fn(),
  forwardList: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    call: mocks.call,
    forwardApi: {
      env: mocks.forwardEnv,
      list: mocks.forwardList,
      create: vi.fn(),
      createSocks: vi.fn(),
      createRemote: vi.fn(),
      remove: vi.fn(),
    },
  };
});

import { createForwardSettingsIpcApi } from "../../features/forward/forwardSettingsIpc";
import { ForwardPanel } from "../../features/forward/ForwardPanel";
import { useUi } from "../../app/store";

const settingsRule = {
  id: "rule-1",
  assetId: "asset-1",
  name: "db",
  kind: "local",
  listenHost: "127.0.0.1",
  listenPort: 13306,
  targetHost: "db.internal",
  targetPort: 5432,
  autoStart: false,
  status: "stopped",
};

const recoverySettings = { enabled: false, maxConcurrentConnections: 4, skipProduction: true };

function mockSettingsCalls(overrides: Record<string, unknown> = {}): void {
  mocks.call.mockImplementation((cmd: string, args?: Record<string, unknown>) => {
    if (cmd in overrides) {
      const value = overrides[cmd];
      return value instanceof Error ? Promise.reject(value) : Promise.resolve(value);
    }
    switch (cmd) {
      case "forward_rule_settings_list":
        return Promise.resolve([settingsRule]);
      case "forward_rule_recovery_settings_get":
        return Promise.resolve(recoverySettings);
      case "forward_rule_set_auto_start":
        return Promise.resolve({ id: args?.id, autoStart: args?.autoStart });
      case "forward_rule_recovery_settings_set":
        return Promise.resolve({ ...recoverySettings, enabled: args?.enabled });
      case "forward_rule_recover_now":
        return Promise.resolve([]);
      default:
        return Promise.reject(new Error(`unexpected command: ${cmd}`));
    }
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mockSettingsCalls();
  mocks.forwardEnv.mockResolvedValue({ available: true, platform: "other", listenHost: "127.0.0.1" });
  mocks.forwardList.mockResolvedValue([]);
  useUi.setState({ pushToast: mocks.toast, sessions: [], accountEpoch: 0 });
});

let mounted: MountedView | undefined;
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("forward settings IPC adapter", () => {
  it("lists rules through the typed settings command and maps the DTO shape", async () => {
    const api = createForwardSettingsIpcApi();
    const rules = await api.listRules();
    expect(mocks.call).toHaveBeenCalledWith("forward_rule_settings_list");
    expect(rules).toEqual([settingsRule]);
  });

  it("toggles autoStart and reads/writes the recovery switch through typed commands", async () => {
    const api = createForwardSettingsIpcApi();
    await api.setRuleAutoStart("rule-1", true);
    expect(mocks.call).toHaveBeenCalledWith("forward_rule_set_auto_start", { id: "rule-1", autoStart: true });

    await expect(api.getRecoverySettings()).resolves.toEqual(recoverySettings);
    await api.setRecoverySettings({ enabled: true, maxConcurrentConnections: 4, skipProduction: true });
    expect(mocks.call).toHaveBeenCalledWith("forward_rule_recovery_settings_set", { enabled: true });
  });

  it("recoverNow stays silent for clean outcomes and deliberate skips", async () => {
    mockSettingsCalls({
      forward_rule_recover_now: [
        { assetId: "asset-1", sessionId: "s1", started: ["rule-1"] },
        { assetId: "asset-prod", skipped: "生产环境资产不参与启动恢复" },
      ],
    });
    const api = createForwardSettingsIpcApi();
    await expect(api.recoverNow()).resolves.toBeUndefined();
  });

  it("recoverNow reports device failures instead of swallowing them", async () => {
    mockSettingsCalls({
      forward_rule_recover_now: [
        { assetId: "asset-1", error: "恢复设备连接失败: host key pending" },
        { assetId: "asset-2", sessionId: "s2", started: ["rule-2"] },
      ],
    });
    const api = createForwardSettingsIpcApi();
    await expect(api.recoverNow()).rejects.toThrow("asset-1: 恢复设备连接失败: host key pending");
  });
});

describe("ForwardPanel settings section", () => {
  function mountForwardPanel(sessionId?: string): void {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mounted = mount(
      createElement(QueryClientProvider, { client }, createElement(ForwardPanel, { sessionId })),
    );
  }

  function settingsSwitch(label: string): HTMLButtonElement {
    const button = mounted!.container.querySelector<HTMLButtonElement>(`button[role="switch"][aria-label="${label}"]`);
    if (!button) throw new Error(`settings switch not found: ${label}`);
    return button;
  }

  it("loads rules through the real adapter and toggles autoStart", async () => {
    mountForwardPanel();
    await waitFor(() => expect(settingsSwitch("自动启动 db").getAttribute("aria-checked")).toBe("false"));

    click(settingsSwitch("自动启动 db"));
    await waitFor(() =>
      expect(mocks.call).toHaveBeenCalledWith("forward_rule_set_auto_start", { id: "rule-1", autoStart: true }),
    );
    await waitFor(() => expect(settingsSwitch("自动启动 db").getAttribute("aria-checked")).toBe("true"));
  });

  it("surfaces a recover-now failure in the panel instead of claiming success", async () => {
    mockSettingsCalls({
      forward_rule_recovery_settings_get: { ...recoverySettings, enabled: true },
      forward_rule_recover_now: [{ assetId: "asset-1", error: "恢复设备连接失败: dial refused" }],
    });
    mountForwardPanel();
    await waitFor(() => expect(settingsSwitch("启动时自动恢复转发").getAttribute("aria-checked")).toBe("true"));

    click([...mounted!.container.querySelectorAll("button")].find((b) => b.textContent?.trim() === "立即恢复")!);
    await waitFor(() => expect(mocks.call).toHaveBeenCalledWith("forward_rule_recover_now"));
    await waitFor(() => {
      const alert = mounted!.container.querySelector('[role="alert"]');
      expect(alert?.textContent).toContain("asset-1: 恢复设备连接失败: dial refused");
    });
  });

  it("reloads rules from scratch when the account generation advances", async () => {
    mountForwardPanel();
    await waitFor(() => expect(settingsSwitch("自动启动 db")).toBeDefined());
    const listCalls = () =>
      mocks.call.mock.calls.filter(([cmd]) => cmd === "forward_rule_settings_list").length;
    const before = listCalls();

    act(() => {
      useUi.getState().advanceAccountEpoch();
    });
    await waitFor(() => expect(listCalls()).toBeGreaterThan(before));
    await waitFor(() => expect(settingsSwitch("自动启动 db").getAttribute("aria-checked")).toBe("false"));
  });

  it("keeps the app-wide settings section out of per-session forward tabs", async () => {
    mountForwardPanel("s");
    await flush();
    expect(mounted!.container.querySelector('section[aria-label="转发设置"]')).toBeNull();
    expect(mocks.call).not.toHaveBeenCalledWith("forward_rule_settings_list");
  });
});
