/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  mergeWithDefaults,
  registerAccountPreferenceStore,
  getAccountPreferenceStore,
  getAccountOverrides,
  type AccountPreferenceStore,
  type AccountPreferenceView,
} from "../app/preferences";
import {
  getKeybinding,
  loadKeybindings,
  resetAllKeybindings,
  resetKeybinding,
  setKeybinding,
} from "../app/keybindings";

const prefsMocks = vi.hoisted(() => ({
  get: vi.fn(),
  put: vi.fn(),
}));

const authApiMocks = vi.hoisted(() => ({
  register: vi.fn(),
  init: vi.fn(),
  changePassword: vi.fn(),
  me: vi.fn(),
  login: vi.fn(),
  logout: vi.fn(),
  dekGet: vi.fn(),
  dekUpload: vi.fn(),
  status: vi.fn(),
  recoveryReset: vi.fn(),
}));

vi.mock("../ipc/authApi", async (importOriginal) => {
  const original = await importOriginal<typeof import("../ipc/authApi")>();
  return {
    ...original,
    authApi: authApiMocks,
    preferencesApi: {
      get: prefsMocks.get,
      put: prefsMocks.put,
    },
  };
});

const DEMO_USER = {
  id: "u-1",
  username: "alice",
  display_name: "Alice",
  role: "user" as const,
  state: "active" as const,
  must_change_password: false,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1,
};

// memoryStore 以线上扁平键形状模拟 M140 /auth/preferences 存储。
function memoryStore(initial: Record<string, unknown> = {}, defaults: Record<string, unknown> = {}) {
  const data = { ...initial };
  const store = {
    data,
    puts: [] as Record<string, unknown>[],
    clears: [] as string[][],
    async getView(): Promise<AccountPreferenceView> {
      return { defaults: { ...defaults }, overrides: { ...data } };
    },
    async putOverrides(set: Record<string, unknown>) {
      store.puts.push(set);
      Object.assign(data, set);
    },
    async deleteOverrides(keys: string[]) {
      store.clears.push(keys);
      for (const key of keys) delete data[key];
    },
  };
  return store;
}

beforeEach(() => {
  window.localStorage.clear();
  loadKeybindings();
});

afterEach(() => {
  registerAccountPreferenceStore(null);
  window.localStorage.clear();
  loadKeybindings();
  vi.clearAllMocks();
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

describe("账号级键位覆盖(M140 扁平白名单键)", () => {
  it("未注册后端时只有设备本地值,不冒充账号覆盖", () => {
    expect(getAccountPreferenceStore()).toBeNull();
    expect(getKeybinding("newTerminal")).toBe("Mod+t");
  });

  it("注册后端后账号扁平覆盖生效,写回持久化为单个扁平键", async () => {
    const store = memoryStore({ "keybinding.newTerminal": "Mod+Shift+t" });
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t"));

    setKeybinding("closeTab", "Mod+Shift+w");
    await vi.waitFor(() => expect(store.data["keybinding.closeTab"]).toBe("Mod+Shift+w"));
    expect(store.puts).toEqual([{ "keybinding.closeTab": "Mod+Shift+w" }]);
  });

  it("连续改键保留已加载的账号快照,设备本地层只含本机改过的键", async () => {
    const store = memoryStore({
      "keybinding.newTerminal": "Mod+Shift+t",
      "keybinding.closeTab": "Mod+Shift+w",
    });
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t"));

    setKeybinding("globalSearch", "Mod+Shift+g");
    setKeybinding("toggleSplit", "Mod+Shift+\\");
    // 账号快照不被后续改键冲掉,账号存储里的其他键也不被整表覆盖
    expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t");
    expect(getKeybinding("closeTab")).toBe("Mod+Shift+w");
    expect(store.data["keybinding.newTerminal"]).toBe("Mod+Shift+t");
    expect(store.data["keybinding.closeTab"]).toBe("Mod+Shift+w");
    expect(store.puts).toEqual([{ "keybinding.globalSearch": "Mod+Shift+g" }, { "keybinding.toggleSplit": "Mod+Shift+\\" }]);
    expect(JSON.parse(window.localStorage.getItem("nexterm.keybindings.v1")!)).toEqual({
      globalSearch: "Mod+Shift+g",
      toggleSplit: "Mod+Shift+\\",
    });
  });

  it("改回默认值时清除账号覆盖", async () => {
    const store = memoryStore({ "keybinding.newTerminal": "Mod+Shift+t" });
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t"));

    setKeybinding("newTerminal", "Mod+t");
    expect(getKeybinding("newTerminal")).toBe("Mod+t");
    expect(store.data["keybinding.newTerminal"]).toBeUndefined();
    expect(store.clears).toContainEqual(["keybinding.newTerminal"]);
  });

  it("resetKeybinding 清除对应账号覆盖并回默认", async () => {
    const store = memoryStore({ "keybinding.closeTab": "Mod+Shift+w" });
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("closeTab")).toBe("Mod+Shift+w"));

    resetKeybinding("closeTab");
    expect(getKeybinding("closeTab")).toBe("Mod+w");
    expect(store.data["keybinding.closeTab"]).toBeUndefined();
    expect(store.clears).toContainEqual(["keybinding.closeTab"]);
  });

  it("resetAllKeybindings 清除全部账号键位覆盖并回默认", async () => {
    const store = memoryStore({
      "keybinding.closeTab": "Mod+Shift+w",
      "keybinding.newTerminal": "Mod+Shift+t",
    });
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("closeTab")).toBe("Mod+Shift+w"));
    setKeybinding("globalSearch", "Mod+Shift+g");

    resetAllKeybindings();
    expect(getKeybinding("closeTab")).toBe("Mod+w");
    expect(getKeybinding("newTerminal")).toBe("Mod+t");
    expect(getKeybinding("globalSearch")).toBe("Mod+k");
    expect(store.data).toEqual({});
    expect(store.clears.flat().sort()).toEqual([
      "keybinding.closeTab",
      "keybinding.globalSearch",
      "keybinding.newTerminal",
    ]);
  });

  it("设备本地值与账号覆盖并存时账号优先", async () => {
    window.localStorage.setItem("nexterm.keybindings.v1", JSON.stringify({ newTerminal: "Mod+y" }));
    loadKeybindings();
    expect(getKeybinding("newTerminal")).toBe("Mod+y");

    const store = memoryStore({ "keybinding.newTerminal": "Mod+Shift+t" });
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t"));
  });
});

