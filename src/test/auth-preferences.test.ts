/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  mergeWithDefaults,
  registerAccountPreferenceStore,
  getAccountPreferenceStore,
  getAccountOverrides,
  getTerminalPrefs,
  setTerminalDefaultMode,
  setTerminalCloseAction,
  setTerminalGpuAcceleration,
  setTerminalLinkActivation,
  subscribeTerminalPrefs,
  type AccountPreferenceStore,
  type AccountPreferenceView,
} from "../app/preferences";
import {
  getKeybinding,
  loadKeybindings,
  resetAllKeybindings,
  resetKeybinding,
  setKeybinding,
  takeKeybindingMigrationNotice,
} from "../app/keybindings";
import { flushMacrotask } from "./features/reactTestUtils";

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

// The preference-store lifecycle tests exercise register/logout state
// transitions, not the KDF: wrapDEKWithPassword derives the KEK with
// production Argon2id params (t=3, m=64MiB, p=4), which costs ~27s per call
// and blows the test timeout under load. The KDF itself is covered against
// the Go vectors in auth-crypto.test.ts, so stub the envelope wrap here.
vi.mock("../features/auth/crypto", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../features/auth/crypto")>();
  return {
    ...actual,
    wrapDEKWithPassword: vi.fn(async () => ({
      envelope: new Uint8Array(28),
      salt: new Uint8Array(16),
      params: JSON.stringify({ t: 1, m: 8, p: 1 }),
    })),
  };
});

const DEMO_USER = {
  id: "u-1",
  username: "alice",
  display_name: "Alice",
  role: "user" as const,
  state: "active" as const,
  must_change_password: false,
  mfa_enabled: false,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1,
};

// memoryStore simulates the M140 /auth/preferences storage with the production flat-key shape.
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
  it("prioritizes account overrides over device-local values and falls back to safe defaults", () => {
    const defaults = { osc52: false, fontSize: 13 };
    expect(mergeWithDefaults(defaults)).toEqual({ osc52: false, fontSize: 13 });
    expect(mergeWithDefaults(defaults, { osc52: true })).toEqual({ osc52: true, fontSize: 13 });
    expect(mergeWithDefaults(defaults, { osc52: true }, { fontSize: 15 })).toEqual({ osc52: true, fontSize: 15 });
    expect(mergeWithDefaults(defaults, { osc52: true }, { osc52: false })).toEqual({ osc52: false, fontSize: 13 });
  });
});

