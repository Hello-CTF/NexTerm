/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  mergeWithDefaults,
  registerAccountPreferenceStore,
  getAccountPreferenceStore,
  type AccountPreferenceStore,
} from "../app/preferences";
import {
  applyAccountKeybindingOverrides,
  getKeybinding,
  loadKeybindings,
  setKeybinding,
} from "../app/keybindings";

const prefsMocks = vi.hoisted(() => ({
  get: vi.fn(),
  put: vi.fn(),
}));

vi.mock("../ipc/authApi", async (importOriginal) => {
  const original = await importOriginal<typeof import("../ipc/authApi")>();
  return {
    ...original,
    preferencesApi: {
      get: prefsMocks.get,
      put: prefsMocks.put,
    },
  };
});

function memoryStore(initial: Record<string, unknown> = {}): AccountPreferenceStore & { data: Record<string, unknown> } {
  const data = { ...initial };
  return {
    data,
    async getOverrides() {
      return { ...data };
    },
    async putOverride(key, value) {
      data[key] = value;
    },
    async deleteOverride(key) {
      delete data[key];
    },
  };
}

beforeEach(() => {
  window.localStorage.clear();
  loadKeybindings();
});

afterEach(() => {
  registerAccountPreferenceStore(null);
  window.localStorage.clear();
  loadKeybindings();
});

describe("mergeWithDefaults", () => {
  it("账号覆盖优先于设备本地值,缺省回退到安全默认", () => {
    const defaults = { osc52: false, fontSize: 13 };
    expect(mergeWithDefaults(defaults)).toEqual({ osc52: false, fontSize: 13 });
    expect(mergeWithDefaults(defaults, { osc52: true })).toEqual({ osc52: true, fontSize: 13 });
    expect(mergeWithDefaults(defaults, { osc52: true }, { fontSize: 15 })).toEqual({ osc52: true, fontSize: 15 });
    expect(mergeWithDefaults(defaults, { osc52: true }, { osc52: false })).toEqual({ osc52: false, fontSize: 13 });
  });
});

describe("账号级键位覆盖(M140 后端接口)", () => {
  it("未注册后端时只有设备本地值,不冒充账号覆盖", () => {
    expect(getAccountPreferenceStore()).toBeNull();
    expect(getKeybinding("newTerminal")).toBe("Mod+t");
  });

  it("注册后端后账号覆盖生效,写回持久化到账号存储", async () => {
    const store = memoryStore({ "keybindings.overrides": { newTerminal: "Mod+Shift+t" } });
    registerAccountPreferenceStore(store);
    // M140 后端加载覆盖后回填,键位快照按账号覆盖重建
    applyAccountKeybindingOverrides(store.data["keybindings.overrides"]);
    expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t");

    setKeybinding("closeTab", "Mod+Shift+w");
    await vi.waitFor(() => {
      expect(store.data["keybindings.overrides"]).toMatchObject({ closeTab: "Mod+Shift+w" });
    });
  });

  it("设备本地值与账号覆盖并存时账号优先", () => {
    window.localStorage.setItem("nexterm.keybindings.v1", JSON.stringify({ newTerminal: "Mod+y" }));
    loadKeybindings();
    expect(getKeybinding("newTerminal")).toBe("Mod+y");

    const store = memoryStore({ "keybindings.overrides": { newTerminal: "Mod+Shift+t" } });
    registerAccountPreferenceStore(store);
    applyAccountKeybindingOverrides(store.data["keybindings.overrides"]);
    expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t");
  });
});

describe("生产链路:登录注册偏好存储并加载快照", () => {
  it("登录后注册存储,加载覆盖重建 input/appearance/keybindings 快照", async () => {
    const { useAuth } = await import("../features/auth/store");
    const { getInputPrefs, getAppearancePrefs } = await import("../app/preferences");
    prefsMocks.get.mockResolvedValue({
      defaults: {},
      overrides: {
        inputPrefs: { selectionAutoCopy: true },
        appearancePrefs: { uiFontPreset: 14.5, uiFontScale: 1, terminalFontSize: 13, terminalTheme: "dark" },
        "keybindings.overrides": { newTerminal: "Mod+Shift+t" },
      },
      effective: {},
    });
    prefsMocks.put.mockResolvedValue({ defaults: {}, overrides: {}, effective: {} });

    useAuth.setState({
      status: { initialized: true, registration_open: false, auth: "on" },
      user: { id: "u-1", username: "alice", display_name: "", role: "user", state: "active", must_change_password: false, created_at: 1, updated_at: 1, last_login_at: 1 },
      dek: null,
      gate: "ready",
      pendingRecoveryKey: null,
      error: null,
    });

    // 走登录后的注册路径(与 store.login 相同)
    const { registerAccountPreferenceStore } = await import("../app/preferences");
    const { createHttpPreferenceStore } = await import("../app/preferences");
    registerAccountPreferenceStore(createHttpPreferenceStore());

    await vi.waitFor(() => expect(getAccountPreferenceStore()).not.toBeNull());
    await vi.waitFor(() => expect(prefsMocks.get).toHaveBeenCalled());

    // 快照按账号覆盖重建
    await vi.waitFor(() => expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t"));
    await vi.waitFor(() => expect(getInputPrefs().selectionAutoCopy).toBe(true));
    await vi.waitFor(() => expect(getAppearancePrefs().uiFontPreset).toBe(14.5));
  });

  it("写回持久化到账号存储,清除覆盖回默认", async () => {
    const { setSelectionAutoCopy } = await import("../app/preferences");
    prefsMocks.get.mockResolvedValue({ defaults: {}, overrides: {}, effective: {} });
    prefsMocks.put.mockResolvedValue({ defaults: {}, overrides: {}, effective: {} });

    const { registerAccountPreferenceStore, createHttpPreferenceStore } = await import("../app/preferences");
    registerAccountPreferenceStore(createHttpPreferenceStore());
    await vi.waitFor(() => expect(prefsMocks.get).toHaveBeenCalled());

    setSelectionAutoCopy(true);
    await vi.waitFor(() => expect(prefsMocks.put).toHaveBeenCalledWith({ set: { inputPrefs: { selectionAutoCopy: true } } }));

    const store = getAccountPreferenceStore();
    expect(store).not.toBeNull();
    await store!.deleteOverride("inputPrefs");
    await vi.waitFor(() => expect(prefsMocks.put).toHaveBeenCalledWith({ clear: ["inputPrefs"] }));
  });
});
