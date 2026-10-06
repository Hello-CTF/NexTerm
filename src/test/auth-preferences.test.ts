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