describe("account-level keybinding overrides (M140 flat allowlisted keys)", () => {
  it("uses only device-local values without a registered backend and does not invent account overrides", () => {
    expect(getAccountPreferenceStore()).toBeNull();
    expect(getKeybinding("newTerminal")).toBe("Mod+t");
  });

  it("applies flat account overrides after registration and persists writes as individual flat keys", async () => {
    const store = memoryStore({ "keybinding.newTerminal": "Mod+Shift+t" });
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t"));

    setKeybinding("closeTab", "Mod+Shift+w");
    await vi.waitFor(() => expect(store.data["keybinding.closeTab"]).toBe("Mod+Shift+w"));
    expect(store.puts).toEqual([{ "keybinding.closeTab": "Mod+Shift+w" }]);
  });

  it("keeps the loaded account snapshot across key changes and stores only locally changed keys on the device", async () => {
    const store = memoryStore({
      "keybinding.newTerminal": "Mod+Shift+t",
      "keybinding.closeTab": "Mod+Shift+w",
    });
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t"));

    setKeybinding("quickConnect", "Mod+Shift+g");
    setKeybinding("splitRight", "Mod+Alt+\\");
    // The account snapshot is not wiped by later key changes, and other keys in the account store are not overwritten wholesale.
    expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t");
    expect(getKeybinding("closeTab")).toBe("Mod+Shift+w");
    expect(store.data["keybinding.newTerminal"]).toBe("Mod+Shift+t");
    expect(store.data["keybinding.closeTab"]).toBe("Mod+Shift+w");
    expect(store.puts).toEqual([{ "keybinding.quickConnect": "Mod+Shift+g" }, { "keybinding.splitRight": "Mod+Alt+\\" }]);
    expect(JSON.parse(window.localStorage.getItem("nexterm.keybindings.v1")!)).toEqual({
      quickConnect: "Mod+Shift+g",
      splitRight: "Mod+Alt+\\",
    });
  });

  it("clears the account override when a key is restored to its default", async () => {
    const store = memoryStore({ "keybinding.newTerminal": "Mod+Shift+t" });
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t"));

    setKeybinding("newTerminal", "Mod+t");
    expect(getKeybinding("newTerminal")).toBe("Mod+t");
    expect(store.data["keybinding.newTerminal"]).toBeUndefined();
    expect(store.clears).toContainEqual(["keybinding.newTerminal"]);
  });

  it("clears the matching account override and restores the default in resetKeybinding", async () => {
    const store = memoryStore({ "keybinding.closeTab": "Mod+Shift+w" });
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("closeTab")).toBe("Mod+Shift+w"));

    resetKeybinding("closeTab");
    expect(getKeybinding("closeTab")).toBe("Mod+w");
    expect(store.data["keybinding.closeTab"]).toBeUndefined();
    expect(store.clears).toContainEqual(["keybinding.closeTab"]);
  });

  it("clears all account keybinding overrides and restores defaults in resetAllKeybindings", async () => {
    const store = memoryStore({
      "keybinding.closeTab": "Mod+Shift+w",
      "keybinding.newTerminal": "Mod+Shift+t",
    });
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("closeTab")).toBe("Mod+Shift+w"));
    setKeybinding("quickConnect", "Mod+Shift+g");

    resetAllKeybindings();
    expect(getKeybinding("closeTab")).toBe("Mod+w");
    expect(getKeybinding("newTerminal")).toBe("Mod+t");
    expect(getKeybinding("quickConnect")).toBe("Mod+Shift+k");
    expect(store.data).toEqual({});
    expect(store.clears.flat().sort()).toEqual([
      "keybinding.closeTab",
      "keybinding.newTerminal",
      "keybinding.quickConnect",
    ]);
  });

  it("prioritizes account overrides when both device-local values and account overrides exist", async () => {
    window.localStorage.setItem("nexterm.keybindings.v1", JSON.stringify({ newTerminal: "Mod+y" }));
    loadKeybindings();
    expect(getKeybinding("newTerminal")).toBe("Mod+y");

    const store = memoryStore({ "keybinding.newTerminal": "Mod+Shift+t" });
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t"));
  });
});

describe("default terminal mode preferences", () => {
  it("keeps durable as the initial mode and saves an unregistered device choice", async () => {
    registerAccountPreferenceStore(null);
    expect(getTerminalPrefs().defaultMode).toBe("durable");
    await setTerminalDefaultMode("regular");
    expect(getTerminalPrefs().defaultMode).toBe("regular");
    registerAccountPreferenceStore(null);
    expect(getTerminalPrefs().defaultMode).toBe("regular");
  });

  it("follows server defaults and account overrides rather than a different device choice", async () => {
    registerAccountPreferenceStore(null);
    await setTerminalDefaultMode("regular");
    const store = memoryStore({}, { "terminal.defaultMode": "durable" });
    registerAccountPreferenceStore(store);
    await flushMacrotask();
    expect(getTerminalPrefs().defaultMode).toBe("durable");

    await setTerminalDefaultMode("regular");
    expect(store.puts).toContainEqual({ "terminal.defaultMode": "regular" });
    expect(getAccountOverrides()["terminal.defaultMode"]).toBe("regular");
    expect(getTerminalPrefs().defaultMode).toBe("regular");
  });

  it("leaves the effective choice unchanged when saving fails", async () => {
    const store = memoryStore({ "terminal.defaultMode": "durable" });
    registerAccountPreferenceStore(store);
    await flushMacrotask();
    vi.spyOn(store, "putOverrides").mockRejectedValue(new Error("storage unavailable"));

    await expect(setTerminalDefaultMode("regular")).rejects.toThrow("storage unavailable");
    expect(getTerminalPrefs().defaultMode).toBe("durable");
    expect(getAccountOverrides()["terminal.defaultMode"]).toBe("durable");
  });

  it("keeps an unregistered desktop on device preferences when sync health refreshes", async () => {
    registerAccountPreferenceStore(null);
    await setTerminalDefaultMode("regular");
    const store = memoryStore({});
    vi.spyOn(store, "getView").mockResolvedValue({ defaults: {}, overrides: {}, deviceOnly: true });
    registerAccountPreferenceStore(store);
    await flushMacrotask();
    expect(getTerminalPrefs().defaultMode).toBe("regular");
    await setTerminalDefaultMode("durable");
    expect(store.puts).toEqual([]);
    expect(getTerminalPrefs().defaultMode).toBe("durable");
  });

  it("does not copy an account terminal mode into device settings when changing the close action", async () => {
    registerAccountPreferenceStore(null);
    await setTerminalDefaultMode("regular");
    const store = memoryStore({ "terminal.defaultMode": "durable" });
    registerAccountPreferenceStore(store);
    await flushMacrotask();
    expect(getTerminalPrefs().defaultMode).toBe("durable");

    setTerminalCloseAction("kill");
    registerAccountPreferenceStore(null);
    expect(getTerminalPrefs().defaultMode).toBe("regular");
    expect(getTerminalPrefs().closeAction).toBe("kill");
  });

  it("ignores a delayed save result after switching accounts", async () => {
    const store = memoryStore({ "terminal.defaultMode": "durable" });
    registerAccountPreferenceStore(store);
    await flushMacrotask();
    let finishSave: (() => void) | undefined;
    vi.spyOn(store, "putOverrides").mockImplementation(() => new Promise<void>((resolve) => { finishSave = resolve; }));
    const saving = setTerminalDefaultMode("regular");
    registerAccountPreferenceStore(memoryStore({ "terminal.defaultMode": "durable" }));
    await flushMacrotask();
    finishSave?.();
    await saving;
    expect(getTerminalPrefs().defaultMode).toBe("durable");
    expect(getAccountOverrides()["terminal.defaultMode"]).toBe("durable");
  });
});

