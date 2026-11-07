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