describe("键位快照的服务端全局默认层(内置 < 服务端默认 < 本地 < 账号覆盖)", () => {
  it("仅有服务端默认时生效,未声明的键仍回内置默认", async () => {
    const store = memoryStore({}, { "keybinding.newTerminal": "Mod+Shift+t" });
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t"));
    expect(getKeybinding("closeTab")).toBe("Mod+w");
  });

  it("服务端默认 < 设备本地 < 账号覆盖 逐层优先", async () => {
    window.localStorage.setItem(
      "nexterm.keybindings.v1",
      JSON.stringify({ newTerminal: "Mod+y", globalSearch: "Mod+Shift+j" }),
    );
    loadKeybindings();
    const store = memoryStore(
      { "keybinding.newTerminal": "Mod+Shift+t" },
      { "keybinding.newTerminal": "Mod+Shift+g", "keybinding.globalSearch": "Mod+Shift+k" },
    );
    registerAccountPreferenceStore(store);
    // 账号覆盖胜本地与服务端默认
    await vi.waitFor(() => expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t"));
    // 设备本地胜服务端默认
    await vi.waitFor(() => expect(getKeybinding("globalSearch")).toBe("Mod+Shift+j"));
    // 未声明的键回内置默认
    expect(getKeybinding("toggleSplit")).toBe("Mod+\\");
  });

  it("resetKeybinding 清账号覆盖后落到服务端默认而不是内置默认", async () => {
    const store = memoryStore(
      { "keybinding.newTerminal": "Mod+Shift+t" },
      { "keybinding.newTerminal": "Mod+Shift+g" },
    );
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t"));

    resetKeybinding("newTerminal");
    expect(getKeybinding("newTerminal")).toBe("Mod+Shift+g");
    expect(store.clears).toContainEqual(["keybinding.newTerminal"]);
  });

  it("resetAllKeybindings 清本地与账号覆盖,保留服务端默认层", async () => {
    const store = memoryStore(
      { "keybinding.closeTab": "Mod+Shift+w" },
      { "keybinding.closeTab": "Mod+Shift+c", "keybinding.newTerminal": "Mod+Shift+g" },
    );
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("closeTab")).toBe("Mod+Shift+w"));
    setKeybinding("globalSearch", "Mod+Shift+j");

    resetAllKeybindings();
    expect(getKeybinding("closeTab")).toBe("Mod+Shift+c");
    expect(getKeybinding("newTerminal")).toBe("Mod+Shift+g");
    expect(getKeybinding("globalSearch")).toBe("Mod+k");
    expect(store.clears.flat().sort()).toEqual(["keybinding.closeTab", "keybinding.globalSearch"]);
  });

  it("syncNow 走服务端默认与账号覆盖(线上白名单键)", async () => {
    const store = memoryStore(
      { "keybinding.syncNow": "Mod+Shift+y" },
      { "keybinding.commandPalette": "Mod+Shift+p" },
    );
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("syncNow")).toBe("Mod+Shift+y"));
    await vi.waitFor(() => expect(getKeybinding("commandPalette")).toBe("Mod+Shift+p"));

    setKeybinding("syncNow", "Mod+Shift+n");
    await vi.waitFor(() => expect(store.data["keybinding.syncNow"]).toBe("Mod+Shift+n"));
    expect(store.puts).toContainEqual({ "keybinding.syncNow": "Mod+Shift+n" });
  });
});