describe("terminal GPU acceleration preference", () => {
  afterEach(() => registerAccountPreferenceStore(null));

  it("defaults to auto and round-trips through the account store", async () => {
    registerAccountPreferenceStore(null);
    expect(getTerminalPrefs().gpuAcceleration).toBe("auto");
    const store = memoryStore({});
    registerAccountPreferenceStore(store);
    await flushMacrotask();
    const changed = vi.fn();
    const unsubscribe = subscribeTerminalPrefs(changed);
    setTerminalGpuAcceleration("off");
    setTerminalGpuAcceleration("off");
    unsubscribe();
    expect(changed).toHaveBeenCalledTimes(1);
    expect(store.puts).toEqual([{ "terminal.gpuAcceleration": "off" }]);

    // Another device reads the saved override.
    registerAccountPreferenceStore(null);
    window.localStorage.clear();
    registerAccountPreferenceStore(memoryStore({ ...store.data }));
    await flushMacrotask();
    expect(getTerminalPrefs().gpuAcceleration).toBe("off");
  });

  it("keeps a device value without an account and ignores unknown stored values", async () => {
    registerAccountPreferenceStore(null);
    setTerminalGpuAcceleration("on");
    registerAccountPreferenceStore(null);
    expect(getTerminalPrefs().gpuAcceleration).toBe("on");

    registerAccountPreferenceStore(memoryStore({ "terminal.gpuAcceleration": "webgl" }));
    await flushMacrotask();
    expect(getTerminalPrefs().gpuAcceleration).toBe("auto");
  });
});

describe("terminal link activation preference", () => {
  afterEach(() => registerAccountPreferenceStore(null));

  it("defaults to modifier-click and round-trips through the account store", async () => {
    registerAccountPreferenceStore(null);
    window.localStorage.clear();
    registerAccountPreferenceStore(null);
    expect(getTerminalPrefs().linkActivation).toBe("modifier");
    const store = memoryStore({});
    registerAccountPreferenceStore(store);
    await flushMacrotask();
    const changed = vi.fn();
    const unsubscribe = subscribeTerminalPrefs(changed);
    setTerminalLinkActivation("click");
    setTerminalLinkActivation("click");
    unsubscribe();
    expect(changed).toHaveBeenCalledTimes(1);
    expect(store.puts).toEqual([{ "terminal.linkActivation": "click" }]);

    registerAccountPreferenceStore(null);
    window.localStorage.clear();
    registerAccountPreferenceStore(memoryStore({ ...store.data }));
    await flushMacrotask();
    expect(getTerminalPrefs().linkActivation).toBe("click");
  });

  it("ignores unknown stored values", async () => {
    registerAccountPreferenceStore(memoryStore({ "terminal.linkActivation": "double" }));
    await flushMacrotask();
    expect(getTerminalPrefs().linkActivation).toBe("modifier");
  });
});

