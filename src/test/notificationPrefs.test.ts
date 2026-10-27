/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  DEFAULT_NOTIFICATION_PREFS,
  getNotificationPrefs,
  resetNotificationPrefsForTest,
  setNotificationPrefs,
  subscribeNotificationPrefs,
} from "../features/inbox/notificationPrefs";

const KEY = "nexterm.notifications.v1";

beforeEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
  resetNotificationPrefsForTest();
});

describe("notification prefs", () => {
  it("defaults when nothing is stored", () => {
    expect(getNotificationPrefs()).toEqual(DEFAULT_NOTIFICATION_PREFS);
  });

  it("reads stored values and falls back per field for invalid ones", () => {
    localStorage.setItem(KEY, JSON.stringify({ system: false, needsMe: "yes", inferred: true, finished: null }));
    expect(getNotificationPrefs()).toEqual({ system: false, needsMe: true, inferred: true, finished: true, ai: true });
  });

  it("falls back to defaults for corrupt JSON", () => {
    localStorage.setItem(KEY, "{not json");
    expect(getNotificationPrefs()).toEqual(DEFAULT_NOTIFICATION_PREFS);
  });

  it("falls back to defaults when storage throws on read", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("denied");
    });
    expect(getNotificationPrefs()).toEqual(DEFAULT_NOTIFICATION_PREFS);
  });

  it.each(["system", "needsMe", "inferred", "finished", "ai"] as const)("toggles %s alone and persists it", (key) => {
    const next = !DEFAULT_NOTIFICATION_PREFS[key];
    setNotificationPrefs({ [key]: next });
    expect(getNotificationPrefs()).toEqual({ ...DEFAULT_NOTIFICATION_PREFS, [key]: next });
    resetNotificationPrefsForTest();
    expect(getNotificationPrefs()[key]).toBe(next);
  });

  it("notifies subscribers until they unsubscribe", () => {
    const listener = vi.fn();
    const unsubscribe = subscribeNotificationPrefs(listener);
    setNotificationPrefs({ system: false });
    expect(listener).toHaveBeenCalledTimes(1);
    unsubscribe();
    setNotificationPrefs({ system: true });
    expect(listener).toHaveBeenCalledTimes(1);
  });

  it("keeps the choice in memory when storage throws on write", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("quota");
    });
    expect(() => setNotificationPrefs({ ai: false })).not.toThrow();
    expect(getNotificationPrefs().ai).toBe(false);
  });
});
