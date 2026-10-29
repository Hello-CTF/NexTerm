/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  registerAccountPreferenceStore,
  type AccountPreferenceStore,
  type AccountPreferenceView,
} from "../app/preferences";
import { getThemeMode } from "../app/theme";
import { flushMacrotask } from "./features/reactTestUtils";

function storeFor(view: AccountPreferenceView): AccountPreferenceStore {
  return {
    getView: () => Promise.resolve(view),
    putOverrides: () => Promise.resolve(),
    deleteOverrides: () => Promise.resolve(),
  };
}

describe("synced values overwrite this device's values", () => {
  beforeEach(() => {
    localStorage.clear();
    registerAccountPreferenceStore(null);
  });
  afterEach(() => registerAccountPreferenceStore(null));

  it("stores the synced theme and appearance so the next launch paints them first", async () => {
    localStorage.setItem("nexterm.theme.v1", "system");
    registerAccountPreferenceStore(
      storeFor({
        defaults: { "appearance.uiFontPreset": 14.5 },
        overrides: { "appearance.themeMode": "dark", "appearance.terminalFontSize": 17 },
      }),
    );
    await flushMacrotask();

    expect(getThemeMode()).toBe("dark");
    expect(localStorage.getItem("nexterm.theme.v1")).toBe("dark");
    const stored = JSON.parse(localStorage.getItem("nexterm.appearance.v1") ?? "{}") as Record<string, unknown>;
    expect(stored.terminalFontSize).toBe(17);
    // Server defaults stay unmirrored so a later default change still applies.
    expect(stored.uiFontPreset).toBeUndefined();
  });
});