describe("account theme preferences", () => {
  it("applies account overrides without changing the device value and restores the device theme after unregistering", async () => {
    const theme = await import("../app/theme");
    theme.setThemeMode("system");
    const store = memoryStore({ "appearance.themeMode": "light" });
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(theme.getThemeMode()).toBe("light"));
    expect(window.localStorage.getItem("nexterm.theme.v1")).toBe("system");

    registerAccountPreferenceStore(null);
    expect(theme.getThemeMode()).toBe("system");
  });

  it("writes local theme changes back to the account", async () => {
    const store = memoryStore({});
    registerAccountPreferenceStore(store);
    await flushMacrotask();
    const { useUi } = await import("../app/store");

    useUi.getState().setThemeMode("light");
    expect(store.data["appearance.themeMode"]).toBe("light");
    expect(store.puts).toContainEqual({ "appearance.themeMode": "light" });
  });
});

describe("server global defaults in the keybinding snapshot (built-in < server < local < account)", () => {
  it("applies server defaults when present and keeps built-in defaults for undeclared keys", async () => {
    const store = memoryStore({}, { "keybinding.newTerminal": "Mod+Shift+t" });
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t"));
    expect(getKeybinding("closeTab")).toBe("Mod+w");
  });

  it("prioritizes server defaults, device-local values, and account overrides in ascending order", async () => {
    window.localStorage.setItem(
      "nexterm.keybindings.v1",
      JSON.stringify({ newTerminal: "Mod+y", quickConnect: "Mod+Shift+j" }),
    );
    loadKeybindings();
    const store = memoryStore(
      { "keybinding.newTerminal": "Mod+Shift+t" },
      { "keybinding.newTerminal": "Mod+Shift+g", "keybinding.quickConnect": "Mod+Shift+k" },
    );
    registerAccountPreferenceStore(store);
    // Account override beats local and server defaults
    await vi.waitFor(() => expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t"));
    // Device-local beats the server default
    await vi.waitFor(() => expect(getKeybinding("quickConnect")).toBe("Mod+Shift+j"));
    // Undeclared keys fall back to built-in defaults
    expect(getKeybinding("splitDown")).toBe("Mod+\\");
  });

  it("falls back to the server default rather than the built-in default after resetKeybinding", async () => {
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

  it("clears local and account overrides in resetAllKeybindings while preserving server defaults", async () => {
    const store = memoryStore(
      { "keybinding.closeTab": "Mod+Shift+w" },
      { "keybinding.closeTab": "Mod+Shift+c", "keybinding.newTerminal": "Mod+Shift+g" },
    );
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("closeTab")).toBe("Mod+Shift+w"));
    setKeybinding("quickConnect", "Mod+Shift+j");

    resetAllKeybindings();
    expect(getKeybinding("closeTab")).toBe("Mod+Shift+c");
    expect(getKeybinding("newTerminal")).toBe("Mod+Shift+g");
    expect(getKeybinding("quickConnect")).toBe("Mod+Shift+k");
    expect(store.clears.flat().sort()).toEqual(["keybinding.closeTab", "keybinding.quickConnect"]);
  });

  it("uses server defaults and account overrides for reclaimTakeover (production allowlisted key)", async () => {
    const store = memoryStore(
      { "keybinding.reclaimTakeover": "Mod+Shift+y" },
      { "keybinding.commandPalette": "Mod+Shift+p" },
    );
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("reclaimTakeover")).toBe("Mod+Shift+y"));
    await vi.waitFor(() => expect(getKeybinding("commandPalette")).toBe("Mod+Shift+p"));

    setKeybinding("reclaimTakeover", "Mod+Shift+n");
    await vi.waitFor(() => expect(store.data["keybinding.reclaimTakeover"]).toBe("Mod+Shift+n"));
    expect(store.puts).toContainEqual({ "keybinding.reclaimTakeover": "Mod+Shift+n" });
  });
});

