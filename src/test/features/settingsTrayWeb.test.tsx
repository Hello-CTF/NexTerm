/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, flush, mountWithClient, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    getPermission: vi.fn(),
    vaultStatus: vi.fn(),
    trayGet: vi.fn(),
    traySet: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    aiApi: { getPermission: mocks.getPermission, setPermission: vi.fn(), testProvider: vi.fn() },
    vaultApi: {
      status: mocks.vaultStatus,
      initDpapi: vi.fn(),
      initMaster: vi.fn(),
      unlock: vi.fn(),
      changePassword: vi.fn(),
      setAutoLock: vi.fn(),
    },
    desktopTrayApi: { get: mocks.trayGet, set: mocks.traySet },
    assetApi: { auditQuery: vi.fn().mockResolvedValue([]) },
  };
});
vi.mock("../../ui/dialogs", () => ({ ask: vi.fn(), promptText: vi.fn() }));
vi.mock("../../features/settings/MemoryCard", () => ({ MemoryCard: () => null }));
vi.mock("../../features/settings/KnownHostsCard", () => ({ KnownHostsCard: () => null }));
vi.mock("../../features/settings/SyncCard", () => ({ SyncCard: () => null }));
vi.mock("../../features/ai/ModelPanel", () => ({ ModelManager: () => null }));

import { SettingsView } from "../../features/settings/SettingsView";
import { setWindowsPlatform } from "../../app/platform";
import { getTerminalPrefs, registerAccountPreferenceStore } from "../../app/preferences";
import { useUi } from "../../app/store";

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  registerAccountPreferenceStore(null);
  setWindowsPlatform(true);
  mocks.getPermission.mockResolvedValue({ mode: "read_write", dangerRules: [] });
  mocks.vaultStatus.mockResolvedValue({ initialized: true, mode: "dpapi", unlocked: true, autoLockMinutes: 0 });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  setWindowsPlatform(false);
  registerAccountPreferenceStore(null);
});

describe("web tray settings platform gate", () => {
  it("does not render or invoke desktop-only tray commands", async () => {
    mounted = mountWithClient(createElement(SettingsView));
    await flush();
    await flush();

    expect(mounted.container.textContent).not.toContain("关闭时保留后台运行");
    expect(mocks.trayGet).not.toHaveBeenCalled();
    expect(mocks.traySet).not.toHaveBeenCalled();
  });
});

describe("default terminal mode settings", () => {
  it("saves the selected default and renders the effective account value", async () => {
    const put = vi.fn().mockResolvedValue(undefined);
    registerAccountPreferenceStore({ getView: async () => ({ defaults: {}, overrides: { "terminal.defaultMode": "durable" } }), putOverrides: put, deleteOverrides: async () => {} });
    mounted = mountWithClient(createElement(SettingsView));
    await flush();
    const group = mounted.container.querySelector('[aria-label="默认终端模式"]');
    const regular = [...(group?.querySelectorAll("button") ?? [])].find((button) => button.textContent === "普通终端");
    if (!regular) throw new Error("Regular terminal option not found");
    click(regular);
    await waitFor(() => expect(regular.getAttribute("aria-pressed")).toBe("true"));

    expect(put).toHaveBeenCalledWith({ "terminal.defaultMode": "regular" });
    expect(getTerminalPrefs().defaultMode).toBe("regular");
  });

  it("reports a failed save and keeps the previous mode selected", async () => {
    const toast = vi.fn();
    useUi.setState({ pushToast: toast });
    registerAccountPreferenceStore({ getView: async () => ({ defaults: {}, overrides: { "terminal.defaultMode": "durable" } }), putOverrides: async () => { throw new Error("保存失败"); }, deleteOverrides: async () => {} });
    mounted = mountWithClient(createElement(SettingsView));
    await flush();
    const group = mounted.container.querySelector('[aria-label="默认终端模式"]');
    const regular = [...(group?.querySelectorAll("button") ?? [])].find((button) => button.textContent === "普通终端");
    if (!regular) throw new Error("Regular terminal option not found");
    click(regular);
    await waitFor(() => expect(toast).toHaveBeenCalledWith("error", "保存默认终端模式失败：保存失败"));

    expect(regular.getAttribute("aria-pressed")).toBe("false");
    expect(getTerminalPrefs().defaultMode).toBe("durable");
  });
});

