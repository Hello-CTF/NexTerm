/** @vitest-environment jsdom */
import { afterEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { mount, type MountedView } from "./reactTestUtils";

let mounted: MountedView | undefined;

afterEach(async () => {
  mounted?.unmount();
  mounted = undefined;
  const i18n = await import("../../i18n");
  i18n.applyLocale("zh-CN", {}, {});
  vi.resetModules();
});

describe("interface language", () => {
  it("renders interface text, including text built when a module loads, in the chosen language", async () => {
    vi.resetModules();
    const i18n = await import("../../i18n");
    const ui = (await import("../../i18n/locales/en/ui.json")).default;
    const server = (await import("../../i18n/locales/en/server.json")).default;
    // Catalogs load before the interface modules, as the bootstrap does.
    i18n.applyLocale("en", ui, server);
    const { AppearanceCard } = await import("../../features/settings/AppearanceCard");
    mounted = mount(createElement(AppearanceCard));
    const text = mounted.container.textContent ?? "";
    expect(text).toContain(ui["界面语言"] as string);
    expect(text).toContain(ui["跟随系统"] as string);
    expect(text).not.toMatch(/[一-鿿]/);
    expect(document.documentElement.lang).toBe("en");
  });

  it("translates backend messages through the server catalog", async () => {
    vi.resetModules();
    const i18n = await import("../../i18n");
    const ui = (await import("../../i18n/locales/de/ui.json")).default;
    const server = (await import("../../i18n/locales/de/server.json")).default;
    i18n.applyLocale("de", ui, server);
    const { describeError } = await import("../../ui/errorText");
    const shown = describeError({ code: "vault_locked", message: "" });
    expect(shown).not.toMatch(/[一-鿿]/);
  });
});

const LOCAL_ID = "01J0NEXTERMLOCALDEVICE0001";

describe("built-in local asset name", () => {
  it("shows the stored default name in the interface language and keeps it when saved unchanged", async () => {
    vi.resetModules();
    const calls: Array<[string, unknown]> = [];
    vi.doMock("../../ipc/wails", () => ({
      callDesktop: async (command: string, args: unknown) => {
        calls.push([command, args]);
        if (command === "asset_list") return [{ id: LOCAL_ID, kind: "local", name: "当前设备", builtin: true }, { id: "a", kind: "ssh", name: "当前设备", builtin: false }];
        return { id: LOCAL_ID, kind: "local", name: "当前设备", builtin: true };
      },
    }));
    vi.doMock("../../ipc/env", async (original) => ({ ...(await original<typeof import("../../ipc/env")>()), WEB: false }));
    const i18n = await import("../../i18n");
    i18n.applyLocale("en", { 当前设备: "This device" }, {});
    const { assetApi } = await import("../../ipc/commands");
    const [builtin, other] = await assetApi.list();
    expect(builtin.name).toBe("This device");
    expect(other.name).toBe("当前设备");
    const saved = await assetApi.update({ id: LOCAL_ID, name: "This device" });
    expect(calls.find(([command]) => command === "asset_update")?.[1]).toEqual({ args: { id: LOCAL_ID, name: "当前设备" } });
    expect(saved.name).toBe("This device");
    // Another asset the user names "This device" keeps that name.
    await assetApi.update({ id: "a", name: "This device" });
    expect(calls.filter(([command]) => command === "asset_update")[1]?.[1]).toEqual({ args: { id: "a", name: "This device" } });
    vi.doUnmock("../../ipc/wails");
    vi.doUnmock("../../ipc/env");
  });
});