describe("production path: flat HTTP preference-store contract", () => {
  it("loads defaults/overrides in their real shape after registration and rebuilds the snapshot with inherited superadmin defaults", async () => {
    prefsMocks.get.mockResolvedValue({
      defaults: {
        "appearance.terminalFontSize": 16,
        "appearance.terminalFontFamily": "menlo",
        "appearance.terminalContrast": "high",
        "input.selectionAutoCopy": true,
      },
      overrides: {
        "keybinding.newTerminal": "Mod+Shift+t",
        "appearance.terminalFontFamily": "source-code-pro",
        "appearance.terminalTheme": "light",
        "appearance.terminalContrast": "maximum",
      },
      effective: {},
    });
    prefsMocks.put.mockResolvedValue({ defaults: {}, overrides: {}, effective: {} });

    const { registerAccountPreferenceStore, createHttpPreferenceStore, getInputPrefs, getAppearancePrefs } = await import("../app/preferences");
    registerAccountPreferenceStore(createHttpPreferenceStore());
    await vi.waitFor(() => expect(prefsMocks.get).toHaveBeenCalled(), { timeout: 10000 });

    // Server global defaults are inherited
    await vi.waitFor(() => expect(getInputPrefs().selectionAutoCopy).toBe(true));
    await vi.waitFor(() => expect(getAppearancePrefs().terminalFontSize).toBe(16));
    // Account override takes precedence over global and built-in defaults
    await vi.waitFor(() => expect(getAppearancePrefs().terminalTheme).toBe("light"));
    expect(getAppearancePrefs().terminalFontFamily).toBe("source-code-pro");
    expect(getAppearancePrefs().terminalContrast).toBe("maximum");
    await vi.waitFor(() => expect(getKeybinding("newTerminal")).toBe("Mod+Shift+t"));
  });

  it("writes and clears flat production keys and falls back to defaults after reset", async () => {
    prefsMocks.get.mockResolvedValue({ defaults: {}, overrides: {}, effective: {} });
    prefsMocks.put.mockResolvedValue({ defaults: {}, overrides: {}, effective: {} });

    const { registerAccountPreferenceStore, createHttpPreferenceStore, setSelectionAutoCopy, setTerminalFontFamily, setTerminalContrast, setTerminalTheme, resetAppearancePrefs, getAppearancePrefs } = await import("../app/preferences");
    registerAccountPreferenceStore(createHttpPreferenceStore());
    await vi.waitFor(() => expect(prefsMocks.get).toHaveBeenCalled(), { timeout: 10000 });

    setSelectionAutoCopy(true);
    await vi.waitFor(() => expect(prefsMocks.put).toHaveBeenCalledWith({ set: { "input.selectionAutoCopy": true } }));

    setTerminalFontFamily("fira-code");
    await vi.waitFor(() => expect(prefsMocks.put).toHaveBeenCalledWith({ set: { "appearance.terminalFontFamily": "fira-code" } }));
    setTerminalContrast("maximum");
    await vi.waitFor(() => expect(prefsMocks.put).toHaveBeenCalledWith({ set: { "appearance.terminalContrast": "maximum" } }));
    setTerminalTheme("light");
    await vi.waitFor(() => expect(prefsMocks.put).toHaveBeenCalledWith({ set: { "appearance.terminalTheme": "light" } }));

    resetAppearancePrefs();
    await vi.waitFor(() =>
      expect(prefsMocks.put).toHaveBeenCalledWith({
        clear: ["appearance.uiFontPreset", "appearance.uiFontScale", "appearance.terminalFontSize", "appearance.terminalFontFamily", "appearance.terminalTheme", "appearance.terminalContrast", "appearance.themeMode"],
      }),
    );
    // After clearing, falls back to defaults with no leftover cleared account overrides
    expect(getAppearancePrefs().terminalFontFamily).toBe("cascadia");
    expect(getAppearancePrefs().terminalTheme).toBe("dark");
    expect(getAppearancePrefs().terminalContrast).toBe("standard");
  });

  it("does not overwrite new state with a late response from the previous account (generation guard)", async () => {
    let resolveA: ((view: AccountPreferenceView) => void) | undefined;
    const storeA: AccountPreferenceStore = {
      getView: () => new Promise((resolve) => {
        resolveA = resolve;
      }),
      putOverrides: async () => undefined,
      deleteOverrides: async () => undefined,
    };
    const storeB = memoryStore({
      "appearance.terminalFontFamily": "menlo",
      "appearance.terminalContrast": "high",
    });
    registerAccountPreferenceStore(storeA);
    registerAccountPreferenceStore(storeB);
    await vi.waitFor(() => expect(getAccountPreferenceStore()).toBe(storeB));
    const { getInputPrefs, getAppearancePrefs } = await import("../app/preferences");
    await vi.waitFor(() => expect(getAppearancePrefs().terminalFontFamily).toBe("menlo"));
    expect(getAppearancePrefs().terminalContrast).toBe("high");

    resolveA?.({ defaults: {}, overrides: {
      "input.selectionAutoCopy": true,
      "appearance.terminalFontFamily": "fira-code",
      "appearance.terminalContrast": "maximum",
    } });
    await flushMacrotask();
    expect(getInputPrefs().selectionAutoCopy).toBe(false);
    expect(getAppearancePrefs().terminalFontFamily).toBe("menlo");
    expect(getAppearancePrefs().terminalContrast).toBe("high");
    expect(getAccountOverrides()).toEqual({
      "appearance.terminalFontFamily": "menlo",
      "appearance.terminalContrast": "high",
    });
  });
});

