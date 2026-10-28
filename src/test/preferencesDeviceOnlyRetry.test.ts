/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return { handlers: [] as (() => void)[] };
});

vi.mock("../ipc/events", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../ipc/events")>()),
  listenEvent: vi.fn((_topic: string, handler: () => void) => {
    mocks.handlers.push(handler);
    return Promise.resolve(() => {});
  }),
}));

import {
  getAppearancePrefs,
  registerAccountPreferenceStore,
  type AccountPreferenceStore,
  type AccountPreferenceView,
} from "../app/preferences";
import { flushMacrotask } from "./features/reactTestUtils";

describe("desktop account preferences before the sync session exists", () => {
  beforeEach(() => {
    registerAccountPreferenceStore(null);
    mocks.handlers.length = 0;
  });

  it("loads synced appearance once a later sync status event finds the session", async () => {
    let view: AccountPreferenceView = { defaults: {}, overrides: {}, deviceOnly: true };
    const store: AccountPreferenceStore = {
      getView: () => Promise.resolve(view),
      putOverrides: () => Promise.resolve(),
      deleteOverrides: () => Promise.resolve(),
    };
    const before = getAppearancePrefs().terminalFontSize;
    registerAccountPreferenceStore(store);
    await flushMacrotask();
    expect(getAppearancePrefs().terminalFontSize).toBe(before);

    view = { defaults: {}, overrides: { "appearance.terminalFontSize": before + 3 } };
    for (const handler of mocks.handlers) handler();
    await flushMacrotask();
    expect(getAppearancePrefs().terminalFontSize).toBe(before + 3);
  });
});