describe("生产链路:HTTP 偏好存储的线上扁平契约", () => {
  it("注册后按真实形状加载 defaults/overrides 并重建快照(继承超管全局默认)", async () => {
    prefsMocks.get.mockResolvedValue({
      defaults: { "appearance.terminalFontSize": 16, "input.selectionAutoCopy": true },
      overrides: { "keybinding.newTerminal": "Mod+Shift+t", "appearance.terminalTheme": "light" },
      effective: {},
    });
    prefsMocks.put.mockResolvedValue({ defaults: {}, overrides: {}, effective: {} });

    const { registerAccountPreferenceStore, createHttpPreferenceStore, getInputPrefs, getAppearancePrefs } = await import("../app/preferences");
    registerAccountPreferenceStore(createHttpPreferenceStore());
    await vi.waitFor(() => expect(prefsMocks.get).toHaveBeenCalled());

    // 服务端全局默认被继承
    await vi.waitFor(() => expect(getInputPrefs().selectionAutoCopy).toBe(true));
    await vi.waitFor(() => expect(getAppearancePrefs().terminalFontSize).toBe(16));
    // 账号覆盖优先于全局默认与内置默认
    await vi.waitFor(() => expect(getAppearancePrefs().terminalTheme).toBe("light"));
    await vi.waitFor(() => expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t"));
  });

  it("写回与清除走线上扁平键,reset 后回默认", async () => {
    prefsMocks.get.mockResolvedValue({ defaults: {}, overrides: {}, effective: {} });
    prefsMocks.put.mockResolvedValue({ defaults: {}, overrides: {}, effective: {} });

    const { registerAccountPreferenceStore, createHttpPreferenceStore, setSelectionAutoCopy, setTerminalTheme, resetAppearancePrefs, getAppearancePrefs } = await import("../app/preferences");
    registerAccountPreferenceStore(createHttpPreferenceStore());
    await vi.waitFor(() => expect(prefsMocks.get).toHaveBeenCalled());

    setSelectionAutoCopy(true);
    await vi.waitFor(() => expect(prefsMocks.put).toHaveBeenCalledWith({ set: { "input.selectionAutoCopy": true } }));

    setTerminalTheme("light");
    await vi.waitFor(() => expect(prefsMocks.put).toHaveBeenCalledWith({ set: { "appearance.terminalTheme": "light" } }));

    resetAppearancePrefs();
    await vi.waitFor(() =>
      expect(prefsMocks.put).toHaveBeenCalledWith({
        clear: ["appearance.uiFontPreset", "appearance.uiFontScale", "appearance.terminalFontSize", "appearance.terminalTheme"],
      }),
    );
    // 清除后回默认,不残留已清掉的账号覆盖
    expect(getAppearancePrefs().terminalTheme).toBe("dark");
  });

  it("账号切换时旧账号晚到的响应不覆盖新状态(代次保护)", async () => {
    let resolveA: ((view: AccountPreferenceView) => void) | undefined;
    const storeA: AccountPreferenceStore = {
      getView: () => new Promise((resolve) => {
        resolveA = resolve;
      }),
      putOverrides: async () => undefined,
      deleteOverrides: async () => undefined,
    };
    const storeB = memoryStore({});
    registerAccountPreferenceStore(storeA);
    registerAccountPreferenceStore(storeB);
    await vi.waitFor(() => expect(getAccountPreferenceStore()).toBe(storeB));

    resolveA?.({ defaults: {}, overrides: { "input.selectionAutoCopy": true } });
    await new Promise((resolve) => setTimeout(resolve, 0));
    const { getInputPrefs } = await import("../app/preferences");
    expect(getInputPrefs().selectionAutoCopy).toBe(false);
    expect(getAccountOverrides()).toEqual({});
  });
});

describe("auth store 偏好存储生命周期", () => {
  it("register 后注册偏好存储,logout 注销", async () => {
    const { useAuth } = await import("../features/auth/store");
    authApiMocks.register.mockResolvedValue({ user: DEMO_USER });
    authApiMocks.logout.mockResolvedValue({ ok: true });

    await useAuth.getState().register("alice", "pw", "Alice");
    expect(useAuth.getState().user).not.toBeNull();
    await vi.waitFor(() => expect(getAccountPreferenceStore()).not.toBeNull());

    await useAuth.getState().logout();
    expect(getAccountPreferenceStore()).toBeNull();
    expect(useAuth.getState().user).toBeNull();
  });

  it("401 会话过期时注销偏好存储", async () => {
    const { useAuth } = await import("../features/auth/store");
    const { SESSION_EXPIRED_EVENT } = await import("../ipc/authApi");
    useAuth.setState({ user: DEMO_USER, dek: null, gate: "ready", pendingRecoveryKey: null, error: null });
    const { registerAccountPreferenceStore, createHttpPreferenceStore } = await import("../app/preferences");
    registerAccountPreferenceStore(createHttpPreferenceStore());
    expect(getAccountPreferenceStore()).not.toBeNull();

    window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
    expect(getAccountPreferenceStore()).toBeNull();
    expect(useAuth.getState().user).toBeNull();
  });
});