describe("auth store preference-store lifecycle", () => {
  it("registers the preference store after register and unregisters it on logout", async () => {
    const { useAuth } = await import("../features/auth/store");
    authApiMocks.register.mockResolvedValue({ user: DEMO_USER });
    authApiMocks.logout.mockResolvedValue({ ok: true });

    await useAuth.getState().register("alice", "pw", "Alice");
    expect(useAuth.getState().user).not.toBeNull();
    await vi.waitFor(() => expect(getAccountPreferenceStore()).not.toBeNull(), { timeout: 10000 });

    await useAuth.getState().logout();
    expect(getAccountPreferenceStore()).toBeNull();
    expect(useAuth.getState().user).toBeNull();
  }, 30000);

  it("unregisters the preference store when the session expires with 401", async () => {
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

describe("keybindings replaced by the split tree", () => {
  it("moves a device-local custom split binding to splitDown, rewrites storage once and reports it once", () => {
    window.localStorage.setItem(
      "nexterm.keybindings.v1",
      JSON.stringify({ toggleSplit: "Alt+s", focusUpperPane: "Alt+ArrowUp", focusLowerPane: "Alt+ArrowDown", quickConnect: "Alt+q" }),
    );
    loadKeybindings();
    expect(getKeybinding("splitDown")).toBe("Alt+s");
    expect(getKeybinding("focusPaneUp")).toBe("Alt+ArrowUp");
    expect(getKeybinding("focusPaneDown")).toBe("Alt+ArrowDown");
    expect(getKeybinding("splitRight")).toBe("Mod+Shift+\\");
    expect(JSON.parse(window.localStorage.getItem("nexterm.keybindings.v1")!)).toEqual({
      splitDown: "Alt+s",
      focusPaneUp: "Alt+ArrowUp",
      focusPaneDown: "Alt+ArrowDown",
      quickConnect: "Alt+q",
    });
    const notice = takeKeybindingMigrationNotice();
    expect(notice).toContain("向下分屏");
    expect(notice).toContain("聚焦上方窗格");
    expect(takeKeybindingMigrationNotice()).toBeNull();

    // Resetting the replacement does not bring the old binding back.
    resetKeybinding("splitDown");
    loadKeybindings();
    expect(getKeybinding("splitDown")).toBe("Mod+\\");
  });

  it("keeps an explicit new binding over a stale legacy one and clears legacy-only storage", () => {
    window.localStorage.setItem("nexterm.keybindings.v1", JSON.stringify({ toggleSplit: "Alt+s", splitDown: "Alt+d" }));
    loadKeybindings();
    expect(getKeybinding("splitDown")).toBe("Alt+d");
    expect(takeKeybindingMigrationNotice()).toBeNull();

    window.localStorage.setItem("nexterm.keybindings.v1", JSON.stringify({ toggleSplit: "not a binding" }));
    loadKeybindings();
    expect(getKeybinding("splitDown")).toBe("Mod+\\");
    expect(window.localStorage.getItem("nexterm.keybindings.v1")).toBeNull();
    expect(takeKeybindingMigrationNotice()).toBeNull();
  });

  it("moves account overrides to the new keys and clears the old keys on the server", async () => {
    const store = memoryStore({ "keybinding.toggleSplit": "Mod+Shift+s", "keybinding.focusLowerPane": null });
    registerAccountPreferenceStore(store);
    await vi.waitFor(() => expect(getKeybinding("splitDown")).toBe("Mod+Shift+s"));
    expect(getKeybinding("focusPaneDown")).toBeNull();
    await vi.waitFor(() => expect(store.clears).toEqual([["keybinding.toggleSplit", "keybinding.focusLowerPane"]]));
    expect(store.puts).toEqual([{ "keybinding.splitDown": "Mod+Shift+s", "keybinding.focusPaneDown": null }]);
    expect(store.data).toEqual({ "keybinding.splitDown": "Mod+Shift+s", "keybinding.focusPaneDown": null });
    expect(getAccountOverrides()).toEqual({ "keybinding.splitDown": "Mod+Shift+s", "keybinding.focusPaneDown": null });
    expect(takeKeybindingMigrationNotice()).toContain("向下分屏");
  });
});