describe("terminal GPU acceleration settings", () => {
  it("saves the selected mode to the account and updates the hint", async () => {
    const put = vi.fn().mockResolvedValue(undefined);
    registerAccountPreferenceStore({ getView: async () => ({ defaults: {}, overrides: {} }), putOverrides: put, deleteOverrides: async () => {} });
    mounted = mountWithClient(createElement(SettingsView));
    await flush();
    const group = mounted.container.querySelector('[aria-label="终端 GPU 加速"]');
    const buttons = [...(group?.querySelectorAll("button") ?? [])];
    expect(buttons.find((button) => button.textContent === "自动")?.getAttribute("aria-pressed")).toBe("true");
    const off = buttons.find((button) => button.textContent === "关闭");
    if (!off) throw new Error("GPU off option not found");
    click(off);
    await waitFor(() => expect(off.getAttribute("aria-pressed")).toBe("true"));

    expect(put).toHaveBeenCalledWith({ "terminal.gpuAcceleration": "off" });
    expect(getTerminalPrefs().gpuAcceleration).toBe("off");
    expect(mounted.container.textContent).toContain("始终使用兼容渲染");
  });
});

describe("terminal link activation settings", () => {
  it("defaults to modifier-click and saves plain click to the account", async () => {
    const put = vi.fn().mockResolvedValue(undefined);
    registerAccountPreferenceStore({ getView: async () => ({ defaults: {}, overrides: {} }), putOverrides: put, deleteOverrides: async () => {} });
    mounted = mountWithClient(createElement(SettingsView));
    await flush();
    const group = mounted.container.querySelector('[aria-label="链接打开方式"]');
    const buttons = [...(group?.querySelectorAll("button") ?? [])];
    expect(buttons.find((button) => button.textContent === "Ctrl/Cmd+单击")?.getAttribute("aria-pressed")).toBe("true");
    const plain = buttons.find((button) => button.textContent === "单击");
    if (!plain) throw new Error("plain click option not found");
    click(plain);
    await waitFor(() => expect(plain.getAttribute("aria-pressed")).toBe("true"));

    expect(put).toHaveBeenCalledWith({ "terminal.linkActivation": "click" });
    expect(getTerminalPrefs().linkActivation).toBe("click");
    expect(mounted.container.textContent).toContain("单击终端中的网址或 OSC 8 超链接即打开");
  });
});

describe("multi-line paste confirmation setting", () => {
  it("shows the account mode and saves the chosen mode to the account", async () => {
    const put = vi.fn().mockResolvedValue(undefined);
    registerAccountPreferenceStore({
      getView: async () => ({ defaults: {}, overrides: { "terminal.confirmMultilinePaste": "never" } }),
      putOverrides: put,
      deleteOverrides: async () => {},
    });
    mounted = mountWithClient(createElement(SettingsView));
    await flush();
    const label = [...mounted.container.querySelectorAll("label")].find((node) => node.textContent?.trim() === "多行粘贴确认");
    const select = label ? mounted.container.querySelector<HTMLSelectElement>(`#${CSS.escape(label.htmlFor)}`) : null;
    if (!select) throw new Error("multi-line paste select not found");
    await waitFor(() => expect(select.value).toBe("never"));
    expect([...select.options].map((option) => option.textContent)).toEqual([
      "仅在程序未开启括号粘贴时确认",
      "每次都确认",
      "从不确认",
    ]);
    expect(mounted.container.textContent).toContain("开启括号粘贴的程序（如 bash/zsh）会把多行内容当成一次粘贴，不会逐行执行");

    act(() => {
      select.value = "unbracketed";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await waitFor(() => expect(select.value).toBe("unbracketed"));
    expect(put).toHaveBeenCalledWith({ "terminal.confirmMultilinePaste": "unbracketed" });
    expect(getTerminalPrefs().confirmMultilinePaste).toBe("unbracketed");
  });
});
