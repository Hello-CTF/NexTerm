/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  DEFAULT_NOTIFICATION_PREFS,
  getNotificationPrefs,
  resetNotificationPrefsForTest,
  setNotificationPrefs,
  subscribeNotificationPrefs,
} from "../../features/inbox/notificationPrefs";

const key = "nexterm.notifications.v1";

beforeEach(() => {
  localStorage.clear();
  resetNotificationPrefsForTest();
});
afterEach(() => vi.restoreAllMocks());

describe("device notification preferences", () => {
  it("uses defaults for missing, malformed and inaccessible storage", () => {
    expect(getNotificationPrefs()).toEqual(DEFAULT_NOTIFICATION_PREFS);
    for (const value of ["{invalid", "null"]) {
      localStorage.setItem(key, value);
      resetNotificationPrefsForTest();
      expect(getNotificationPrefs()).toEqual(DEFAULT_NOTIFICATION_PREFS);
    }
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("storage disabled"); });
    resetNotificationPrefsForTest();
    expect(getNotificationPrefs()).toEqual(DEFAULT_NOTIFICATION_PREFS);
  });

  it("restores booleans and defaults each invalid or missing field independently", () => {
    localStorage.setItem(key, JSON.stringify({ system: false, needsMe: "false", inferred: true, finished: 0 }));
    expect(getNotificationPrefs()).toEqual({ system: false, needsMe: true, inferred: true, finished: true, ai: true });
    localStorage.setItem(key, JSON.stringify({ system: true, needsMe: false, inferred: false, finished: false, ai: false }));
    resetNotificationPrefsForTest();
    expect(getNotificationPrefs()).toEqual({ system: true, needsMe: false, inferred: false, finished: false, ai: false });
  });

  it("persists a merged choice and notifies only current subscribers", () => {
    const changed = vi.fn();
    const stop = subscribeNotificationPrefs(changed);
    try {
      setNotificationPrefs({ system: false, inferred: true });
      expect(changed).toHaveBeenCalledOnce();
      expect(JSON.parse(localStorage.getItem(key)!)).toEqual({ ...DEFAULT_NOTIFICATION_PREFS, system: false, inferred: true });
      resetNotificationPrefsForTest();
      expect(getNotificationPrefs().system).toBe(false);
    } finally {
      stop();
    }
    setNotificationPrefs({ ai: false });
    expect(changed).toHaveBeenCalledOnce();
    expect(getNotificationPrefs()).toMatchObject({ system: false, inferred: true, ai: false });
  });

  it("applies the current device choice even when persistence is unavailable", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("quota exhausted"); });
    const changed = vi.fn();
    const stop = subscribeNotificationPrefs(changed);
    try {
      expect(() => setNotificationPrefs({ finished: false })).not.toThrow();
      expect(getNotificationPrefs().finished).toBe(false);
      expect(changed).toHaveBeenCalledOnce();
    } finally {
      stop();
    }
  });
});
